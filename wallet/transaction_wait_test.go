package wallet

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

const transactionWaitTestRoundDuration = 5 * time.Millisecond

func TestTransactionWaitDerivesAffectedAddresses(t *testing.T) {
	ledger := &Ledger{Network: keys.MainNet}
	inputHash := bytes.Repeat([]byte{0x11}, 20)
	outputHash := bytes.Repeat([]byte{0x22}, 20)
	scriptHash := bytes.Repeat([]byte{0x33}, 20)
	resolved := NewPayPubKeyHashOutput(1, inputHash)
	transaction := &Transaction{
		ID: "derived-addresses",
		Inputs: []TransactionInput{
			{ResolvedOutput: &resolved},
			{}, // Unresolved inputs do not contribute an address.
		},
		Outputs: []TransactionOutput{
			NewPayPubKeyHashOutput(2, outputHash),
			NewPayScriptHashOutput(3, scriptHash),
			NewReturnDataOutput([]byte("not an address")),
			NewPayPubKeyHashOutput(4, inputHash), // Duplicate input address.
		},
	}

	addresses, err := ledger.transactionWaitAddresses(transaction)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		keys.EncodeBase58Check(append([]byte{keys.MainNet.PubKeyAddressPrefix()}, inputHash...)),
		keys.EncodeBase58Check(append([]byte{keys.MainNet.PubKeyAddressPrefix()}, outputHash...)),
		keys.EncodeBase58Check(append([]byte{keys.MainNet.ScriptAddressPrefix()}, scriptHash...)),
	}
	if !reflect.DeepEqual(addresses, want) {
		t.Fatalf("wait addresses = %#v, want %#v", addresses, want)
	}
}

func TestTransactionWaitRejectsResolvedScriptHashInput(t *testing.T) {
	ledger := &Ledger{Network: keys.MainNet}
	resolved := NewPayScriptHashOutput(1, bytes.Repeat([]byte{0x41}, 20))
	transaction := &Transaction{
		ID:     "resolved-script-hash",
		Inputs: []TransactionInput{{ResolvedOutput: &resolved}},
	}

	addresses, err := ledger.transactionWaitAddresses(transaction)
	if addresses != nil {
		t.Fatalf("addresses on input script error = %#v, want nil", addresses)
	}
	if !errors.Is(err, ErrTransactionWaitInputScript) {
		t.Fatalf("resolved P2SH input error = %v", err)
	}
}

func TestTransactionWaitEventRequiresEveryAffectedOwnedAddress(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	firstOutput := NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x51}, 20))
	secondOutput := NewPayScriptHashOutput(2, bytes.Repeat([]byte{0x52}, 20))
	firstAddress := transactionWaitTestOutputAddress(t, ledger, firstOutput)
	secondAddress := transactionWaitTestOutputAddress(t, ledger, secondOutput)
	transactionWaitTestOwn(t, ledger, firstAddress, secondAddress, "unrelated-owned-address")
	target := &Transaction{
		ID:      "all-address-event-barrier",
		Outputs: []TransactionOutput{firstOutput, secondOutput},
	}

	done := make(chan error, 1)
	go func() {
		done <- ledger.waitTransaction(
			context.Background(), target, -1, 2,
			transactionWaitTestOptions(100*time.Millisecond),
		)
	}()
	transactionWaitTestAwaitListenerCount(t, ledger, 1)
	if err := ledger.publishTransactionBatch(firstAddress, []*Transaction{{
		ID: target.ID, Height: -1,
	}}); err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAssertPending(t, done, "first of two address events")
	if err := ledger.publishTransactionBatch("unrelated-owned-address", []*Transaction{{
		ID: target.ID, Height: -1,
	}}); err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAssertPending(t, done, "unaffected owned address event")
	if err := ledger.publishTransactionBatch(secondAddress, []*Transaction{{
		ID: target.ID, Height: -1,
	}}); err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAwaitResult(t, done, nil)
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitEventAppliesHeightPredicate(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	output := NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x61}, 20))
	address := transactionWaitTestOutputAddress(t, ledger, output)
	transactionWaitTestOwn(t, ledger, address)
	target := &Transaction{ID: "event-height", Outputs: []TransactionOutput{output}}

	done := make(chan error, 1)
	go func() {
		done <- ledger.waitTransaction(
			context.Background(), target, 5, 2,
			transactionWaitTestOptions(100*time.Millisecond),
		)
	}()
	transactionWaitTestAwaitListenerCount(t, ledger, 1)
	if err := ledger.publishTransactionBatch(address, []*Transaction{{
		ID: target.ID, Height: 4,
	}}); err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAssertPending(t, done, "event below requested height")
	if err := ledger.publishTransactionBatch(address, []*Transaction{{
		ID: target.ID, Height: 5,
	}}); err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAwaitResult(t, done, nil)
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitHistoryHeightMatrix(t *testing.T) {
	testCases := []struct {
		name            string
		localHeight     int64
		requestedHeight int64
		want            bool
	}{
		{name: "below negative", localHeight: -2, requestedHeight: -1, want: false},
		{name: "equal negative", localHeight: -2, requestedHeight: -2, want: true},
		{name: "below positive", localHeight: 1, requestedHeight: 2, want: false},
		{name: "equal positive", localHeight: 2, requestedHeight: 2, want: true},
		{name: "above positive", localHeight: 3, requestedHeight: 2, want: true},
		{name: "mempool satisfies zero", localHeight: 0, requestedHeight: 0, want: true},
		{name: "mempool satisfies positive", localHeight: 0, requestedHeight: 8, want: true},
		{name: "mempool satisfies negative", localHeight: 0, requestedHeight: -1, want: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := newTransactionWaitTestLedger(t)
			address := "owned-history-address"
			transactionWaitTestOwn(t, ledger, address)
			if err := ledger.Database.SetAddressHistory(
				context.Background(), address,
				"history-height:"+transactionWaitTestInteger(testCase.localHeight)+":",
			); err != nil {
				t.Fatal(err)
			}

			matched, err := ledger.waitTransactionRound(
				context.Background(), "history-height", testCase.requestedHeight,
				[]string{address}, transactionWaitTestRoundDuration,
			)
			if err != nil {
				t.Fatal(err)
			}
			if matched != testCase.want {
				t.Fatalf(
					"history height %d at requested height %d matched = %t, want %t",
					testCase.localHeight, testCase.requestedHeight, matched, testCase.want,
				)
			}
			transactionWaitTestAssertNoListeners(t, ledger)
		})
	}
}

func TestTransactionWaitHistoryStopsAtFirstInsufficientAddressRecord(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	addresses := []string{"first-candidate", "second-candidate"}
	transactionWaitTestOwn(t, ledger, addresses...)
	records, err := ledger.Database.GetAddresses(
		context.Background(), ledgerdb.AddressQuery{Addresses: addresses},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("owned records = %d, want 2", len(records))
	}
	first, second := records[0].Address, records[1].Address
	if err := ledger.Database.SetAddressHistory(
		context.Background(), first, "short-circuit-height:1:",
	); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.SetAddressHistory(
		context.Background(), second, "short-circuit-height:2:",
	); err != nil {
		t.Fatal(err)
	}

	matched, err := ledger.waitTransactionRound(
		context.Background(), "short-circuit-height", 2, addresses,
		transactionWaitTestRoundDuration,
	)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("later sufficient address history overrode the first insufficient match")
	}
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitWithNoDerivedAddressesScansEveryOwnedRecord(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	transactionWaitTestOwn(t, ledger, "first-unrelated", "second-unrelated")
	target := NewTransaction()
	if addresses, err := ledger.transactionWaitAddresses(target); err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatalf("empty transaction addresses = %#v, want empty", addresses)
	}
	if err := ledger.Database.SetAddressHistory(
		context.Background(), "second-unrelated", target.ID+":0:",
	); err != nil {
		t.Fatal(err)
	}

	err := ledger.waitTransaction(
		context.Background(), target, 0, 1,
		transactionWaitTestOptions(transactionWaitTestRoundDuration),
	)
	if err != nil {
		t.Fatal(err)
	}
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitRequiresAtLeastOneSelectedOwnedRecord(t *testing.T) {
	t.Run("external derived address", func(t *testing.T) {
		ledger := newTransactionWaitTestLedger(t)
		transactionWaitTestOwn(t, ledger, "different-owned-address")
		target := &Transaction{
			ID: "external-only",
			Outputs: []TransactionOutput{NewPayPubKeyHashOutput(
				1, bytes.Repeat([]byte{0x71}, 20),
			)},
		}
		err := ledger.waitTransaction(
			context.Background(), target, -1, 1,
			transactionWaitTestOptions(transactionWaitTestRoundDuration),
		)
		if !errors.Is(err, ErrTransactionWaitNoRecords) {
			t.Fatalf("external-only wait error = %v", err)
		}
		transactionWaitTestAssertNoListeners(t, ledger)
	})

	t.Run("empty all-record scan", func(t *testing.T) {
		ledger := newTransactionWaitTestLedger(t)
		err := ledger.waitTransaction(
			context.Background(), NewTransaction(), -1, 1,
			transactionWaitTestOptions(transactionWaitTestRoundDuration),
		)
		if !errors.Is(err, ErrTransactionWaitNoRecords) {
			t.Fatalf("empty-record wait error = %v", err)
		}
		transactionWaitTestAssertNoListeners(t, ledger)
	})
}

func TestTransactionWaitNegativeTimeoutIsImmediate(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	target := NewTransaction()
	clockCalls := 0
	err := ledger.waitTransaction(
		context.Background(), target, -1, -0.25,
		transactionWaitOptions{
			roundDuration: time.Hour,
			now: func() time.Duration {
				clockCalls++
				return 0
			},
		},
	)
	if !errors.Is(err, ErrTransactionWaitTimeout) {
		t.Fatalf("negative timeout error = %v", err)
	}
	var timeoutErr *TransactionWaitTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.TransactionID != target.ID {
		t.Fatalf("negative timeout detail = %#v", timeoutErr)
	}
	wantMessage := "Timed out waiting for transaction. " + target.ID
	if err.Error() != wantMessage {
		t.Fatalf("negative timeout message = %q, want %q", err.Error(), wantMessage)
	}
	if clockCalls != 2 {
		t.Fatalf("negative timeout clock calls = %d, want 2 and no wait round", clockCalls)
	}
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitOuterTimingUsesIntegerInclusiveSeconds(t *testing.T) {
	t.Run("timeout one permits round at elapsed second one", func(t *testing.T) {
		ledger := newTransactionWaitTestLedger(t)
		address := "inclusive-second-address"
		transactionWaitTestOwn(t, ledger, address)
		target := &Transaction{ID: "inclusive-second"}
		clockCalls := 0
		var historyErr error
		err := ledger.waitTransaction(
			context.Background(), target, -1, 1,
			transactionWaitOptions{
				roundDuration: transactionWaitTestRoundDuration,
				now: func() time.Duration {
					clockCalls++
					switch clockCalls {
					case 1, 2:
						return 0
					case 3:
						historyErr = ledger.Database.SetAddressHistory(
							context.Background(), address, target.ID+":0:",
						)
						return time.Second
					default:
						return 2 * time.Second
					}
				},
			},
		)
		if historyErr != nil {
			t.Fatal(historyErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if clockCalls != 3 {
			t.Fatalf("inclusive timeout clock calls = %d, want 3", clockCalls)
		}
		transactionWaitTestAssertNoListeners(t, ledger)
	})

	t.Run("zero becomes six hundred seconds", func(t *testing.T) {
		ledger := newTransactionWaitTestLedger(t)
		address := "blocking-timeout-address"
		transactionWaitTestOwn(t, ledger, address)
		target := &Transaction{ID: "blocking-timeout"}
		if err := ledger.Database.SetAddressHistory(
			context.Background(), address, target.ID+":0:",
		); err != nil {
			t.Fatal(err)
		}
		clockCalls := 0
		err := ledger.waitTransaction(
			context.Background(), target, -1, 0,
			transactionWaitOptions{
				roundDuration: transactionWaitTestRoundDuration,
				now: func() time.Duration {
					clockCalls++
					if clockCalls == 1 {
						return 0
					}
					return 600 * time.Second
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if clockCalls != 2 {
			t.Fatalf("blocking timeout clock calls = %d, want 2", clockCalls)
		}
		transactionWaitTestAssertNoListeners(t, ledger)
	})

	t.Run("zero timeout stops after six hundred seconds", func(t *testing.T) {
		ledger := newTransactionWaitTestLedger(t)
		target := NewTransaction()
		clockCalls := 0
		err := ledger.waitTransaction(
			context.Background(), target, -1, 0,
			transactionWaitOptions{
				roundDuration: time.Hour,
				now: func() time.Duration {
					clockCalls++
					if clockCalls == 1 {
						return 0
					}
					return 601 * time.Second
				},
			},
		)
		if !errors.Is(err, ErrTransactionWaitTimeout) {
			t.Fatalf("post-600-second timeout error = %v", err)
		}
		if clockCalls != 2 {
			t.Fatalf("post-600-second clock calls = %d, want 2", clockCalls)
		}
		transactionWaitTestAssertNoListeners(t, ledger)
	})
}

func TestTransactionWaitContextCancellationRemovesListener(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	output := NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x81}, 20))
	address := transactionWaitTestOutputAddress(t, ledger, output)
	transactionWaitTestOwn(t, ledger, address)
	target := &Transaction{ID: "context-cancel", Outputs: []TransactionOutput{output}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ledger.waitTransaction(
			ctx, target, -1, 30,
			transactionWaitTestOptions(100*time.Millisecond),
		)
	}()
	transactionWaitTestAwaitListenerCount(t, ledger, 1)
	cancel()
	transactionWaitTestAwaitResult(t, done, context.Canceled)
	transactionWaitTestAssertNoListeners(t, ledger)
}

func TestTransactionWaitRoundTimeoutRemovesListener(t *testing.T) {
	ledger := newTransactionWaitTestLedger(t)
	address := "owned-with-empty-history"
	transactionWaitTestOwn(t, ledger, address)
	matched, err := ledger.waitTransactionRound(
		context.Background(), "absent-transaction", -1, []string{address},
		transactionWaitTestRoundDuration,
	)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("empty history unexpectedly matched transaction")
	}
	transactionWaitTestAssertNoListeners(t, ledger)
}

func newTransactionWaitTestLedger(t *testing.T) *Ledger {
	t.Helper()
	database, err := ledgerdb.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction wait database: %v", err)
		}
	})
	return &Ledger{Network: keys.MainNet, Database: database}
}

func transactionWaitTestOwn(t *testing.T, ledger *Ledger, addresses ...string) {
	t.Helper()
	addressKeys := make([]ledgerdb.AddressKey, len(addresses))
	for index, address := range addresses {
		addressKeys[index] = ledgerdb.AddressKey{
			Address: address, Chain: 0, PublicKey: []byte{byte(index + 1)},
			ChainCode: []byte{byte(index + 11)}, N: int64(index), Depth: 1,
		}
	}
	if err := ledger.Database.AddKeys(context.Background(), "wait-test-account", addressKeys); err != nil {
		t.Fatal(err)
	}
}

func transactionWaitTestOutputAddress(
	t *testing.T, ledger *Ledger, output TransactionOutput,
) string {
	t.Helper()
	address, err := output.Address(ledger.Network)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func transactionWaitTestOptions(roundDuration time.Duration) transactionWaitOptions {
	started := time.Now()
	return transactionWaitOptions{
		roundDuration: roundDuration,
		now:           func() time.Duration { return time.Since(started) },
	}
}

func transactionWaitTestAwaitListenerCount(t *testing.T, ledger *Ledger, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if transactionWaitTestListenerCount(ledger) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"transaction listener count = %d, want %d",
				transactionWaitTestListenerCount(ledger), want,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func transactionWaitTestListenerCount(ledger *Ledger) int {
	ledger.transactionEvents.mu.Lock()
	defer ledger.transactionEvents.mu.Unlock()
	return len(ledger.transactionEvents.listeners)
}

func transactionWaitTestAssertNoListeners(t *testing.T, ledger *Ledger) {
	t.Helper()
	transactionWaitTestAwaitListenerCount(t, ledger, 0)
}

func transactionWaitTestAssertPending(t *testing.T, done <-chan error, action string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("transaction wait finished after %s: %v", action, err)
	case <-time.After(10 * time.Millisecond):
	}
}

func transactionWaitTestAwaitResult(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("transaction wait error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("transaction wait did not finish")
	}
}

func transactionWaitTestInteger(value int64) string {
	return strconv.FormatInt(value, 10)
}
