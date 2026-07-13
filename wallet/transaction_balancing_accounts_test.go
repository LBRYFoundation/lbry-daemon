package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionCreateAccountValidationMatchesPinnedOrdering(t *testing.T) {
	firstLedger := &Ledger{}
	secondLedger := &Ledger{}
	firstWallet := NewWallet()
	secondWallet := NewWallet()
	first := &Account{ledger: firstLedger, wallet: firstWallet}
	otherLedger := &Account{ledger: secondLedger, wallet: firstWallet}
	otherWallet := &Account{ledger: firstLedger, wallet: secondWallet}
	noWallet := &Account{ledger: firstLedger}

	tests := []struct {
		name    string
		funding []*Account
		change  *Account
		want    error
	}{
		{"mixed funding ledgers", []*Account{first, otherLedger}, first, ErrTransactionFundingLedgerMismatch},
		{"mixed funding wallets", []*Account{first, otherWallet}, first, ErrTransactionFundingWalletMismatch},
		{"change ledger mismatch", []*Account{first}, otherLedger, ErrTransactionChangeLedgerMismatch},
		{"change wallet mismatch", []*Account{first}, otherWallet, ErrTransactionChangeWalletMismatch},
		{"no funding accounts", nil, nil, ErrTransactionNoLedger},
		{"no wallet", []*Account{noWallet}, nil, ErrTransactionNoWallet},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outputs := []TransactionOutput{NewPayPubKeyHashOutput(1, make([]byte, 20))}
			transaction, err := CreateTransaction(
				context.Background(), nil, outputs, test.funding, test.change, false,
			)
			if transaction != nil || !errors.Is(err, test.want) || err.Error() != test.want.Error() {
				t.Fatalf("create result = %#v, %v, want %v", transaction, err, test.want)
			}
			if outputs[0].owner == nil || outputs[0].Position != 0 {
				t.Fatalf("output was not attached before validation: %#v", outputs[0])
			}
		})
	}
}

func TestTransactionCreateStandardSelectionAndChangeVector(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	ledger.FeePerByte = int64(10_000)
	for index, coins := range []uint64{1, 1, 3, 5, 10} {
		persistTransactionBalancingUTXO(
			t, ledger, address, coins*uint64(TransactionCoin), uint32(index+1), int64(index+1), true,
		)
	}

	transaction, err := CreateTransaction(
		ctx, nil,
		[]TransactionOutput{NewPayPubKeyHashOutput(
			3*uint64(TransactionCoin), bytes.Repeat([]byte{0xa1}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionInputAmounts(transaction), []uint64{5 * uint64(TransactionCoin)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected inputs = %v, want %v", got, want)
	}
	if got, want := transactionOutputAmounts(transaction), []uint64{
		3 * uint64(TransactionCoin), 197_520_000,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	selectedID := transaction.Inputs[0].PreviousOutputID()
	assertTransactionOutputReserved(t, ledger, selectedID, true)
	if err := ledger.ReleaseTransaction(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	assertTransactionOutputReserved(t, ledger, selectedID, false)

	transaction, err = CreateTransaction(
		ctx, nil,
		[]TransactionOutput{NewPayPubKeyHashOutput(
			298_000_000, bytes.Repeat([]byte{0xa2}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionInputAmounts(transaction), []uint64{3 * uint64(TransactionCoin)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exact-fee inputs = %v, want %v", got, want)
	}
	if got, want := transactionOutputAmounts(transaction), []uint64{298_000_000}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exact-fee outputs = %v, want %v", got, want)
	}
}

func TestTransactionCreateSQLiteSelectionReservationAndChangeVector(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	ledger.FeePerByte = int64(10_000)
	ledger.CoinSelectionStrategy = CoinSelectionStrategySQLite
	for index, coins := range []uint64{1, 1, 3, 5, 10} {
		persistTransactionBalancingUTXO(
			t, ledger, address, coins*uint64(TransactionCoin), uint32(index+1), int64(index+1), true,
		)
	}

	transaction, err := CreateTransaction(
		ctx, nil,
		[]TransactionOutput{NewPayPubKeyHashOutput(
			3*uint64(TransactionCoin), bytes.Repeat([]byte{0xb1}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionInputAmounts(transaction), []uint64{
		1 * uint64(TransactionCoin), 1 * uint64(TransactionCoin), 3 * uint64(TransactionCoin),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SQLite inputs = %v, want %v", got, want)
	}
	if got, want := transactionOutputAmounts(transaction), []uint64{
		3 * uint64(TransactionCoin), 194_560_000,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SQLite outputs = %v, want %v", got, want)
	}
	for _, input := range transaction.Inputs {
		if input.ResolvedOutput == nil || input.ResolvedOutput.owner == nil ||
			input.ResolvedOutput.owner.Position != -1 {
			t.Fatalf("SQLite parent position = %#v, want -1", input.ResolvedOutput)
		}
		assertTransactionOutputReserved(t, ledger, input.PreviousOutputID(), true)
	}
	if err := ledger.ReleaseTransaction(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	for _, input := range transaction.Inputs {
		assertTransactionOutputReserved(t, ledger, input.PreviousOutputID(), false)
	}
}

func TestSQLiteSelectionUsesStoredAmountButReturnsRawEstimatorAmount(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	ledger.FeePerByte = int64(0)
	ledger.CoinSelectionStrategy = CoinSelectionStrategySQLite
	parent := persistTransactionBalancingStoredUTXO(
		t, ledger, address, 100_000, 1_000_000, 1, 1, true,
	)
	spendables, err := ledger.GetSpendableTransactionInputs(ctx, 500_000, []*Account{account})
	if err != nil {
		t.Fatal(err)
	}
	if len(spendables) != 1 || spendables[0].EffectiveAmount != 100_000 ||
		spendables[0].Input.ResolvedOutput == nil ||
		spendables[0].Input.ResolvedOutput.Amount != 100_000 {
		t.Fatalf("mismatched stored/raw spendable = %#v", spendables)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), true)
}

func TestSpendableStrategyResolutionFollowsEmptyAndInsufficientEarlyReturns(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	ledger.CoinSelectionStrategy = "missing_strategy"
	spendables, err := ledger.GetSpendableTransactionInputs(ctx, 1, []*Account{account})
	if err != nil || len(spendables) != 0 {
		t.Fatalf("empty invalid strategy = %#v, %v", spendables, err)
	}
	parent := persistTransactionBalancingUTXO(
		t, ledger, address, uint64(TransactionCoin), 1, 1, true,
	)
	spendables, err = ledger.GetSpendableTransactionInputs(
		ctx, 2*TransactionCoin, []*Account{account},
	)
	if err != nil || len(spendables) != 0 {
		t.Fatalf("insufficient invalid strategy = %#v, %v", spendables, err)
	}
	spendables, err = ledger.GetSpendableTransactionInputs(
		ctx, TransactionCoin/2, []*Account{account},
	)
	if spendables != nil || !errors.Is(err, ErrUnknownCoinSelectionStrategy) {
		t.Fatalf("funded invalid strategy = %#v, %v", spendables, err)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), false)

	ledger.CoinSelectionStrategy = false
	spendables, err = ledger.GetSpendableTransactionInputs(
		ctx, TransactionCoin/2, []*Account{account},
	)
	if err != nil || len(spendables) != 1 {
		t.Fatalf("falsey strategy fallback = %#v, %v", spendables, err)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), true)
}

func TestTransactionReservationLockWaitIsContextAware(t *testing.T) {
	var mutex contextMutex
	if err := mutex.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mutex.Lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reservation lock = %v", err)
	}
	mutex.Unlock()
	if err := mutex.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	mutex.Unlock()
}

func TestTransactionCreateFailureReleasesCallerSuppliedInput(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	parent := persistTransactionBalancingUTXO(
		t, ledger, address, uint64(TransactionCoin), 1, 1, true,
	)
	if err := ledger.ReserveTransactionOutputs(ctx, []*TransactionOutput{parent}, true); err != nil {
		t.Fatal(err)
	}
	input, err := NewSpendInput(parent)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input},
		[]TransactionOutput{NewPayPubKeyHashOutput(
			2*uint64(TransactionCoin), bytes.Repeat([]byte{0xc1}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if transaction != nil || !errors.Is(err, ErrTransactionInsufficientFunds) {
		t.Fatalf("failed create = %#v, %v", transaction, err)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), false)
}

func TestTransactionCreateCleanupIgnoresCanceledOperationContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	parent := persistTransactionBalancingUTXO(
		t, ledger, address, uint64(TransactionCoin), 1, 1, true,
	)
	if err := ledger.ReserveTransactionOutputs(context.Background(), []*TransactionOutput{parent}, true); err != nil {
		t.Fatal(err)
	}
	input, err := NewSpendInput(parent)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input},
		[]TransactionOutput{NewPayPubKeyHashOutput(
			2*uint64(TransactionCoin), bytes.Repeat([]byte{0xc2}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if transaction != nil || err == nil {
		t.Fatalf("canceled create = %#v, %v", transaction, err)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), false)
}

func TestTransactionCreateInitialFeeFailureDoesNotRelease(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	parent := persistTransactionBalancingUTXO(
		t, ledger, address, uint64(TransactionCoin), 1, 1, true,
	)
	if err := ledger.ReserveTransactionOutputs(ctx, []*TransactionOutput{parent}, true); err != nil {
		t.Fatal(err)
	}
	input, err := NewSpendInput(parent)
	if err != nil {
		t.Fatal(err)
	}
	ledger.FeePerByte = struct{}{}
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input}, nil, []*Account{account}, account, false,
	)
	if transaction != nil || !errors.Is(err, ErrInvalidTransactionLedgerFee) {
		t.Fatalf("invalid-fee create = %#v, %v", transaction, err)
	}
	assertTransactionOutputReserved(t, ledger, parent.ID(), true)
}

func TestTransactionCreateReadsNameFeeOnlyForClaimCreation(t *testing.T) {
	ctx := context.Background()
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	ledger.FeePerNameCharacter = struct{}{}
	parent := persistTransactionBalancingUTXO(
		t, ledger, address, uint64(TransactionCoin), 1, 1, true,
	)
	input, err := NewSpendInput(parent)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input},
		[]TransactionOutput{NewPayPubKeyHashOutput(
			99_989_300, bytes.Repeat([]byte{0xd1}, 20),
		)},
		[]*Account{account}, account, false,
	)
	if err != nil || transaction == nil {
		t.Fatalf("ordinary output with invalid name fee = %#v, %v", transaction, err)
	}
	claim := NewClaimNameOutput(
		99_000_000, "claim", nil, bytes.Repeat([]byte{0xd2}, 20),
	)
	transaction, err = CreateTransaction(
		ctx, []TransactionInput{input}, []TransactionOutput{claim},
		[]*Account{account}, account, false,
	)
	if transaction != nil || !errors.Is(err, ErrInvalidTransactionLedgerFee) {
		t.Fatalf("claim output with invalid name fee = %#v, %v", transaction, err)
	}
}

func TestTransactionLedgerFeeRejectsCoercion(t *testing.T) {
	transaction := NewTransaction()
	for _, value := range []any{"50", 50.5, json.Number("50.5"), math.NaN()} {
		ledger := &Ledger{FeePerByte: value, transactionFeesSet: true}
		policy := transactionLedgerFeePolicy{ledger: func() *Ledger { return ledger }}
		if _, err := policy.BaseFee(transaction); !errors.Is(err, ErrInvalidTransactionLedgerFee) {
			t.Fatalf("fee value %#v produced %v", value, err)
		}
	}
	ledger := &Ledger{FeePerByte: 50.0, transactionFeesSet: true}
	policy := transactionLedgerFeePolicy{ledger: func() *Ledger { return ledger }}
	if fee, err := policy.BaseFee(transaction); err != nil || fee != int64(transaction.BaseSize())*50 {
		t.Fatalf("integral float fee = %d, %v", fee, err)
	}
}

func TestTransactionChangeAddressHashIsLegacyPermissiveBase58Slice(t *testing.T) {
	hash := bytes.Repeat([]byte{0x42}, 20)
	wrongNetworkPayload := append([]byte{0xff}, hash...)
	address := keys.EncodeBase58Check(wrongNetworkPayload)
	decoded, err := transactionChangeAddressHash(address)
	if err != nil || !bytes.Equal(decoded, hash) {
		t.Fatalf("wrong-prefix change hash = %x, %v, want %x", decoded, err, hash)
	}
	last := byte('1')
	if address[len(address)-1] == last {
		last = '2'
	}
	corruptChecksum := address[:len(address)-1] + string(last)
	decoded, err = transactionChangeAddressHash(corruptChecksum)
	if err != nil || !bytes.Equal(decoded, hash) {
		t.Fatalf("bad-checksum change hash = %x, %v, want %x", decoded, err, hash)
	}
	decoded, err = transactionChangeAddressHash("1")
	if err != nil || !bytes.Equal(decoded, []byte{0}) {
		t.Fatalf("short change hash = %x, %v, want 00", decoded, err)
	}
}

func TestTransactionCreateSignsWithPersistedAccountKey(t *testing.T) {
	ctx := context.Background()
	ledger, account, _ := transactionSigningTestWallet(t, ctx)
	privateKey, err := account.Receiving.GetPrivateKey(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.AddKeys(ctx, account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: privateKey.Address(), Chain: ReceiveChain,
		PublicKey: privateKey.PublicKey().CompressedBytes(), ChainCode: []byte{0}, N: 0, Depth: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	identifier := privateKey.Identifier()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(200_000_000, identifier[:]),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	inputs := []TransactionInput{input}
	transaction, err := CreateTransaction(
		ctx, inputs,
		[]TransactionOutput{NewPayPubKeyHashOutput(
			199_987_000, bytes.Repeat([]byte{0xe1}, 20),
		)},
		[]*Account{account}, account, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction.Inputs) != 1 || len(transaction.Outputs) != 1 ||
		bytes.Equal(transaction.Inputs[0].Script.Signature, make([]byte, transactionNullSignatureSize)) ||
		!bytes.Equal(transaction.Inputs[0].Script.PublicKey, privateKey.PublicKey().CompressedBytes()) ||
		!bytes.Equal(inputs[0].Script.Signature, transaction.Inputs[0].Script.Signature) ||
		!bytes.Equal(inputs[0].Script.PublicKey, privateKey.PublicKey().CompressedBytes()) ||
		transaction.Raw == nil || transaction.ID == "" {
		t.Fatalf("signed create result = %#v", transaction)
	}
}

func newTransactionBalancingAccountLedger(t *testing.T) (*Ledger, *Account, string) {
	t.Helper()
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	wallet := NewWallet()
	wallet.AddAccount(account)
	addresses, err := account.EnsureAddressGap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) == 0 {
		t.Fatal("address gap produced no addresses")
	}
	receiving, err := account.Receiving.GetAddresses(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(receiving) == 0 {
		t.Fatal("receiving inventory is empty")
	}
	return ledger, account, receiving[0]
}

func persistTransactionBalancingUTXO(
	t *testing.T,
	ledger *Ledger,
	address string,
	amount uint64,
	nonce uint32,
	height int64,
	verified bool,
) *TransactionOutput {
	return persistTransactionBalancingStoredUTXO(
		t, ledger, address, amount, amount, nonce, height, verified,
	)
}

func persistTransactionBalancingStoredUTXO(
	t *testing.T,
	ledger *Ledger,
	address string,
	rawAmount uint64,
	storedAmount uint64,
	nonce uint32,
	height int64,
	verified bool,
) *TransactionOutput {
	t.Helper()
	hash, err := ledger.addressHash160(address)
	if err != nil {
		t.Fatal(err)
	}
	parent := NewTransaction()
	parent.LockTime = nonce
	parent.AddInputs([]TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0),
		Coinbase: []byte{byte(nonce)},
	}})
	parent.AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(rawAmount, hash[:])})
	parent.Height = height
	parent.Position = int64(nonce)
	parent.IsVerified = verified
	if err := parent.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	outputAddress := address
	if storedAmount > uint64(^uint64(0)>>1) {
		t.Fatal("test amount exceeds int64")
	}
	row := ledgerdb.TransactionIORow{
		Transaction: ledgerdb.TransactionRow{
			TXID: parent.ID, Raw: append([]byte(nil), parent.Raw...),
			Height: height, Position: int64(nonce), IsVerified: verified,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: parent.Outputs[0].ID(), Address: &outputAddress,
			Position: 0, Amount: int64(storedAmount),
			Script: append([]byte(nil), parent.Outputs[0].Script.Source...),
		}},
	}
	if err := ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{row}, address,
		parent.ID+":1:",
	); err != nil {
		t.Fatal(err)
	}
	return &parent.Outputs[0]
}

func transactionInputAmounts(transaction *Transaction) []uint64 {
	amounts := make([]uint64, len(transaction.Inputs))
	for index, input := range transaction.Inputs {
		if input.ResolvedOutput != nil {
			amounts[index] = currentTransactionOutput(input.ResolvedOutput).Amount
		}
	}
	return amounts
}

func transactionOutputAmounts(transaction *Transaction) []uint64 {
	amounts := make([]uint64, len(transaction.Outputs))
	for index, output := range transaction.Outputs {
		amounts[index] = output.Amount
	}
	return amounts
}

func assertTransactionOutputReserved(t *testing.T, ledger *Ledger, outputID string, want bool) {
	t.Helper()
	outputs, err := ledger.Database.GetOutputsByID(context.Background(), []string{outputID})
	if err != nil {
		t.Fatal(err)
	}
	output, exists := outputs[outputID]
	if !exists || output.IsReserved != want {
		t.Fatalf("output %s = %#v, want reserved %v", outputID, output, want)
	}
}
