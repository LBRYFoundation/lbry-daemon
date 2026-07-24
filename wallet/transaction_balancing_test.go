package wallet

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestLegacyTransactionFeePolicyMatchesPinnedFormulas(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x11}, 20)
	input := transactionBalanceInput(t, 10_000, pubKeyHash)
	output := NewPayPubKeyHashOutput(5_000, pubKeyHash)
	transaction := NewTransaction().
		AddInputs([]TransactionInput{input}).
		AddOutputs([]TransactionOutput{output})
	policy := LegacyTransactionFeePolicy{FeePerByte: 50, FeePerNameCharacter: 200}

	if input.Size() != 148 || output.Size() != 34 || transaction.BaseSize() != 10 {
		t.Fatalf("sizes = input %d output %d base %d", input.Size(), output.Size(), transaction.BaseSize())
	}
	baseFee, err := policy.BaseFee(transaction)
	if err != nil || baseFee != 10*50 {
		t.Fatalf("base fee = %d, %v", baseFee, err)
	}
	inputFee, err := policy.InputFee(&transaction.Inputs[0])
	if err != nil || inputFee != 148*50 {
		t.Fatalf("input fee = %d, %v", inputFee, err)
	}
	outputFee, err := policy.OutputFee(&transaction.Outputs[0])
	if err != nil || outputFee != 34*50 {
		t.Fatalf("output fee = %d, %v", outputFee, err)
	}

	claim := NewClaimNameOutput(
		1, "claim-name", []byte{1}, pubKeyHash,
	)
	claimPolicy := LegacyTransactionFeePolicy{FeePerByte: 1, FeePerNameCharacter: 1_000}
	claimFee, err := claimPolicy.OutputFee(&claim)
	if err != nil || claimFee != int64(len("claim-name"))*1_000 {
		t.Fatalf("claim fee = %d, %v", claimFee, err)
	}
	negativePolicy := LegacyTransactionFeePolicy{FeePerByte: -1}
	if fee, err := negativePolicy.OutputFee(&output); err != nil || fee != 0 {
		t.Fatalf("negative ordinary output fee = %d, %v", fee, err)
	}
	dummy := NewPayPubKeyHashOutput(uint64(TransactionCoin), make([]byte, 32))
	if dummy.Size() != 46 {
		t.Fatalf("dummy change output size = %d, want 46", dummy.Size())
	}
}

func TestCreateBalancedTransactionValidatesAfterAttachmentAndBeforeFees(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x10}, 20)
	inputs := []TransactionInput{transactionBalanceInput(t, 20_000, pubKeyHash)}
	outputs := []TransactionOutput{NewPayPubKeyHashOutput(10_000, pubKeyHash)}
	wantErr := errors.New("account validation failed")
	feeCalls := 0
	releaseCalls := 0
	transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
		Inputs: inputs, Outputs: outputs,
		Validate: func(transaction *Transaction) error {
			if transaction == nil || len(transaction.Inputs) != 1 || len(transaction.Outputs) != 1 ||
				transaction.Inputs[0].owner != transaction || transaction.Outputs[0].owner != transaction {
				t.Fatalf("validation transaction = %#v", transaction)
			}
			return wantErr
		},
		FeePolicy: TransactionFeePolicyFuncs{
			BaseFeeFunc: func(*Transaction) (int64, error) { feeCalls++; return 0, nil },
		},
		Release: func(context.Context, *Transaction) error { releaseCalls++; return nil },
	})
	if transaction != nil || !errors.Is(err, wantErr) {
		t.Fatalf("validation result = %#v, %v", transaction, err)
	}
	if feeCalls != 0 || releaseCalls != 0 {
		t.Fatalf("validation side effects = fees %d release %d", feeCalls, releaseCalls)
	}
	if inputs[0].owner == nil || outputs[0].owner == nil {
		t.Fatal("caller slices were not attached before validation")
	}
}

func TestCreateBalancedTransactionAttachesSelectedInputsBeforeOverflowCleanup(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x12}, 20)
	selected := []TransactionSpendable{
		{Input: transactionBalanceInput(t, 1, pubKeyHash), EffectiveAmount: math.MaxInt64},
		{Input: transactionBalanceInput(t, 1, pubKeyHash), EffectiveAmount: 1},
	}
	releasedInputs := -1
	transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
		Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(1, pubKeyHash)},
		FeePolicy: LegacyTransactionFeePolicy{},
		SelectSpendables: func(context.Context, int64) ([]TransactionSpendable, error) {
			return selected, nil
		},
		Release: func(_ context.Context, transaction *Transaction) error {
			releasedInputs = len(transaction.Inputs)
			return nil
		},
		SkipSigning: true,
	})
	if transaction != nil || !errors.Is(err, ErrTransactionBalancing) || releasedInputs != 2 {
		t.Fatalf("overflow create = %#v, %v, released inputs %d", transaction, err, releasedInputs)
	}
	if selected[0].Input.owner == nil || selected[1].Input.owner == nil {
		t.Fatalf("selected inputs were not attached before overflow: %#v", selected)
	}
}

func TestCreateBalancedTransactionSelectsChangeAndSignsInPinnedOrder(t *testing.T) {
	ctx := context.Background()
	pubKeyHash := bytes.Repeat([]byte{0x21}, 20)
	selectedInput := transactionBalanceInput(t, 20_000, pubKeyHash)
	policy := LegacyTransactionFeePolicy{FeePerByte: 1}
	events := make([]string, 0, 4)
	releaseCalls := 0

	transaction, err := CreateBalancedTransaction(ctx, TransactionBalanceOptions{
		Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(10_000, pubKeyHash)},
		FeePolicy: policy,
		SelectSpendables: func(got context.Context, deficit int64) ([]TransactionSpendable, error) {
			events = append(events, "select")
			if got != ctx || deficit != 10_044 {
				t.Fatalf("selector = ctx %v deficit %d", got, deficit)
			}
			return []TransactionSpendable{{Input: selectedInput, EffectiveAmount: 19_852}}, nil
		},
		ChangeAddress: func(got context.Context) (string, error) {
			events = append(events, "address")
			if got != ctx {
				t.Fatalf("change context = %v", got)
			}
			return "change-address", nil
		},
		AddressHash: func(got context.Context, address string) ([]byte, error) {
			events = append(events, "hash")
			if got != ctx || address != "change-address" {
				t.Fatalf("hash resolver = %v, %q", got, address)
			}
			return bytes.Repeat([]byte{0x22}, 20), nil
		},
		Signer: func(got context.Context, transaction *Transaction) error {
			events = append(events, "sign")
			if got != ctx || len(transaction.Inputs) != 1 || len(transaction.Outputs) != 2 {
				t.Fatalf("sign state = %v, %#v", got, transaction)
			}
			return nil
		},
		Release: func(context.Context, *Transaction) error {
			releaseCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"select", "address", "hash", "sign"}) ||
		releaseCalls != 0 || len(transaction.Inputs) != 1 || len(transaction.Outputs) != 2 ||
		transaction.Outputs[1].Amount != 9_752 ||
		!bytes.Equal(transaction.Outputs[1].Script.PubKeyHash, bytes.Repeat([]byte{0x22}, 20)) {
		t.Fatalf("balanced transaction = events %v release %d tx %#v", events, releaseCalls, transaction)
	}
}

func TestCreateBalancedTransactionUsesStrictDustBoundary(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x31}, 20)
	policy := transactionFixedFeePolicy(0, 0, 0)
	for _, test := range []struct {
		name        string
		effective   int64
		wantOutputs int
	}{
		{"equal dust", 11_000, 1},
		{"above dust", 11_001, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			changeCalls := 0
			transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
				Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(10_000, pubKeyHash)},
				FeePolicy: policy,
				SelectSpendables: func(context.Context, int64) ([]TransactionSpendable, error) {
					return []TransactionSpendable{{
						Input:           transactionBalanceInput(t, uint64(test.effective), pubKeyHash),
						EffectiveAmount: test.effective,
					}}, nil
				},
				ChangeAddress: func(context.Context) (string, error) {
					changeCalls++
					return "change", nil
				},
				AddressHash: func(context.Context, string) ([]byte, error) {
					return pubKeyHash, nil
				},
				SkipSigning: true,
			})
			if err != nil || len(transaction.Outputs) != test.wantOutputs ||
				changeCalls != test.wantOutputs-1 {
				t.Fatalf("dust balance = %#v, calls %d, %v", transaction, changeCalls, err)
			}
		})
	}
}

func TestCreateBalancedTransactionRunsExactlyFiveEmptyOutputPasses(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x41}, 20)
	policy := transactionFixedFeePolicy(10, 0, 5)
	deficits := make([]int64, 0, TransactionBalancePasses)
	transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
		FeePolicy: policy,
		SelectSpendables: func(_ context.Context, deficit int64) ([]TransactionSpendable, error) {
			deficits = append(deficits, deficit)
			return []TransactionSpendable{{
				Input:           transactionBalanceInput(t, 100, pubKeyHash),
				EffectiveAmount: deficit,
			}}, nil
		},
		SkipSigning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deficits, []int64{10, 16, 16, 16, 16}) ||
		len(transaction.Inputs) != TransactionBalancePasses || len(transaction.Outputs) != 0 {
		t.Fatalf("five-pass result = deficits %v tx %#v", deficits, transaction)
	}
}

func TestCreateBalancedTransactionReleaseAndPartialMutationBoundaries(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x51}, 20)
	policy := transactionFixedFeePolicy(0, 0, 0)
	t.Run("insufficient funds", func(t *testing.T) {
		releases := 0
		var released *Transaction
		transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
			Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(10, pubKeyHash)},
			FeePolicy: policy,
			SelectSpendables: func(context.Context, int64) ([]TransactionSpendable, error) {
				return nil, nil
			},
			Release: func(_ context.Context, transaction *Transaction) error {
				releases++
				released = transaction
				return nil
			},
			SkipSigning: true,
		})
		if transaction != nil || !errors.Is(err, ErrTransactionBalancing) ||
			!errors.Is(err, ErrTransactionInsufficientFunds) || releases != 1 ||
			released == nil || len(released.Outputs) != 1 {
			t.Fatalf("insufficient result = %#v, %v, releases %d/%#v", transaction, err, releases, released)
		}
	})

	t.Run("initial fee failure is outside release", func(t *testing.T) {
		feeErr := errors.New("initial fee")
		releases := 0
		_, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
			Outputs: []TransactionOutput{NewPayPubKeyHashOutput(10, pubKeyHash)},
			FeePolicy: TransactionFeePolicyFuncs{
				BaseFeeFunc: func(*Transaction) (int64, error) { return 0, feeErr },
			},
			Release: func(context.Context, *Transaction) error {
				releases++
				return nil
			},
		})
		if !errors.Is(err, feeErr) || releases != 0 {
			t.Fatalf("initial fee error = %v, releases %d", err, releases)
		}
	})

	t.Run("change failure sees selected input", func(t *testing.T) {
		changeErr := errors.New("change failed")
		selected := transactionBalanceInput(t, 2_000, pubKeyHash)
		var released *Transaction
		_, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
			Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(100, pubKeyHash)},
			FeePolicy: policy,
			SelectSpendables: func(context.Context, int64) ([]TransactionSpendable, error) {
				return []TransactionSpendable{{Input: selected, EffectiveAmount: 2_000}}, nil
			},
			ChangeAddress: func(context.Context) (string, error) { return "", changeErr },
			AddressHash: func(context.Context, string) ([]byte, error) {
				return pubKeyHash, nil
			},
			Release: func(_ context.Context, transaction *Transaction) error {
				released = transaction
				return nil
			},
			SkipSigning: true,
		})
		if !errors.Is(err, changeErr) || released == nil || len(released.Inputs) != 1 ||
			len(released.Outputs) != 1 {
			t.Fatalf("change error = %v, released %#v", err, released)
		}
	})

	t.Run("sign failure and release masking", func(t *testing.T) {
		signErr := errors.New("sign failed")
		releaseErr := errors.New("release failed")
		input := transactionBalanceInput(t, 100, pubKeyHash)
		var released *Transaction
		_, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
			Inputs:    []TransactionInput{input},
			Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(100, pubKeyHash)},
			FeePolicy: policy,
			Signer: func(context.Context, *Transaction) error {
				return signErr
			},
			Release: func(_ context.Context, transaction *Transaction) error {
				released = transaction
				return releaseErr
			},
		})
		if !errors.Is(err, releaseErr) || errors.Is(err, signErr) ||
			errors.Is(err, ErrTransactionBalancing) || released == nil {
			t.Fatalf("masked release error = %v, released %#v", err, released)
		}
	})
}

func TestCreateBalancedTransactionPreservesNonemptyShortSelectionQuirk(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x61}, 20)
	selectorCalls := 0
	transaction, err := CreateBalancedTransaction(context.Background(), TransactionBalanceOptions{
		Outputs:   []TransactionOutput{NewPayPubKeyHashOutput(1_000, pubKeyHash)},
		FeePolicy: transactionFixedFeePolicy(0, 0, 0),
		SelectSpendables: func(context.Context, int64) ([]TransactionSpendable, error) {
			selectorCalls++
			return []TransactionSpendable{{
				Input:           transactionBalanceInput(t, 1, pubKeyHash),
				EffectiveAmount: 1,
			}}, nil
		},
		SkipSigning: true,
	})
	if err != nil || selectorCalls != 1 || transaction.InputSum() != 1 || transaction.OutputSum() != 1_000 {
		t.Fatalf("short selection quirk = %#v, calls %d, %v", transaction, selectorCalls, err)
	}
}

func transactionFixedFeePolicy(base, input, output int64) TransactionFeePolicyFuncs {
	return TransactionFeePolicyFuncs{
		BaseFeeFunc:   func(*Transaction) (int64, error) { return base, nil },
		InputFeeFunc:  func(*TransactionInput) (int64, error) { return input, nil },
		OutputFeeFunc: func(*TransactionOutput) (int64, error) { return output, nil },
	}
}

func transactionBalanceInput(t *testing.T, amount uint64, pubKeyHash []byte) TransactionInput {
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
