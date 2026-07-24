package wallet

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestLocalAddressStatusAndHistoryMatchesElectrumContract(t *testing.T) {
	status, history, err := LocalAddressStatusAndHistory("a:1:")
	if err != nil || status == nil ||
		*status != "25910722f9ea01726fbc273f179ba64e96bf88e19dc75ca5cfd30f0885c6fe27" ||
		!reflect.DeepEqual(history, []AddressHistoryEntry{{TxHash: "a", Height: 1}}) {
		t.Fatalf("status/history = %v, %#v, %v", status, history, err)
	}
	status, history, err = LocalAddressStatusAndHistory("")
	if err != nil || status != nil || len(history) != 0 {
		t.Fatalf("empty status/history = %v, %#v, %v", status, history, err)
	}
	status, history, err = LocalAddressStatusAndHistory("a:1")
	if err != nil || status == nil || len(history) != 0 {
		t.Fatalf("unterminated status/history = %v, %#v, %v", status, history, err)
	}
	_, history, err = LocalAddressStatusAndHistory("a:1:b:")
	if err != nil || !reflect.DeepEqual(history, []AddressHistoryEntry{{TxHash: "a", Height: 1}}) {
		t.Fatalf("odd status history = %#v, %v", history, err)
	}
	if _, _, err := LocalAddressStatusAndHistory("a:not-int:"); err == nil {
		t.Fatal("non-integer history height was accepted")
	}
}

func TestSPVAddressQueuePreservesOrderWithoutProducerBackpressure(t *testing.T) {
	queue := newSPVAddressQueue()
	const total = 10_000
	for index := 0; index < total; index++ {
		queue.Push(spvAddressEnvelope{params: index})
	}
	select {
	case <-queue.wake:
	default:
		t.Fatal("queued address updates did not signal the consumer")
	}
	for index := 0; index < total; index++ {
		envelope, ok := queue.Pop()
		if !ok || envelope.params != index {
			t.Fatalf("queued address update %d = %#v, %t", index, envelope.params, ok)
		}
	}
	if _, ok := queue.Pop(); ok {
		t.Fatal("address queue retained an update after draining")
	}
}

func TestDeterministicAddressGapInventoryAndAnnouncementFailure(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	account.Receiving.Gap = 4
	account.Receiving.MaximumUsesPerAddress = 2
	created, err := account.Receiving.EnsureAddressGap(ctx)
	if err != nil || len(created) != 4 {
		t.Fatalf("initial gap = %v, %v", created, err)
	}
	if repeated, err := account.Receiving.EnsureAddressGap(ctx); err != nil || len(repeated) != 0 {
		t.Fatalf("full gap retry = %v, %v", repeated, err)
	}
	records, err := account.Receiving.GetAddressRecords(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byIndex := addressRecordsByIndex(records)
	if err := ledger.Database.SetAddressHistory(ctx, byIndex[0].Address, "a:1:"); err != nil {
		t.Fatal(err)
	}
	created, err = account.Receiving.EnsureAddressGap(ctx)
	if err != nil || len(created) != 1 {
		t.Fatalf("oldest-used gap = %v, %v", created, err)
	}
	records, err = account.Receiving.GetAddressRecords(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byIndex = addressRecordsByIndex(records)
	if err := ledger.Database.SetAddressHistory(ctx, byIndex[4].Address, "b:2:"); err != nil {
		t.Fatal(err)
	}
	created, err = account.Receiving.EnsureAddressGap(ctx)
	if err != nil || len(created) != 4 {
		t.Fatalf("newest-used gap = %v, %v", created, err)
	}
	records, err = account.Receiving.GetAddressRecords(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	byIndex = addressRecordsByIndex(records)
	if err := ledger.Database.SetAddressHistory(ctx, byIndex[2].Address, "x:1:y:2:z:3:"); err != nil {
		t.Fatal(err)
	}
	records, err = account.Receiving.GetAddressRecords(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordNs(records); !reflect.DeepEqual(got, []int64{1, 3, 5, 6, 7, 8, 0, 4, 2}) {
		t.Fatalf("inventory order = %v", got)
	}
	usable, err := account.Receiving.GetAddressRecords(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := addressRecordNs(usable); !reflect.DeepEqual(got, []int64{1, 3, 5, 6, 7, 8, 0, 4}) {
		t.Fatalf("usable inventory = %v", got)
	}
	if maximumGap, err := account.Receiving.GetMaxGap(ctx); err != nil || maximumGap != 1 {
		t.Fatalf("maximum gap = %d, %v", maximumGap, err)
	}
	selected, err := account.Receiving.GetOrCreateUsableAddress(ctx)
	if err != nil || !containsAddressRecord(usable, selected) {
		t.Fatalf("selected usable address = %q, %v", selected, err)
	}

	failingLedger, failingAccount := newAddressTestLedger(t, DeterministicChainGenerator)
	failingAccount.Receiving.Gap = 2
	failure := errors.New("controlled announcement failure")
	network := &addressSyncTestNetwork{connected: true, subscribeErr: failure}
	failingLedger.SPVNetwork = network
	if _, err := failingAccount.Receiving.EnsureAddressGap(ctx); !errors.Is(err, failure) {
		t.Fatalf("announcement failure = %v", err)
	}
	persisted, err := failingAccount.Receiving.GetAddressRecords(ctx, false)
	if err != nil || len(persisted) != 2 {
		t.Fatalf("persisted after failure = %d, %v", len(persisted), err)
	}
	if retry, err := failingAccount.Receiving.EnsureAddressGap(ctx); err != nil || len(retry) != 0 {
		t.Fatalf("retry after failure = %v, %v", retry, err)
	}
	if len(network.subscribeCalls) != 1 {
		t.Fatalf("subscription attempts = %d", len(network.subscribeCalls))
	}
}

func TestSingleAddressGapIgnoresUsabilityLimit(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	created, err := account.Receiving.EnsureAddressGap(ctx)
	if err != nil || !reflect.DeepEqual(created, []string{account.ID}) {
		t.Fatalf("single first gap = %v, %v", created, err)
	}
	if created, err := account.Change.EnsureAddressGap(ctx); err != nil || len(created) != 0 {
		t.Fatalf("single second gap = %v, %v", created, err)
	}
	if err := ledger.Database.SetAddressHistory(ctx, account.ID, "a:1:b:2:c:3:"); err != nil {
		t.Fatal(err)
	}
	usable, err := account.Receiving.GetAddresses(ctx, true)
	if err != nil || !reflect.DeepEqual(usable, []string{account.ID}) {
		t.Fatalf("single usable = %v, %v", usable, err)
	}
	if selected, err := account.Change.GetOrCreateUsableAddress(ctx); err != nil || selected != account.ID {
		t.Fatalf("single selected address = %q, %v", selected, err)
	}
}

func TestSPVAddressSubscriptionBatchesAndTransactionSync(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	network := &addressSyncTestNetwork{connected: true, transactions: make(map[string][]byte)}
	addresses := make([]string, 2001)
	for index := range addresses {
		addresses[index] = fmt.Sprintf("address-%04d", index)
	}
	if err := ledger.subscribeAddresses(ctx, network, account.Receiving, addresses); err != nil {
		t.Fatal(err)
	}
	if got := addressSubscriptionLengths(network.subscribeCalls); !reflect.DeepEqual(got, []int{1000, 1000, 1}) {
		t.Fatalf("subscription batches = %v", got)
	}

	if _, err := account.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	transaction, raw := addressSyncTransaction(t, 7, targetHash, "", nil)
	network.transactions[transaction.ID] = raw
	network.history = map[string]any{
		account.ID: addressSyncRemoteHistory([]AddressHistoryEntry{{TxHash: transaction.ID, Height: 0}}),
	}
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "remote-status"}, account.Receiving,
	)
	if err != nil {
		t.Fatalf("transaction history sync = %v", err)
	}
	if ledger.addressOutOfSyncCount() != 0 || network.historyCalls != 1 ||
		!reflect.DeepEqual(addressSubscriptionLengths(network.transactionCalls), []int{1}) {
		t.Fatalf(
			"history sync state = out-of-sync %d, history calls %d, transaction calls %v",
			ledger.addressOutOfSyncCount(), network.historyCalls,
			addressSubscriptionLengths(network.transactionCalls),
		)
	}
	localStatus, localHistory, err := ledger.GetLocalAddressStatusAndHistory(ctx, account.ID, nil)
	if err != nil || localStatus == nil || !reflect.DeepEqual(
		localHistory, []AddressHistoryEntry{{TxHash: transaction.ID, Height: 0}},
	) {
		t.Fatalf("local status/history = %v, %#v, %v", localStatus, localHistory, err)
	}
	stored, err := ledger.Database.GetTransaction(ctx, transaction.ID)
	if err != nil || stored == nil {
		t.Fatalf("stored transaction = %#v, %v", stored, err)
	}
	if err := ledger.updateSPVAddressHistory(ctx, network, []any{account.ID, nil}, nil); err != nil {
		t.Fatalf("removal-only empty status = %v", err)
	}
	if ledger.addressOutOfSyncCount() != 0 || network.historyCalls != 2 {
		t.Fatalf("removal-only state = out-of-sync %d, calls %d", ledger.addressOutOfSyncCount(), network.historyCalls)
	}
	_, localHistory, err = ledger.GetLocalAddressStatusAndHistory(ctx, account.ID, nil)
	if err != nil || len(localHistory) != 1 {
		t.Fatalf("removal-only local history = %#v, %v", localHistory, err)
	}
}

func TestSPVAddressTransactionSyncCommitsOneAndTwoBatchHistories(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		count   int
		batches []int
	}{
		{name: "one batch", count: 3, batches: []int{3}},
		{name: "two batches", count: 101, batches: []int{100, 1}},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
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
				connected: true, transactions: make(map[string][]byte),
				history: make(map[string]any),
			}
			remote := make([]AddressHistoryEntry, fixture.count)
			for index := range remote {
				transaction, raw := addressSyncTransaction(
					t, uint32(1000+index), targetHash, "", nil,
				)
				remote[index] = AddressHistoryEntry{TxHash: transaction.ID, Height: 0}
				network.transactions[transaction.ID] = raw
			}
			network.history[account.ID] = addressSyncRemoteHistory(remote)
			ledger.SPVNetwork = network
			if err := ledger.updateSPVAddressHistory(
				ctx, network, []any{account.ID, "changed"}, account.Receiving,
			); err != nil {
				t.Fatal(err)
			}
			if got := addressSubscriptionLengths(network.transactionCalls); !reflect.DeepEqual(got, fixture.batches) {
				t.Fatalf("transaction batches = %v", got)
			}
			record, err := ledger.Database.GetAddress(ctx, account.ID)
			if err != nil || record == nil || record.History == nil ||
				*record.History != addressSyncHistoryString(remote) ||
				record.UsedTimes != int64(fixture.count) {
				t.Fatalf("address after sync = %#v, %v", record, err)
			}
			for _, index := range []int{0, fixture.count - 1} {
				stored, err := ledger.Database.GetTransaction(ctx, remote[index].TxHash)
				if err != nil || stored == nil {
					t.Fatalf("stored transaction %d = %#v, %v", index, stored, err)
				}
			}
		})
	}
}

func TestSPVAddressTransactionSyncResolvesManagerAndRestoresGapAfterFinalHistory(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	account.Receiving.Gap = 1
	account.Change.Gap = 0
	created, err := account.Receiving.EnsureAddressGap(ctx)
	if err != nil || len(created) != 1 {
		t.Fatalf("initial address gap = %v, %v", created, err)
	}
	targetHash, err := ledger.addressHash160(created[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction, raw := addressSyncTransaction(t, 1500, targetHash, "", nil)
	remote := []AddressHistoryEntry{{TxHash: transaction.ID, Height: 0}}
	network := &addressSyncTestNetwork{
		connected:    true,
		transactions: map[string][]byte{transaction.ID: raw},
		history:      map[string]any{created[0]: addressSyncRemoteHistory(remote)},
	}
	ledger.SPVNetwork = network
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{created[0], "changed"}, nil,
	); err != nil {
		t.Fatal(err)
	}
	records, err := account.Receiving.GetAddressRecords(ctx, false)
	byIndex := addressRecordsByIndex(records)
	if err != nil || len(records) != 2 || byIndex[0].Address != created[0] ||
		byIndex[0].UsedTimes != 1 || byIndex[1].UsedTimes != 0 {
		t.Fatalf("restored address gap = %#v, %v", records, err)
	}
	if !reflect.DeepEqual(network.subscribeCalls, [][]string{{byIndex[1].Address}}) {
		t.Fatalf("restored gap subscriptions = %#v", network.subscribeCalls)
	}
}

func TestSPVAddressTransactionSyncKeepsEarlierBatchAndEmptyHistoryOnSecondBatchFailure(t *testing.T) {
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
	remote := make([]AddressHistoryEntry, 102)
	for index := range remote {
		var script []byte
		if index == len(remote)-1 {
			script = transactionClaimScript(
				transactionOpClaimName, []byte{0xff}, nil, []byte{0}, nil,
				transactionP2PKH(targetHash[:]),
			)
		}
		transaction, raw := addressSyncTransaction(
			t, uint32(2000+index), targetHash, "", script,
		)
		remote[index] = AddressHistoryEntry{TxHash: transaction.ID, Height: 0}
		network.transactions[transaction.ID] = raw
	}
	network.history[account.ID] = addressSyncRemoteHistory(remote)
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "changed"}, account.Receiving,
	)
	if !errors.Is(err, ErrInvalidTransactionClaimName) {
		t.Fatalf("second batch error = %v", err)
	}
	if got := addressSubscriptionLengths(network.transactionCalls); !reflect.DeepEqual(got, []int{100, 2}) {
		t.Fatalf("transaction batches = %v", got)
	}
	first, err := ledger.Database.GetTransaction(ctx, remote[0].TxHash)
	if err != nil || first == nil {
		t.Fatalf("first committed transaction = %#v, %v", first, err)
	}
	for _, index := range []int{100, 101} {
		stored, err := ledger.Database.GetTransaction(ctx, remote[index].TxHash)
		if err != nil || stored != nil {
			t.Fatalf("rolled-back transaction %d = %#v, %v", index, stored, err)
		}
	}
	record, err := ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil || *record.History != "" ||
		record.UsedTimes != 0 || ledger.addressOutOfSyncCount() != 0 {
		t.Fatalf("address after failed second batch = %#v, out-of-sync %d, %v", record, ledger.addressOutOfSyncCount(), err)
	}
}

func TestSPVAddressTransactionSyncSavesPartialHubBatchBeforeFinalHistoryFailure(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := account.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, firstRaw := addressSyncTransaction(t, 2_500, targetHash, "", nil)
	second, secondRaw := addressSyncTransaction(t, 2_501, targetHash, "", nil)
	remote := []AddressHistoryEntry{
		{TxHash: first.ID, Height: 0},
		{TxHash: second.ID, Height: 0},
	}
	network := &addressSyncTestNetwork{
		connected: true,
		transactions: map[string][]byte{
			first.ID:  firstRaw,
			second.ID: secondRaw,
		},
		transactionOmit: map[string]bool{second.ID: true},
		history:         map[string]any{account.ID: addressSyncRemoteHistory(remote)},
	}
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "partial"}, account.Receiving,
	)
	if !errors.Is(err, ErrInvalidAddressStatus) {
		t.Fatalf("partial batch history error = %v", err)
	}
	storedFirst, firstErr := ledger.Database.GetTransaction(ctx, first.ID)
	storedSecond, secondErr := ledger.Database.GetTransaction(ctx, second.ID)
	if firstErr != nil || storedFirst == nil || secondErr != nil || storedSecond != nil {
		t.Fatalf(
			"partial batch transactions = first %#v/%v, second %#v/%v",
			storedFirst, firstErr, storedSecond, secondErr,
		)
	}
	record, err := ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil || *record.History != "" {
		t.Fatalf("partial batch address history = %#v, %v", record, err)
	}
}

func TestSPVAddressTransactionSyncUsesOnlyContiguousPrefixAndPreservesRemovalOnlyHistory(t *testing.T) {
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
	transactions := make([]*Transaction, 4)
	for index := range transactions {
		transaction, raw := addressSyncTransaction(t, uint32(3000+index), targetHash, "", nil)
		transactions[index] = transaction
		network.transactions[transaction.ID] = raw
	}
	local := []AddressHistoryEntry{
		{TxHash: transactions[0].ID, Height: 0},
		{TxHash: transactions[1].ID, Height: 0},
	}
	if err := ledger.Database.SetAddressHistory(ctx, account.ID, addressSyncHistoryString(local)); err != nil {
		t.Fatal(err)
	}
	remote := []AddressHistoryEntry{
		local[0],
		{TxHash: transactions[2].ID, Height: 0},
		local[1],
	}
	network.history[account.ID] = addressSyncRemoteHistory(remote)
	ledger.SPVNetwork = network
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "inserted"}, account.Receiving,
	); err != nil {
		t.Fatal(err)
	}
	if len(network.transactionCalls) != 1 || !reflect.DeepEqual(
		network.transactionCalls[0], []string{transactions[2].ID, transactions[1].ID},
	) {
		t.Fatalf("non-prefix requests = %#v", network.transactionCalls)
	}
	record, err := ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil ||
		*record.History != addressSyncHistoryString(remote) {
		t.Fatalf("inserted history = %#v, %v", record, err)
	}

	network.mu.Lock()
	network.transactionCalls = nil
	reorg := []AddressHistoryEntry{
		remote[0],
		{TxHash: transactions[3].ID, Height: 0},
	}
	network.history[account.ID] = addressSyncRemoteHistory(reorg)
	network.mu.Unlock()
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "reorg"}, account.Receiving,
	); err != nil {
		t.Fatal(err)
	}
	if len(network.transactionCalls) != 1 || !reflect.DeepEqual(
		network.transactionCalls[0], []string{transactions[3].ID},
	) {
		t.Fatalf("reorg transaction requests = %#v", network.transactionCalls)
	}
	record, err = ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil ||
		*record.History != addressSyncHistoryString(reorg) {
		t.Fatalf("reorg history = %#v, %v", record, err)
	}

	network.mu.Lock()
	network.transactionCalls = nil
	network.history[account.ID] = addressSyncRemoteHistory(reorg[:1])
	network.mu.Unlock()
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "removed"}, account.Receiving,
	); err != nil {
		t.Fatal(err)
	}
	if len(network.transactionCalls) != 0 {
		t.Fatalf("removal-only transaction requests = %#v", network.transactionCalls)
	}
	record, err = ledger.Database.GetAddress(ctx, account.ID)
	if err != nil || record == nil || record.History == nil ||
		*record.History != addressSyncHistoryString(reorg) {
		t.Fatalf("removal-only history = %#v, %v", record, err)
	}
}

func TestSPVAddressTransactionSyncResolvesSameBatchInputs(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := account.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherHash := transactionPersistenceHash(0x7a)
	parent, parentRaw := addressSyncTransaction(t, 4000, targetHash, "", nil)
	child, childRaw := addressSyncTransaction(t, 4001, otherHash, parent.ID, nil)
	remote := []AddressHistoryEntry{
		{TxHash: parent.ID, Height: 0},
		{TxHash: child.ID, Height: 0},
	}
	network := &addressSyncTestNetwork{
		connected: true,
		transactions: map[string][]byte{
			parent.ID: parentRaw,
			child.ID:  childRaw,
		},
		history: map[string]any{account.ID: addressSyncRemoteHistory(remote)},
	}
	ledger.SPVNetwork = network
	if err := ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "spend"}, account.Receiving,
	); err != nil {
		t.Fatal(err)
	}
	storedOutputs, err := ledger.Database.GetOutputsByID(
		ctx, []string{parent.ID + ":0", child.ID + ":0"},
	)
	if err != nil || len(storedOutputs) != 2 ||
		storedOutputs[child.ID+":0"].Address == nil {
		t.Fatalf("same-batch owned outputs = %#v, %v", storedOutputs, err)
	}
	wantChildAddress := keys.EncodeBase58Check(append([]byte{ledger.Network.PubKeyAddressPrefix()}, otherHash[:]...))
	if *storedOutputs[child.ID+":0"].Address != wantChildAddress {
		t.Fatalf("outgoing child address = %q, want %q", *storedOutputs[child.ID+":0"].Address, wantChildAddress)
	}
}

func TestSPVAddressTransactionSyncRejectsComputedTransactionIDMismatch(t *testing.T) {
	ctx := context.Background()
	ledger, account := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := account.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	observer := channelManagerSeedAccount(t)
	ledger.addAccount(observer)
	root, err := observer.DeterministicChannelKeys.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := root.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	channelScript := transactionClaimScript(
		transactionOpClaimName, []byte("channel"), nil,
		makeV2ChannelClaim(candidate.PublicKey().CompressedBytes()), nil,
		transactionP2PKH(targetHash[:]),
	)
	transaction, raw := addressSyncTransaction(t, 5000, targetHash, "", channelScript)
	requested := strings.Repeat("ab", 32)
	network := &addressSyncTestNetwork{
		connected:    true,
		transactions: map[string][]byte{requested: raw},
		history: map[string]any{account.ID: addressSyncRemoteHistory([]AddressHistoryEntry{{
			TxHash: requested, Height: 0,
		}})},
	}
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{account.ID, "mismatch"}, account.Receiving,
	)
	var mismatch *TransactionIDMismatchError
	if !errors.Is(err, ErrTransactionIDMismatch) || !errors.As(err, &mismatch) ||
		mismatch.Requested != requested || mismatch.Computed != transaction.ID {
		t.Fatalf("transaction ID mismatch = %#v, %v", mismatch, err)
	}
	if observer.DeterministicChannelKeys.LastKnown != 1 ||
		observer.DeterministicChannelKeys.GetPrivateKey(candidate.Address()) == nil {
		t.Fatalf(
			"channel observation before ID mismatch = last_known %d cached %t",
			observer.DeterministicChannelKeys.LastKnown,
			observer.DeterministicChannelKeys.GetPrivateKey(candidate.Address()) != nil,
		)
	}
	record, recordErr := ledger.Database.GetAddress(ctx, account.ID)
	if recordErr != nil || record == nil || record.History != nil {
		t.Fatalf("history after ID mismatch = %#v, %v", record, recordErr)
	}
}

func TestSPVAddressTransactionSyncObservesChannelKeysBeforeBatchSave(t *testing.T) {
	ctx := context.Background()
	ledger, watchedAccount := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := watchedAccount.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(watchedAccount.ID)
	if err != nil {
		t.Fatal(err)
	}

	observers := []*Account{channelManagerSeedAccount(t), channelManagerSeedAccount(t)}
	for _, account := range observers {
		ledger.addAccount(account)
	}
	root, err := observers[0].DeterministicChannelKeys.PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := root.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	claim := makeV2ChannelClaim(candidate.PublicKey().CompressedBytes())
	script := transactionClaimScript(
		transactionOpClaimName, []byte{0xff}, nil, claim, nil,
		transactionP2PKH(targetHash[:]),
	)
	transaction, raw := addressSyncTransaction(t, 5_100, targetHash, "", script)
	remote := []AddressHistoryEntry{{TxHash: transaction.ID, Height: 0}}
	network := &addressSyncTestNetwork{
		connected:    true,
		transactions: map[string][]byte{transaction.ID: raw},
		history:      map[string]any{watchedAccount.ID: addressSyncRemoteHistory(remote)},
	}
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{watchedAccount.ID, "channel"}, watchedAccount.Receiving,
	)
	if !errors.Is(err, ErrInvalidTransactionClaimName) {
		t.Fatalf("post-observation save error = %v", err)
	}
	for index, account := range observers {
		manager := account.DeterministicChannelKeys
		if manager.LastKnown != 1 || manager.GetPrivateKey(candidate.Address()) == nil {
			t.Fatalf(
				"observer %d state = last_known %d cached %t",
				index, manager.LastKnown, manager.GetPrivateKey(candidate.Address()) != nil,
			)
		}
	}
	stored, err := ledger.Database.GetTransaction(ctx, transaction.ID)
	if err != nil || stored != nil {
		t.Fatalf("transaction after failed save = %#v, %v", stored, err)
	}
}

func TestSPVAddressTransactionSyncRejectsInvalidChannelKeyBeforeBatchSave(t *testing.T) {
	ctx := context.Background()
	ledger, watchedAccount := newAddressTestLedger(t, SingleAddressGenerator)
	if _, err := watchedAccount.Receiving.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	targetHash, err := ledger.addressHash160(watchedAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	observer := channelManagerSeedAccount(t)
	ledger.addAccount(observer)
	script := transactionClaimScript(
		transactionOpClaimName, []byte("channel"), nil,
		makeV2ChannelClaim([]byte{1}), nil, transactionP2PKH(targetHash[:]),
	)
	transaction, raw := addressSyncTransaction(t, 5_101, targetHash, "", script)
	remote := []AddressHistoryEntry{{TxHash: transaction.ID, Height: 0}}
	network := &addressSyncTestNetwork{
		connected:    true,
		transactions: map[string][]byte{transaction.ID: raw},
		history:      map[string]any{watchedAccount.ID: addressSyncRemoteHistory(remote)},
	}
	ledger.SPVNetwork = network
	err = ledger.updateSPVAddressHistory(
		ctx, network, []any{watchedAccount.ID, "channel"}, watchedAccount.Receiving,
	)
	if !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("invalid channel key error = %v", err)
	}
	manager := observer.DeterministicChannelKeys
	if manager.LastKnown != 0 || len(manager.Cache) != 0 {
		t.Fatalf("invalid-key manager state = last_known %d cache %d", manager.LastKnown, len(manager.Cache))
	}
	stored, err := ledger.Database.GetTransaction(ctx, transaction.ID)
	if err != nil || stored != nil {
		t.Fatalf("invalid-key transaction = %#v, %v", stored, err)
	}
}

func TestSPVAddressTransactionSyncRejectsInvalidWatchedAddressBeforeFetching(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newAddressTestLedger(t, SingleAddressGenerator)
	address := "not-a-wallet-address"
	network := &addressSyncTestNetwork{
		connected: true,
		history: map[string]any{address: addressSyncRemoteHistory([]AddressHistoryEntry{{
			TxHash: strings.Repeat("cd", 32), Height: 0,
		}})},
	}
	err := ledger.updateSPVAddressHistory(
		ctx, network, []any{address, "changed"}, nil,
	)
	if !errors.Is(err, ErrInvalidWalletAddress) || network.historyCalls != 1 ||
		len(network.transactionCalls) != 0 {
		t.Fatalf(
			"invalid watched address = %v, history calls %d, transaction calls %#v",
			err, network.historyCalls, network.transactionCalls,
		)
	}
}

func TestSPVAddressWorkerSynchronizesInventoryAndReconnects(t *testing.T) {
	ledger, _ := newAddressTestLedger(t, DeterministicChainGenerator)
	network := &addressSyncTestNetwork{connected: true, transactions: make(map[string][]byte)}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	network.EmitConnected()
	waitForAddressSnapshot(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.AddressCycles == 1 && snapshot.HistoryUpdates == 26 && !snapshot.AddressSyncing
	})
	snapshot := ledger.SPVSnapshot()
	if snapshot.AddressErr != nil || snapshot.AddressBatches != 2 ||
		snapshot.SubscribedAddresses != 26 || snapshot.GeneratedAddresses != 26 ||
		snapshot.OutOfSyncAddresses != 0 || !snapshot.WalletReady {
		t.Fatalf("initial address snapshot = %#v", snapshot)
	}
	network.EmitConnected()
	waitForAddressSnapshot(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.AddressCycles == 2 && snapshot.HistoryUpdates == 52 && !snapshot.AddressSyncing
	})
	snapshot = ledger.SPVSnapshot()
	if snapshot.SubscribedAddresses != 52 || snapshot.GeneratedAddresses != 26 ||
		snapshot.AddressBatches != 4 {
		t.Fatalf("reconnected address snapshot = %#v", snapshot)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.Running || !snapshot.AddressWorkerDone {
		t.Fatalf("stopped address snapshot = %#v", snapshot)
	}
}

func TestSPVAddressWorkerRunsDifferentAddressesConcurrently(t *testing.T) {
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	account.Receiving.Gap = 2
	account.Change.Gap = 0
	network := &addressSyncTestNetwork{connected: true, transactions: make(map[string][]byte)}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	network.EmitConnected()
	waitForAddressSnapshot(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.AddressCycles == 1 && snapshot.HistoryUpdates == 2
	})
	records, err := account.Receiving.GetAddressRecords(context.Background(), false)
	if err != nil || len(records) != 2 {
		t.Fatalf("concurrency inventory = %d, %v", len(records), err)
	}
	first, second := records[0].Address, records[1].Address
	firstHash, err := ledger.addressHash160(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := ledger.addressHash160(second)
	if err != nil {
		t.Fatal(err)
	}
	firstTransaction, firstRaw := addressSyncTransaction(t, 6000, firstHash, "", nil)
	secondTransaction, secondRaw := addressSyncTransaction(t, 6001, secondHash, "", nil)
	firstRelease := make(chan struct{})
	network.mu.Lock()
	network.history = map[string]any{
		first: addressSyncRemoteHistory([]AddressHistoryEntry{{
			TxHash: firstTransaction.ID, Height: 0,
		}}),
		second: addressSyncRemoteHistory([]AddressHistoryEntry{{
			TxHash: secondTransaction.ID, Height: 0,
		}}),
	}
	network.transactions[firstTransaction.ID] = firstRaw
	network.transactions[secondTransaction.ID] = secondRaw
	network.historyBlocks = map[string]<-chan struct{}{first: firstRelease}
	network.historyStarted = make(chan string, 2)
	network.mu.Unlock()
	network.EmitAddress([]any{first, "first-status"})
	if started := waitForAddressHistoryStart(t, network.historyStarted); started != first {
		t.Fatalf("first history start = %q", started)
	}
	network.EmitAddress([]any{second, "second-status"})
	if started := waitForAddressHistoryStart(t, network.historyStarted); started != second {
		t.Fatalf("second history start while first blocked = %q", started)
	}
	waitForAddressSnapshot(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.HistoryUpdates >= 3
	})
	close(firstRelease)
	for _, expected := range []struct {
		address string
		txid    string
	}{{first, firstTransaction.ID}, {second, secondTransaction.ID}} {
		waitForStoredAddressHistory(t, ledger, expected.address, expected.txid+":0:")
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.OutOfSyncAddresses != 0 || snapshot.AddressErr != nil {
		t.Fatalf("concurrent address snapshot = %#v", snapshot)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWalletReadinessWaitsForInitialAddressHistories(t *testing.T) {
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	account.Receiving.Gap = 1
	account.Change.Gap = 0
	release := make(chan struct{})
	network := &addressSyncTestNetwork{
		connected: true, transactions: make(map[string][]byte),
		subscriptionStatus: "remote-status", historyBlock: release,
		historyStarted: make(chan string, 1),
	}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	network.EmitConnected()
	_ = waitForAddressHistoryStart(t, network.historyStarted)
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := ledger.WaitSPVReady(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness before history completion = %v, snapshot %#v", err, ledger.SPVSnapshot())
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.UpdateTasks == 0 || snapshot.PendingHistories == 0 {
		t.Fatalf("pending history snapshot = %#v", snapshot)
	}
	close(release)
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	defer readyCancel()
	if err := ledger.WaitSPVReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	network.mu.Lock()
	network.connected = false
	network.mu.Unlock()
	if snapshot := ledger.SPVSnapshot(); !snapshot.WalletReady || snapshot.UpdateTasks != 0 {
		t.Fatalf("readiness was revoked after disconnect: %#v", snapshot)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func newAddressTestLedger(t *testing.T, generator string) (*Ledger, *Account) {
	t.Helper()
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	headers := NewHeaders(
		":memory:", WithHeaderValidation(false), withHeaderCheckpoints(checkpointTable{}),
	)
	headers.genesisHash = nil
	if err := headers.Open(); err != nil {
		database.Close(ctx)
		t.Fatal(err)
	}
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: generator})},
	))
	if err != nil {
		database.Close(ctx)
		headers.Close()
		t.Fatal(err)
	}
	ledger := &Ledger{Network: keys.MainNet, Database: database, Headers: headers}
	ledger.addAccount(account)
	t.Cleanup(func() {
		if ledger.SPVSnapshot().Running {
			_ = ledger.StopSPVCheckpointSync(context.Background())
		}
		_ = database.Close(context.Background())
		if headers.opened {
			_ = headers.Close()
		}
	})
	return ledger, account
}

func addressRecordsByIndex(records []ledgerdb.AddressRecord) map[int64]ledgerdb.AddressRecord {
	result := make(map[int64]ledgerdb.AddressRecord, len(records))
	for _, record := range records {
		result[record.N] = record
	}
	return result
}

func addressRecordNs(records []ledgerdb.AddressRecord) []int64 {
	result := make([]int64, len(records))
	for index, record := range records {
		result[index] = record.N
	}
	return result
}

func containsAddressRecord(records []ledgerdb.AddressRecord, address string) bool {
	for _, record := range records {
		if record.Address == address {
			return true
		}
	}
	return false
}

func addressSubscriptionLengths(calls [][]string) []int {
	lengths := make([]int, len(calls))
	for index, call := range calls {
		lengths[index] = len(call)
	}
	return lengths
}

func waitForAddressSnapshot(
	t *testing.T, ledger *Ledger, ready func(LedgerSPVSnapshot) bool,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := ledger.SPVSnapshot(); ready(snapshot) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("address synchronization did not finish: %#v", ledger.SPVSnapshot())
}

func waitForAddressHistoryStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case address := <-started:
		return address
	case <-time.After(time.Second):
		t.Fatal("address history did not start")
		return ""
	}
}

func waitForStoredAddressHistory(
	t *testing.T, ledger *Ledger, address, history string,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, err := ledger.Database.GetAddress(context.Background(), address)
		if err == nil && record != nil && record.History != nil && *record.History == history {
			return
		}
		time.Sleep(time.Millisecond)
	}
	record, err := ledger.Database.GetAddress(context.Background(), address)
	t.Fatalf("stored address history for %s = %#v, %v", address, record, err)
}

type addressSyncTestNetwork struct {
	mu sync.Mutex

	headerHandler      func(context.Context, any)
	addressHandler     func(context.Context, any)
	connectedHandler   func(context.Context)
	connected          bool
	subscribeErr       error
	subscribeCalls     [][]string
	history            map[string]any
	historyCalls       int
	historyBlocks      map[string]<-chan struct{}
	historyBlock       <-chan struct{}
	historyStarted     chan string
	subscriptionStatus any
	transactions       map[string][]byte
	transactionOmit    map[string]bool
	transactionCalls   [][]string
	transactionError   map[int]error
}

func (network *addressSyncTestNetwork) Start(context.Context) error { return nil }

func (network *addressSyncTestNetwork) Stop(context.Context) error {
	network.mu.Lock()
	network.connected = false
	network.mu.Unlock()
	return nil
}

func (network *addressSyncTestNetwork) RemoteHeight() int { return 0 }

func (network *addressSyncTestNetwork) IsConnected() bool {
	network.mu.Lock()
	defer network.mu.Unlock()
	return network.connected
}

func (network *addressSyncTestNetwork) SetHeaderNotificationHandler(handler func(context.Context, any)) {
	network.mu.Lock()
	network.headerHandler = handler
	network.mu.Unlock()
}

func (network *addressSyncTestNetwork) SetAddressNotificationHandler(handler func(context.Context, any)) {
	network.mu.Lock()
	network.addressHandler = handler
	network.mu.Unlock()
}

func (network *addressSyncTestNetwork) SetConnectedHandler(handler func(context.Context)) {
	network.mu.Lock()
	network.connectedHandler = handler
	network.mu.Unlock()
}

func (network *addressSyncTestNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return map[string]any{"hex": ""}, nil
}

func (network *addressSyncTestNetwork) RetriableValue(
	ctx context.Context, method string, params []any, _ bool,
) (any, error) {
	if method == SPVTransactionBatchMethod {
		requested := make([]string, len(params))
		result := make(map[string]any, len(params))
		network.mu.Lock()
		call := len(network.transactionCalls)
		for index, value := range params {
			txid, ok := value.(string)
			if !ok {
				network.mu.Unlock()
				return nil, fmt.Errorf("transaction parameter %d has type %T", index, value)
			}
			requested[index] = txid
			if network.transactionOmit[txid] {
				continue
			}
			raw, exists := network.transactions[txid]
			if !exists {
				network.mu.Unlock()
				return nil, fmt.Errorf("transaction %s is not configured", txid)
			}
			result[txid] = []any{hex.EncodeToString(raw), nil}
		}
		network.transactionCalls = append(network.transactionCalls, requested)
		err := network.transactionError[call]
		network.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	if method == SPVTransactionMerkleMethod {
		return map[string]any{}, nil
	}
	if method != SPVAddressHistoryMethod || len(params) != 1 {
		return nil, fmt.Errorf("unexpected address RPC %s %#v", method, params)
	}
	address, _ := params[0].(string)
	network.mu.Lock()
	network.historyCalls++
	result := network.history[address]
	blocked := network.historyBlocks[address]
	if blocked == nil {
		blocked = network.historyBlock
	}
	started := network.historyStarted
	network.mu.Unlock()
	if started != nil {
		started <- address
	}
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if result == nil {
		return []any{}, nil
	}
	return result, nil
}

func addressSyncTransaction(
	t *testing.T, nonce uint32, outputHash [20]byte, previousID string, outputScript []byte,
) (*Transaction, []byte) {
	t.Helper()
	if outputScript == nil {
		outputScript = transactionP2PKH(outputHash[:])
	}
	var raw bytes.Buffer
	_ = binary.Write(&raw, binary.LittleEndian, uint32(1))
	raw.WriteByte(1)
	if previousID == "" {
		raw.Write(make([]byte, 32))
		_ = binary.Write(&raw, binary.LittleEndian, ^uint32(0))
		coinbase := make([]byte, 4)
		binary.LittleEndian.PutUint32(coinbase, nonce)
		raw.WriteByte(byte(len(coinbase)))
		raw.Write(coinbase)
	} else {
		display, err := hex.DecodeString(previousID)
		if err != nil || len(display) != 32 {
			t.Fatalf("previous transaction ID %q = %x, %v", previousID, display, err)
		}
		for left, right := 0, len(display)-1; left < right; left, right = left+1, right-1 {
			display[left], display[right] = display[right], display[left]
		}
		raw.Write(display)
		_ = binary.Write(&raw, binary.LittleEndian, uint32(0))
		raw.WriteByte(0)
	}
	_ = binary.Write(&raw, binary.LittleEndian, ^uint32(0))
	raw.WriteByte(1)
	_ = binary.Write(&raw, binary.LittleEndian, uint64(nonce)+1)
	raw.WriteByte(byte(len(outputScript)))
	raw.Write(outputScript)
	_ = binary.Write(&raw, binary.LittleEndian, nonce)
	encoded := append([]byte(nil), raw.Bytes()...)
	transaction, err := ParseTransaction(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return transaction, encoded
}

func addressSyncRemoteHistory(entries []AddressHistoryEntry) []any {
	history := make([]any, len(entries))
	for index, entry := range entries {
		history[index] = map[string]any{"tx_hash": entry.TxHash, "height": entry.Height}
	}
	return history
}

func addressSyncHistoryString(entries []AddressHistoryEntry) string {
	var history strings.Builder
	for _, entry := range entries {
		history.WriteString(serializeAddressHistoryEntry(entry))
	}
	return history.String()
}

func (network *addressSyncTestNetwork) SubscribeAddresses(
	ctx context.Context, addresses []string,
) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	network.mu.Lock()
	defer network.mu.Unlock()
	network.subscribeCalls = append(network.subscribeCalls, append([]string(nil), addresses...))
	if network.subscribeErr != nil {
		return nil, network.subscribeErr
	}
	statuses := make([]any, len(addresses))
	for index := range statuses {
		statuses[index] = network.subscriptionStatus
	}
	return statuses, nil
}

func (network *addressSyncTestNetwork) EmitConnected() {
	network.mu.Lock()
	handler := network.connectedHandler
	network.mu.Unlock()
	if handler != nil {
		handler(context.Background())
	}
}

func (network *addressSyncTestNetwork) EmitAddress(params any) {
	network.mu.Lock()
	handler := network.addressHandler
	network.mu.Unlock()
	if handler != nil {
		handler(context.Background(), params)
	}
}
