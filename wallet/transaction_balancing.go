package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
)

const (
	TransactionCoin          int64 = 100_000_000
	TransactionDust          int64 = 1_000
	TransactionBalancePasses       = 5
)

var (
	ErrTransactionBalancing               = errors.New("wallet transaction balancing failed")
	ErrTransactionInsufficientFunds       = errors.New("insufficient transaction funds")
	ErrTransactionBalanceCallbackRequired = errors.New("transaction balancing callback is required")
)

// TransactionFeePolicy supplies the three fee calculations used by the
// balancing algorithm without coupling it to a Ledger.
type TransactionFeePolicy interface {
	BaseFee(*Transaction) (int64, error)
	InputFee(*TransactionInput) (int64, error)
	OutputFee(*TransactionOutput) (int64, error)
}

// TransactionFeePolicyFuncs adapts callbacks into TransactionFeePolicy.
type TransactionFeePolicyFuncs struct {
	BaseFeeFunc   func(*Transaction) (int64, error)
	InputFeeFunc  func(*TransactionInput) (int64, error)
	OutputFeeFunc func(*TransactionOutput) (int64, error)
}

func (policy TransactionFeePolicyFuncs) BaseFee(transaction *Transaction) (int64, error) {
	if policy.BaseFeeFunc == nil {
		return 0, fmt.Errorf(
			"%w: base fee", ErrTransactionBalanceCallbackRequired,
		)
	}
	return policy.BaseFeeFunc(transaction)
}

func (policy TransactionFeePolicyFuncs) InputFee(input *TransactionInput) (int64, error) {
	if policy.InputFeeFunc == nil {
		return 0, fmt.Errorf(
			"%w: input fee", ErrTransactionBalanceCallbackRequired,
		)
	}
	return policy.InputFeeFunc(input)
}

func (policy TransactionFeePolicyFuncs) OutputFee(output *TransactionOutput) (int64, error) {
	if policy.OutputFeeFunc == nil {
		return 0, fmt.Errorf(
			"%w: output fee", ErrTransactionBalanceCallbackRequired,
		)
	}
	return policy.OutputFeeFunc(output)
}

// LegacyTransactionFeePolicy implements the pinned byte and claim-name fee
// formulas used by Ledger.
type LegacyTransactionFeePolicy struct {
	FeePerByte          int64
	FeePerNameCharacter int64
}

func (policy LegacyTransactionFeePolicy) BaseFee(transaction *Transaction) (int64, error) {
	if transaction == nil {
		return 0, fmt.Errorf("%w: nil transaction", ErrTransactionBalancing)
	}
	return multiplyTransactionBalanceAmount(int64(transaction.BaseSize()), policy.FeePerByte)
}

func (policy LegacyTransactionFeePolicy) InputFee(input *TransactionInput) (int64, error) {
	if input == nil {
		return 0, fmt.Errorf("%w: nil input", ErrTransactionBalancing)
	}
	return multiplyTransactionBalanceAmount(int64(input.Size()), policy.FeePerByte)
}

func (policy LegacyTransactionFeePolicy) OutputFee(output *TransactionOutput) (int64, error) {
	if output == nil {
		return 0, fmt.Errorf("%w: nil output", ErrTransactionBalancing)
	}
	if output.Script.Err != nil {
		return 0, fmt.Errorf("%w: output script: %w", ErrTransactionBalancing, output.Script.Err)
	}
	byteFee, err := multiplyTransactionBalanceAmount(int64(output.Size()), policy.FeePerByte)
	if err != nil {
		return 0, err
	}
	nameFee := int64(0)
	if output.Script.IsClaimName() {
		nameFee, err = multiplyTransactionBalanceAmount(
			int64(len(output.Script.ClaimName)), policy.FeePerNameCharacter,
		)
		if err != nil {
			return 0, err
		}
	}
	if nameFee > byteFee {
		return nameFee, nil
	}
	return byteFee, nil
}

type TransactionSpendable struct {
	Input           TransactionInput
	EffectiveAmount int64
}

type TransactionSpendableSelector func(
	context.Context, int64,
) ([]TransactionSpendable, error)

type TransactionChangeAddressProvider func(context.Context) (string, error)

type TransactionAddressHashResolver func(context.Context, string) ([]byte, error)

type TransactionBalanceSigner func(context.Context, *Transaction) error

type TransactionBalanceReleaser func(context.Context, *Transaction) error

type TransactionBalanceValidator func(*Transaction) error

type TransactionBalanceOptions struct {
	Inputs           []TransactionInput
	Outputs          []TransactionOutput
	Validate         TransactionBalanceValidator
	FeePolicy        TransactionFeePolicy
	SelectSpendables TransactionSpendableSelector
	ChangeAddress    TransactionChangeAddressProvider
	AddressHash      TransactionAddressHashResolver
	Signer           TransactionBalanceSigner
	Release          TransactionBalanceReleaser
	SkipSigning      bool
}

// CreateBalancedTransaction reproduces Transaction.create's five-pass balance
// loop. Account validation belongs to the adapter that constructs the callbacks.
func CreateBalancedTransaction(
	ctx context.Context, options TransactionBalanceOptions,
) (*Transaction, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	transaction := NewTransaction().AddInputs(options.Inputs).AddOutputs(options.Outputs)
	if options.Validate != nil {
		if err := options.Validate(transaction); err != nil {
			return nil, err
		}
	}
	if options.FeePolicy == nil {
		return nil, fmt.Errorf(
			"%w: %w: fee policy", ErrTransactionBalancing,
			ErrTransactionBalanceCallbackRequired,
		)
	}

	// These calculations precede Python's try block. Their failures therefore do
	// not release the supplied or partially constructed transaction.
	cost, err := transactionInitialCost(transaction, options.FeePolicy)
	if err != nil {
		return nil, fmt.Errorf("%w: initial cost: %w", ErrTransactionBalancing, err)
	}
	payment, err := transactionInitialPayment(transaction, options.FeePolicy)
	if err != nil {
		return nil, fmt.Errorf("%w: initial payment: %w", ErrTransactionBalancing, err)
	}

	err = balanceAndSignTransaction(ctx, transaction, options, cost, payment)
	if err == nil {
		return transaction, nil
	}
	if options.Release == nil {
		return nil, err
	}
	if releaseErr := options.Release(ctx, transaction); releaseErr != nil {
		// The awaited release sits inside Python's except block. If release
		// itself fails, that exception replaces the original create failure.
		return nil, releaseErr
	}
	return nil, err
}

func balanceAndSignTransaction(
	ctx context.Context, transaction *Transaction, options TransactionBalanceOptions,
	cost, payment int64,
) error {
	for pass := 0; pass < TransactionBalancePasses; pass++ {
		if payment < cost {
			if options.SelectSpendables == nil {
				return fmt.Errorf(
					"%w: %w: spendable selector",
					ErrTransactionBalancing, ErrTransactionBalanceCallbackRequired,
				)
			}
			deficit, err := subtractTransactionBalanceAmount(cost, payment)
			if err != nil {
				return err
			}
			spendables, err := options.SelectSpendables(ctx, deficit)
			if err != nil {
				return fmt.Errorf("%w: select spendables: %w", ErrTransactionBalancing, err)
			}
			if len(spendables) == 0 {
				return fmt.Errorf(
					"%w: %w", ErrTransactionBalancing, ErrTransactionInsufficientFunds,
				)
			}
			selectedInputs := make([]TransactionInput, len(spendables))
			for index := range spendables {
				selectedInputs[index] = spendables[index].Input
			}
			for index := range spendables {
				payment, err = addTransactionBalanceAmount(payment, spendables[index].EffectiveAmount)
				if err != nil {
					attachTransactionSpendables(transaction, spendables, selectedInputs)
					return err
				}
			}
			attachTransactionSpendables(transaction, spendables, selectedInputs)
		}

		costOfChange, err := transactionChangeCost(transaction, options.FeePolicy)
		if err != nil {
			return fmt.Errorf("%w: change cost: %w", ErrTransactionBalancing, err)
		}
		if payment > cost {
			change, err := subtractTransactionBalanceAmount(payment, cost)
			if err != nil {
				return err
			}
			changeAmount, err := subtractTransactionBalanceAmount(change, costOfChange)
			if err != nil {
				return err
			}
			if changeAmount > TransactionDust {
				if options.ChangeAddress == nil {
					return fmt.Errorf(
						"%w: %w: change address",
						ErrTransactionBalancing, ErrTransactionBalanceCallbackRequired,
					)
				}
				if options.AddressHash == nil {
					return fmt.Errorf(
						"%w: %w: address hash",
						ErrTransactionBalancing, ErrTransactionBalanceCallbackRequired,
					)
				}
				address, err := options.ChangeAddress(ctx)
				if err != nil {
					return fmt.Errorf("%w: change address: %w", ErrTransactionBalancing, err)
				}
				pubKeyHash, err := options.AddressHash(ctx, address)
				if err != nil {
					return fmt.Errorf("%w: change address hash: %w", ErrTransactionBalancing, err)
				}
				transaction.AddOutputs([]TransactionOutput{
					NewPayPubKeyHashOutput(uint64(changeAmount), pubKeyHash),
				})
			}
		}

		if len(transaction.Outputs) > 0 {
			break
		}
		cost, err = addTransactionBalanceAmount(cost, costOfChange)
		if err != nil {
			return err
		}
		cost, err = addTransactionBalanceAmount(cost, 1)
		if err != nil {
			return err
		}
	}

	if !options.SkipSigning {
		if options.Signer == nil {
			return fmt.Errorf(
				"%w: %w: signer", ErrTransactionBalancing,
				ErrTransactionBalanceCallbackRequired,
			)
		}
		if err := options.Signer(ctx, transaction); err != nil {
			return fmt.Errorf("%w: sign transaction: %w", ErrTransactionBalancing, err)
		}
	}
	return nil
}

func attachTransactionSpendables(
	transaction *Transaction,
	spendables []TransactionSpendable,
	selectedInputs []TransactionInput,
) {
	transaction.AddInputs(selectedInputs)
	for index := range spendables {
		spendables[index].Input = selectedInputs[index]
	}
}

func transactionInitialCost(
	transaction *Transaction, policy TransactionFeePolicy,
) (int64, error) {
	cost, err := policy.BaseFee(transaction)
	if err != nil {
		return 0, err
	}
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		amount, err := transactionBalanceUint64(output.Amount)
		if err != nil {
			return 0, err
		}
		fee, err := policy.OutputFee(output)
		if err != nil {
			return 0, err
		}
		amount, err = addTransactionBalanceAmount(amount, fee)
		if err != nil {
			return 0, err
		}
		cost, err = addTransactionBalanceAmount(cost, amount)
		if err != nil {
			return 0, err
		}
	}
	return cost, nil
}

func transactionInitialPayment(
	transaction *Transaction, policy TransactionFeePolicy,
) (int64, error) {
	payment := int64(0)
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput == nil {
			return 0, fmt.Errorf("input %d: %w", index, ErrUnattachedTransactionOutput)
		}
		amount, err := transactionBalanceUint64(currentTransactionOutput(input.ResolvedOutput).Amount)
		if err != nil {
			return 0, err
		}
		fee, err := policy.InputFee(input)
		if err != nil {
			return 0, err
		}
		effectiveAmount, err := subtractTransactionBalanceAmount(amount, fee)
		if err != nil {
			return 0, err
		}
		payment, err = addTransactionBalanceAmount(payment, effectiveAmount)
		if err != nil {
			return 0, err
		}
	}
	return payment, nil
}

func transactionChangeCost(
	transaction *Transaction, policy TransactionFeePolicy,
) (int64, error) {
	baseFee, err := policy.BaseFee(transaction)
	if err != nil {
		return 0, err
	}
	dummyChange := NewPayPubKeyHashOutput(uint64(TransactionCoin), make([]byte, 32))
	outputFee, err := policy.OutputFee(&dummyChange)
	if err != nil {
		return 0, err
	}
	return addTransactionBalanceAmount(baseFee, outputFee)
}

func transactionBalanceUint64(value uint64) (int64, error) {
	converted := new(big.Int).SetUint64(value)
	if !converted.IsInt64() {
		return 0, fmt.Errorf(
			"%w: amount %d is outside int64", ErrTransactionBalancing, value,
		)
	}
	return converted.Int64(), nil
}

func addTransactionBalanceAmount(left, right int64) (int64, error) {
	result := new(big.Int).Add(big.NewInt(left), big.NewInt(right))
	if !result.IsInt64() {
		return 0, fmt.Errorf(
			"%w: amount addition overflows int64", ErrTransactionBalancing,
		)
	}
	return result.Int64(), nil
}

func subtractTransactionBalanceAmount(left, right int64) (int64, error) {
	result := new(big.Int).Sub(big.NewInt(left), big.NewInt(right))
	if !result.IsInt64() {
		return 0, fmt.Errorf(
			"%w: amount subtraction overflows int64", ErrTransactionBalancing,
		)
	}
	return result.Int64(), nil
}

func multiplyTransactionBalanceAmount(left, right int64) (int64, error) {
	result := new(big.Int).Mul(big.NewInt(left), big.NewInt(right))
	if !result.IsInt64() {
		return 0, fmt.Errorf(
			"%w: fee multiplication overflows int64", ErrTransactionBalancing,
		)
	}
	return result.Int64(), nil
}
