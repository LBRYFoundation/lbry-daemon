package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"lbry/daemon/backgrounddownloader"
	"lbry/daemon/blob"
	"lbry/daemon/componentgraph"
	"lbry/daemon/config"
	"lbry/daemon/daemonlog"
	"lbry/daemon/database"
	"lbry/daemon/dht"
	"lbry/daemon/diskspace"
	"lbry/daemon/exchangerate"
	"lbry/daemon/fileanalysis"
	"lbry/daemon/filemanager"
	"lbry/daemon/metrics"
	"lbry/daemon/peer"
	"lbry/daemon/rpc"
	"lbry/daemon/stream"
	"lbry/daemon/torrent"
	"lbry/daemon/trackerannouncer"
	"lbry/daemon/wallet"
	walletspv "lbry/daemon/wallet/spv"
)

const shutdownTimeout = 5 * time.Second

var legacyComponents = legacyComponentNames()

func legacyComponentNames() []string {
	components := componentgraph.LegacyComponents()
	names := make([]string, len(components))
	for index, component := range components {
		names[index] = component.Name
	}
	return names
}

type managedService struct {
	name      string
	component string
	listener  net.Listener
	serve     func(net.Listener) error
	shutdown  func(context.Context) error
}

type serviceDefinition struct {
	name      string
	component string
	address   string
	serve     func(net.Listener) error
	shutdown  func(context.Context) error
}

type serviceResult struct {
	name string
	err  error
}

type daemonApp struct {
	services           []managedService
	dht                *dht.Node
	announcer          *dht.BlobAnnouncer
	background         *backgrounddownloader.Manager
	status             *componentStatus
	logController      *daemonlog.Controller
	databaseStart      func() error
	databaseStop       func() error
	blobManager        *blob.BlobManager
	blobStart          func() error
	blobStop           func()
	diskSpace          *diskspace.Manager
	fileManager        *filemanager.Manager
	tracker            *trackerannouncer.Manager
	torrent            *torrent.Session
	exchangeRates      *exchangerate.Manager
	exchangeCancel     context.CancelFunc
	walletPaymentsNoOp bool

	walletMu               sync.RWMutex
	walletManager          *wallet.WalletManager
	walletPersistenceStart func(context.Context) error
	walletShuttingDown     bool
	walletReadyCancel      context.CancelFunc
	walletReadyDone        chan struct{}
	walletStarted          chan struct{}
	walletStartErr         error

	stopRequested chan struct{}
	stopOnce      sync.Once
	shutdownOnce  sync.Once
	shutdownErr   error
}

func newDaemonApp(status *componentStatus) *daemonApp {
	return &daemonApp{
		status:        status,
		stopRequested: make(chan struct{}),
	}
}

func newProductionApp(arguments map[string]any, initialHeaders string) (*daemonApp, error) {
	return newProductionAppWithLogging(arguments, initialHeaders, nil)
}

func newProductionAppWithLogging(
	arguments map[string]any,
	initialHeaders string,
	logging *daemonlog.Options,
) (*daemonApp, error) {
	return newProductionAppWithDependencies(
		arguments, initialHeaders, logging, walletManagerFromSettings,
	)
}

type walletManagerFactory func(*config.Store) (*wallet.WalletManager, error)

func newProductionAppWithDependencies(
	arguments map[string]any,
	initialHeaders string,
	logging *daemonlog.Options,
	newWalletManager walletManagerFactory,
) (*daemonApp, error) {
	if newWalletManager == nil {
		newWalletManager = walletManagerFromSettings
	}
	var logController *daemonlog.Controller
	completed := false
	defer func() {
		if !completed && logController != nil {
			_ = logController.Close()
		}
	}()

	paths := config.DefaultPaths()
	settings, err := config.New(config.Options{Paths: &paths, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	skippedComponents := stringSliceSetting(settings, "components_to_skip")
	if _, err := componentgraph.LegacyStartStages(skippedComponents); err != nil {
		return nil, fmt.Errorf("resolve component startup order: %w", err)
	}
	if err := prepareStartupDirectories(settings); err != nil {
		return nil, err
	}
	if initialHeaders != "" {
		if err := installInitialHeaders(settings, initialHeaders); err != nil {
			return nil, err
		}
	}
	dataDir, err := stringSetting(settings, "data_dir")
	if err != nil {
		return nil, err
	}
	downloadDir, err := stringSetting(settings, "download_dir")
	if err != nil {
		return nil, err
	}
	if logging != nil {
		logOptions := *logging
		logOptions.DataDir = dataDir
		logController, err = daemonlog.ConfigureStandardLogger(logOptions)
		if err != nil {
			return nil, fmt.Errorf("configure logging: %w", err)
		}
	}
	installationID, err := config.LoadOrCreateInstallationID(dataDir)
	if err != nil {
		return nil, err
	}

	mediaAnalyzer := fileanalysis.New(func() fileanalysis.Config { return fileAnalysisConfig(settings) })
	status := newComponentStatus(
		installationID, skippedComponents,
		mediaAnalyzer.Status(context.Background(), false, false),
	)
	status.setFFmpegProvider(func() map[string]any {
		return mediaAnalyzer.Status(context.Background(), false, false)
	})
	app := newDaemonApp(status)
	app.logController = logController
	if !status.isSkipped(componentgraph.ExchangeRateManager) {
		app.exchangeRates = exchangerate.NewProduction(nil)
	}
	var resolvedClaimStore *database.ResolvedClaimStore
	if !status.isSkipped(componentgraph.Database) {
		resolvedClaimStore = database.NewResolvedClaimStore(dataDir)
		migrator := database.NewMigrator(dataDir, downloadDir)
		app.databaseStart = func() error {
			if _, err := database.EnsureRevision(dataDir, migrator.MigrationFunc); err != nil {
				return err
			}
			if err := resolvedClaimStore.Open(context.Background()); err != nil {
				return fmt.Errorf("open daemon database: %w", err)
			}
			return nil
		}
		app.databaseStop = resolvedClaimStore.Close
	}
	if !status.isSkipped(componentgraph.Wallet) {
		app.walletPersistenceStart = func(ctx context.Context) error {
			app.walletMu.Lock()
			defer app.walletMu.Unlock()
			if app.walletShuttingDown {
				return errors.New("daemon shutdown has started")
			}
			manager, err := newWalletManager(settings)
			if err != nil {
				return err
			}
			app.walletManager = manager

			results := manager.OpenLedgersPersistence(ctx)
			if err := wallet.LedgerPersistenceOpenError(results); err != nil {
				closeErr := manager.CloseLedgersPersistence(context.Background())
				if closeErr != nil {
					closeErr = fmt.Errorf("clean up wallet persistence: %w", closeErr)
				}
				return errors.Join(err, closeErr)
			}
			return nil
		}
	}
	var blobManager *blob.BlobManager
	if status.isSkipped(componentgraph.BlobManager) {
		blobManager = blob.NewManager()
	} else {
		saveBlobs, settingErr := boolSetting(settings, "save_blobs")
		if settingErr != nil {
			return nil, settingErr
		}
		blobManager = blob.NewConfiguredManager(filepath.Join(dataDir, "blobfiles"), saveBlobs)
		app.blobStart = func() error {
			files, startErr := blobManager.Start()
			if startErr != nil {
				return startErr
			}
			if resolvedClaimStore != nil {
				if syncErr := resolvedClaimStore.SyncStoredBlobs(context.Background(), files); syncErr != nil {
					blobManager.Stop()
					return fmt.Errorf("synchronize stored blobs: %w", syncErr)
				}
				blobManager.SetCompletionStore(resolvedClaimStore)
			}
			return nil
		}
		app.blobStop = blobManager.Stop
	}
	app.blobManager = blobManager
	if !status.isSkipped(componentgraph.DiskSpace) && resolvedClaimStore != nil {
		contentLimit, settingErr := integerSetting(settings, "blob_storage_limit")
		if settingErr != nil {
			return nil, settingErr
		}
		networkLimit, settingErr := integerSetting(settings, "network_storage_limit")
		if settingErr != nil {
			return nil, settingErr
		}
		app.diskSpace = diskspace.New(
			resolvedClaimStore, blobManager, int64(contentLimit), int64(networkLimit), 0,
		)
		status.setDetailProvider(componentgraph.DiskSpace, func() map[string]any {
			return app.diskSpace.Status(context.Background())
		})
	}
	if !status.isSkipped(componentgraph.FileManager) && resolvedClaimStore != nil {
		reflectStreams, settingErr := boolSetting(settings, "reflect_streams")
		if settingErr != nil {
			return nil, settingErr
		}
		reflectorServers, settingErr := serverAddressesSetting(settings, "reflector_servers")
		if settingErr != nil {
			return nil, settingErr
		}
		reflectConcurrency, settingErr := integerSetting(settings, "concurrent_reflector_uploads")
		if settingErr != nil {
			return nil, settingErr
		}
		downloadTimeout, settingErr := floatSetting(settings, "download_timeout")
		if settingErr != nil {
			return nil, settingErr
		}
		app.fileManager = filemanager.New(
			resolvedClaimStore, blobManager, downloadDir,
			filemanager.WithReflection(reflectStreams, reflectorServers, reflectConcurrency, 0),
			filemanager.WithDownloadTimeout(time.Duration(downloadTimeout*float64(time.Second))),
		)
		status.setDetailProvider(componentgraph.FileManager, app.fileManager.Status)
		if app.diskSpace != nil {
			app.diskSpace.SetDownloadController(app.fileManager)
		}
	}
	status.setDetailProvider(componentgraph.BlobManager, func() map[string]any {
		return blobComponentStatus(blobManager)
	})
	status.setDetailProvider(componentgraph.DHT, func() map[string]any {
		return dhtComponentStatus(app.dht)
	})
	status.setDetailProvider(componentgraph.Wallet, func() map[string]any {
		return walletComponentStatus(app.walletManagerForRPC())
	})
	status.setDetailProvider(componentgraph.UPnP, defaultUPnPComponentStatus)
	if !status.isSkipped(componentgraph.WalletServerPayments) {
		maxFee, settingErr := stringSetting(settings, "max_wallet_server_fee")
		if settingErr != nil {
			return nil, settingErr
		}
		app.walletPaymentsNoOp = decimalIsZero(maxFee)
		status.setDetailProvider(componentgraph.WalletServerPayments, func() map[string]any {
			return map[string]any{"max_fee": maxFee, "running": false}
		})
	}
	rpcOptions := []rpc.ServerOption{
		rpc.WithSettingsStore(settings),
		rpc.WithStatusProvider(status),
		rpc.WithShutdown(app.RequestStop),
		rpc.WithWalletManagerProvider(app.walletManagerForRPC),
		rpc.WithBlobManager(blobManager),
		rpc.WithDHTNodeProvider(func() *dht.Node { return app.dht }),
		rpc.WithFileAnalyzer(mediaAnalyzer),
	}
	if app.exchangeRates != nil {
		rpcOptions = append(rpcOptions, rpc.WithExchangeRateConverter(app.exchangeRates))
	}
	if resolvedClaimStore != nil {
		rpcOptions = append(rpcOptions, rpc.WithResolvedClaimSaver(resolvedClaimStore))
		rpcOptions = append(rpcOptions, rpc.WithManagedFileLister(resolvedClaimStore))
	}
	if app.diskSpace != nil {
		rpcOptions = append(rpcOptions, rpc.WithManagedBlobCleaner(app.diskSpace))
	}
	streamOptions := []stream.Option{}
	var rpcServer *rpc.RPCServer
	streamOptions = append(streamOptions, stream.WithStreamGet(func(
		ctx context.Context, uri string,
	) (string, error) {
		if app.fileManager == nil {
			return "", errors.New("file manager is unavailable")
		}
		if err := app.fileManager.WaitReady(ctx); err != nil {
			return "", err
		}
		if rpcServer == nil {
			return "", errors.New("managed get is unavailable")
		}
		return rpcServer.StreamingGet(ctx, uri)
	}, func() bool {
		enabled, _ := settings.Get("streaming_get")
		value, _ := enabled.(bool)
		return value
	}))
	if app.fileManager != nil {
		streamOptions = append(streamOptions, stream.WithStreamLifecycle(
			app.fileManager.MarkManagedStreamActive,
			app.fileManager.StopManagedStreamIfIdle,
			2*time.Second,
		))
		streamOptions = append(streamOptions, stream.WithManagedStreamLookup(func(
			ctx context.Context, sdHash string,
		) (stream.ManagedStreamInfo, bool, error) {
			row, found, err := app.fileManager.LookupManagedStream(ctx, sdHash)
			if err != nil {
				return stream.ManagedStreamInfo{}, false, err
			}
			if !found {
				return stream.ManagedStreamInfo{}, false, nil
			}
			info, err := managedStreamInfo(row)
			return info, err == nil, err
		}))
	}
	streamServer := stream.CreateServer(blobManager, streamOptions...)
	if app.fileManager != nil {
		rpcOptions = append(rpcOptions, rpc.WithManagedFileController(&streamingFileController{
			files: app.fileManager, streams: streamServer,
		}))
	}
	rpcServer = rpc.CreateServer(rpcOptions...)

	apiAddress, err := stringSetting(settings, "api")
	if err != nil {
		return nil, err
	}
	streamingAddress, err := stringSetting(settings, "streaming_server")
	if err != nil {
		return nil, err
	}
	interfaceAddress, err := stringSetting(settings, "network_interface")
	if err != nil {
		return nil, err
	}
	if !status.isSkipped(componentgraph.Libtorrent) {
		app.torrent = torrent.NewSession(interfaceAddress, torrent.DefaultPort)
		status.setDetailProvider(componentgraph.Libtorrent, app.torrent.Status)
		torrentContext, cancelTorrent := context.WithCancel(context.Background())
		app.services = append(app.services, managedService{
			name:      "torrent_session",
			component: componentgraph.Libtorrent,
			serve: func(net.Listener) error {
				if startErr := app.torrent.Start(); startErr != nil {
					return startErr
				}
				status.setComponent(componentgraph.Libtorrent, true)
				tcpAddress, udpAddress := app.torrent.Addresses()
				log.Printf("BitTorrent session: listening on TCP %s and UDP %s.", tcpAddress, udpAddress)
				<-torrentContext.Done()
				stopErr := app.torrent.Stop()
				status.setComponent(componentgraph.Libtorrent, false)
				return errors.Join(torrentContext.Err(), stopErr)
			},
			shutdown: func(context.Context) error {
				cancelTorrent()
				return app.torrent.Stop()
			},
		})
	}
	tcpPort, err := integerSetting(settings, "tcp_port")
	if err != nil {
		return nil, err
	}
	udpPort, err := integerSetting(settings, "udp_port")
	if err != nil {
		return nil, err
	}
	knownDHTNodes, err := serverAddressesSetting(settings, "known_dht_nodes")
	if err != nil {
		return nil, err
	}
	fixedPeers, err := serverAddressesSetting(settings, "fixed_peers")
	if err != nil {
		return nil, err
	}
	fixedPeerDelay, err := floatSetting(settings, "fixed_peer_delay")
	if err != nil {
		return nil, err
	}
	trackerServers, err := serverAddressesSetting(settings, "tracker_servers")
	if err != nil {
		return nil, err
	}
	blobManager.SetFetcher(blob.NetworkFetcher(blob.NetworkFetcherOptions{
		NodeProvider:   func() *dht.Node { return app.dht },
		FixedPeers:     fixedPeers,
		FixedPeerDelay: time.Duration(fixedPeerDelay * float64(time.Second)),
		TrackerServers: trackerServers,
		AnnouncePort:   tcpPort,
	}))
	if !status.isSkipped(componentgraph.TrackerAnnouncer) && resolvedClaimStore != nil {
		app.tracker = trackerannouncer.New(resolvedClaimStore, trackerServers, tcpPort)
	}
	prometheusPort, err := integerSetting(settings, "prometheus_port")
	if err != nil {
		return nil, err
	}

	serviceDefinitions := []serviceDefinition{
		{name: "rpc", address: apiAddress, serve: rpcServer.Serve, shutdown: rpcServer.Shutdown},
		{name: "stream", address: streamingAddress, serve: streamServer.Serve, shutdown: streamServer.Shutdown},
	}
	if prometheusPort != 0 {
		metricsServer := metrics.CreateServer()
		serviceDefinitions = append(serviceDefinitions, serviceDefinition{
			name:     "metrics",
			address:  metrics.ListenAddress(prometheusPort),
			serve:    metricsServer.Serve,
			shutdown: metricsServer.Shutdown,
		})
	}
	// The peer protocol depends on full wallet readiness in the pinned graph.
	// Persistence alone cannot provide its payment/address contract, so it stays
	// gated until the SPV wallet component can complete startup.
	for _, definition := range serviceDefinitions {
		listener, listenErr := net.Listen("tcp", definition.address)
		if listenErr != nil {
			app.closeListeners()
			return nil, fmt.Errorf("listen for %s on %s: %w", definition.name, definition.address, listenErr)
		}
		app.services = append(app.services, managedService{
			name:      definition.name,
			component: definition.component,
			listener:  listener,
			serve:     definition.serve,
			shutdown:  definition.shutdown,
		})
	}
	if app.fileManager != nil {
		fileContext, cancelFiles := context.WithCancel(context.Background())
		app.services = append(app.services, managedService{
			name:      "file_manager",
			component: componentgraph.FileManager,
			serve: func(net.Listener) error {
				if err := app.waitForWalletReady(fileContext); err != nil {
					return err
				}
				if err := app.fileManager.Start(fileContext); err != nil {
					return err
				}
				status.setComponent(componentgraph.FileManager, true)
				<-fileContext.Done()
				stopContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				err := app.fileManager.Stop(stopContext)
				status.setComponent(componentgraph.FileManager, false)
				return errors.Join(fileContext.Err(), err)
			},
			shutdown: func(ctx context.Context) error {
				cancelFiles()
				return app.fileManager.Stop(ctx)
			},
		})
	}
	if app.tracker != nil && app.fileManager != nil {
		trackerContext, cancelTracker := context.WithCancel(context.Background())
		app.services = append(app.services, managedService{
			name:      "tracker_announcer",
			component: componentgraph.TrackerAnnouncer,
			serve: func(net.Listener) error {
				if err := app.fileManager.WaitReady(trackerContext); err != nil {
					return err
				}
				if err := app.tracker.Start(trackerContext); err != nil {
					return err
				}
				status.setComponent(componentgraph.TrackerAnnouncer, true)
				<-trackerContext.Done()
				stopContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer cancel()
				err := app.tracker.Stop(stopContext)
				status.setComponent(componentgraph.TrackerAnnouncer, false)
				return errors.Join(trackerContext.Err(), err)
			},
			shutdown: func(ctx context.Context) error {
				cancelTracker()
				return app.tracker.Stop(ctx)
			},
		})
	}
	if !status.isSkipped(componentgraph.PeerProtocolServer) {
		peerServer := peer.CreateServer(blobManager)
		peerContext, cancelPeer := context.WithCancel(context.Background())
		var peerListenerMu sync.Mutex
		var peerListener net.Listener
		app.services = append(app.services, managedService{
			name:      "peer",
			component: componentgraph.PeerProtocolServer,
			serve: func(net.Listener) error {
				if err := app.waitForWalletReady(peerContext); err != nil {
					return err
				}
				manager := app.walletManagerForRPC()
				if manager == nil || manager.DefaultAccount() == nil || manager.DefaultAccount().Receiving == nil {
					return errors.New("peer server requires a default wallet receiving account")
				}
				paymentAddress, addressErr := manager.DefaultAccount().Receiving.GetOrCreateUsableAddress(peerContext)
				if addressErr != nil {
					return fmt.Errorf("select peer payment address: %w", addressErr)
				}
				listener, listenErr := net.Listen("tcp", net.JoinHostPort(interfaceAddress, strconv.Itoa(tcpPort)))
				if listenErr != nil {
					return fmt.Errorf("listen for peer on %s:%d: %w", interfaceAddress, tcpPort, listenErr)
				}
				peerListenerMu.Lock()
				peerListener = listener
				peerListenerMu.Unlock()
				peerServer.SetPaymentAddress(paymentAddress)
				status.setComponent(componentgraph.PeerProtocolServer, true)
				log.Printf("Blob server listening on TCP %s", listener.Addr())
				err := peerServer.Serve(listener)
				status.setComponent(componentgraph.PeerProtocolServer, false)
				return err
			},
			shutdown: func(ctx context.Context) error {
				cancelPeer()
				peerListenerMu.Lock()
				listener := peerListener
				peerListenerMu.Unlock()
				if listener != nil {
					_ = listener.Close()
				}
				return peerServer.Shutdown(ctx)
			},
		})
	}

	if !status.isSkipped(componentgraph.DHT) {
		nodeID, err := dht.LoadOrCreateNodeID(dataDir)
		if err != nil {
			app.closeListeners()
			return nil, err
		}
		app.dht, err = dht.NewNodeWithID(udpPort, nodeID)
		if err != nil {
			app.closeListeners()
			return nil, err
		}
		app.dht.TCPPort = tcpPort
		app.dht.BindIP, err = resolveInterfaceIP(interfaceAddress)
		if err != nil {
			app.closeListeners()
			return nil, err
		}
		app.dht.BootstrapNodes = knownDHTNodes
		if resolvedClaimStore != nil && !status.isSkipped(componentgraph.HashAnnouncer) {
			headOnly, settingErr := integerSetting(settings, "announce_head_and_sd_only")
			if settingErr != nil {
				app.closeListeners()
				return nil, settingErr
			}
			concurrency, settingErr := integerSetting(settings, "concurrent_blob_announcers")
			if settingErr != nil {
				app.closeListeners()
				return nil, settingErr
			}
			app.announcer = dht.NewBlobAnnouncer(
				app.dht, resolvedClaimStore, headOnly != 0, concurrency,
			)
		}
	}
	if !status.isSkipped(componentgraph.BackgroundDownloader) && app.diskSpace != nil {
		downloadTimeout, settingErr := floatSetting(settings, "blob_download_timeout")
		if settingErr != nil {
			app.closeListeners()
			return nil, settingErr
		}
		descriptorTimeout := time.Duration(downloadTimeout * float64(time.Second))
		var backgroundNode backgrounddownloader.Node
		if app.dht != nil {
			backgroundNode = app.dht
		}
		app.background = backgrounddownloader.New(
			backgroundNode, blobManager, app.diskSpace,
			backgrounddownloader.WithDownloadTimeouts(descriptorTimeout, descriptorTimeout*10),
		)
		status.setDetailProvider(componentgraph.BackgroundDownloader, app.background.Status)
	}
	completed = true
	return app, nil
}

func managedStreamInfo(row database.ManagedFileRow) (stream.ManagedStreamInfo, error) {
	canonical, err := hex.DecodeString(row.SerializedMetadataHex)
	if err != nil {
		return stream.ManagedStreamInfo{}, fmt.Errorf("decode managed claim metadata: %w", err)
	}
	value, err := wallet.DecodeClaimValue(canonical)
	if err != nil {
		return stream.ManagedStreamInfo{}, fmt.Errorf("decode managed stream claim: %w", err)
	}
	source, ok := value.Value["source"].(map[string]any)
	if !ok {
		return stream.ManagedStreamInfo{}, errors.New("managed claim has no stream source")
	}
	info := stream.ManagedStreamInfo{}
	info.MIMEType, _ = source["media_type"].(string)
	if size, ok := source["size"].(string); ok && size != "" && size != "0" {
		parsed, parseErr := strconv.ParseUint(size, 10, strconv.IntSize)
		if parseErr != nil {
			return stream.ManagedStreamInfo{}, fmt.Errorf("decode managed stream size: %w", parseErr)
		}
		info.Size = int(parsed)
	}
	return info, nil
}

type streamingFileController struct {
	files    *filemanager.Manager
	streams  *stream.StreamServer
	mu       sync.Mutex
	deleting map[string]bool
}

func (controller *streamingFileController) StartManagedFile(
	ctx context.Context, row database.ManagedFileRow,
) error {
	if err := controller.files.StartManagedFile(ctx, row); err != nil {
		return err
	}
	controller.streams.AllowManagedStream(row.SDHash)
	controller.streams.ScheduleManagedStreamIdle(row.SDHash)
	return nil
}

func (controller *streamingFileController) StopManagedFile(
	ctx context.Context, row database.ManagedFileRow,
) error {
	if err := controller.streams.CancelManagedStream(ctx, row.SDHash); err != nil {
		return err
	}
	return controller.files.StopManagedFile(ctx, row)
}

func (controller *streamingFileController) SaveManagedFile(
	ctx context.Context, row database.ManagedFileRow, fileName, directory *string,
) (database.ManagedFileRow, error) {
	updated, err := controller.files.SaveManagedFile(ctx, row, fileName, directory)
	if err == nil {
		controller.streams.AllowManagedStream(updated.SDHash)
	}
	return updated, err
}

func (controller *streamingFileController) RegisterManagedFile(
	ctx context.Context, row database.ManagedFileRow,
) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.deleting[row.SDHash] {
		return errors.New("managed stream is being deleted")
	}
	if err := controller.files.RegisterManagedFile(ctx, row); err != nil {
		return err
	}
	controller.streams.AllowManagedStream(row.SDHash)
	return nil
}

func (controller *streamingFileController) PrepareManagedFileDelete(
	ctx context.Context, row database.ManagedFileRow,
) error {
	controller.mu.Lock()
	if controller.deleting == nil {
		controller.deleting = make(map[string]bool)
	}
	controller.deleting[row.SDHash] = true
	controller.mu.Unlock()
	if err := controller.streams.BlockManagedStream(ctx, row.SDHash); err != nil {
		controller.finishDelete(row, false)
		return err
	}
	if err := controller.files.StopManagedFile(ctx, row); err != nil {
		controller.finishDelete(row, false)
		return err
	}
	return nil
}

func (controller *streamingFileController) FinishManagedFileDelete(
	row database.ManagedFileRow, deleted bool,
) {
	controller.finishDelete(row, deleted)
}

func (controller *streamingFileController) finishDelete(row database.ManagedFileRow, deleted bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if deleted {
		controller.files.ForgetManagedFile(row.SDHash)
	}
	controller.streams.AllowManagedStream(row.SDHash)
	delete(controller.deleting, row.SDHash)
}

func (app *daemonApp) RequestStop() {
	app.stopOnce.Do(func() { close(app.stopRequested) })
}

func (app *daemonApp) walletManagerForRPC() *wallet.WalletManager {
	app.walletMu.RLock()
	defer app.walletMu.RUnlock()
	if app.walletShuttingDown {
		return nil
	}
	return app.walletManager
}

func (app *daemonApp) Run(ctx context.Context) error {
	if app.logController != nil {
		defer app.logController.Close()
	}
	if app.status != nil && !app.status.isSkipped(componentgraph.UPnP) {
		// Python treats UPnP readiness as completion of the discovery attempt;
		// a missing gateway does not keep daemon startup pending.
		app.status.setComponent(componentgraph.UPnP, true)
	}
	if app.status != nil && !app.status.isSkipped(componentgraph.WalletServerPayments) &&
		!app.walletPaymentsNoOp {
		log.Printf("Wallet server payments: non-zero fees are not implemented; component remains pending.")
	}
	if app.databaseStart != nil {
		if err := app.databaseStart(); err != nil {
			app.closeListeners()
			return err
		}
		if app.status != nil && !app.status.isSkipped(componentgraph.Database) {
			app.status.setComponent(componentgraph.Database, true)
		}
	}
	if app.blobStart != nil {
		if err := app.blobStart(); err != nil {
			app.closeListeners()
			return errors.Join(fmt.Errorf("start blob manager: %w", err), app.closeDatabase())
		}
		if app.status != nil {
			app.status.setComponent(componentgraph.BlobManager, true)
		}
	}
	if app.diskSpace != nil {
		if err := app.diskSpace.Start(context.Background()); err != nil {
			app.closeListeners()
			return errors.Join(fmt.Errorf("start disk space manager: %w", err), app.closeBlobManager(), app.closeDatabase())
		}
		if app.status != nil {
			app.status.setComponent(componentgraph.DiskSpace, true)
		}
	}
	if app.walletPersistenceStart != nil {
		// Daemon cancellation controls the run loop, not component initialization
		// in the pinned SDK. A pre-cancelled Run still performs orderly startup and
		// shutdown, so persistence uses an independent context here.
		if err := app.walletPersistenceStart(context.Background()); err != nil {
			app.closeListeners()
			return errors.Join(fmt.Errorf("start wallet persistence: %w", err),
				app.closeDiskSpace(), app.closeBlobManager(), app.closeDatabase())
		}
		if err := app.startWalletCheckpointSync(context.Background()); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			cleanupErr := app.closeWalletPersistence(cleanupCtx)
			cancel()
			app.closeListeners()
			return errors.Join(
				fmt.Errorf("start wallet SPV checkpoint sync: %w", err),
				cleanupErr, app.closeDiskSpace(), app.closeBlobManager(), app.closeDatabase(),
			)
		}
		app.startWalletReadinessMonitor()
	}
	if app.exchangeRates != nil {
		exchangeContext, cancel := context.WithCancel(context.Background())
		app.exchangeCancel = cancel
		app.exchangeRates.Start(exchangeContext)
		if app.status != nil {
			app.status.setComponent(componentgraph.ExchangeRateManager, true)
		}
	}
	if app.dht != nil {
		if err := app.dht.Start(); err != nil {
			app.closeListeners()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			walletErr := app.closeWalletPersistence(cleanupCtx)
			cancel()
			if app.status != nil && !app.status.isSkipped(componentgraph.BlobManager) {
				app.status.setComponent(componentgraph.BlobManager, false)
			}
			return errors.Join(err, walletErr, app.closeDiskSpace(), app.closeBlobManager(), app.closeDatabase())
		}
		if app.status != nil {
			app.status.setComponent(componentgraph.DHT, true)
		}
		if app.announcer != nil {
			app.announcer.Start()
			if app.status != nil {
				app.status.setComponent(componentgraph.HashAnnouncer, true)
			}
		}
	}
	if app.background != nil {
		if err := app.background.Start(context.Background()); err != nil {
			app.closeListeners()
			if app.announcer != nil {
				app.announcer.Stop()
			}
			if app.dht != nil {
				app.dht.Stop()
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			walletErr := app.closeWalletPersistence(cleanupCtx)
			cancel()
			return errors.Join(
				fmt.Errorf("start background downloader: %w", err), walletErr,
				app.closeDiskSpace(), app.closeBlobManager(), app.closeDatabase(),
			)
		}
		if app.status != nil {
			app.status.setComponent(componentgraph.BackgroundDownloader, true)
		}
	}

	results := make(chan serviceResult, len(app.services))
	for _, service := range app.services {
		service := service
		go func() {
			results <- serviceResult{name: service.name, err: service.serve(service.listener)}
		}()
		if app.status != nil && service.component != "" &&
			service.component != componentgraph.PeerProtocolServer &&
			service.component != componentgraph.FileManager &&
			service.component != componentgraph.TrackerAnnouncer &&
			service.component != componentgraph.Libtorrent {
			app.status.setComponent(service.component, true)
		}
	}

	var unexpected *serviceResult
	select {
	case <-ctx.Done():
	case <-app.stopRequested:
	case result := <-results:
		unexpected = &result
	}

	shutdownErr := app.Shutdown()
	remaining := len(app.services)
	if unexpected != nil {
		remaining--
	}
	for range remaining {
		result := <-results
		if unexpected == nil && !expectedServiceClose(result.err) {
			result := result
			unexpected = &result
		}
	}
	if unexpected != nil {
		if unexpected.err == nil {
			return fmt.Errorf("%s service stopped unexpectedly", unexpected.name)
		}
		return fmt.Errorf("%s service stopped: %w", unexpected.name, unexpected.err)
	}
	return shutdownErr
}

func (app *daemonApp) waitForWalletReady(ctx context.Context) error {
	app.walletMu.RLock()
	started := app.walletStarted
	app.walletMu.RUnlock()
	if started == nil {
		return errors.New("wallet readiness monitor is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-started:
	}
	app.walletMu.RLock()
	err := app.walletStartErr
	app.walletMu.RUnlock()
	return err
}

func (app *daemonApp) Shutdown() error {
	app.shutdownOnce.Do(func() {
		app.walletMu.Lock()
		app.walletShuttingDown = true
		walletReadyCancel := app.walletReadyCancel
		walletReadyDone := app.walletReadyDone
		app.walletMu.Unlock()
		if walletReadyCancel != nil {
			walletReadyCancel()
		}
		if walletReadyDone != nil {
			<-walletReadyDone
		}

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		type shutdownResult struct {
			name string
			err  error
		}
		results := make(chan shutdownResult, len(app.services))
		shutdowns := 0
		for _, service := range app.services {
			if service.shutdown != nil {
				shutdowns++
				service := service
				go func() { results <- shutdownResult{name: service.name, err: service.shutdown(ctx)} }()
			}
		}
		for range shutdowns {
			result := <-results
			if result.err != nil && app.shutdownErr == nil {
				app.shutdownErr = fmt.Errorf("shut down %s service: %w", result.name, result.err)
			}
		}
		app.closeListeners()
		if err := app.closeBackgroundDownloader(ctx); err != nil && app.shutdownErr == nil {
			app.shutdownErr = err
		}
		if app.announcer != nil {
			app.announcer.Stop()
		}
		if app.dht != nil {
			app.dht.Stop()
		}
		if app.exchangeCancel != nil {
			app.exchangeCancel()
		}
		if err := app.closeDiskSpace(); err != nil && app.shutdownErr == nil {
			app.shutdownErr = err
		}
		if err := app.closeWalletPersistence(ctx); err != nil && app.shutdownErr == nil {
			app.shutdownErr = err
		}
		if err := app.closeBlobManager(); err != nil && app.shutdownErr == nil {
			app.shutdownErr = err
		}
		if err := app.closeDatabase(); err != nil && app.shutdownErr == nil {
			app.shutdownErr = err
		}
		if app.status != nil {
			app.status.stopAll()
		}
	})
	return app.shutdownErr
}

func (app *daemonApp) closeBackgroundDownloader(ctx context.Context) error {
	if app.background == nil {
		return nil
	}
	if err := app.background.Stop(ctx); err != nil {
		return fmt.Errorf("stop background downloader: %w", err)
	}
	if app.status != nil && !app.status.isSkipped(componentgraph.BackgroundDownloader) {
		app.status.setComponent(componentgraph.BackgroundDownloader, false)
	}
	return nil
}

func (app *daemonApp) closeDiskSpace() error {
	if app.diskSpace == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := app.diskSpace.Stop(ctx); err != nil {
		return fmt.Errorf("stop disk space manager: %w", err)
	}
	if app.status != nil && !app.status.isSkipped(componentgraph.DiskSpace) {
		app.status.setComponent(componentgraph.DiskSpace, false)
	}
	return nil
}

func (app *daemonApp) closeBlobManager() error {
	if app.blobStop == nil {
		return nil
	}
	app.blobStop()
	if app.status != nil && !app.status.isSkipped(componentgraph.BlobManager) {
		app.status.setComponent(componentgraph.BlobManager, false)
	}
	return nil
}

func (app *daemonApp) closeDatabase() error {
	if app.databaseStop == nil {
		return nil
	}
	if err := app.databaseStop(); err != nil {
		return fmt.Errorf("close daemon database: %w", err)
	}
	if app.status != nil && !app.status.isSkipped(componentgraph.Database) {
		app.status.setComponent(componentgraph.Database, false)
	}
	return nil
}

func (app *daemonApp) closeWalletPersistence(ctx context.Context) error {
	app.walletMu.RLock()
	manager := app.walletManager
	app.walletMu.RUnlock()
	if manager == nil {
		return nil
	}
	spvErr := manager.StopSPVCheckpointSync(ctx)
	persistenceErr := manager.CloseLedgersPersistence(ctx)
	if spvErr != nil {
		spvErr = fmt.Errorf("stop wallet SPV checkpoint sync: %w", spvErr)
	}
	if persistenceErr != nil {
		persistenceErr = fmt.Errorf("close wallet persistence: %w", persistenceErr)
	}
	return errors.Join(spvErr, persistenceErr)
}

func (app *daemonApp) startWalletCheckpointSync(ctx context.Context) error {
	app.walletMu.RLock()
	manager := app.walletManager
	shuttingDown := app.walletShuttingDown
	app.walletMu.RUnlock()
	if manager == nil {
		return nil
	}
	if shuttingDown {
		return errors.New("daemon shutdown has started")
	}
	return manager.StartSPVCheckpointSync(ctx)
}

type walletLogState struct {
	initialized  bool
	connected    bool
	server       string
	initialTip   bool
	fillDone     bool
	ready        bool
	networkErr   string
	addressErr   string
	fillErr      string
	lastProgress time.Time
	lastBatches  int
	lastHistory  int
}

func syncErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (app *daemonApp) startWalletReadinessMonitor() {
	app.walletMu.Lock()
	if app.walletManager == nil || app.walletReadyCancel != nil {
		app.walletMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	app.walletReadyCancel = cancel
	done := make(chan struct{})
	app.walletReadyDone = done
	started := make(chan struct{})
	app.walletStarted = started
	manager := app.walletManager
	app.walletMu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		states := make(map[string]walletLogState)
		startupResult := make(chan error, 1)
		go func() { startupResult <- manager.CompleteStartup(ctx) }()
		for {
			if manager != nil {
				ledgers := manager.OrderedLedgers()
				for _, ledger := range ledgers {
					snapshot := ledger.SPVSnapshot()
					networkSnapshot := walletspv.Snapshot{}
					if source, ok := ledger.SPVNetwork.(interface {
						IsConnected() bool
						Snapshot() walletspv.Snapshot
					}); ok {
						networkSnapshot = source.Snapshot()
					}
					state := states[ledger.ID()]
					app.logWalletProgress(ledger.ID(), snapshot, networkSnapshot, &state)
					states[ledger.ID()] = state
				}
			}
			select {
			case <-ctx.Done():
				return
			case err := <-startupResult:
				app.walletMu.Lock()
				app.walletStartErr = err
				close(started)
				app.walletMu.Unlock()
				if err != nil {
					log.Printf("Wallet: startup synchronization failed: %v", err)
				} else if app.status != nil && !app.status.isSkipped(componentgraph.Wallet) {
					app.markWalletReady()
				}
				startupResult = nil
			case <-ticker.C:
			}
		}
	}()
}

func (app *daemonApp) markWalletReady() {
	if app == nil || app.status == nil || app.status.isSkipped(componentgraph.Wallet) {
		return
	}
	app.status.setComponent(componentgraph.Wallet, true)
	if app.walletPaymentsNoOp && !app.status.isSkipped(componentgraph.WalletServerPayments) {
		app.status.setComponent(componentgraph.WalletServerPayments, true)
	}
}

func (app *daemonApp) logWalletProgress(
	ledgerID string, snapshot wallet.LedgerSPVSnapshot, network walletspv.Snapshot, state *walletLogState,
) {
	now := time.Now()
	server := ""
	if network.Server.Host != "" {
		server = network.Server.String()
	}
	if !state.initialized {
		log.Printf("Wallet (%s): connecting to an SPV server...", ledgerID)
		state.initialized = true
	}
	if network.Connected && (!state.connected || state.server != server) {
		log.Printf("Wallet (%s): connected to SPV server %s (height %d).", ledgerID, server, network.RemoteHeight)
	} else if !network.Connected && state.connected {
		log.Printf("Wallet (%s): connection to SPV server %s lost; retrying.", ledgerID, state.server)
	}
	if snapshot.InitialTipDone && !state.initialTip {
		log.Printf("Wallet (%s): headers synchronized at height %d.", ledgerID, snapshot.TipHeight)
	}
	if snapshot.FillDone && !state.fillDone && snapshot.FillErr == nil {
		log.Printf("Wallet (%s): header checkpoints verified.", ledgerID)
	}

	networkErr := syncErrorText(network.LastError)
	addressErr := syncErrorText(snapshot.AddressErr)
	fillErr := syncErrorText(snapshot.FillErr)
	logSyncErrorTransition(ledgerID, "SPV connection", state.networkErr, networkErr)
	logSyncErrorTransition(ledgerID, "address synchronization", state.addressErr, addressErr)
	logSyncErrorTransition(ledgerID, "header checkpoint synchronization", state.fillErr, fillErr)

	progressChanged := snapshot.AddressBatches != state.lastBatches || snapshot.HistoryUpdates != state.lastHistory
	if !snapshot.WalletReady && snapshot.AddressCycles > 0 && progressChanged &&
		(state.lastProgress.IsZero() || now.Sub(state.lastProgress) >= 10*time.Second) {
		log.Printf(
			"Wallet (%s): synchronizing addresses: %d subscribed, %d histories updated (%d batches).",
			ledgerID, snapshot.SubscribedAddresses, snapshot.HistoryUpdates, snapshot.AddressBatches,
		)
		state.lastProgress = now
		state.lastBatches = snapshot.AddressBatches
		state.lastHistory = snapshot.HistoryUpdates
	}
	if snapshot.WalletReady && !state.ready {
		log.Printf("Wallet (%s): synchronization complete at height %d.", ledgerID, snapshot.TipHeight)
	}

	state.connected = network.Connected
	if server != "" {
		state.server = server
	}
	state.initialTip = snapshot.InitialTipDone
	state.fillDone = snapshot.FillDone
	state.ready = snapshot.WalletReady
	state.networkErr = networkErr
	state.addressErr = addressErr
	state.fillErr = fillErr
}

func logSyncErrorTransition(ledgerID, phase, previous, current string) {
	if current != "" && current != previous {
		log.Printf("Wallet (%s): %s failed: %s", ledgerID, phase, current)
	} else if current == "" && previous != "" {
		log.Printf("Wallet (%s): %s recovered.", ledgerID, phase)
	}
}

func (app *daemonApp) closeListeners() {
	for _, service := range app.services {
		if service.listener != nil {
			_ = service.listener.Close()
		}
	}
}

func expectedServiceClose(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, http.ErrServerClosed)
}

func resolveInterfaceIP(address string) (net.IP, error) {
	if ip := net.ParseIP(address); ip != nil {
		return ip, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", address)
	if err != nil {
		return nil, fmt.Errorf("setting %q must resolve to an IP address, got %q: %w", "network_interface", address, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("setting %q must resolve to an IP address, got %q", "network_interface", address)
	}
	return addresses[0], nil
}

type componentStatus struct {
	mu              sync.RWMutex
	installationID  string
	skipped         []string
	startup         map[string]bool
	skippedSet      map[string]struct{}
	ffmpeg          map[string]any
	ffmpegProvider  func() map[string]any
	detailProviders map[string]func() map[string]any
}

func newComponentStatus(installationID string, skipped []string, ffmpeg map[string]any) *componentStatus {
	status := &componentStatus{
		installationID:  installationID,
		skipped:         append(make([]string, 0, len(skipped)), skipped...),
		startup:         make(map[string]bool),
		skippedSet:      make(map[string]struct{}, len(skipped)),
		ffmpeg:          cloneMap(ffmpeg),
		detailProviders: make(map[string]func() map[string]any),
	}
	for _, name := range skipped {
		status.skippedSet[name] = struct{}{}
	}
	for _, name := range legacyComponents {
		if !status.isSkipped(name) {
			status.startup[name] = false
		}
	}
	return status
}

func (status *componentStatus) isSkipped(name string) bool {
	_, skipped := status.skippedSet[name]
	return skipped
}

func (status *componentStatus) setComponent(name string, running bool) {
	status.mu.Lock()
	defer status.mu.Unlock()
	status.startup[name] = running
}

func (status *componentStatus) setDetailProvider(name string, provider func() map[string]any) {
	status.mu.Lock()
	defer status.mu.Unlock()
	if provider == nil || status.isSkipped(name) {
		delete(status.detailProviders, name)
		return
	}
	status.detailProviders[name] = provider
}

func (status *componentStatus) setFFmpegProvider(provider func() map[string]any) {
	status.mu.Lock()
	status.ffmpegProvider = provider
	status.mu.Unlock()
}

func (status *componentStatus) stopAll() {
	status.mu.Lock()
	defer status.mu.Unlock()
	for name := range status.startup {
		status.startup[name] = false
	}
}

func (status *componentStatus) Status() map[string]any {
	status.mu.RLock()
	startup := make(map[string]any, len(status.startup))
	isRunning := true
	for name, running := range status.startup {
		startup[name] = running
		isRunning = isRunning && running
	}
	providers := make(map[string]func() map[string]any, len(status.detailProviders))
	for name, provider := range status.detailProviders {
		providers[name] = provider
	}
	installationID := status.installationID
	skipped := append(make([]string, 0, len(status.skipped)), status.skipped...)
	ffmpeg := cloneMap(status.ffmpeg)
	ffmpegProvider := status.ffmpegProvider
	status.mu.RUnlock()
	if ffmpegProvider != nil {
		ffmpeg = cloneMap(ffmpegProvider())
	}

	result := map[string]any{
		"ffmpeg_status":      ffmpeg,
		"installation_id":    installationID,
		"is_running":         isRunning,
		"skipped_components": skipped,
		"startup_status":     startup,
	}
	for name, provider := range providers {
		if detail := provider(); len(detail) > 0 {
			result[name] = cloneMap(detail)
		}
	}
	return result
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func blobComponentStatus(manager *blob.BlobManager) map[string]any {
	return map[string]any{
		"connections":    map[string]any{},
		"finished_blobs": manager.CompletedBlobCount(),
	}
}

func dhtComponentStatus(node *dht.Node) map[string]any {
	var nodeID any
	if node != nil {
		nodeID = node.NodeIDHex()
	}
	return map[string]any{
		"node_id":                nodeID,
		"peers_in_routing_table": node.RoutingPeerCount(),
	}
}

func walletComponentStatus(manager *wallet.WalletManager) map[string]any {
	if manager == nil {
		return nil
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil
	}
	network, ok := ledger.SPVNetwork.(interface {
		Snapshot() walletspv.Snapshot
		KnownHubCount() int
	})
	if !ok {
		return nil
	}
	snapshot := network.Snapshot()
	var connected any
	servers := make([]any, 0, 1)
	if snapshot.Connected {
		connected = snapshot.Server.String()
		servers = append(servers, map[string]any{
			"host": snapshot.Server.Host, "port": snapshot.Server.Port,
			"latency": nil, "availability": true,
		})
	}
	result := map[string]any{
		"connected": connected, "connected_features": snapshot.Features,
		"servers": servers, "known_servers": network.KnownHubCount(),
		"available_servers": len(servers),
	}
	if snapshot.RemoteHeight > 0 {
		localHeight := ledger.SPVSnapshot().TipHeight
		result["headers_synchronization_progress"] = headerSynchronizationProgress(
			localHeight, snapshot.RemoteHeight,
		)
		result["blocks"] = max(localHeight, 0)
		result["blocks_behind"] = max(snapshot.RemoteHeight-localHeight, 0)
		if bestHash, err := ledger.Headers.BestHash(); err == nil {
			result["best_blockhash"] = bestHash
		} else {
			result["best_blockhash"] = nil
		}
	}
	return result
}

func headerSynchronizationProgress(localHeight, remoteHeight int) int {
	if localHeight <= 0 || remoteHeight <= 0 {
		return 0
	}
	return min((localHeight*100+remoteHeight-1)/remoteHeight, 100)
}

func defaultUPnPComponentStatus() map[string]any {
	return map[string]any{
		"aioupnp_version":   "0.0.18",
		"dht_redirect_set":  false,
		"external_ip":       nil,
		"gateway":           "No gateway found",
		"peer_redirect_set": false,
		"redirects":         map[string]any{},
	}
}

func decimalIsZero(value string) bool {
	decimal, ok := new(big.Rat).SetString(value)
	return ok && decimal.Sign() == 0
}

func fileAnalysisConfig(settings *config.Store) fileanalysis.Config {
	stringValue := func(name string) string {
		value, _ := settings.Get(name)
		text, _ := value.(string)
		return text
	}
	intValue := func(name string) int {
		value, _ := integerSetting(settings, name)
		return value
	}
	return fileanalysis.Config{
		FFmpegPath: stringValue("ffmpeg_path"), DataDir: stringValue("data_dir"),
		VideoEncoder: stringValue("video_encoder"), VideoScaler: stringValue("video_scaler"),
		AudioEncoder: stringValue("audio_encoder"), VolumeFilter: stringValue("volume_filter"),
		VideoBitrateMax:    intValue("video_bitrate_maximum"),
		VolumeAnalysisTime: intValue("volume_analysis_time"),
	}
}

func walletManagerFromSettings(settings *config.Store) (*wallet.WalletManager, error) {
	blockchainName, err := stringSetting(settings, "blockchain_name")
	if err != nil {
		return nil, err
	}
	walletDir, err := stringSetting(settings, "wallet_dir")
	if err != nil {
		return nil, err
	}
	hubTimeout, err := floatSetting(settings, "hub_timeout")
	if err != nil {
		return nil, err
	}
	concurrentHubRequests, err := integerSetting(settings, "concurrent_hub_requests")
	if err != nil {
		return nil, err
	}
	transactionCacheSize, err := integerSetting(settings, "transaction_cache_size")
	if err != nil {
		return nil, err
	}
	coinSelectionStrategy, err := stringSetting(settings, "coin_selection_strategy")
	if err != nil {
		return nil, err
	}
	defaultServers, _ := settings.Get("lbryum_servers")
	defaultServerDefaults, _ := settings.Default("lbryum_servers")
	spvServers, err := spvServersSetting(settings, "lbryum_servers")
	if err != nil {
		return nil, err
	}
	jurisdiction, _ := settings.Get("jurisdiction")
	var jurisdictionConstraint *string
	if jurisdiction != nil {
		text, ok := jurisdiction.(string)
		if !ok {
			return nil, fmt.Errorf("setting %q must resolve to a string or null, got %T", "jurisdiction", jurisdiction)
		}
		jurisdictionConstraint = &text
	}
	knownHubs, err := walletspv.OpenKnownHubs(filepath.Join(walletDir, walletspv.KnownHubsFilename))
	if err != nil {
		return nil, fmt.Errorf("load known SPV hubs: %w", err)
	}

	runtimeConfig := func() (wallet.LBRYNetConfig, error) {
		currentServers, _ := settings.Get("lbryum_servers")
		currentHubTimeout, configErr := floatSetting(settings, "hub_timeout")
		if configErr != nil {
			return wallet.LBRYNetConfig{}, configErr
		}
		currentConcurrent, configErr := integerSetting(settings, "concurrent_hub_requests")
		if configErr != nil {
			return wallet.LBRYNetConfig{}, configErr
		}
		currentJurisdiction, _ := settings.Get("jurisdiction")
		return wallet.LBRYNetConfig{
			BlockchainName: blockchainName, WalletDir: walletDir,
			HubTimeout: currentHubTimeout, DefaultServers: currentServers,
			DefaultServerDefaults: defaultServerDefaults,
			LBryumServersSet:      settings.IsSet("lbryum_servers"),
			KnownHubs:             knownHubs, Jurisdiction: currentJurisdiction,
			ConcurrentHubRequests: currentConcurrent,
		}, nil
	}
	reconfigureSPV := func(ctx context.Context, ledger *wallet.Ledger, current wallet.LBRYNetConfig) error {
		servers, configErr := spvServersSetting(settings, "lbryum_servers")
		if configErr != nil {
			return configErr
		}
		var explicit []walletspv.Server
		if current.LBryumServersSet {
			explicit = servers
		}
		var constraint *string
		if current.Jurisdiction != nil {
			text, ok := current.Jurisdiction.(string)
			if !ok {
				return fmt.Errorf("setting %q must resolve to a string or null, got %T", "jurisdiction", current.Jurisdiction)
			}
			constraint = &text
		}
		network, networkErr := walletspv.NewNetwork(walletspv.NetworkConfig{
			Servers: servers, ExplicitServers: explicit, KnownHubs: knownHubs,
			Jurisdiction: constraint, Selector: spvCandidateSelector(len(explicit) > 0),
			Client: walletspv.ClientConfig{
				RequestTimeout: time.Duration(current.HubTimeout * float64(time.Second)),
				Concurrency:    current.ConcurrentHubRequests,
			},
		})
		if networkErr != nil {
			return networkErr
		}
		return ledger.SetSPVNetwork(network)
	}
	manager, err := wallet.WalletManagerFromLBRYNetConfig(wallet.LBRYNetConfig{
		BlockchainName:            blockchainName,
		WalletDir:                 walletDir,
		Wallets:                   stringSliceSetting(settings, "wallets"),
		HubTimeout:                hubTimeout,
		DefaultServers:            defaultServers,
		KnownHubs:                 knownHubs,
		Jurisdiction:              jurisdiction,
		ConcurrentHubRequests:     concurrentHubRequests,
		TransactionCacheSize:      transactionCacheSize,
		CoinSelectionStrategy:     coinSelectionStrategy,
		DefaultServerDefaults:     defaultServerDefaults,
		LBryumServersSet:          settings.IsSet("lbryum_servers"),
		LBryumServersSetToDefault: settings.IsSetToDefault("lbryum_servers"),
		ClearLBryumServers: func() error {
			_, clearErr := settings.Clear("lbryum_servers")
			return clearErr
		},
		Reload:         runtimeConfig,
		ReconfigureSPV: reconfigureSPV,
	}, nil)
	if err != nil {
		return nil, err
	}
	for _, ledger := range manager.OrderedLedgers() {
		var explicitServers []walletspv.Server
		if settings.IsSet("lbryum_servers") {
			explicitServers = spvServers
		}
		network, networkErr := walletspv.NewNetwork(walletspv.NetworkConfig{
			Servers:         spvServers,
			ExplicitServers: explicitServers,
			KnownHubs:       knownHubs,
			Jurisdiction:    jurisdictionConstraint,
			Selector:        spvCandidateSelector(len(explicitServers) > 0),
			Client: walletspv.ClientConfig{
				RequestTimeout: time.Duration(hubTimeout * float64(time.Second)),
				Concurrency:    concurrentHubRequests,
			},
		})
		if networkErr != nil {
			return nil, networkErr
		}
		if networkErr := ledger.SetSPVNetwork(network); networkErr != nil {
			return nil, networkErr
		}
	}
	return manager, nil
}

func spvCandidateSelector(explicit bool) walletspv.CandidateSelector {
	if explicit {
		return walletspv.SequentialSelector{}
	}
	return walletspv.NewUDPSelector()
}

func prepareStartupDirectories(settings *config.Store) error {
	for _, name := range []string{"data_dir", "download_dir", "wallet_dir"} {
		path, err := stringSetting(settings, name)
		if err != nil {
			return err
		}
		if err := ensureWritableDirectory(path); err != nil {
			return fmt.Errorf("prepare setting %q: %w", name, err)
		}
	}
	return nil
}

func ensureWritableDirectory(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	testFile, err := os.CreateTemp(path, ".lbry-write-test-*")
	if err != nil {
		return fmt.Errorf("the following directory is not writable: %s", path)
	}
	testPath := testFile.Name()
	closeErr := testFile.Close()
	removeErr := os.Remove(testPath)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func installInitialHeaders(settings *config.Store, sourcePath string) error {
	walletDir, err := stringSetting(settings, "wallet_dir")
	if err != nil {
		return err
	}
	ledgerDir := filepath.Join(walletDir, "lbc_mainnet")
	if err := ensureWritableDirectory(ledgerDir); err != nil {
		return fmt.Errorf("prepare ledger directory: %w", err)
	}

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect initial headers: %w", err)
	}
	destinationPath := filepath.Join(ledgerDir, "headers")
	currentSize := int64(0)
	if destinationInfo, statErr := os.Stat(destinationPath); statErr == nil {
		currentSize = destinationInfo.Size()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect current headers: %w", statErr)
	}
	if sourceInfo.Size() <= currentSize {
		return nil
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open initial headers: %w", err)
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, sourceInfo.Mode().Perm())
	if err != nil {
		return fmt.Errorf("open headers destination: %w", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return fmt.Errorf("copy initial headers: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close headers destination: %w", err)
	}
	if err := os.Chmod(destinationPath, sourceInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("set headers permissions: %w", err)
	}
	return nil
}

func stringSetting(settings *config.Store, name string) (string, error) {
	value, exists := settings.Get(name)
	if !exists {
		return "", fmt.Errorf("missing setting %q", name)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("setting %q must resolve to a string, got %T", name, value)
	}
	return text, nil
}

func integerSetting(settings *config.Store, name string) (int, error) {
	value, exists := settings.Get(name)
	if !exists {
		return 0, fmt.Errorf("missing setting %q", name)
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case config.BigInteger:
		parsed, err := strconv.ParseInt(string(typed), 10, 0)
		if err != nil {
			return 0, fmt.Errorf("setting %q is outside the Go integer range: %w", name, err)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("setting %q must resolve to an integer, got %T", name, value)
	}
}

func floatSetting(settings *config.Store, name string) (float64, error) {
	value, exists := settings.Get(name)
	if !exists {
		return 0, fmt.Errorf("missing setting %q", name)
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("setting %q must resolve to a float, got %T", name, value)
	}
	return number, nil
}

func boolSetting(settings *config.Store, name string) (bool, error) {
	value, exists := settings.Get(name)
	if !exists {
		return false, fmt.Errorf("missing setting %q", name)
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("setting %q must resolve to a boolean, got %T", name, value)
	}
	return result, nil
}

func stringSliceSetting(settings *config.Store, name string) []string {
	value, exists := settings.Get(name)
	if !exists {
		return []string{}
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}

func serverAddressesSetting(settings *config.Store, name string) ([]string, error) {
	value, exists := settings.Get(name)
	if !exists {
		return nil, fmt.Errorf("missing setting %q", name)
	}
	servers, ok := value.([]config.Server)
	if !ok {
		return nil, fmt.Errorf("setting %q must resolve to servers, got %T", name, value)
	}
	addresses := make([]string, len(servers))
	for index, server := range servers {
		port, err := configServerPort(name, server.Port)
		if err != nil {
			return nil, err
		}
		addresses[index] = net.JoinHostPort(server.Host, strconv.Itoa(port))
	}
	return addresses, nil
}

func spvServersSetting(settings *config.Store, name string) ([]walletspv.Server, error) {
	value, exists := settings.Get(name)
	if !exists {
		return nil, fmt.Errorf("missing setting %q", name)
	}
	servers, ok := value.([]config.Server)
	if !ok {
		return nil, fmt.Errorf("setting %q must resolve to servers, got %T", name, value)
	}
	converted := make([]walletspv.Server, len(servers))
	for index, server := range servers {
		port, err := configServerPort(name, server.Port)
		if err != nil {
			return nil, err
		}
		converted[index] = walletspv.Server{Host: server.Host, Port: port}
	}
	return converted, nil
}

func configServerPort(name string, value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case config.BigInteger:
		parsed, err := strconv.ParseInt(string(typed), 10, 0)
		if err != nil {
			return 0, fmt.Errorf("setting %q contains a server port outside the Go integer range: %w", name, err)
		}
		return int(parsed), nil
	default:
		return 0, fmt.Errorf("setting %q contains a non-integer server port of type %T", name, value)
	}
}
