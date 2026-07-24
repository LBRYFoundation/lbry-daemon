package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"lbry/daemon/wallet/keys"
)

const (
	SPVAddressSubscriptionBatchSize = 1000
	SPVAddressSubscribeMethod       = "blockchain.address.subscribe"
	SPVAddressHistoryMethod         = "blockchain.address.get_history"
)

var (
	ErrInvalidAddressStatus  = errors.New("invalid SPV address status")
	ErrTransactionIDMismatch = errors.New("fetched transaction ID does not match the requested ID")
	ErrInvalidWalletAddress  = errors.New("invalid wallet address")
)

type AddressHistoryEntry struct {
	TxHash string
	Height int64
}

type TransactionIDMismatchError struct {
	Requested string
	Computed  string
}

func (err *TransactionIDMismatchError) Error() string {
	if err == nil {
		return ErrTransactionIDMismatch.Error()
	}
	return fmt.Sprintf(
		"%s: requested %s, computed %s",
		ErrTransactionIDMismatch, err.Requested, err.Computed,
	)
}

func (err *TransactionIDMismatchError) Unwrap() error {
	return ErrTransactionIDMismatch
}

type ledgerAddressState struct {
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	sequences map[string]*addressUpdateSequence
	outOfSync map[string]struct{}
}

type spvAddressQueue struct {
	mu     sync.Mutex
	values []spvAddressEnvelope
	head   int
	wake   chan struct{}
}

func newSPVAddressQueue() *spvAddressQueue {
	return &spvAddressQueue{wake: make(chan struct{}, 1)}
}

func (queue *spvAddressQueue) Push(envelope spvAddressEnvelope) {
	if queue == nil {
		return
	}
	queue.mu.Lock()
	wasEmpty := queue.head == len(queue.values)
	queue.values = append(queue.values, envelope)
	queue.mu.Unlock()
	if wasEmpty {
		select {
		case queue.wake <- struct{}{}:
		default:
		}
	}
}

func (queue *spvAddressQueue) Pop() (spvAddressEnvelope, bool) {
	if queue == nil {
		return spvAddressEnvelope{}, false
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.head == len(queue.values) {
		return spvAddressEnvelope{}, false
	}
	envelope := queue.values[queue.head]
	queue.values[queue.head] = spvAddressEnvelope{}
	queue.head++
	if queue.head == len(queue.values) {
		queue.values = queue.values[:0]
		queue.head = 0
	}
	return envelope, true
}

type addressUpdateSequence struct {
	mu      sync.Mutex
	ready   *sync.Cond
	issued  uint64
	running uint64
}

func newAddressUpdateSequence() *addressUpdateSequence {
	sequence := &addressUpdateSequence{}
	sequence.ready = sync.NewCond(&sequence.mu)
	return sequence
}

func (sequence *addressUpdateSequence) Issue() uint64 {
	sequence.mu.Lock()
	ticket := sequence.issued
	sequence.issued++
	sequence.mu.Unlock()
	return ticket
}

func (sequence *addressUpdateSequence) Wait(ticket uint64) {
	sequence.mu.Lock()
	for ticket != sequence.running {
		sequence.ready.Wait()
	}
	sequence.mu.Unlock()
}

func (sequence *addressUpdateSequence) Done() {
	sequence.mu.Lock()
	sequence.running++
	sequence.ready.Broadcast()
	sequence.mu.Unlock()
}

func LocalAddressStatusAndHistory(history string) (*string, []AddressHistoryEntry, error) {
	var status *string
	if history != "" {
		digest := sha256.Sum256([]byte(history))
		encoded := hex.EncodeToString(digest[:])
		status = &encoded
	}
	parts := strings.Split(history, ":")
	parts = parts[:len(parts)-1]
	entries := make([]AddressHistoryEntry, 0, len(parts)/2)
	for index := 0; index+1 < len(parts); index += 2 {
		height, err := pythonAddressHistoryInteger(parts[index+1])
		if err != nil {
			return status, nil, err
		}
		entries = append(entries, AddressHistoryEntry{TxHash: parts[index], Height: height})
	}
	return status, entries, nil
}

func (ledger *Ledger) GetLocalAddressStatusAndHistory(
	ctx context.Context, address string, suppliedHistory *string,
) (*string, []AddressHistoryEntry, error) {
	history := ""
	if suppliedHistory != nil {
		history = *suppliedHistory
	}
	// Python treats both an omitted and an explicitly empty history as a DB
	// lookup request.
	if history == "" {
		if ledger == nil || ledger.Database == nil {
			return nil, nil, ErrAddressManagerUnavailable
		}
		record, err := ledger.Database.GetAddress(ctx, address)
		if err != nil {
			return nil, nil, err
		}
		if record != nil && record.History != nil {
			history = *record.History
		}
	}
	return LocalAddressStatusAndHistory(history)
}

func (ledger *Ledger) enqueueSPVConnected(_ context.Context) {
	ledger.enqueueSPVAddressEnvelope(spvAddressEnvelope{connected: true})
}

func (ledger *Ledger) enqueueSPVAddress(_ context.Context, params any) {
	ledger.enqueueSPVAddressEnvelope(spvAddressEnvelope{params: params})
}

func (ledger *Ledger) enqueueSPVAddressEnvelope(envelope spvAddressEnvelope) {
	if ledger == nil {
		return
	}
	ledger.spvSync.mu.Lock()
	if !ledger.spvSync.running || ledger.spvSync.addressUpdates == nil ||
		ledger.spvSync.context == nil {
		ledger.spvSync.mu.Unlock()
		return
	}
	updates := ledger.spvSync.addressUpdates
	syncCtx := ledger.spvSync.context
	ledger.spvSync.mu.Unlock()
	if syncCtx.Err() == nil {
		updates.Push(envelope)
	}
}

func (ledger *Ledger) announceAddresses(
	ctx context.Context, manager *AddressManager, addresses []string,
) error {
	if len(addresses) == 0 {
		return nil
	}
	source, ok := ledger.SPVNetwork.(LedgerSPVAddressSource)
	if ok {
		if err := ledger.subscribeAddresses(ctx, source, manager, addresses); err != nil {
			return err
		}
	}
	ledger.spvSync.mu.Lock()
	ledger.spvSync.generatedCount += len(addresses)
	ledger.spvSync.mu.Unlock()
	return nil
}

func (ledger *Ledger) subscribeAddresses(
	ctx context.Context,
	source LedgerSPVAddressSource,
	manager *AddressManager,
	addresses []string,
) error {
	if source == nil || !source.IsConnected() || len(addresses) == 0 {
		return nil
	}
	remaining := append([]string(nil), addresses...)
	for len(remaining) > 0 {
		batchLength := min(len(remaining), SPVAddressSubscriptionBatchSize)
		batch := append([]string(nil), remaining[:batchLength]...)
		statuses, err := source.SubscribeAddresses(ctx, batch)
		if err != nil {
			return err
		}
		ledger.spvSync.mu.Lock()
		ledger.spvSync.addressBatches++
		ledger.spvSync.addressCount += len(batch)
		ledger.spvSync.mu.Unlock()
		for index := 0; index < len(batch) && index < len(statuses); index++ {
			ledger.enqueueSPVAddressEnvelope(spvAddressEnvelope{
				params: []any{batch[index], statuses[index]}, manager: manager,
			})
		}
		remaining = remaining[batchLength:]
	}
	return nil
}

func (ledger *Ledger) runSPVAddressWorker(
	ctx context.Context,
	generation uint64,
	source LedgerSPVAddressSource,
	updates *spvAddressQueue,
	done chan<- struct{},
	initialTipDone <-chan struct{},
) {
	defer close(done)
	var historyUpdates sync.WaitGroup
	historyDone := make(chan struct{})
	pendingHistories := 0
	cycleResults := make(chan error, 1)
	cycleRunning := false
	cyclePending := false
	var cycleErr error
	defer func() {
		if cycleRunning {
			<-cycleResults
		}
		historyUpdates.Wait()
		ledger.setAddressSyncing(generation, false)
	}()
	if initialTipDone != nil {
		select {
		case <-initialTipDone:
		case <-ctx.Done():
			return
		}
	}
	pendingConnection := false
	startCycle := func() {
		cycleRunning = true
		ledger.setAddressSyncing(generation, true)
		go func() { cycleResults <- ledger.syncSPVAccounts(ctx, source) }()
	}
	startHistoryUpdate := func(envelope spvAddressEnvelope) {
		address, _, err := parseSPVAddressStatus(envelope.params)
		if err != nil {
			ledger.recordAddressUpdate(generation, err)
			return
		}
		sequence := ledger.addressUpdateSequence(address)
		ticket := sequence.Issue()
		historyUpdates.Add(1)
		pendingHistories++
		ledger.adjustPendingHistories(generation, 1)
		go func() {
			defer func() {
				ledger.adjustPendingHistories(generation, -1)
				historyUpdates.Done()
				select {
				case historyDone <- struct{}{}:
				case <-ctx.Done():
				}
			}()
			sequence.Wait(ticket)
			defer sequence.Done()
			err := ledger.updateSPVAddressHistory(ctx, source, envelope.params, envelope.manager)
			ledger.recordAddressUpdate(generation, err)
		}()
	}
	handleEnvelope := func(envelope spvAddressEnvelope) {
		if envelope.connected {
			if cycleRunning {
				pendingConnection = true
			} else {
				startCycle()
			}
			return
		}
		startHistoryUpdate(envelope)
	}
	drainUpdates := func() {
		for ctx.Err() == nil {
			envelope, ok := updates.Pop()
			if !ok {
				return
			}
			handleEnvelope(envelope)
		}
	}
	finishCycle := func() {
		if !cyclePending || pendingHistories != 0 {
			return
		}
		cyclePending = false
		ledger.recordAddressCycle(generation, cycleErr)
		cycleErr = nil
		if pendingConnection {
			pendingConnection = false
			startCycle()
		}
	}
	for {
		select {
		case <-updates.wake:
			drainUpdates()
			finishCycle()
		case err := <-cycleResults:
			cycleRunning = false
			cyclePending = true
			cycleErr = err
			// subscribeAddresses enqueues every initial status before the cycle
			// returns, so drain that queue before evaluating readiness.
			drainUpdates()
			finishCycle()
		case <-historyDone:
			if pendingHistories > 0 {
				pendingHistories--
			}
			finishCycle()
		case <-ctx.Done():
			return
		}
	}
}

func (ledger *Ledger) syncSPVAccounts(
	ctx context.Context, source LedgerSPVAddressSource,
) error {
	accounts := ledger.AccountsSnapshot()
	if !source.IsConnected() || len(accounts) == 0 {
		return nil
	}
	errorsByAccount := make([]error, len(accounts))
	var wait sync.WaitGroup
	wait.Add(len(accounts))
	for index, account := range accounts {
		index, account := index, account
		go func() {
			defer wait.Done()
			errorsByAccount[index] = ledger.syncSPVAccount(ctx, source, account)
		}()
	}
	wait.Wait()
	return errors.Join(errorsByAccount...)
}

func (ledger *Ledger) syncSPVAccount(
	ctx context.Context, source LedgerSPVAddressSource, account *Account,
) error {
	if account == nil {
		return nil
	}
	for _, manager := range account.AddressManagers() {
		addresses, err := manager.GetAddresses(ctx, false)
		if err != nil {
			return err
		}
		if err := ledger.subscribeAddresses(ctx, source, manager, addresses); err != nil {
			return err
		}
	}
	if _, err := account.EnsureAddressGap(ctx); err != nil {
		return err
	}
	return account.ensureDeterministicChannelCachePrimed(ctx)
}

// SubscribeAccount applies the connected-ledger path used when a wallet or
// account is added after startup: existing addresses first, then gap creation.
func (ledger *Ledger) SubscribeAccount(ctx context.Context, account *Account) error {
	if ledger == nil || account == nil {
		return nil
	}
	source, ok := ledger.SPVNetwork.(LedgerSPVAddressSource)
	if !ok || !source.IsConnected() {
		return nil
	}
	return ledger.syncSPVAccount(ctx, source, account)
}

func (ledger *Ledger) updateSPVAddressHistory(
	ctx context.Context,
	source LedgerSPVAddressSource,
	params any,
	manager *AddressManager,
) error {
	address, remoteStatus, err := parseSPVAddressStatus(params)
	if err != nil {
		return err
	}
	addressLock := ledger.addressUpdateLock(address)
	addressLock.Lock()
	defer addressLock.Unlock()
	ledger.setAddressOutOfSync(address, false)
	localStatus, localHistory, err := ledger.GetLocalAddressStatusAndHistory(ctx, address, nil)
	if err != nil {
		return err
	}
	if optionalStringsEqual(localStatus, remoteStatus) {
		return nil
	}
	result, err := source.RetriableValue(
		ctx, SPVAddressHistoryMethod, []any{address}, true,
	)
	if err != nil {
		return err
	}
	remoteHistory, err := parseSPVAddressHistory(result)
	if err != nil {
		return err
	}
	localSet := make(map[AddressHistoryEntry]struct{}, len(localHistory))
	for _, entry := range localHistory {
		localSet[entry] = struct{}{}
	}
	needed := make(map[AddressHistoryEntry]struct{})
	for _, entry := range remoteHistory {
		if _, exists := localSet[entry]; !exists {
			needed[entry] = struct{}{}
		}
	}
	// Python deliberately leaves the local history alone when the only
	// difference is that the server omitted entries we already have.
	if len(needed) == 0 {
		return nil
	}

	targetHash, err := ledger.addressHash160(address)
	if err != nil {
		return err
	}
	pendingHistory := make(map[int]string, len(remoteHistory))
	alreadySynced := make(map[AddressHistoryEntry]struct{})
	alreadySyncedOffset := 0
	for index, entry := range remoteHistory {
		if index == alreadySyncedOffset && index < len(localHistory) &&
			entry == localHistory[index] {
			pendingHistory[index] = serializeAddressHistoryEntry(entry)
			alreadySynced[entry] = struct{}{}
			alreadySyncedOffset++
		}
	}

	txIndexes := make(map[string]int, len(remoteHistory))
	requests := make([]TransactionFetchRequest, 0, len(remoteHistory)-len(alreadySynced))
	remoteTXIDs := make(map[string]struct{}, len(remoteHistory))
	for index, entry := range remoteHistory {
		txIndexes[entry.TxHash] = index
		remoteTXIDs[entry.TxHash] = struct{}{}
		if _, exists := alreadySynced[entry]; exists {
			continue
		}
		requests = append(requests, TransactionFetchRequest{
			TxID: entry.TxHash, Height: entry.Height,
		})
	}

	// RequestTransactions normally executes the complete plan before
	// returning. Executing one planned batch at a time preserves the pinned
	// commit boundary: an earlier batch remains saved when a later one fails.
	for _, planned := range PlanTransactionFetchBatches(
		requests, transactionFetchHighestCheckpointHeight(ledger.Headers),
	) {
		executed, err := ledger.requestTransactionBatch(ctx, source, planned)
		if err != nil {
			return err
		}
		transactions := make([]*Transaction, 0, len(executed.Results))
		for _, fetched := range executed.Results {
			transaction := fetched.Result.Transaction
			if transaction == nil {
				return fmt.Errorf("%w: fetched transaction is nil", ErrInvalidAddressStatus)
			}
			if err := ledger.maybeHasChannelKey(transaction); err != nil {
				return err
			}
			if transaction.ID != fetched.Result.Request.TxID {
				return &TransactionIDMismatchError{
					Requested: fetched.Result.Request.TxID,
					Computed:  transaction.ID,
				}
			}
			transactions = append(transactions, transaction)
		}
		if err := ledger.ResolveTransactionInputs(ctx, transactions, remoteTXIDs); err != nil {
			return err
		}
		if err := ledger.SaveTransactionIOBatch(
			ctx, transactions, address, targetHash, "",
		); err != nil {
			return err
		}
		if err := ledger.publishTransactionBatch(address, transactions); err != nil {
			return err
		}
		for _, transaction := range transactions {
			index, exists := txIndexes[transaction.ID]
			if !exists {
				return &TransactionIDMismatchError{Computed: transaction.ID}
			}
			pendingHistory[index] = serializeAddressHistoryEntry(AddressHistoryEntry{
				TxHash: transaction.ID, Height: transaction.Height,
			})
		}
	}

	if len(pendingHistory) != len(remoteHistory) {
		return fmt.Errorf(
			"%w: synchronized %d of %d address history entries",
			ErrInvalidAddressStatus, len(pendingHistory), len(remoteHistory),
		)
	}
	var syncedHistory strings.Builder
	for index := range remoteHistory {
		entry, exists := pendingHistory[index]
		if !exists {
			return fmt.Errorf("%w: missing synchronized history index %d", ErrInvalidAddressStatus, index)
		}
		syncedHistory.WriteString(entry)
	}
	serializedHistory := syncedHistory.String()
	if err := ledger.Database.SetAddressHistory(ctx, address, serializedHistory); err != nil {
		return err
	}

	if manager == nil {
		manager, err = ledger.getAddressManagerForAddress(ctx, address)
		if err != nil {
			return err
		}
	}
	if manager != nil {
		if _, err := manager.EnsureAddressGap(ctx); err != nil {
			return err
		}
	}

	localStatus, synchronized, err := ledger.GetLocalAddressStatusAndHistory(
		ctx, address, &serializedHistory,
	)
	if err != nil {
		return err
	}
	if optionalStringsEqual(localStatus, remoteStatus) || reflect.DeepEqual(synchronized, remoteHistory) {
		return nil
	}
	ledger.setAddressOutOfSync(address, true)
	return nil
}

func (ledger *Ledger) maybeHasChannelKey(transaction *Transaction) error {
	if ledger == nil || transaction == nil {
		return nil
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		if !output.Script.IsClaimName() && !output.Script.IsUpdateClaim() {
			continue
		}
		claim, canDecode := decodeTransactionClaim(output.Script.Claim)
		if !canDecode || claim.TXOType != TransactionOutputTypeChannel {
			continue
		}
		publicKey, isChannel, err := DecodeChannelClaimPublicKey(output.Script.Source)
		if errors.Is(err, ErrDecodedClaimNotChannel) {
			continue
		}
		if err != nil {
			return err
		}
		if !isChannel {
			continue
		}
		for _, account := range ledger.AccountsSnapshot() {
			if account == nil || account.DeterministicChannelKeys == nil {
				continue
			}
			if _, err := account.DeterministicChannelKeys.MaybeGenerateForChannel(publicKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func serializeAddressHistoryEntry(entry AddressHistoryEntry) string {
	return entry.TxHash + ":" + strconv.FormatInt(entry.Height, 10) + ":"
}

func (ledger *Ledger) addressHash160(address string) ([20]byte, error) {
	var hash [20]byte
	if ledger == nil {
		return hash, fmt.Errorf("%w: ledger is nil", ErrInvalidWalletAddress)
	}
	payload, err := keys.DecodeBase58Check(address)
	if err != nil {
		return hash, fmt.Errorf("%w %q: %v", ErrInvalidWalletAddress, address, err)
	}
	if len(payload) != 21 {
		return hash, fmt.Errorf(
			"%w %q: decoded payload has length %d", ErrInvalidWalletAddress, address, len(payload),
		)
	}
	if payload[0] != ledger.Network.PubKeyAddressPrefix() &&
		payload[0] != ledger.Network.ScriptAddressPrefix() {
		return hash, fmt.Errorf(
			"%w %q: prefix %x does not belong to %s",
			ErrInvalidWalletAddress, address, payload[0], ledger.Network.ID(),
		)
	}
	copy(hash[:], payload[1:])
	return hash, nil
}

func (ledger *Ledger) getAddressManagerForAddress(
	ctx context.Context, address string,
) (*AddressManager, error) {
	if ledger == nil || ledger.Database == nil {
		return nil, ErrAddressManagerUnavailable
	}
	record, err := ledger.Database.GetAddress(ctx, address)
	if err != nil || record == nil {
		return nil, err
	}
	for _, account := range ledger.AccountsSnapshot() {
		if account == nil || account.ID != record.Account {
			continue
		}
		for _, manager := range account.AddressManagers() {
			if manager != nil && manager.ChainNumber == record.Chain {
				return manager, nil
			}
		}
		return nil, nil
	}
	return nil, nil
}

func parseSPVAddressStatus(params any) (string, *string, error) {
	values, ok := params.([]any)
	if !ok || len(values) != 2 {
		return "", nil, fmt.Errorf("%w: notification has type %T", ErrInvalidAddressStatus, params)
	}
	address, ok := values[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("%w: address has type %T", ErrInvalidAddressStatus, values[0])
	}
	if values[1] == nil {
		return address, nil, nil
	}
	status, ok := values[1].(string)
	if !ok {
		return "", nil, fmt.Errorf("%w: status has type %T", ErrInvalidAddressStatus, values[1])
	}
	return address, &status, nil
}

func parseSPVAddressHistory(value any) ([]AddressHistoryEntry, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: history has type %T", ErrInvalidAddressStatus, value)
	}
	history := make([]AddressHistoryEntry, len(items))
	for index, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: history item has type %T", ErrInvalidAddressStatus, item)
		}
		txHash, ok := record["tx_hash"].(string)
		if !ok {
			return nil, fmt.Errorf("%w: tx_hash has type %T", ErrInvalidAddressStatus, record["tx_hash"])
		}
		height, err := spvAddressInteger(record["height"])
		if err != nil {
			return nil, fmt.Errorf("%w: height: %v", ErrInvalidAddressStatus, err)
		}
		history[index] = AddressHistoryEntry{TxHash: txHash, Height: height}
	}
	return history, nil
}

func optionalStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func pythonAddressHistoryInteger(value string) (int64, error) {
	integer, err := accountPythonIntString(value)
	if err != nil || !integer.IsInt64() {
		return 0, fmt.Errorf("invalid literal for int(): %q", value)
	}
	return integer.Int64(), nil
}

func spvAddressInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return 0, fmt.Errorf("%s is not an integer", typed)
		}
	case float32, float64, string:
		return 0, fmt.Errorf("%T is not an integer", value)
	case *big.Int:
		if typed == nil {
			return 0, errors.New("integer is nil")
		}
	case big.Int, bool:
	default:
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || reflected.Kind() < reflect.Int || reflected.Kind() > reflect.Uint64 {
			return 0, fmt.Errorf("%T is not an integer", value)
		}
	}
	integer, err := accountPythonInt(value)
	if err != nil || !integer.IsInt64() {
		return 0, errors.New("integer is outside the supported range")
	}
	return integer.Int64(), nil
}

func (ledger *Ledger) addressUpdateLock(address string) *sync.Mutex {
	ledger.addressState.mu.Lock()
	defer ledger.addressState.mu.Unlock()
	if ledger.addressState.locks == nil {
		ledger.addressState.locks = make(map[string]*sync.Mutex)
	}
	if ledger.addressState.locks[address] == nil {
		ledger.addressState.locks[address] = &sync.Mutex{}
	}
	return ledger.addressState.locks[address]
}

func (ledger *Ledger) addressUpdateSequence(address string) *addressUpdateSequence {
	ledger.addressState.mu.Lock()
	defer ledger.addressState.mu.Unlock()
	if ledger.addressState.sequences == nil {
		ledger.addressState.sequences = make(map[string]*addressUpdateSequence)
	}
	if ledger.addressState.sequences[address] == nil {
		ledger.addressState.sequences[address] = newAddressUpdateSequence()
	}
	return ledger.addressState.sequences[address]
}

func (ledger *Ledger) setAddressOutOfSync(address string, outOfSync bool) {
	ledger.addressState.mu.Lock()
	defer ledger.addressState.mu.Unlock()
	if ledger.addressState.outOfSync == nil {
		ledger.addressState.outOfSync = make(map[string]struct{})
	}
	if outOfSync {
		ledger.addressState.outOfSync[address] = struct{}{}
	} else {
		delete(ledger.addressState.outOfSync, address)
	}
}

func (ledger *Ledger) addressOutOfSyncCount() int {
	if ledger == nil {
		return 0
	}
	ledger.addressState.mu.Lock()
	defer ledger.addressState.mu.Unlock()
	return len(ledger.addressState.outOfSync)
}

func (ledger *Ledger) setAddressSyncing(generation uint64, syncing bool) {
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.addressSyncing = syncing
	}
	ledger.spvSync.mu.Unlock()
}

func (ledger *Ledger) recordAddressCycle(generation uint64, err error) {
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.addressCycles++
		ledger.spvSync.addressSyncing = false
		if err != nil && !errors.Is(err, context.Canceled) {
			ledger.spvSync.addressErr = err
		} else if err == nil {
			ledger.spvSync.addressErr = nil
		}
		ledger.maybeMarkReadyLocked()
	}
	ledger.spvSync.mu.Unlock()
}

func (ledger *Ledger) adjustPendingHistories(generation uint64, change int) {
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.pendingHistories += change
		if ledger.spvSync.pendingHistories < 0 {
			ledger.spvSync.pendingHistories = 0
		}
	}
	ledger.spvSync.mu.Unlock()
}

func (ledger *Ledger) recordAddressUpdate(generation uint64, err error) {
	ledger.spvSync.mu.Lock()
	if ledger.spvSync.generation == generation {
		ledger.spvSync.historyUpdates++
		if err != nil && !errors.Is(err, context.Canceled) {
			ledger.spvSync.addressErr = err
		} else if err == nil {
			ledger.spvSync.addressErr = nil
		}
	}
	ledger.spvSync.mu.Unlock()
}
