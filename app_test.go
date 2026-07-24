package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/componentgraph"
	"lbry/daemon/config"
	"lbry/daemon/daemonlog"
	"lbry/daemon/database"
	"lbry/daemon/dht"
	"lbry/daemon/rpc"
	"lbry/daemon/wallet"
	walletspv "lbry/daemon/wallet/spv"
)

func TestSPVCandidateSelectorConnectsExplicitServersDirectly(t *testing.T) {
	if _, ok := spvCandidateSelector(true).(walletspv.SequentialSelector); !ok {
		t.Fatalf("explicit server selector = %T, want SequentialSelector", spvCandidateSelector(true))
	}
	if _, ok := spvCandidateSelector(false).(*walletspv.UDPSelector); !ok {
		t.Fatalf("automatic server selector = %T, want *UDPSelector", spvCandidateSelector(false))
	}
}

func TestManagedStreamInfoUsesClaimSourceSizeAndMediaType(t *testing.T) {
	claim, err := wallet.BuildStreamClaim(nil, false, map[string]any{
		"file_name": "movie.mp4", "media_type": "video/custom", "file_size": "12345",
		"sd_hash": strings.Repeat("01", 48),
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := managedStreamInfo(database.ManagedFileRow{
		SerializedMetadataHex: hex.EncodeToString(claim),
	})
	if err != nil || info.Size != 12345 || info.MIMEType != "video/custom" {
		t.Fatalf("managed stream info = %#v, %v", info, err)
	}
}

func TestWalletProgressLoggingUsesReadableTransitions(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	state := walletLogState{}
	app := &daemonApp{}
	app.logWalletProgress("lbc_mainnet", wallet.LedgerSPVSnapshot{}, walletspv.Snapshot{}, &state)
	app.logWalletProgress("lbc_mainnet", wallet.LedgerSPVSnapshot{
		InitialTipDone: true, FillDone: true, AddressCycles: 1,
		TipHeight: 2089430, AddressBatches: 42, SubscribedAddresses: 800, HistoryUpdates: 750,
	}, walletspv.Snapshot{
		Connected: true, Server: walletspv.Server{Host: "hub.example", Port: 50001}, RemoteHeight: 2089430,
	}, &state)

	text := output.String()
	for _, want := range []string{
		"connecting to an SPV server", "connected to SPV server hub.example:50001",
		"headers synchronized at height 2089430", "header checkpoints verified",
		"synchronizing addresses: 800 subscribed, 750 histories updated (42 batches)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("wallet log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "connected=true") || strings.Contains(text, "network_error=") {
		t.Fatalf("wallet log retained diagnostic state dump:\n%s", text)
	}
}

func TestHeaderSynchronizationProgress(t *testing.T) {
	for _, test := range []struct {
		local, remote, want int
	}{
		{0, 100, 0}, {50, 100, 50}, {99, 100, 99},
		{100, 100, 100}, {101, 100, 100}, {1_905_330, 2_089_452, 92},
	} {
		if got := headerSynchronizationProgress(test.local, test.remote); got != test.want {
			t.Fatalf("header progress (%d, %d) = %d, want %d", test.local, test.remote, got, test.want)
		}
	}
}

func TestPrepareStartupDirectoriesAndInitialHeaders(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "config", "daemon_settings.yml"),
		DataDir:     filepath.Join(root, "data"),
		DownloadDir: filepath.Join(root, "downloads"),
		WalletDir:   filepath.Join(root, "wallet"),
	}
	settings, err := config.New(config.Options{
		Paths:       &paths,
		Environment: map[string]string{},
		InMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareStartupDirectories(settings); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.DataDir, paths.DownloadDir, paths.WalletDir} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("startup directory %s: info=%v err=%v", path, info, err)
		}
	}

	source := filepath.Join(root, "initial_headers")
	if err := os.WriteFile(source, []byte("larger headers"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := installInitialHeaders(settings, source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(paths.WalletDir, "lbc_mainnet", "headers")
	if data, err := os.ReadFile(destination); err != nil || string(data) != "larger headers" {
		t.Fatalf("installed headers = %q, %v", data, err)
	}

	if err := os.WriteFile(source, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installInitialHeaders(settings, source); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "larger headers" {
		t.Fatalf("smaller source replaced headers: %q, %v", data, err)
	}
}

func TestNewProductionAppUsesStartupArguments(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	downloadDir := filepath.Join(root, "downloads")
	walletDir := filepath.Join(root, "wallet")
	source := filepath.Join(root, "initial_headers")
	if err := os.WriteFile(source, []byte("pinned headers"), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(dataDir, "daemon_settings.yml"),
		"data_dir":           dataDir,
		"download_dir":       downloadDir,
		"network_interface":  "127.0.0.1",
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         walletDir,
	}

	app, err := newProductionApp(arguments, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	if app.dht != nil {
		t.Fatal("DHT was constructed despite components_to_skip")
	}
	if app.tracker == nil {
		t.Fatal("tracker announcer was not configured")
	}
	listeners, deferredFiles := 0, 0
	for _, service := range app.services {
		if service.listener == nil {
			if service.component == componentgraph.FileManager {
				deferredFiles++
			}
			continue
		}
		listeners++
		address, ok := service.listener.Addr().(*net.TCPAddr)
		if !ok || !address.IP.IsLoopback() || address.Port == 0 {
			t.Fatalf("%s listener address = %v", service.name, service.listener.Addr())
		}
	}
	if listeners != 2 || deferredFiles != 1 {
		t.Fatalf("services = %d listeners and %d deferred file managers", listeners, deferredFiles)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "install_id"),
		filepath.Join(walletDir, "lbc_mainnet", "headers"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("startup artifact %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "node_id")); !os.IsNotExist(err) {
		t.Fatalf("node_id exists with DHT skipped: %v", err)
	}
	status := app.status.Status()
	if _, exists := status[componentgraph.DHT]; exists {
		t.Fatalf("skipped DHT detail = %#v", status[componentgraph.DHT])
	}
	if _, exists := status[componentgraph.PeerProtocolServer]; exists {
		t.Fatalf("peer server unexpectedly exposed detail = %#v", status[componentgraph.PeerProtocolServer])
	}
	if got := status[componentgraph.BlobManager]; !reflect.DeepEqual(got, blobComponentStatus(nil)) {
		t.Fatalf("blob detail = %#v", got)
	}
	if got, want := status[componentgraph.WalletServerPayments], map[string]any{
		"max_fee": "0.0", "running": false,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wallet payment detail = %#v, want %#v", got, want)
	}
}

func TestWalletReadinessStartsDefaultNoOpPaymentComponent(t *testing.T) {
	status := newComponentStatus("id", nil, nil)
	app := newDaemonApp(status)
	app.walletPaymentsNoOp = true
	app.markWalletReady()
	startup := status.Status()["startup_status"].(map[string]any)
	if startup[componentgraph.Wallet] != true || startup[componentgraph.WalletServerPayments] != true {
		t.Fatalf("wallet readiness components = %#v", startup)
	}

	paidStatus := newComponentStatus("paid", nil, nil)
	paid := newDaemonApp(paidStatus)
	paid.markWalletReady()
	paidStartup := paidStatus.Status()["startup_status"].(map[string]any)
	if paidStartup[componentgraph.Wallet] != true || paidStartup[componentgraph.WalletServerPayments] != false {
		t.Fatalf("non-zero payment readiness components = %#v", paidStartup)
	}
}

func TestDecimalIsZero(t *testing.T) {
	for _, value := range []string{"0", "0.0", "0.00000000", "-0"} {
		if !decimalIsZero(value) {
			t.Fatalf("decimalIsZero(%q) = false", value)
		}
	}
	for _, value := range []string{"0.00000001", "1.0", "-1", "invalid"} {
		if decimalIsZero(value) {
			t.Fatalf("decimalIsZero(%q) = true", value)
		}
	}
}

func TestNewProductionAppRejectsSkippedComponentDependency(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht"},
		"config":             filepath.Join(dataDir, "daemon_settings.yml"),
		"data_dir":           dataDir,
		"download_dir":       filepath.Join(root, "downloads"),
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}

	app, err := newProductionApp(arguments, "")
	if app != nil {
		_ = app.Shutdown()
		t.Fatal("newProductionApp returned an app for an invalid component graph")
	}
	var missing *componentgraph.MissingDependenciesError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want *componentgraph.MissingDependenciesError", err, err)
	}
	want := []componentgraph.Dependency{{Component: componentgraph.HashAnnouncer, Required: componentgraph.DHT}}
	if !reflect.DeepEqual(missing.Dependencies, want) {
		t.Fatalf("missing dependencies = %#v, want %#v", missing.Dependencies, want)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid graph created startup data directory: %v", statErr)
	}
}

func TestProductionAppMigratesOlderDatabaseBeforeOpeningStore(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database.RevisionPath(dataDir), []byte("14"), 0o600); err != nil {
		t.Fatal(err)
	}
	connection, err := sql.Open("sqlite", database.SQLitePath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`CREATE TABLE blob (
        blob_hash char(96) primary key not null,
        blob_length integer not null,
        next_announce_time integer not null,
        should_announce integer not null default 0,
        status text not null,
        last_announced_time integer,
        single_announce integer
    )`); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(dataDir, "daemon_settings.yml"),
		"data_dir":           dataDir,
		"download_dir":       filepath.Join(root, "downloads"),
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	if err := app.databaseStart(); err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(database.RevisionPath(dataDir))
	if readErr != nil || string(contents) != "15" {
		t.Fatalf("database revision = %q, %v; want 15", contents, readErr)
	}
	connection, err = sql.Open("sqlite", database.SQLitePath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var count int
	if err := connection.QueryRow(`
        SELECT COUNT(*) FROM pragma_table_info('blob')
        WHERE name IN ('added_on', 'is_mine')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revision-15 blob column count = %d, %v", count, err)
	}
	if err := connection.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='peer'",
	).Scan(&count); err != nil || count != 1 {
		t.Fatalf("peer table count = %d, %v", count, err)
	}
}

func TestNewProductionAppUsesConfiguredDHTNodes(t *testing.T) {
	root := t.TempDir()
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"peer_protocol_server"},
		"config":             filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":           filepath.Join(root, "data"),
		"download_dir":       filepath.Join(root, "downloads"),
		"known_dht_nodes":    []string{"127.0.0.1:4444", "localhost:5555"},
		"network_interface":  "127.0.0.1",
		"streaming_server":   "127.0.0.1:0",
		"udp_port":           "0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	if app.dht == nil {
		t.Fatal("DHT was not constructed")
	}
	want := []string{"127.0.0.1:4444", "localhost:5555"}
	if !reflect.DeepEqual(app.dht.BootstrapNodes, want) {
		t.Fatalf("bootstrap nodes = %#v, want %#v", app.dht.BootstrapNodes, want)
	}
	status := app.status.Status()
	if got, want := status[componentgraph.DHT], dhtComponentStatus(app.dht); !reflect.DeepEqual(got, want) {
		t.Fatalf("DHT detail = %#v, want %#v", got, want)
	}
	if got, want := status[componentgraph.UPnP], defaultUPnPComponentStatus(); !reflect.DeepEqual(got, want) {
		t.Fatalf("UPnP detail = %#v, want %#v", got, want)
	}
}

func TestNewProductionAppServesConfiguredPrometheusPort(t *testing.T) {
	root := t.TempDir()
	probe, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	prometheusPort := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":           filepath.Join(root, "data"),
		"download_dir":       filepath.Join(root, "downloads"),
		"lbryum_servers":     []string{"127.0.0.1:1"},
		"prometheus_port":    prometheusPort,
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}

	var metricsAddress string
	for _, service := range app.services {
		if service.name == "metrics" {
			metricsAddress = service.listener.Addr().String()
			break
		}
	}
	if metricsAddress == "" {
		_ = app.Shutdown()
		t.Fatal("metrics service was not configured")
	}

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(ctx) }()
	endpoint := "http://127.0.0.1:" + strconv.Itoa(prometheusPort) + "/metrics"
	var response *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err = http.Get(endpoint)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		<-runResult
		t.Fatalf("metrics request failed: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("go_goroutines")) {
		cancel()
		<-runResult
		t.Fatalf("metrics status=%d body=%q error=%v", response.StatusCode, body, readErr)
	}
	cancel()
	if err := <-runResult; err != nil {
		t.Fatal(err)
	}
}

func TestProductionLoggingUsesEffectiveDataDirectory(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "effective-data")
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(root, "settings.yml"),
		"data_dir":           dataDir,
		"download_dir":       filepath.Join(root, "downloads"),
		"lbryum_servers":     []string{"127.0.0.1:1"},
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	var restored, console bytes.Buffer
	log.SetOutput(&restored)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	app, err := newProductionAppWithLogging(arguments, "", &daemonlog.Options{Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	log.Print("integrated startup message")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx); err != nil {
		t.Fatal(err)
	}
	log.Print("restored logger message")

	contents, err := os.ReadFile(filepath.Join(dataDir, "lbrynet.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(contents, []byte("integrated startup message")) || bytes.Contains(contents, []byte("restored logger message")) {
		t.Fatalf("file log = %q", contents)
	}
	if !strings.Contains(console.String(), "integrated startup message") {
		t.Fatalf("console log = %q", console.String())
	}
	if !strings.Contains(restored.String(), "restored logger message") {
		t.Fatalf("restored log = %q", restored.String())
	}
	revision, err := os.ReadFile(database.RevisionPath(dataDir))
	if err != nil || string(revision) != "15" {
		t.Fatalf("database revision = %q, %v; want 15", revision, err)
	}
	if info, err := os.Stat(database.SQLitePath(dataDir)); err != nil || info.IsDir() {
		t.Fatalf("daemon SQLite startup artifact = %#v, %v", info, err)
	}
}

func TestProductionAppOwnsWalletPersistenceWithoutClaimingReadiness(t *testing.T) {
	root := t.TempDir()
	walletDir := filepath.Join(root, "wallet")
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":           filepath.Join(root, "data"),
		"download_dir":       filepath.Join(root, "downloads"),
		"lbryum_servers":     []string{"127.0.0.1:1"},
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         walletDir,
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}
	if app.walletManager != nil {
		_ = app.Shutdown()
		t.Fatal("wallet manager was constructed before component startup")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx); err != nil {
		t.Fatal(err)
	}
	app.walletMu.RLock()
	manager := app.walletManager
	app.walletMu.RUnlock()
	if manager == nil || manager.DefaultWallet() == nil || manager.DefaultAccount() == nil {
		t.Fatalf("started manager = %#v", manager)
	}
	if manager.Running {
		t.Fatal("persistence prefix advertised the full wallet manager as running")
	}
	for _, ledger := range manager.OrderedLedgers() {
		if ledger.Database.IsOpen() {
			t.Fatalf("ledger %s database remained open after Run", ledger.ID())
		}
		if ledger.SPVNetwork == nil {
			t.Fatalf("ledger %s has no configured SPV network", ledger.ID())
		}
		if snapshot := ledger.SPVSnapshot(); snapshot.Running || !snapshot.FillDone {
			t.Fatalf("ledger %s SPV checkpoint lifecycle = %#v", ledger.ID(), snapshot)
		}
		network, ok := ledger.SPVNetwork.(*walletspv.Network)
		if !ok || network.Snapshot().Running {
			t.Fatalf("ledger %s live SPV network remained running", ledger.ID())
		}
	}
	for _, path := range []string{
		filepath.Join(walletDir, "wallets", "default_wallet"),
		filepath.Join(walletDir, "lbc_mainnet", "blockchain.db"),
		filepath.Join(walletDir, "lbc_mainnet", "headers"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("wallet persistence artifact %s: %v", path, err)
		}
	}
	headerInfo, err := os.Stat(filepath.Join(walletDir, "lbc_mainnet", "headers"))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1_243_000 * 112); headerInfo.Size() != want {
		t.Fatalf("mainnet checkpoint header size = %d, want %d", headerInfo.Size(), want)
	}
	missing := manager.OrderedLedgers()[0].Headers.MissingCheckpointedChunks()
	if len(missing) != 1_243 {
		t.Fatalf("mainnet missing checkpoint count = %d, want 1243", len(missing))
	}
	if missing[0] != 1_242_000 || missing[len(missing)-1] != 0 {
		t.Fatalf("mainnet missing checkpoint endpoints = %d..%d", missing[0], missing[len(missing)-1])
	}
	startup := app.status.Status()["startup_status"].(map[string]any)
	if startup[componentgraph.Wallet] != false {
		t.Fatalf("wallet readiness status = %#v", startup[componentgraph.Wallet])
	}
}

func TestWalletManagerFromSettingsLoadsKnownHubsAndJurisdiction(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		Config:      filepath.Join(root, "data", "daemon_settings.yml"),
		DataDir:     filepath.Join(root, "data"),
		DownloadDir: filepath.Join(root, "downloads"),
		WalletDir:   filepath.Join(root, "wallet"),
	}
	settings, err := config.New(config.Options{
		Paths:       &paths,
		Arguments:   map[string]any{"jurisdiction": "US", "lbryum_servers": []string{"default:50001"}},
		Environment: map[string]string{},
		InMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareStartupDirectories(settings); err != nil {
		t.Fatal(err)
	}
	knownPath := filepath.Join(paths.WalletDir, walletspv.KnownHubsFilename)
	if err := os.WriteFile(knownPath, []byte("known:50002:\n  country: US\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := walletManagerFromSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	ledgers := manager.OrderedLedgers()
	if len(ledgers) != 1 {
		t.Fatalf("ledgers = %d", len(ledgers))
	}
	known, ok := ledgers[0].Config["known_hubs"].(*walletspv.KnownHubs)
	if !ok || known.Path() != knownPath || known.Len() != 1 || known.Snapshot()[0].Server.Host != "known" {
		t.Fatalf("configured known hubs = %#v", ledgers[0].Config["known_hubs"])
	}
	if jurisdiction := ledgers[0].Config["jurisdiction"]; jurisdiction != "US" {
		t.Fatalf("configured jurisdiction = %#v", jurisdiction)
	}
	if _, ok := ledgers[0].SPVNetwork.(*walletspv.Network); !ok {
		t.Fatalf("configured SPV network = %T", ledgers[0].SPVNetwork)
	}
}

func TestProductionAppGatesPeerProtocolUntilWalletReadiness(t *testing.T) {
	root := t.TempDir()
	arguments := map[string]any{
		"api":               "127.0.0.1:0",
		"config":            filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":          filepath.Join(root, "data"),
		"download_dir":      filepath.Join(root, "downloads"),
		"network_interface": "127.0.0.1",
		"streaming_server":  "127.0.0.1:0",
		"tcp_port":          0,
		"udp_port":          0,
		"wallet_dir":        filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	foundPeer := false
	foundFileManager := false
	for _, service := range app.services {
		if service.component == componentgraph.PeerProtocolServer || service.name == "peer" {
			foundPeer = true
			if service.listener != nil {
				t.Fatalf("peer listener bound before wallet readiness: %#v", service.listener)
			}
		}
		if service.component == componentgraph.FileManager {
			foundFileManager = true
			if service.listener != nil {
				t.Fatalf("file manager exposed a listener before wallet readiness: %#v", service.listener)
			}
		}
	}
	if !foundPeer {
		t.Fatal("deferred peer service was not configured")
	}
	if !foundFileManager {
		t.Fatal("deferred file manager service was not configured")
	}
	startup := app.status.Status()["startup_status"].(map[string]any)
	if startup[componentgraph.PeerProtocolServer] != false {
		t.Fatalf("peer readiness status = %#v", startup[componentgraph.PeerProtocolServer])
	}
	if startup[componentgraph.FileManager] != false {
		t.Fatalf("file manager readiness status = %#v", startup[componentgraph.FileManager])
	}
}

func TestProductionAppDefersWalletConfigurationFailureToRun(t *testing.T) {
	root := t.TempDir()
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"blockchain_name":    "unknown_chain",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":           filepath.Join(root, "data"),
		"download_dir":       filepath.Join(root, "downloads"),
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	err = app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), `unknown blockchain name "unknown_chain"`) {
		t.Fatalf("Run error = %v", err)
	}
	for _, service := range app.services {
		if service.listener == nil {
			continue
		}
		if connection, dialErr := net.DialTimeout("tcp", service.listener.Addr().String(), 25*time.Millisecond); dialErr == nil {
			connection.Close()
			t.Fatalf("%s listener remained open after wallet configuration failure", service.name)
		}
	}
}

func TestRPCStopReturnsResponseBeforeAppShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	status := newComponentStatus("stable-install-id", []string{"wallet"}, map[string]any{
		"available":            false,
		"which":                nil,
		"analyze_audio_volume": true,
	})
	app := newDaemonApp(status)
	server := rpc.CreateServer(rpc.WithStatusProvider(status), rpc.WithShutdown(app.RequestStop))
	app.services = []managedService{{
		name:     "rpc",
		listener: listener,
		serve:    server.Serve,
		shutdown: server.Shutdown,
	}}

	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(context.Background()) }()
	endpoint := "http://" + listener.Addr().String() + "/"

	statusPayload := postRPC(t, endpoint, `{"method":"status"}`)
	statusResult := statusPayload["result"].(map[string]any)
	if statusResult["installation_id"] != "stable-install-id" || statusResult["is_running"] != false {
		t.Fatalf("status result = %#v", statusResult)
	}
	if skipped := statusResult["skipped_components"].([]any); len(skipped) != 1 || skipped[0] != "wallet" {
		t.Fatalf("skipped components = %#v", statusResult["skipped_components"])
	}
	startup := statusResult["startup_status"].(map[string]any)
	if _, exists := startup["wallet"]; exists {
		t.Fatalf("skipped wallet present in startup status: %#v", startup)
	}
	if startup["database"] != false {
		t.Fatalf("unimplemented database status = %#v", startup["database"])
	}

	stopPayload := postRPC(t, endpoint, `{"method":"stop"}`)
	if stopPayload["result"] != "Shutting down" {
		t.Fatalf("stop result = %#v", stopPayload["result"])
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after RPC stop")
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond)
	if err == nil {
		connection.Close()
		t.Fatal("RPC listener remained open after shutdown")
	}
}

func TestConcurrentStopRequestsAreIdempotent(t *testing.T) {
	app := newDaemonApp(newComponentStatus("id", nil, nil))
	var callers sync.WaitGroup
	for range 64 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			app.RequestStop()
		}()
	}
	callers.Wait()
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseComponentTracksRunLifecycle(t *testing.T) {
	status := newComponentStatus("id", nil, nil)
	app := newDaemonApp(status)
	started := make(chan struct{})
	app.databaseStart = func() error {
		close(started)
		return nil
	}
	app.databaseStop = func() error { return nil }

	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("database did not start")
	}
	deadline := time.Now().Add(time.Second)
	var startup map[string]any
	for time.Now().Before(deadline) {
		startup = status.Status()["startup_status"].(map[string]any)
		if startup[componentgraph.Database] == true {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if startup[componentgraph.Database] != true {
		t.Fatalf("database status while open = %#v", startup[componentgraph.Database])
	}

	app.RequestStop()
	if err := <-runResult; err != nil {
		t.Fatal(err)
	}
	startup = status.Status()["startup_status"].(map[string]any)
	if startup[componentgraph.Database] != false {
		t.Fatalf("database status after close = %#v", startup[componentgraph.Database])
	}
}

func TestProductionBlobAndDiskSpaceComponentsTrackRunLifecycle(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	app, err := newProductionApp(map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(dataDir, "daemon_settings.yml"),
		"data_dir":           dataDir,
		"download_dir":       filepath.Join(root, "downloads"),
		"lbryum_servers":     []string{"127.0.0.1:1"},
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		startup := app.status.Status()["startup_status"].(map[string]any)
		if startup[componentgraph.BlobManager] == true && startup[componentgraph.DiskSpace] == true &&
			startup[componentgraph.BackgroundDownloader] == true {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-runResult
			t.Fatalf("component startup status = %#v", startup)
		}
		time.Sleep(time.Millisecond)
	}
	if info, err := os.Stat(filepath.Join(dataDir, "blobfiles")); err != nil || !info.IsDir() {
		t.Fatalf("blobfiles directory = %#v, %v", info, err)
	}
	detail := app.status.Status()[componentgraph.DiskSpace].(map[string]any)
	if detail["running"] != true || detail["total_used_mb"] != int64(0) {
		t.Fatalf("disk space detail = %#v", detail)
	}
	backgroundDetail := app.status.Status()[componentgraph.BackgroundDownloader].(map[string]any)
	if backgroundDetail["running"] != false || backgroundDetail["available_free_space_mb"] != nil ||
		backgroundDetail["ongoing_download"] != false {
		t.Fatalf("inert background downloader detail = %#v", backgroundDetail)
	}
	cancel()
	if err := <-runResult; err != nil {
		t.Fatal(err)
	}
	startup := app.status.Status()["startup_status"].(map[string]any)
	if startup[componentgraph.BlobManager] != false || startup[componentgraph.DiskSpace] != false ||
		startup[componentgraph.BackgroundDownloader] != false {
		t.Fatalf("component shutdown status = %#v", startup)
	}
}

func TestUnexpectedServiceExitStopsSiblings(t *testing.T) {
	wantErr := errors.New("serve failed")
	closed := make(chan struct{})
	siblingListener := &channelListener{closed: closed}
	app := newDaemonApp(nil)
	app.services = []managedService{
		{
			name:     "failed",
			listener: inertListener{},
			serve:    func(net.Listener) error { return wantErr },
		},
		{
			name:     "sibling",
			listener: siblingListener,
			serve: func(listener net.Listener) error {
				_, err := listener.Accept()
				return err
			},
		},
	}
	err := app.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	select {
	case <-closed:
	default:
		t.Fatal("sibling listener was not closed")
	}
}

func TestCleanServiceExitIsUnexpected(t *testing.T) {
	app := newDaemonApp(nil)
	app.services = []managedService{{
		name:     "exited",
		listener: inertListener{},
		serve:    func(net.Listener) error { return nil },
	}}
	if err := app.Run(context.Background()); err == nil || err.Error() != "exited service stopped unexpectedly" {
		t.Fatalf("Run error = %v", err)
	}
}

func TestResolveInterfaceIPAcceptsHostnames(t *testing.T) {
	ip, err := resolveInterfaceIP("localhost")
	if err != nil {
		t.Fatal(err)
	}
	if !ip.IsLoopback() || ip.To4() == nil {
		t.Fatalf("localhost resolved to %v", ip)
	}
}

func TestComponentStatusReturnsIndependentSnapshots(t *testing.T) {
	status := newComponentStatus("id", []string{"wallet"}, map[string]any{"available": false})
	status.setComponent("dht", true)
	first := status.Status()
	first["installation_id"] = "changed"
	first["startup_status"].(map[string]any)["dht"] = false
	first["skipped_components"].([]string)[0] = "changed"
	first["ffmpeg_status"].(map[string]any)["available"] = true
	second := status.Status()
	if second["installation_id"] != "id" || second["is_running"] != false {
		t.Fatalf("status mutated through snapshot: %#v", second)
	}
	if second["startup_status"].(map[string]any)["dht"] != true || second["skipped_components"].([]string)[0] != "wallet" {
		t.Fatalf("nested status mutated through snapshot: %#v", second)
	}
	if second["ffmpeg_status"].(map[string]any)["available"] != false {
		t.Fatalf("ffmpeg status mutated through snapshot: %#v", second)
	}
}

func TestComponentStatusKeepsEmptySkippedComponentsAsArray(t *testing.T) {
	status := newComponentStatus("id", nil, map[string]any{})
	encoded, err := json.Marshal(status.Status())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"skipped_components":[]`)) {
		t.Fatalf("status JSON = %s, want an empty skipped_components array", encoded)
	}
}

func TestComponentStatusIncludesUnskippedDetailProviders(t *testing.T) {
	status := newComponentStatus("id", []string{"wallet"}, map[string]any{"available": false})
	status.setDetailProvider("dht", func() map[string]any {
		return map[string]any{"node_id": "abc", "peers_in_routing_table": 3}
	})
	status.setDetailProvider("wallet", func() map[string]any {
		return map[string]any{"connected": true}
	})
	status.setDetailProvider("blob_manager", func() map[string]any { return nil })

	result := status.Status()
	if !reflect.DeepEqual(result["dht"], map[string]any{"node_id": "abc", "peers_in_routing_table": 3}) {
		t.Fatalf("dht detail = %#v", result["dht"])
	}
	if _, exists := result["wallet"]; exists {
		t.Fatalf("skipped wallet detail = %#v", result["wallet"])
	}
	if _, exists := result["blob_manager"]; exists {
		t.Fatalf("empty blob detail = %#v", result["blob_manager"])
	}
}

func TestLegacyComponentDetailStatusShapes(t *testing.T) {
	manager := blob.NewManager()
	data := []byte("one")
	digest := sha512.Sum384(data)
	if err := manager.Set(hex.EncodeToString(digest[:]), data, false); err != nil {
		t.Fatal(err)
	}
	if got, want := blobComponentStatus(manager), map[string]any{
		"connections": map[string]any{}, "finished_blobs": 1,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("blob detail = %#v, want %#v", got, want)
	}

	if got, want := dhtComponentStatus(nil), map[string]any{
		"node_id": nil, "peers_in_routing_table": 0,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unstarted DHT detail = %#v, want %#v", got, want)
	}
	var nodeID [dht.HashSize]byte
	for index := range nodeID {
		nodeID[index] = byte(index + 1)
	}
	node, err := dht.NewNodeWithID(0, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dhtComponentStatus(node), map[string]any{
		"node_id": node.NodeIDHex(), "peers_in_routing_table": 0,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("constructed DHT detail = %#v, want %#v", got, want)
	}

	wantUPnP := map[string]any{
		"aioupnp_version": "0.0.18", "redirects": map[string]any{},
		"gateway": "No gateway found", "dht_redirect_set": false,
		"peer_redirect_set": false, "external_ip": nil,
	}
	if got := defaultUPnPComponentStatus(); !reflect.DeepEqual(got, wantUPnP) {
		t.Fatalf("UPnP detail = %#v, want %#v", got, wantUPnP)
	}
}

func postRPC(t *testing.T, endpoint, body string) map[string]any {
	t.Helper()
	response, err := http.Post(endpoint, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status = %d: %s", response.StatusCode, data)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode response: %v\n%s", err, data)
	}
	return payload
}

type inertListener struct{}

func (inertListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (inertListener) Close() error              { return nil }
func (inertListener) Addr() net.Addr            { return testAddress("inert") }

type channelListener struct {
	closed chan struct{}
	once   sync.Once
}

func (listener *channelListener) Accept() (net.Conn, error) {
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *channelListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*channelListener) Addr() net.Addr { return testAddress("channel") }

type testAddress string

func (address testAddress) Network() string { return string(address) }
func (address testAddress) String() string  { return string(address) }
