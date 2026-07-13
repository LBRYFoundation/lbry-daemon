package wallet

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

const DefaultTransactionCacheSize = 1024

var (
	ErrHubOutputsFetch        = errors.New("hub outputs fetch failed")
	ErrLedgerTransactionCache = errors.New("ledger transaction cache failed")
)

type HubOutputsPage struct {
	Items   []HubInflatedResult
	Blocked HubBlockedSummary
	Offset  uint32
	Total   uint32
}

type LedgerTransactionCacheError struct {
	Name    string
	Message string
}

func (err *LedgerTransactionCacheError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *LedgerTransactionCacheError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *LedgerTransactionCacheError) Unwrap() error {
	return ErrLedgerTransactionCache
}

type ledgerTransactionCacheEntry struct {
	txid        string
	transaction *Transaction
}

type ledgerTransactionCache struct {
	mu       sync.Mutex
	capacity int
	order    list.List
	entries  map[string]*list.Element
}

func newLedgerTransactionCache(capacity int) *ledgerTransactionCache {
	return &ledgerTransactionCache{
		capacity: capacity,
		entries:  make(map[string]*list.Element),
	}
}

func (cache *ledgerTransactionCache) get(txid string) (*Transaction, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.getLocked(txid)
}

func (cache *ledgerTransactionCache) getLocked(txid string) (*Transaction, bool) {
	element, exists := cache.entries[txid]
	if !exists {
		return nil, false
	}
	cache.order.MoveToBack(element)
	return element.Value.(*ledgerTransactionCacheEntry).transaction, true
}

func (cache *ledgerTransactionCache) insertPlaceholder(txid string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.insertPlaceholderLocked(txid)
}

func (cache *ledgerTransactionCache) insertPlaceholderLocked(txid string) error {
	if element, exists := cache.entries[txid]; exists {
		cache.order.MoveToBack(element)
		return nil
	}
	if cache.capacity < 1 {
		return ledgerTransactionCacheError("KeyError", "'dictionary is empty'")
	}
	if len(cache.entries) >= cache.capacity {
		oldest := cache.order.Front()
		if oldest == nil {
			return ledgerTransactionCacheError("KeyError", "'dictionary is empty'")
		}
		cache.order.Remove(oldest)
		delete(cache.entries, oldest.Value.(*ledgerTransactionCacheEntry).txid)
	}
	entry := &ledgerTransactionCacheEntry{txid: txid}
	cache.entries[txid] = cache.order.PushBack(entry)
	return nil
}

func (cache *ledgerTransactionCache) setExisting(txid string, transaction *Transaction) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.setExistingLocked(txid, transaction)
}

func (cache *ledgerTransactionCache) setExistingLocked(txid string, transaction *Transaction) error {
	element, exists := cache.entries[txid]
	if !exists {
		return ledgerTransactionCacheError(
			"AttributeError", "'NoneType' object has no attribute 'tx'",
		)
	}
	element.Value.(*ledgerTransactionCacheEntry).transaction = transaction
	cache.order.MoveToBack(element)
	return nil
}

func (cache *ledgerTransactionCache) plan(
	ordered []TransactionFetchRequest,
) ([]*Transaction, []TransactionFetchRequest, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	misses := make([]TransactionFetchRequest, 0, len(ordered))
	hitIDs := make([]string, 0, len(ordered))
	hits := make(map[string]struct{}, len(ordered))
	for _, request := range ordered {
		transaction, exists := cache.getLocked(request.TxID)
		if exists && transaction != nil && transaction.IsVerified {
			if _, duplicate := hits[request.TxID]; !duplicate {
				hits[request.TxID] = struct{}{}
				hitIDs = append(hitIDs, request.TxID)
			}
			continue
		}
		if !exists {
			if err := cache.insertPlaceholderLocked(request.TxID); err != nil {
				return nil, nil, err
			}
		}
		misses = append(misses, request)
	}

	transactions := make([]*Transaction, 0, len(hitIDs)+len(misses))
	for _, txid := range hitIDs {
		transaction, exists := cache.getLocked(txid)
		if !exists {
			return nil, nil, ledgerTransactionCacheError(
				"AttributeError", "'NoneType' object has no attribute 'tx'",
			)
		}
		transactions = append(transactions, transaction)
	}
	return transactions, misses, nil
}

func (cache *ledgerTransactionCache) storeBatch(
	order []string, transactions map[string]*Transaction,
) ([]*Transaction, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	stored := make([]*Transaction, 0, len(order))
	for _, txid := range order {
		transaction := transactions[txid]
		if err := cache.setExistingLocked(txid, transaction); err != nil {
			return nil, err
		}
		stored = append(stored, transaction)
	}
	return stored, nil
}

func (cache *ledgerTransactionCache) clear() {
	cache.mu.Lock()
	cache.order.Init()
	clear(cache.entries)
	cache.mu.Unlock()
}

func (cache *ledgerTransactionCache) length() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}

func (ledger *Ledger) ledgerTransactionCache() *ledgerTransactionCache {
	ledger.transactionCacheOnce.Do(func() {
		ledger.transactionCache = newLedgerTransactionCache(
			configuredTransactionCacheSize(ledger.Config),
		)
	})
	return ledger.transactionCache
}

func (ledger *Ledger) clearTransactionCache() {
	if ledger == nil {
		return
	}
	ledger.ledgerTransactionCache().clear()
}

func configuredTransactionCacheSize(config LedgerConfig) int {
	if config == nil {
		return DefaultTransactionCacheSize
	}
	value, exists := config["tx_cache_size"]
	if !exists {
		return DefaultTransactionCacheSize
	}
	switch typed := value.(type) {
	case int:
		return typed
	case bool:
		if typed {
			return 1
		}
		return 0
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return signedTransactionCacheSize(typed)
	case uint:
		return unsignedTransactionCacheSize(uint64(typed))
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return unsignedTransactionCacheSize(uint64(typed))
	case uint64:
		return unsignedTransactionCacheSize(typed)
	case float32:
		return floatingTransactionCacheSize(float64(typed))
	case float64:
		return floatingTransactionCacheSize(typed)
	default:
		return DefaultTransactionCacheSize
	}
}

func signedTransactionCacheSize(value int64) int {
	maximum := int(^uint(0) >> 1)
	minimum := -maximum - 1
	if value > int64(maximum) {
		return maximum
	}
	if value < int64(minimum) {
		return 0
	}
	return int(value)
}

func unsignedTransactionCacheSize(value uint64) int {
	maximum := int(^uint(0) >> 1)
	if value > uint64(maximum) {
		return maximum
	}
	return int(value)
}

func floatingTransactionCacheSize(value float64) int {
	maximum := int(^uint(0) >> 1)
	if math.IsNaN(value) || math.IsInf(value, 1) || value >= float64(maximum) {
		return maximum
	}
	if value <= 0 || math.IsInf(value, -1) {
		return 0
	}
	return int(math.Ceil(value))
}

// InflateHubOutputs performs the cached transaction phase of
// Ledger._inflate_outputs, inflates shared cached outputs, then replaces only
// primary output results with throwaway annotation copies.
func (ledger *Ledger) InflateHubOutputs(
	ctx context.Context, outputs *HubOutputs,
) (HubOutputsPage, error) {
	var page HubOutputsPage
	err := ledger.processHubOutputs(ctx, outputs, func(inflated HubOutputsPage) error {
		page = inflated
		return nil
	})
	return page, err
}

func (ledger *Ledger) processHubOutputs(
	ctx context.Context,
	outputs *HubOutputs,
	process func(HubOutputsPage) error,
) error {
	if ledger == nil {
		return fmt.Errorf("%w: ledger is nil", ErrHubOutputsFetch)
	}
	if outputs == nil {
		return fmt.Errorf("%w: outputs are nil", ErrHubOutputsFetch)
	}
	if process == nil {
		return fmt.Errorf("%w: output processor is nil", ErrHubOutputsFetch)
	}
	transactions, err := ledger.prepareHubOutputs(ctx, outputs)
	if err != nil {
		return err
	}

	ledger.hubOutputsInflateMu.Lock()
	defer ledger.hubOutputsInflateMu.Unlock()
	page, err := ledger.inflatePreparedHubOutputs(outputs, transactions)
	if err != nil {
		return err
	}
	return process(page)
}

func (ledger *Ledger) prepareHubOutputs(
	ctx context.Context, outputs *HubOutputs,
) ([]*Transaction, error) {
	requests := outputs.TransactionRequests()
	if len(requests) == 0 {
		return nil, nil
	}
	return ledger.fetchCachedHubTransactions(ctx, requests)
}

func (ledger *Ledger) inflatePreparedHubOutputs(
	outputs *HubOutputs, transactions []*Transaction,
) (HubOutputsPage, error) {
	items, blocked, err := outputs.Inflate(transactions)
	if err != nil {
		return HubOutputsPage{}, err
	}
	for index := range items {
		if items[index].Output != nil {
			items[index].Output = cloneResolvedTransactionOutputForAnnotations(items[index].Output)
		}
	}
	return HubOutputsPage{
		Items: items, Blocked: blocked, Offset: outputs.Offset, Total: outputs.Total,
	}, nil
}

func (ledger *Ledger) fetchCachedHubTransactions(
	ctx context.Context, requests []TransactionFetchRequest,
) ([]*Transaction, error) {
	ordered := append([]TransactionFetchRequest(nil), requests...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].Height < ordered[right].Height
	})
	cache := ledger.ledgerTransactionCache()
	transactions, misses, err := cache.plan(ordered)
	if err != nil {
		return nil, err
	}
	if len(misses) == 0 {
		return transactions, nil
	}

	source, ok := ledger.SPVNetwork.(LedgerSPVAddressSource)
	if !ok || source == nil {
		return nil, fmt.Errorf(
			"%w: ledger SPV network does not support retriable values",
			ErrTransactionFetchUnavailable,
		)
	}
	if err := validateTransactionFetchExecution(ledger, source); err != nil {
		return nil, err
	}
	globalRemoteHeights := make(map[string]int64, len(misses))
	for _, request := range misses {
		globalRemoteHeights[request.TxID] = request.Height
	}
	batches := PlanTransactionFetchBatches(
		misses, transactionFetchHighestCheckpointHeight(ledger.Headers),
	)
	for _, batch := range batches {
		batch.RemoteHeights = globalRemoteHeights
		executed, err := ledger.requestTransactionBatch(ctx, source, batch)
		if err != nil {
			return nil, err
		}
		batchOrder := make([]string, 0, len(executed.Results))
		batchTransactions := make(map[string]*Transaction, len(executed.Results))
		for _, result := range executed.Results {
			transaction := result.Result.Transaction
			if transaction == nil {
				return nil, malformedTransactionFetchResult(
					result.Result.Request.TxID, "transaction", errors.New("transaction is nil"),
				)
			}
			if _, exists := batchTransactions[transaction.ID]; !exists {
				batchOrder = append(batchOrder, transaction.ID)
			}
			batchTransactions[transaction.ID] = transaction
		}
		stored, err := cache.storeBatch(batchOrder, batchTransactions)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, stored...)
	}
	return transactions, nil
}

func ledgerTransactionCacheError(name, message string) error {
	return &LedgerTransactionCacheError{Name: name, Message: message}
}
