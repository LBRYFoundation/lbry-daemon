package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"lbry/daemon/wallet/keys"
)

var transactionBalancingOraclePinnedSources = map[string]string{
	"lbry/error/__init__.py":                  "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/wallet/coinselection.py":            "96c686fc3a9037468e6d9c684080af4ee84f3710be7f6b42f1ddcc6ce5dc474e",
	"lbry/wallet/constants.py":                "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
	"tests/unit/wallet/test_coinselection.py": "effdccee1eba922d311ca85c6a7c8eb0cc5381d8e54f05331e813d97249d63f7",
}

type transactionBalancingOracleResponse struct {
	Reference struct {
		Commit                string            `json:"commit"`
		Version               string            `json:"version"`
		SourceSHA256          map[string]string `json:"source_sha256"`
		BalancingSourceSHA256 map[string]string `json:"balancing_source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion             string `json:"python_version"`
		TransactionCreateExecuted bool   `json:"transaction_create_executed"`
	} `json:"metadata"`
	Balancing transactionBalancingOracleCases `json:"balancing"`
}

type transactionBalancingOracleCases struct {
	FeeContract transactionBalancingOracleFee       `json:"fee_contract"`
	Success     []transactionBalancingOracleOutcome `json:"success"`
	Failures    []transactionBalancingOracleOutcome `json:"failures"`
	Validation  []transactionBalancingOracleOutcome `json:"validation"`
}

type transactionBalancingOracleFee struct {
	FeePerByte           int64 `json:"fee_per_byte"`
	FeePerNameCharacter  int64 `json:"fee_per_name_char"`
	Dust                 int64 `json:"dust"`
	InputSize            int   `json:"input_size"`
	InputFee             int64 `json:"input_fee"`
	InputEffectiveAmount int64 `json:"input_effective_amount"`
	OrdinaryOutputSize   int   `json:"ordinary_output_size"`
	OrdinaryOutputFee    int64 `json:"ordinary_output_fee"`
	ChangeProbeSize      int   `json:"change_probe_size"`
	ChangeProbeFee       int64 `json:"change_probe_fee"`
	ClaimOutputSize      int   `json:"claim_output_size"`
	ClaimOutputFee       int64 `json:"claim_output_fee"`
	BaseSize             int   `json:"base_size"`
	BaseFee              int64 `json:"base_fee"`
	EffectiveInputSum    int64 `json:"effective_input_sum"`
	TotalOutputSum       int64 `json:"total_output_sum"`
}

type transactionBalancingOracleOutcome struct {
	Name          string                                 `json:"name"`
	OK            bool                                   `json:"ok"`
	SignRequested bool                                   `json:"sign_requested"`
	Transaction   *transactionBalancingOracleTransaction `json:"transaction"`
	ErrorType     *string                                `json:"error_type"`
	ErrorMessage  *string                                `json:"error_message"`
	SideEffects   transactionBalancingOracleSideEffects  `json:"side_effects"`
}

type transactionBalancingOracleTransaction struct {
	Version                 uint32   `json:"version"`
	LockTime                uint32   `json:"locktime"`
	RawHex                  string   `json:"raw_hex"`
	ID                      string   `json:"id"`
	Size                    int      `json:"size"`
	BaseSize                int      `json:"base_size"`
	InputSum                uint64   `json:"input_sum"`
	OutputSum               uint64   `json:"output_sum"`
	Fee                     int64    `json:"fee"`
	InputAmounts            []uint64 `json:"input_amounts"`
	InputEffectiveAmounts   []int64  `json:"input_effective_amounts"`
	InputSizes              []int    `json:"input_sizes"`
	InputSequences          []uint32 `json:"input_sequences"`
	InputPreviousIDs        []string `json:"input_previous_ids"`
	InputScriptsHex         []string `json:"input_scripts_hex"`
	InputSignaturesHex      []string `json:"input_signatures_hex"`
	InputPublicKeysHex      []string `json:"input_public_keys_hex"`
	OutputAmounts           []uint64 `json:"output_amounts"`
	OutputSizes             []int    `json:"output_sizes"`
	OutputScriptsHex        []string `json:"output_scripts_hex"`
	OutputInternalTransfers []*bool  `json:"output_internal_transfers"`
}

type transactionBalancingOracleSideEffects struct {
	SelectionCalls            []transactionBalancingOracleSelection `json:"selection_calls"`
	SelectionBatchesRemaining int                                   `json:"selection_batches_remaining"`
	ChangeCalls               map[string]int                        `json:"change_calls"`
	AddressHashCalls          []string                              `json:"address_hash_calls"`
	KeyLookups                []string                              `json:"key_lookups"`
	ReleaseCount              int                                   `json:"release_count"`
	ReleaseSnapshots          []transactionBalancingOracleRelease   `json:"release_snapshots"`
}

type transactionBalancingOracleSelection struct {
	Amount   int64    `json:"amount"`
	Accounts []string `json:"accounts"`
}

type transactionBalancingOracleRelease struct {
	InputAmounts     []*uint64 `json:"input_amounts"`
	OutputAmounts    []uint64  `json:"output_amounts"`
	InputScriptsHex  []*string `json:"input_scripts_hex"`
	OutputScriptsHex []string  `json:"output_scripts_hex"`
}

func TestTransactionBalancingMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionBalancingOracle(t)
	if oracle.Reference.Commit != transactionOraclePinnedCommit ||
		oracle.Reference.Version != transactionOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.BalancingSourceSHA256, transactionBalancingOraclePinnedSources) {
		t.Fatalf("transaction balancing oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.TransactionCreateExecuted {
		t.Fatalf("transaction balancing oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction balancing oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}

	assertTransactionBalancingFeeContract(t, oracle.Balancing.FeeContract)
	assertTransactionBalancingValidation(t, oracle.Balancing.Validation)
	assertGoTransactionBalancingSuccess(t, oracle.Balancing.Success)
	assertGoTransactionBalancingFailures(t, oracle.Balancing.Failures)
}

func assertTransactionBalancingFeeContract(t *testing.T, fee transactionBalancingOracleFee) {
	t.Helper()
	want := transactionBalancingOracleFee{
		FeePerByte: 50, FeePerNameCharacter: 200_000, Dust: 1_000,
		InputSize: 148, InputFee: 7_400, InputEffectiveAmount: 92_600,
		OrdinaryOutputSize: 34, OrdinaryOutputFee: 1_700,
		ChangeProbeSize: 46, ChangeProbeFee: 2_300,
		ClaimOutputSize: 42, ClaimOutputFee: 600_000,
		BaseSize: 10, BaseFee: 500, EffectiveInputSum: 92_600, TotalOutputSum: 1_701,
	}
	if !reflect.DeepEqual(fee, want) {
		t.Fatalf("transaction balancing fee contract = %+v, want %+v", fee, want)
	}

	policy := LegacyTransactionFeePolicy{
		FeePerByte: fee.FeePerByte, FeePerNameCharacter: fee.FeePerNameCharacter,
	}
	input := transactionBalancingOracleInput(t, 100_000, bytes.Repeat([]byte{0x81}, 20))
	ordinary := NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x82}, 20))
	changeProbe := NewPayPubKeyHashOutput(uint64(TransactionCoin), make([]byte, 32))
	claim := NewClaimNameOutput(1, "abc", nil, bytes.Repeat([]byte{0x83}, 20))
	transaction := NewTransaction().
		AddInputs([]TransactionInput{input}).
		AddOutputs([]TransactionOutput{ordinary})
	baseFee, err := policy.BaseFee(transaction)
	if err != nil {
		t.Fatal(err)
	}
	inputFee, err := policy.InputFee(&transaction.Inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	ordinaryFee, err := policy.OutputFee(&ordinary)
	if err != nil {
		t.Fatal(err)
	}
	changeProbeFee, err := policy.OutputFee(&changeProbe)
	if err != nil {
		t.Fatal(err)
	}
	claimFee, err := policy.OutputFee(&claim)
	if err != nil {
		t.Fatal(err)
	}
	got := transactionBalancingOracleFee{
		FeePerByte: fee.FeePerByte, FeePerNameCharacter: fee.FeePerNameCharacter,
		Dust: int64(TransactionDust), InputSize: transaction.Inputs[0].Size(),
		InputFee:             inputFee,
		InputEffectiveAmount: int64(transaction.Inputs[0].ResolvedOutput.Amount) - inputFee,
		OrdinaryOutputSize:   ordinary.Size(), OrdinaryOutputFee: ordinaryFee,
		ChangeProbeSize: changeProbe.Size(), ChangeProbeFee: changeProbeFee,
		ClaimOutputSize: claim.Size(), ClaimOutputFee: claimFee,
		BaseSize: transaction.BaseSize(), BaseFee: baseFee,
		EffectiveInputSum: int64(transaction.InputSum()) - inputFee,
		TotalOutputSum:    int64(ordinary.Amount) + ordinaryFee,
	}
	if !reflect.DeepEqual(got, fee) {
		t.Fatalf("Go transaction balancing fee contract = %+v, Python %+v", got, fee)
	}
}

func assertTransactionBalancingValidation(
	t *testing.T, validation []transactionBalancingOracleOutcome,
) {
	t.Helper()
	wants := []struct{ name, message string }{
		{"mixed funding ledgers", "All funding accounts used to create a transaction must be on the same ledger."},
		{"mixed funding wallets", "All funding accounts used to create a transaction must be from the same wallet."},
		{"change ledger mismatch", "Change account must use same ledger as funding accounts."},
		{"change wallet mismatch", "Change account must use same wallet as funding accounts."},
		{"no funding accounts", "No ledger found."},
		{"no wallet", "No wallet found."},
	}
	if len(validation) != len(wants) {
		t.Fatalf("validation cases = %d, want %d", len(validation), len(wants))
	}
	for index, want := range wants {
		got := validation[index]
		if got.Name != want.name || got.OK || got.ErrorType == nil || *got.ErrorType != "ValueError" ||
			got.ErrorMessage == nil || *got.ErrorMessage != want.message ||
			got.Transaction != nil || got.SideEffects.ReleaseCount != 0 ||
			len(got.SideEffects.SelectionCalls) != 0 || len(got.SideEffects.ReleaseSnapshots) != 0 {
			t.Fatalf("validation case %d = %+v, want %+v", index, got, want)
		}
	}
}

func assertGoTransactionBalancingSuccess(t *testing.T, expected []transactionBalancingOracleOutcome) {
	t.Helper()
	policy := LegacyTransactionFeePolicy{FeePerByte: 50, FeePerNameCharacter: 200_000}
	type fixture struct {
		name    string
		options TransactionBalanceOptions
		harness *transactionBalancingOracleHarness
	}
	fixtures := make([]fixture, 0, 7)

	harness := newTransactionBalancingOracleHarness()
	fixtures = append(fixtures, fixture{
		name: "provided input and output with change", harness: harness,
		options: harness.options(
			[]TransactionInput{transactionBalancingOracleInput(
				t, 1_600_000, bytes.Repeat([]byte{0x11}, 20),
			)},
			[]TransactionOutput{NewPayPubKeyHashOutput(
				1_500_000, bytes.Repeat([]byte{0x12}, 20),
			)}, policy, false,
		),
	})

	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 1_000_000, 0x21),
		}},
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 1_100_000, 0x22),
		}},
	}
	fixtures = append(fixtures, fixture{
		name: "deficit selects once then breaks underfunded", harness: harness,
		options: harness.options(nil, []TransactionOutput{NewPayPubKeyHashOutput(
			2_000_000, bytes.Repeat([]byte{0x23}, 20),
		)}, policy, false),
	})

	for _, dust := range []struct {
		name   string
		amount uint64
	}{
		{"change exactly dust is omitted", 913_400},
		{"change above dust is added", 913_401},
	} {
		harness = newTransactionBalancingOracleHarness()
		fixtures = append(fixtures, fixture{
			name: dust.name, harness: harness,
			options: harness.options(
				[]TransactionInput{transactionBalancingOracleInput(
					t, dust.amount, bytes.Repeat([]byte{0x31}, 20),
				)},
				[]TransactionOutput{NewPayPubKeyHashOutput(
					900_000, bytes.Repeat([]byte{0x32}, 20),
				)}, policy, false,
			),
		})
	}

	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 8_400, 0x42),
		}},
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 15_304, 0x43),
		}},
	}
	fixtures = append(fixtures, fixture{
		name: "zero-output retry then change", harness: harness,
		options: harness.options([]TransactionInput{transactionBalancingOracleInput(
			t, 11_200, bytes.Repeat([]byte{0x41}, 20),
		)}, nil, policy, false),
	})

	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 13_002, 0x52),
		}},
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 13_002, 0x53),
		}},
	}
	fixtures = append(fixtures, fixture{
		name: "five-pass zero-output return", harness: harness,
		options: harness.options([]TransactionInput{transactionBalancingOracleInput(
			t, 11_200, bytes.Repeat([]byte{0x51}, 20),
		)}, nil, policy, false),
	})

	harness = newTransactionBalancingOracleHarness()
	privateKey := transactionSigningOraclePrivateKey(t, 8)
	identifier := privateKey.Identifier()
	address := "hash160:" + hex.EncodeToString(identifier[:])
	harness.signingKeys[address] = privateKey
	fixtures = append(fixtures, fixture{
		name: "default signing enabled", harness: harness,
		options: harness.options(
			[]TransactionInput{transactionBalancingOracleInput(
				t, 1_600_000, identifier[:],
			)},
			[]TransactionOutput{NewPayPubKeyHashOutput(
				1_500_000, bytes.Repeat([]byte{0x61}, 20),
			)}, policy, true,
		),
	})

	if len(expected) != len(fixtures) {
		t.Fatalf("transaction balancing success cases = %d, want %d", len(expected), len(fixtures))
	}
	for index, fixture := range fixtures {
		want := expected[index]
		if want.Name != fixture.name || !want.OK || want.Transaction == nil ||
			want.ErrorType != nil || want.ErrorMessage != nil ||
			want.SignRequested != !fixture.options.SkipSigning {
			t.Fatalf("transaction balancing success contract %d = %+v", index, want)
		}
		transaction, err := CreateBalancedTransaction(context.Background(), fixture.options)
		if err != nil {
			t.Fatalf("%s: %v", fixture.name, err)
		}
		gotTransaction := summarizeTransactionBalancingOracleTransaction(transaction)
		if !reflect.DeepEqual(gotTransaction, want.Transaction) {
			t.Fatalf("%s transaction =\n%+v\nPython =\n%+v", fixture.name, gotTransaction, want.Transaction)
		}
		gotSideEffects := fixture.harness.sideEffects()
		if !reflect.DeepEqual(gotSideEffects, want.SideEffects) {
			t.Fatalf("%s side effects = %+v, Python %+v", fixture.name, gotSideEffects, want.SideEffects)
		}
	}
}

func assertGoTransactionBalancingFailures(t *testing.T, expected []transactionBalancingOracleOutcome) {
	t.Helper()
	policy := LegacyTransactionFeePolicy{FeePerByte: 50, FeePerNameCharacter: 200_000}
	type fixture struct {
		name      string
		options   TransactionBalanceOptions
		harness   *transactionBalancingOracleHarness
		wantIs    []error
		wantNotIs []error
	}
	fixtures := make([]fixture, 0, 7)

	initialFeeError := errors.New("initial fee failed")
	harness := newTransactionBalancingOracleHarness()
	options := harness.options(nil, []TransactionOutput{NewPayPubKeyHashOutput(
		1_000_000, bytes.Repeat([]byte{0x70}, 20),
	)}, policy, false)
	options.FeePolicy = TransactionFeePolicyFuncs{
		BaseFeeFunc: func(*Transaction) (int64, error) { return 0, initialFeeError },
	}
	fixtures = append(fixtures, fixture{
		name: "initial fee failure is not released", options: options, harness: harness,
		wantIs: []error{ErrTransactionBalancing, initialFeeError},
	})

	harness = newTransactionBalancingOracleHarness()
	fixtures = append(fixtures, fixture{
		name: "insufficient immediately", harness: harness,
		options: harness.options(nil, []TransactionOutput{NewPayPubKeyHashOutput(
			1_000_000, bytes.Repeat([]byte{0x71}, 20),
		)}, policy, false),
		wantIs: []error{ErrTransactionBalancing, ErrTransactionInsufficientFunds},
	})

	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{
		{spendables: []TransactionSpendable{
			transactionBalancingOracleSpendable(t, 8_400, 0x73),
		}},
		{},
	}
	fixtures = append(fixtures, fixture{
		name: "insufficient after partial selection", harness: harness,
		options: harness.options([]TransactionInput{transactionBalancingOracleInput(
			t, 11_200, bytes.Repeat([]byte{0x72}, 20),
		)}, nil, policy, false),
		wantIs: []error{ErrTransactionBalancing, ErrTransactionInsufficientFunds},
	})

	selectorError := errors.New("selection failed")
	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{{err: selectorError}}
	fixtures = append(fixtures, fixture{
		name: "selector failure", harness: harness,
		options: harness.options(nil, []TransactionOutput{NewPayPubKeyHashOutput(
			1_000_000, bytes.Repeat([]byte{0x74}, 20),
		)}, policy, false),
		wantIs: []error{ErrTransactionBalancing, selectorError},
	})

	changeError := errors.New("change address failed")
	harness = newTransactionBalancingOracleHarness()
	harness.changeError = changeError
	fixtures = append(fixtures, fixture{
		name: "change address failure", harness: harness,
		options: harness.options(
			[]TransactionInput{transactionBalancingOracleInput(
				t, 1_600_000, bytes.Repeat([]byte{0x75}, 20),
			)},
			[]TransactionOutput{NewPayPubKeyHashOutput(
				1_500_000, bytes.Repeat([]byte{0x76}, 20),
			)}, policy, false,
		),
		wantIs: []error{ErrTransactionBalancing, changeError},
	})

	harness = newTransactionBalancingOracleHarness()
	missingKey := transactionSigningOraclePrivateKey(t, 9).Identifier()
	fixtures = append(fixtures, fixture{
		name: "signing key failure", harness: harness,
		options: harness.options(
			[]TransactionInput{transactionBalancingOracleInput(
				t, 1_600_000, missingKey[:],
			)},
			[]TransactionOutput{NewPayPubKeyHashOutput(
				1_500_000, bytes.Repeat([]byte{0x77}, 20),
			)}, policy, true,
		),
		wantIs: []error{
			ErrTransactionBalancing, ErrTransactionSigning,
			ErrTransactionSigningKeyUnavailable,
		},
	})

	originalError := errors.New("selection failed before release")
	releaseError := errors.New("release failed")
	harness = newTransactionBalancingOracleHarness()
	harness.selectionBatches = []transactionBalancingOracleBatch{{err: originalError}}
	harness.releaseError = releaseError
	fixtures = append(fixtures, fixture{
		name: "release failure masks balancing failure", harness: harness,
		options: harness.options(nil, []TransactionOutput{NewPayPubKeyHashOutput(
			1_000_000, bytes.Repeat([]byte{0x78}, 20),
		)}, policy, false),
		wantIs:    []error{releaseError},
		wantNotIs: []error{ErrTransactionBalancing, originalError},
	})

	wantPythonErrors := []struct{ name, kind, message string }{
		{"initial fee failure is not released", "RuntimeError", "initial fee failed"},
		{"insufficient immediately", "InsufficientFundsError", "Not enough funds to cover this transaction."},
		{"insufficient after partial selection", "InsufficientFundsError", "Not enough funds to cover this transaction."},
		{"selector failure", "RuntimeError", "selection failed"},
		{"change address failure", "RuntimeError", "change address failed"},
		{"signing key failure", "AssertionError", "Cannot find private key for signing output."},
		{"release failure masks balancing failure", "RuntimeError", "release failed"},
	}
	if len(expected) != len(fixtures) || len(expected) != len(wantPythonErrors) {
		t.Fatalf("transaction balancing failure cases = %d, want %d", len(expected), len(fixtures))
	}
	for index, fixture := range fixtures {
		want := expected[index]
		wantPython := wantPythonErrors[index]
		if want.Name != fixture.name || want.Name != wantPython.name || want.OK ||
			want.Transaction != nil || want.ErrorType == nil || *want.ErrorType != wantPython.kind ||
			want.ErrorMessage == nil || *want.ErrorMessage != wantPython.message ||
			want.SignRequested != !fixture.options.SkipSigning {
			t.Fatalf("transaction balancing failure contract %d = %+v", index, want)
		}
		transaction, err := CreateBalancedTransaction(context.Background(), fixture.options)
		if transaction != nil || err == nil {
			t.Fatalf("%s transaction = %#v, error %v", fixture.name, transaction, err)
		}
		for _, target := range fixture.wantIs {
			if !errors.Is(err, target) {
				t.Fatalf("%s error = %v, want errors.Is(_, %v)", fixture.name, err, target)
			}
		}
		for _, target := range fixture.wantNotIs {
			if errors.Is(err, target) {
				t.Fatalf("%s error = %v, unexpectedly errors.Is(_, %v)", fixture.name, err, target)
			}
		}
		gotSideEffects := fixture.harness.sideEffects()
		if !reflect.DeepEqual(gotSideEffects, want.SideEffects) {
			t.Fatalf("%s side effects = %+v, Python %+v", fixture.name, gotSideEffects, want.SideEffects)
		}
	}
}

type transactionBalancingOracleBatch struct {
	spendables []TransactionSpendable
	err        error
}

type transactionBalancingOracleHarness struct {
	selectionBatches []transactionBalancingOracleBatch
	selectionCalls   []transactionBalancingOracleSelection
	changeCalls      map[string]int
	addressHashCalls []string
	keyLookups       []string
	signingKeys      map[string]*keys.PrivateKey
	releaseSnapshots []transactionBalancingOracleRelease
	changeError      error
	releaseError     error
}

func newTransactionBalancingOracleHarness() *transactionBalancingOracleHarness {
	return &transactionBalancingOracleHarness{
		selectionBatches: make([]transactionBalancingOracleBatch, 0),
		selectionCalls:   make([]transactionBalancingOracleSelection, 0),
		changeCalls:      map[string]int{"funding": 0},
		addressHashCalls: make([]string, 0),
		keyLookups:       make([]string, 0),
		signingKeys:      make(map[string]*keys.PrivateKey),
		releaseSnapshots: make([]transactionBalancingOracleRelease, 0),
	}
}

func (harness *transactionBalancingOracleHarness) options(
	inputs []TransactionInput, outputs []TransactionOutput,
	feePolicy TransactionFeePolicy, sign bool,
) TransactionBalanceOptions {
	return TransactionBalanceOptions{
		Inputs: inputs, Outputs: outputs, FeePolicy: feePolicy,
		SelectSpendables: func(
			_context context.Context, amount int64,
		) ([]TransactionSpendable, error) {
			harness.selectionCalls = append(
				harness.selectionCalls,
				transactionBalancingOracleSelection{
					Amount: amount, Accounts: []string{"funding"},
				},
			)
			if len(harness.selectionBatches) == 0 {
				return nil, nil
			}
			batch := harness.selectionBatches[0]
			harness.selectionBatches = harness.selectionBatches[1:]
			return batch.spendables, batch.err
		},
		ChangeAddress: func(context.Context) (string, error) {
			harness.changeCalls["funding"]++
			if harness.changeError != nil {
				return "", harness.changeError
			}
			return "change-address", nil
		},
		AddressHash: func(_ context.Context, address string) ([]byte, error) {
			harness.addressHashCalls = append(harness.addressHashCalls, address)
			return bytes.Repeat([]byte{0xa1}, 20), nil
		},
		Signer: func(ctx context.Context, transaction *Transaction) error {
			return transaction.Sign(ctx, func(
				_context context.Context, _index int,
				_input *TransactionInput, output *TransactionOutput,
			) (*keys.PrivateKey, error) {
				address := "hash160:" + hex.EncodeToString(output.Script.PubKeyHash)
				harness.keyLookups = append(harness.keyLookups, address)
				return harness.signingKeys[address], nil
			})
		},
		Release: func(_ context.Context, transaction *Transaction) error {
			harness.releaseSnapshots = append(
				harness.releaseSnapshots,
				snapshotTransactionBalancingOracleRelease(transaction),
			)
			return harness.releaseError
		},
		SkipSigning: !sign,
	}
}

func (harness *transactionBalancingOracleHarness) sideEffects() transactionBalancingOracleSideEffects {
	return transactionBalancingOracleSideEffects{
		SelectionCalls: append(
			make([]transactionBalancingOracleSelection, 0, len(harness.selectionCalls)),
			harness.selectionCalls...,
		),
		SelectionBatchesRemaining: len(harness.selectionBatches),
		ChangeCalls: map[string]int{
			"funding": harness.changeCalls["funding"],
		},
		AddressHashCalls: append(make([]string, 0, len(harness.addressHashCalls)), harness.addressHashCalls...),
		KeyLookups:       append(make([]string, 0, len(harness.keyLookups)), harness.keyLookups...),
		ReleaseCount:     len(harness.releaseSnapshots),
		ReleaseSnapshots: append(
			make([]transactionBalancingOracleRelease, 0, len(harness.releaseSnapshots)),
			harness.releaseSnapshots...,
		),
	}
}

func transactionBalancingOracleInput(
	t *testing.T, amount uint64, pubKeyHash []byte,
) TransactionInput {
	t.Helper()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(amount, pubKeyHash),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func transactionBalancingOracleSpendable(
	t *testing.T, amount uint64, pubKeyHashByte byte,
) TransactionSpendable {
	t.Helper()
	input := transactionBalancingOracleInput(
		t, amount, bytes.Repeat([]byte{pubKeyHashByte}, 20),
	)
	return TransactionSpendable{
		Input: input, EffectiveAmount: int64(amount) - int64(input.Size())*50,
	}
}

func summarizeTransactionBalancingOracleTransaction(
	transaction *Transaction,
) *transactionBalancingOracleTransaction {
	result := &transactionBalancingOracleTransaction{
		Version: transaction.Version, LockTime: transaction.LockTime,
		RawHex: hex.EncodeToString(transaction.Raw), ID: transaction.ID,
		Size: transaction.Size(), BaseSize: transaction.BaseSize(),
		InputSum: transaction.InputSum(), OutputSum: transaction.OutputSum(),
		Fee:                     transaction.Fee(),
		InputAmounts:            make([]uint64, len(transaction.Inputs)),
		InputEffectiveAmounts:   make([]int64, len(transaction.Inputs)),
		InputSizes:              make([]int, len(transaction.Inputs)),
		InputSequences:          make([]uint32, len(transaction.Inputs)),
		InputPreviousIDs:        make([]string, len(transaction.Inputs)),
		InputScriptsHex:         make([]string, len(transaction.Inputs)),
		InputSignaturesHex:      make([]string, len(transaction.Inputs)),
		InputPublicKeysHex:      make([]string, len(transaction.Inputs)),
		OutputAmounts:           make([]uint64, len(transaction.Outputs)),
		OutputSizes:             make([]int, len(transaction.Outputs)),
		OutputScriptsHex:        make([]string, len(transaction.Outputs)),
		OutputInternalTransfers: make([]*bool, len(transaction.Outputs)),
	}
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		resolvedOutput := currentTransactionOutput(input.ResolvedOutput)
		result.InputAmounts[index] = resolvedOutput.Amount
		result.InputEffectiveAmounts[index] = int64(resolvedOutput.Amount) - int64(input.Size())*50
		result.InputSizes[index] = input.Size()
		result.InputSequences[index] = input.Sequence
		result.InputPreviousIDs[index] = input.PreviousOutputID()
		result.InputScriptsHex[index] = hex.EncodeToString(input.Script.Source)
		result.InputSignaturesHex[index] = hex.EncodeToString(input.Script.Signature)
		result.InputPublicKeysHex[index] = hex.EncodeToString(input.Script.PublicKey)
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		result.OutputAmounts[index] = output.Amount
		result.OutputSizes[index] = output.Size()
		result.OutputScriptsHex[index] = hex.EncodeToString(output.Script.Source)
	}
	return result
}

func snapshotTransactionBalancingOracleRelease(
	transaction *Transaction,
) transactionBalancingOracleRelease {
	result := transactionBalancingOracleRelease{
		InputAmounts:     make([]*uint64, len(transaction.Inputs)),
		OutputAmounts:    make([]uint64, len(transaction.Outputs)),
		InputScriptsHex:  make([]*string, len(transaction.Inputs)),
		OutputScriptsHex: make([]string, len(transaction.Outputs)),
	}
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput != nil {
			amount := currentTransactionOutput(input.ResolvedOutput).Amount
			result.InputAmounts[index] = &amount
		}
		if input.Script.Template != "" || input.Script.Source != nil || input.Script.Err != nil {
			script := hex.EncodeToString(input.Script.Source)
			result.InputScriptsHex[index] = &script
		}
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		result.OutputAmounts[index] = output.Amount
		result.OutputScriptsHex[index] = hex.EncodeToString(output.Script.Source)
	}
	return result
}

func runTransactionBalancingOracle(t *testing.T) transactionBalancingOracleResponse {
	t.Helper()
	sdkRoot, script := transactionOraclePaths(t)
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction balancing oracle failed: %v\n%s", err, output)
	}
	var oracle transactionBalancingOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction balancing oracle: %v\n%s", err, output)
	}
	return oracle
}
