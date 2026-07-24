package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestLedgerInflateHubOutputsEmptySkipsTransactionFetch(t *testing.T) {
	ledger := &Ledger{}
	page, err := ledger.InflateHubOutputs(context.Background(), &HubOutputs{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 0 ||
		page.Blocked.Channels == nil || len(page.Blocked.Channels) != 0 ||
		page.Blocked.Total != 0 || page.Offset != 0 || page.Total != 0 {
		t.Fatalf("empty inflated page = %#v", page)
	}
	if ledger.transactionCache != nil {
		t.Fatal("empty page initialized the transaction fetch cache")
	}
}

func TestNewLedgerSnapshotsTransactionCacheCapacity(t *testing.T) {
	config := LedgerConfig{
		"data_path":     t.TempDir(),
		"tx_cache_size": 1,
	}
	ledger, err := newLedger(keys.RegTest, config)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.transactionCache == nil || ledger.transactionCache.capacity != 1 {
		t.Fatalf("constructed transaction cache = %#v", ledger.transactionCache)
	}
	config["tx_cache_size"] = 2
	if cache := ledger.ledgerTransactionCache(); cache.capacity != 1 {
		t.Fatalf("mutated config changed cache capacity to %d", cache.capacity)
	}
}

func TestHubOutputsFetchRetainsCompletedBatchCacheOnLaterFailure(t *testing.T) {
	const requestCount = TransactionFetchBatchSize + 1
	requests := make([]TransactionFetchRequest, requestCount)
	rawByID := make(map[string]string, requestCount)
	for index := range requestCount {
		rawHex := transactionFetchFixtureHex + fmt.Sprintf("%08x", index)
		raw, err := hex.DecodeString(rawHex)
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := ParseTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		requests[index] = TransactionFetchRequest{
			TxID: transaction.ID, Height: int64(index),
		}
		rawByID[transaction.ID] = rawHex
	}

	firstResponse := make(map[string]any, TransactionFetchBatchSize)
	for _, request := range requests[:TransactionFetchBatchSize] {
		firstResponse[request.TxID] = []any{rawByID[request.TxID], nil}
	}
	secondBatchError := errors.New("second batch failed")
	rpc := &transactionFetchExecutionRPC{
		responses: []any{firstResponse, nil},
		errors:    []error{nil, secondBatchError},
	}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: rpc}
	_, err := ledger.fetchCachedHubTransactions(context.Background(), requests)
	if !errors.Is(err, secondBatchError) {
		t.Fatalf("fetch error = %v, want %v", err, secondBatchError)
	}
	if calls := rpc.snapshotCalls(); len(calls) != 2 ||
		len(calls[0].params) != TransactionFetchBatchSize || len(calls[1].params) != 1 {
		t.Fatalf("batch calls = %#v", calls)
	}

	cache := ledger.ledgerTransactionCache()
	for _, request := range requests[:TransactionFetchBatchSize] {
		transaction, exists := cache.get(request.TxID)
		if !exists || transaction == nil || transaction.ID != request.TxID {
			t.Fatalf("completed batch cache entry %s = %#v, %t", request.TxID, transaction, exists)
		}
	}
	last, exists := cache.get(requests[TransactionFetchBatchSize].TxID)
	if !exists || last != nil {
		t.Fatalf("failed batch placeholder = %#v, %t", last, exists)
	}
}

func TestLedgerRejectedLiveHeaderClearsTransactionCache(t *testing.T) {
	headers, chain := liveTipHeaders(t, 3, 2)
	network := &lifecycleTipNetwork{
		remoteHeight: 2,
		live:         map[int]string{1: ""},
	}
	ledger := &Ledger{Headers: headers}
	cache := ledger.ledgerTransactionCache()
	if err := cache.insertPlaceholder("cached"); err != nil {
		t.Fatal(err)
	}
	if err := cache.setExisting("cached", &Transaction{IsVerified: true}); err != nil {
		t.Fatal(err)
	}

	err := ledger.updateSPVTip(context.Background(), 0, network, &SPVLiveHeaderUpdate{
		Height: 2,
		Hex:    hex.EncodeToString(chain[0]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cache.length() != 0 {
		t.Fatalf("transaction cache length after rejected header = %d, want 0", cache.length())
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.RejectedHeaders != 1 {
		t.Fatalf("SPV snapshot after rejected header = %#v", snapshot)
	}
}

func TestLedgerInflateHubOutputsSerializesSharedCacheMutation(t *testing.T) {
	transaction := mustFetchTransaction(t)
	transaction.IsVerified = true
	ledger := &Ledger{}
	cache := ledger.ledgerTransactionCache()
	if err := cache.insertPlaceholder(transaction.ID); err != nil {
		t.Fatal(err)
	}
	if err := cache.setExisting(transaction.ID, transaction); err != nil {
		t.Fatal(err)
	}

	newOutputs := func(shortURL string) *HubOutputs {
		return &HubOutputs{TXOs: []*HubOutput{{
			TransactionHash: transaction.Hash[:],
			Claim:           &HubClaimMeta{ShortURL: shortURL},
		}}}
	}
	outputs := []*HubOutputs{newOutputs("first"), newOutputs("second")}
	const iterations = 250
	start := make(chan struct{})
	errors := make(chan error, len(outputs))
	var workers sync.WaitGroup
	for index, fixture := range outputs {
		index, fixture := index, fixture
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			want := "lbry://" + []string{"first", "second"}[index]
			for range iterations {
				page, err := ledger.InflateHubOutputs(context.Background(), fixture)
				if err != nil {
					errors <- err
					return
				}
				if len(page.Items) != 1 || page.Items[0].Output == nil ||
					page.Items[0].Output.Meta["short_url"] != want {
					errors <- fmt.Errorf("worker %d inflated page = %#v", index, page)
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}
