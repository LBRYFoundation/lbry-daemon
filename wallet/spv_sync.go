package wallet

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrLedgerSPVUnavailable = errors.New("ledger SPV network is unavailable")
	ErrLedgerSPVRunning     = errors.New("ledger SPV checkpoint synchronization is running")
	ErrLedgerSPVStopped     = errors.New("ledger SPV synchronization stopped before readiness")
)

type LedgerSPVNetwork interface {
	SPVHeaderRPC
	Start(context.Context) error
	Stop(context.Context) error
	RemoteHeight() int
}

type LedgerSPVHeaderSource interface {
	SetHeaderNotificationHandler(func(context.Context, any))
}

type LedgerSPVAddressSource interface {
	IsConnected() bool
	SetAddressNotificationHandler(func(context.Context, any))
	SetConnectedHandler(func(context.Context))
	SubscribeAddresses(context.Context, []string) ([]any, error)
	RetriableValue(context.Context, string, []any, bool) (any, error)
}

type spvHeaderEnvelope struct {
	update SPVLiveHeaderUpdate
	err    error
}

type spvAddressEnvelope struct {
	connected bool
	params    any
	manager   *AddressManager
}

type ledgerSPVSync struct {
	mu       sync.Mutex
	headerMu sync.Mutex

	running          bool
	context          context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	fillErr          error
	tipUpdates       chan spvHeaderEnvelope
	tipDone          chan struct{}
	initialTipDone   bool
	tipErr           error
	tipHeight        int
	tipChange        int
	rejectedHeaders  int
	addressUpdates   *spvAddressQueue
	addressDone      chan struct{}
	addressErr       error
	addressCycles    int
	addressBatches   int
	addressCount     int
	generatedCount   int
	historyUpdates   int
	addressSyncing   bool
	pendingHistories int
	addressRequired  bool
	ready            bool
	readyDone        chan struct{}
	readySignaled    bool
	generation       uint64
}

type LedgerSPVSnapshot struct {
	Running             bool
	FillDone            bool
	FillErr             error
	InitialTipDone      bool
	TipWorkerDone       bool
	TipErr              error
	TipHeight           int
	TipChange           int
	RejectedHeaders     int
	AddressWorkerDone   bool
	AddressErr          error
	AddressCycles       int
	AddressBatches      int
	SubscribedAddresses int
	GeneratedAddresses  int
	HistoryUpdates      int
	AddressSyncing      bool
	OutOfSyncAddresses  int
	WalletReady         bool
	UpdateTasks         int
	PendingHistories    int
}

func (ledger *Ledger) SetSPVNetwork(network LedgerSPVNetwork) error {
	if ledger == nil {
		return errors.New("ledger is nil")
	}
	if ledger.Headers == nil {
		return errors.New("ledger headers are nil")
	}
	var getter HeaderChunkGetter
	var err error
	if network != nil {
		getter, err = NewSPVHeaderChunkGetter(network, network.RemoteHeight)
		if err != nil {
			return err
		}
	}
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.running {
		ledger.spvSync.mu.Unlock()
		return ErrLedgerSPVRunning
	}
	previous := ledger.SPVNetwork
	ledger.SPVNetwork = network
	ledger.Headers.SetChunkGetter(getter)
	ledger.spvSync.mu.Unlock()
	if source, ok := previous.(LedgerSPVHeaderSource); ok {
		source.SetHeaderNotificationHandler(nil)
	}
	if source, ok := previous.(LedgerSPVAddressSource); ok {
		source.SetAddressNotificationHandler(nil)
		source.SetConnectedHandler(nil)
	}
	if source, ok := network.(LedgerSPVHeaderSource); ok {
		source.SetHeaderNotificationHandler(ledger.enqueueSPVHeader)
	}
	if source, ok := network.(LedgerSPVAddressSource); ok {
		source.SetAddressNotificationHandler(ledger.enqueueSPVAddress)
		source.SetConnectedHandler(ledger.enqueueSPVConnected)
	}
	return nil
}

// StartSPVCheckpointSync starts the live network, performs the initial tip pull,
// fills general missing checkpoints, and restores address subscriptions on each
// connection. Transaction-backed histories and wallet readiness remain gated.
func (ledger *Ledger) StartSPVCheckpointSync(ctx context.Context) error {
	if ledger == nil {
		return errors.New("ledger is nil")
	}
	if ctx == nil {
		return errors.New("ledger SPV context is nil")
	}
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.running {
		ledger.spvSync.mu.Unlock()
		return nil
	}
	network := ledger.SPVNetwork
	if network == nil {
		ledger.spvSync.mu.Unlock()
		return ErrLedgerSPVUnavailable
	}
	if ledger.Headers == nil {
		ledger.spvSync.mu.Unlock()
		return errors.New("ledger headers are nil")
	}
	ledger.Headers.mu.RLock()
	headersOpen := ledger.Headers.opened
	ledger.Headers.mu.RUnlock()
	if !headersOpen {
		ledger.spvSync.mu.Unlock()
		return ErrHeadersNotOpen
	}
	syncCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	_, hasTipSource := network.(LedgerSPVHeaderSource)
	_, hasAddressSource := network.(LedgerSPVAddressSource)
	hasAddressSource = hasAddressSource && len(ledger.AccountsSnapshot()) > 0
	var tipUpdates chan spvHeaderEnvelope
	var tipDone chan struct{}
	var initialTipDone chan struct{}
	var tipLockAcquired chan struct{}
	if hasTipSource {
		tipUpdates = make(chan spvHeaderEnvelope, 64)
		tipDone = make(chan struct{})
		initialTipDone = make(chan struct{})
		tipLockAcquired = make(chan struct{})
	}
	var addressUpdates *spvAddressQueue
	var addressDone chan struct{}
	if hasAddressSource {
		addressUpdates = newSPVAddressQueue()
		addressDone = make(chan struct{})
	}
	ledger.spvSync.generation++
	generation := ledger.spvSync.generation
	ledger.spvSync.running = true
	ledger.spvSync.context = syncCtx
	ledger.spvSync.cancel = cancel
	ledger.spvSync.done = done
	ledger.spvSync.fillErr = nil
	ledger.spvSync.tipUpdates = tipUpdates
	ledger.spvSync.tipDone = tipDone
	ledger.spvSync.initialTipDone = !hasTipSource
	ledger.spvSync.tipErr = nil
	ledger.spvSync.tipHeight = ledger.Headers.Height()
	ledger.spvSync.tipChange = 0
	ledger.spvSync.rejectedHeaders = 0
	ledger.spvSync.addressUpdates = addressUpdates
	ledger.spvSync.addressDone = addressDone
	ledger.spvSync.addressErr = nil
	ledger.spvSync.addressCycles = 0
	ledger.spvSync.addressBatches = 0
	ledger.spvSync.addressCount = 0
	ledger.spvSync.generatedCount = 0
	ledger.spvSync.historyUpdates = 0
	ledger.spvSync.addressSyncing = false
	ledger.spvSync.pendingHistories = 0
	ledger.spvSync.addressRequired = hasAddressSource
	ledger.spvSync.ready = false
	ledger.spvSync.readyDone = make(chan struct{})
	ledger.spvSync.readySignaled = false
	if err := network.Start(syncCtx); err != nil {
		cancel()
		ledger.spvSync.running = false
		ledger.spvSync.context = nil
		ledger.spvSync.cancel = nil
		ledger.spvSync.done = nil
		ledger.spvSync.tipUpdates = nil
		ledger.spvSync.tipDone = nil
		ledger.spvSync.addressUpdates = nil
		ledger.spvSync.addressDone = nil
		ledger.signalReadyWaitLocked(false)
		ledger.spvSync.mu.Unlock()
		return err
	}
	ledger.maybeMarkReadyLocked()
	ledger.spvSync.mu.Unlock()
	if hasTipSource {
		go ledger.runSPVTipWorker(
			syncCtx, generation, network, tipUpdates, tipDone, initialTipDone, tipLockAcquired,
		)
		<-tipLockAcquired
	}
	if hasAddressSource {
		go ledger.runSPVAddressWorker(
			syncCtx, generation, network.(LedgerSPVAddressSource),
			addressUpdates, addressDone, initialTipDone,
		)
	}
	go ledger.runSPVCheckpointFill(syncCtx, generation, done, initialTipDone, hasTipSource)
	return nil
}

func (ledger *Ledger) enqueueSPVHeader(_ context.Context, params any) {
	envelope := spvHeaderEnvelope{}
	envelope.update, envelope.err = parseSPVLiveHeaderUpdate(params)
	ledger.spvSync.mu.Lock()
	if !ledger.spvSync.running || ledger.spvSync.tipUpdates == nil || ledger.spvSync.context == nil {
		ledger.spvSync.mu.Unlock()
		return
	}
	updates := ledger.spvSync.tipUpdates
	syncCtx := ledger.spvSync.context
	ledger.spvSync.mu.Unlock()
	select {
	case updates <- envelope:
	case <-syncCtx.Done():
	}
}

func (ledger *Ledger) runSPVTipWorker(
	ctx context.Context,
	generation uint64,
	network LedgerSPVNetwork,
	updates <-chan spvHeaderEnvelope,
	done chan<- struct{},
	initialDone chan<- struct{},
	lockAcquired chan<- struct{},
) {
	defer close(done)
	ledger.spvSync.headerMu.Lock()
	close(lockAcquired)
	err := ledger.updateSPVTip(ctx, generation, network, nil)
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.initialTipDone = true
		ledger.spvSync.tipErr = err
		ledger.maybeMarkReadyLocked()
	}
	ledger.spvSync.mu.Unlock()
	close(initialDone)
	ledger.spvSync.headerMu.Unlock()

	for {
		select {
		case envelope := <-updates:
			ledger.spvSync.headerMu.Lock()
			if envelope.err != nil {
				err = envelope.err
			} else {
				err = ledger.updateSPVTip(ctx, generation, network, &envelope.update)
			}
			ledger.spvSync.headerMu.Unlock()
			ledger.spvSync.mu.Lock()
			if ledger.spvSync.generation == generation {
				ledger.spvSync.tipErr = err
			}
			ledger.spvSync.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func (ledger *Ledger) updateSPVTip(
	ctx context.Context, generation uint64, network LedgerSPVNetwork, update *SPVLiveHeaderUpdate,
) error {
	hooks := spvLiveHeaderHooks{
		onAdded: func(height, change int) {
			ledger.spvSync.mu.Lock()
			if ledger.spvSync.generation == generation {
				ledger.spvSync.tipHeight = height
				ledger.spvSync.tipChange = change
			}
			ledger.spvSync.mu.Unlock()
		},
		onRejected: func() {
			ledger.clearTransactionCache()
			ledger.spvSync.mu.Lock()
			if ledger.spvSync.generation == generation {
				ledger.spvSync.rejectedHeaders++
			}
			ledger.spvSync.mu.Unlock()
		},
		onRewind: func(rewindCtx context.Context, height int) error {
			if ledger.Database == nil {
				return nil
			}
			return ledger.Database.RewindBlockchain(rewindCtx, height)
		},
	}
	return updateSPVLiveHeaders(ctx, ledger.Headers, network, ledger.ID(), update, hooks)
}

func (ledger *Ledger) runSPVCheckpointFill(
	ctx context.Context,
	generation uint64,
	done chan<- struct{},
	initialTipDone <-chan struct{},
	serialized bool,
) {
	defer close(done)
	var err error
	if initialTipDone != nil {
		select {
		case <-initialTipDone:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err == nil {
		if serialized {
			for _, height := range ledger.Headers.MissingCheckpointedChunks() {
				ledger.spvSync.headerMu.Lock()
				err = ledger.Headers.EnsureChunkAt(ctx, height)
				ledger.spvSync.headerMu.Unlock()
				if err != nil {
					break
				}
			}
		} else {
			err = ledger.Headers.FillMissingCheckpoints(ctx)
		}
	}
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.fillErr = err
	}
	ledger.spvSync.mu.Unlock()
}

func (ledger *Ledger) StopSPVCheckpointSync(ctx context.Context) error {
	if ledger == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("ledger SPV stop context is nil")
	}
	ledger.spvSync.mu.Lock()
	if !ledger.spvSync.running {
		ledger.spvSync.mu.Unlock()
		return nil
	}
	cancel := ledger.spvSync.cancel
	done := ledger.spvSync.done
	tipDone := ledger.spvSync.tipDone
	addressDone := ledger.spvSync.addressDone
	generation := ledger.spvSync.generation
	network := ledger.SPVNetwork
	ledger.spvSync.running = false
	ledger.signalReadyWaitLocked(false)
	ledger.spvSync.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var stopErr error
	if network != nil {
		stopErr = network.Stop(ctx)
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			stopErr = errors.Join(stopErr, ctx.Err())
		}
	}
	if tipDone != nil {
		select {
		case <-tipDone:
		case <-ctx.Done():
			stopErr = errors.Join(stopErr, ctx.Err())
		}
	}
	if addressDone != nil {
		select {
		case <-addressDone:
		case <-ctx.Done():
			stopErr = errors.Join(stopErr, ctx.Err())
		}
	}
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.context = nil
		ledger.spvSync.cancel = nil
		ledger.spvSync.tipUpdates = nil
		ledger.spvSync.addressUpdates = nil
	}
	ledger.spvSync.mu.Unlock()
	return stopErr
}

func (ledger *Ledger) SPVSnapshot() LedgerSPVSnapshot {
	if ledger == nil {
		return LedgerSPVSnapshot{}
	}
	ledger.spvSync.mu.Lock()
	defer ledger.spvSync.mu.Unlock()
	snapshot := LedgerSPVSnapshot{
		Running:             ledger.spvSync.running,
		FillErr:             ledger.spvSync.fillErr,
		InitialTipDone:      ledger.spvSync.initialTipDone,
		TipErr:              ledger.spvSync.tipErr,
		TipHeight:           ledger.spvSync.tipHeight,
		TipChange:           ledger.spvSync.tipChange,
		RejectedHeaders:     ledger.spvSync.rejectedHeaders,
		AddressErr:          ledger.spvSync.addressErr,
		AddressCycles:       ledger.spvSync.addressCycles,
		AddressBatches:      ledger.spvSync.addressBatches,
		SubscribedAddresses: ledger.spvSync.addressCount,
		GeneratedAddresses:  ledger.spvSync.generatedCount,
		HistoryUpdates:      ledger.spvSync.historyUpdates,
		AddressSyncing:      ledger.spvSync.addressSyncing,
		PendingHistories:    ledger.spvSync.pendingHistories,
		OutOfSyncAddresses:  ledger.addressOutOfSyncCount(),
		WalletReady:         ledger.spvSync.ready,
	}
	if ledger.spvSync.done != nil {
		select {
		case <-ledger.spvSync.done:
			snapshot.FillDone = true
		default:
		}
	}
	if ledger.spvSync.tipDone != nil {
		select {
		case <-ledger.spvSync.tipDone:
			snapshot.TipWorkerDone = true
		default:
		}
	}
	if ledger.spvSync.addressDone != nil {
		select {
		case <-ledger.spvSync.addressDone:
			snapshot.AddressWorkerDone = true
		default:
		}
	}
	if !snapshot.InitialTipDone {
		snapshot.UpdateTasks++
	}
	if snapshot.AddressSyncing && snapshot.PendingHistories == 0 {
		snapshot.UpdateTasks++
	}
	snapshot.UpdateTasks += snapshot.PendingHistories
	return snapshot
}

func (ledger *Ledger) maybeMarkReadyLocked() {
	if !ledger.spvSync.running || ledger.spvSync.ready || !ledger.spvSync.initialTipDone ||
		ledger.spvSync.tipErr != nil ||
		(ledger.spvSync.addressRequired && ledger.spvSync.addressCycles == 0) {
		return
	}
	ledger.spvSync.ready = true
	ledger.signalReadyWaitLocked(true)
}

func (ledger *Ledger) signalReadyWaitLocked(ready bool) {
	if ready {
		ledger.spvSync.ready = true
	}
	if ledger.spvSync.readyDone != nil && !ledger.spvSync.readySignaled {
		close(ledger.spvSync.readyDone)
		ledger.spvSync.readySignaled = true
	}
}

// WaitSPVReady waits for the generation's one-shot Python on_ready boundary.
func (ledger *Ledger) WaitSPVReady(ctx context.Context) error {
	if ledger == nil {
		return errors.New("ledger is nil")
	}
	if ctx == nil {
		return errors.New("ledger readiness context is nil")
	}
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.ready {
		ledger.spvSync.mu.Unlock()
		return nil
	}
	if !ledger.spvSync.running || ledger.spvSync.readyDone == nil {
		ledger.spvSync.mu.Unlock()
		return ErrLedgerSPVStopped
	}
	readyDone := ledger.spvSync.readyDone
	ledger.spvSync.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-readyDone:
	}
	ledger.spvSync.mu.Lock()
	ready := ledger.spvSync.ready
	ledger.spvSync.mu.Unlock()
	if !ready {
		return ErrLedgerSPVStopped
	}
	return nil
}

func (manager *WalletManager) StartSPVCheckpointSync(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("wallet SPV context is nil")
	}
	manager.lifecycleMu.Lock()
	manager.Running = true
	manager.lifecycleMu.Unlock()
	var startErr error
	for _, ledger := range manager.OrderedLedgers() {
		if err := ledger.StartSPVCheckpointSync(ctx); err != nil {
			startErr = errors.Join(startErr, fmt.Errorf("start ledger %s SPV checkpoint sync: %w", ledger.ID(), err))
		}
	}
	return startErr
}

func (manager *WalletManager) StopSPVCheckpointSync(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("wallet SPV stop context is nil")
	}
	var stopErr error
	for _, ledger := range manager.OrderedLedgers() {
		if err := ledger.StopSPVCheckpointSync(ctx); err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("stop ledger %s SPV checkpoint sync: %w", ledger.ID(), err))
		}
	}
	manager.lifecycleMu.Lock()
	manager.Running = false
	manager.lifecycleMu.Unlock()
	return stopErr
}

func (manager *WalletManager) IsRunning() bool {
	if manager == nil {
		return false
	}
	manager.lifecycleMu.RLock()
	defer manager.lifecycleMu.RUnlock()
	return manager.Running
}

func (manager *WalletManager) Ready() bool {
	if manager == nil || !manager.IsRunning() {
		return false
	}
	ledgers := manager.OrderedLedgers()
	if len(ledgers) == 0 {
		return false
	}
	for _, ledger := range ledgers {
		if !ledger.SPVSnapshot().WalletReady {
			return false
		}
	}
	return true
}

func (manager *WalletManager) WaitReady(ctx context.Context) error {
	if manager == nil {
		return errors.New("wallet manager is nil")
	}
	if ctx == nil {
		return errors.New("wallet readiness context is nil")
	}
	ledgers := manager.OrderedLedgers()
	if len(ledgers) == 0 {
		return ErrDefaultWalletMissing
	}
	for _, ledger := range ledgers {
		if err := ledger.WaitSPVReady(ctx); err != nil {
			return fmt.Errorf("wait for ledger %s readiness: %w", ledger.ID(), err)
		}
	}
	return nil
}

// CompleteStartup runs the post-ready operations from Python Ledger.start.
func (manager *WalletManager) CompleteStartup(ctx context.Context) error {
	if err := manager.WaitReady(ctx); err != nil {
		return err
	}
	for _, ledger := range manager.OrderedLedgers() {
		if err := ledger.ReleaseAllOutputs(ctx); err != nil {
			return fmt.Errorf("release ledger %s reserved outputs: %w", ledger.ID(), err)
		}
	}
	for _, account := range manager.Accounts() {
		if _, err := account.MigrateChannelKeys(); err != nil {
			return err
		}
		if _, err := account.SaveMaxGap(ctx); err != nil {
			return err
		}
	}
	return nil
}
