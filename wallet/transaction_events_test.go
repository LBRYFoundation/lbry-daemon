package wallet

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestTransactionEventsUseReverseBatchAndSubscriptionOrder(t *testing.T) {
	ledger := &Ledger{}
	transactions := []*Transaction{{ID: "first"}, {ID: "second"}, {ID: "third"}}
	var calls []string
	cancelFirst := ledger.SubscribeTransactions(func(event TransactionEvent) error {
		calls = append(calls, "one:"+event.Address+":"+event.Transaction.ID)
		return nil
	})
	cancelSecond := ledger.SubscribeTransactions(func(event TransactionEvent) error {
		calls = append(calls, "two:"+event.Address+":"+event.Transaction.ID)
		return nil
	})
	if err := ledger.publishTransactionBatch("watched", transactions); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"one:watched:third", "two:watched:third",
		"one:watched:second", "two:watched:second",
		"one:watched:first", "two:watched:first",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("transaction event calls = %#v, want %#v", calls, want)
	}

	cancelFirst()
	cancelFirst()
	if err := ledger.publishTransactionBatch("watched", transactions[:1]); err != nil {
		t.Fatal(err)
	}
	want = append(want, "two:watched:first")
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls after cancellation = %#v, want %#v", calls, want)
	}
	cancelSecond()
	cancelSecond()
	if err := ledger.publishTransactionBatch("watched", transactions[:1]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("fully canceled listeners received an event: %#v", calls)
	}
	ledger.SubscribeTransactions(nil)()
	var nilLedger *Ledger
	nilLedger.SubscribeTransactions(func(TransactionEvent) error { return nil })()
}

func TestTransactionEventErrorStopsLaterHandlersAndEvents(t *testing.T) {
	ledger := &Ledger{}
	failure := errors.New("controlled transaction handler failure")
	var calls []string
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		calls = append(calls, "one:"+event.Transaction.ID)
		return nil
	})
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		calls = append(calls, "two:"+event.Transaction.ID)
		return failure
	})
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		calls = append(calls, "three:"+event.Transaction.ID)
		return nil
	})
	err := ledger.publishTransactionBatch(
		"watched", []*Transaction{{ID: "first"}, {ID: "second"}},
	)
	if !errors.Is(err, failure) {
		t.Fatalf("transaction event error = %v", err)
	}
	if want := []string{"one:second", "two:second"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls before handler failure = %#v, want %#v", calls, want)
	}
}

func TestTransactionEventBatchesDoNotInterleave(t *testing.T) {
	ledger := &Ledger{}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var startedOnce sync.Once
	var callsMu sync.Mutex
	var calls []string
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		if event.Address == "first-address" {
			startedOnce.Do(func() {
				close(firstStarted)
				<-releaseFirst
			})
		}
		callsMu.Lock()
		calls = append(calls, event.Address+":"+event.Transaction.ID)
		callsMu.Unlock()
		return nil
	})

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- ledger.publishTransactionBatch(
			"first-address", []*Transaction{{ID: "a1"}, {ID: "a2"}},
		)
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first transaction batch did not enter its handler")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- ledger.publishTransactionBatch(
			"second-address", []*Transaction{{ID: "b1"}, {ID: "b2"}},
		)
	}()
	time.Sleep(10 * time.Millisecond)
	callsMu.Lock()
	if len(calls) != 0 {
		t.Fatalf("second batch interleaved while first handler was blocked: %#v", calls)
	}
	callsMu.Unlock()
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	want := []string{
		"first-address:a2", "first-address:a1",
		"second-address:b2", "second-address:b1",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("serialized batch events = %#v, want %#v", calls, want)
	}
}

func TestTransactionSubscriptionPersistsAcrossSPVRestarts(t *testing.T) {
	ledger, _ := newAddressTestLedger(t, DeterministicChainGenerator)
	network := &addressSyncTestNetwork{connected: false, transactions: make(map[string][]byte)}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	var calls int
	ledger.SubscribeTransactions(func(TransactionEvent) error {
		calls++
		return nil
	})
	for cycle := 0; cycle < 2; cycle++ {
		if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.publishTransactionBatch("watched", []*Transaction{{ID: "after-restart"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("persistent listener calls = %d, want 1", calls)
	}
}

func TestAddressSyncTransactionEventsObserveCommittedEmptyHistory(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := account.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	network := &addressSyncTestNetwork{
		connected: true, transactions: make(map[string][]byte), history: make(map[string]any),
	}
	remote := make([]AddressHistoryEntry, 3)
	for index := range remote {
		transaction, raw := addressSyncTransaction(t, uint32(8000+index), targetHash, "", nil)
		remote[index] = AddressHistoryEntry{TxHash: transaction.ID, Height: 0}
		network.transactions[transaction.ID] = raw
	}
	network.history[account.ID] = addressSyncRemoteHistory(remote)
	ledger.SPVNetwork = network
	var events []string
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		if event.Address != account.ID {
			return fmt.Errorf("event address %q, want %q", event.Address, account.ID)
		}
		stored, err := ledger.Database.GetTransaction(ctx, event.Transaction.ID)
		if err != nil {
			return err
		}
		if stored == nil {
			return fmt.Errorf("transaction %s was not committed before its event", event.Transaction.ID)
		}
		record, err := ledger.Database.GetAddress(ctx, account.ID)
		if err != nil {
			return err
		}
		if record == nil || record.History == nil || *record.History != "" {
			return fmt.Errorf("address history at event = %#v, want empty", record)
		}
		events = append(events, event.Transaction.ID)
		return nil
	})
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "changed"}, account.Receiving,
	); err != nil {
		t.Fatal(err)
	}
	want := []string{remote[2].TxHash, remote[1].TxHash, remote[0].TxHash}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("committed transaction events = %#v, want %#v", events, want)
	}
	record, err := ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil ||
		*record.History != addressSyncHistoryString(remote) {
		t.Fatalf("final address history = %#v, %v", record, err)
	}
}

func TestAddressSyncTransactionHandlerFailureKeepsCommittedBatchesWithoutFinalHistory(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	account.Receiving.Gap = 1
	account.Change.Gap = 0
	addresses, err := account.Receiving.EnsureAddressGap(ctx)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("initial addresses = %#v, %v", addresses, err)
	}
	targetHash, err := ledger.addressHash160(addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	network := &addressSyncTestNetwork{
		connected: true, transactions: make(map[string][]byte), history: make(map[string]any),
	}
	remote := make([]AddressHistoryEntry, 101)
	for index := range remote {
		transaction, raw := addressSyncTransaction(t, uint32(9000+index), targetHash, "", nil)
		remote[index] = AddressHistoryEntry{TxHash: transaction.ID, Height: 0}
		network.transactions[transaction.ID] = raw
	}
	network.history[addresses[0]] = addressSyncRemoteHistory(remote)
	ledger.SPVNetwork = network
	failure := errors.New("controlled post-commit handler failure")
	var events []string
	ledger.SubscribeTransactions(func(event TransactionEvent) error {
		events = append(events, event.Transaction.ID)
		if event.Transaction.ID == remote[100].TxHash {
			return failure
		}
		return nil
	})
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{addresses[0], "changed"}, account.Receiving,
	)
	if !errors.Is(err, failure) {
		t.Fatalf("address sync handler error = %v", err)
	}
	if len(events) != 101 || events[0] != remote[99].TxHash ||
		events[99] != remote[0].TxHash || events[100] != remote[100].TxHash {
		t.Fatalf("events before second-batch failure = %d %#v", len(events), events)
	}
	for _, index := range []int{0, 99, 100} {
		stored, err := ledger.Database.GetTransaction(ctx, remote[index].TxHash)
		if err != nil || stored == nil {
			t.Fatalf("committed transaction %d = %#v, %v", index, stored, err)
		}
	}
	record, err := ledger.Database.GetAddress(ctx, addresses[0])
	if err != nil || record == nil || record.History == nil || *record.History != "" || record.UsedTimes != 0 {
		t.Fatalf("address after handler failure = %#v, %v", record, err)
	}
	records, err := account.Receiving.GetAddressRecords(ctx, false)
	if err != nil || len(records) != 1 {
		t.Fatalf("address gap after handler failure = %#v, %v", records, err)
	}
}
