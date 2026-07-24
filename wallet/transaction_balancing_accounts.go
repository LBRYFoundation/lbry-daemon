package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionFundingLedgerMismatch = errors.New(
		"All funding accounts used to create a transaction must be on the same ledger.",
	)
	ErrTransactionFundingWalletMismatch = errors.New(
		"All funding accounts used to create a transaction must be from the same wallet.",
	)
	ErrTransactionChangeLedgerMismatch = errors.New(
		"Change account must use same ledger as funding accounts.",
	)
	ErrTransactionChangeWalletMismatch = errors.New(
		"Change account must use same wallet as funding accounts.",
	)
	ErrTransactionNoLedger = errors.New("No ledger found.")
	ErrTransactionNoWallet = errors.New("No wallet found.")

	ErrInvalidTransactionLedgerFee       = errors.New("invalid transaction ledger fee")
	ErrTransactionReservationUnavailable = errors.New("transaction output reservation is unavailable")
	ErrTransactionChangeAccount          = errors.New("transaction change account is unavailable")
	ErrTransactionCoinSelectionStrategy  = errors.New("unknown transaction coin selection strategy")
)

// CreateTransaction is the account-backed counterpart to Python's
// Transaction.create. The sign argument corresponds to its sign=True keyword.
func CreateTransaction(
	ctx context.Context,
	inputs []TransactionInput,
	outputs []TransactionOutput,
	fundingAccounts []*Account,
	changeAccount *Account,
	sign bool,
) (*Transaction, error) {
	var ledger *Ledger
	var wallet *Wallet
	feePolicy := transactionLedgerFeePolicy{ledger: func() *Ledger { return ledger }}
	return CreateBalancedTransaction(ctx, TransactionBalanceOptions{
		Inputs: inputs, Outputs: outputs,
		Validate: func(*Transaction) error {
			var err error
			ledger, wallet, err = transactionCreateLedgerAndWallet(fundingAccounts, changeAccount)
			return err
		},
		FeePolicy: feePolicy,
		SelectSpendables: func(ctx context.Context, deficit int64) ([]TransactionSpendable, error) {
			return ledger.GetSpendableTransactionInputs(ctx, deficit, fundingAccounts)
		},
		ChangeAddress: func(ctx context.Context) (string, error) {
			if changeAccount == nil || changeAccount.Change == nil {
				return "", ErrTransactionChangeAccount
			}
			return changeAccount.Change.GetOrCreateUsableAddress(ctx)
		},
		AddressHash: func(_ context.Context, address string) ([]byte, error) {
			if changeAccount == nil || changeAccount.ledger == nil {
				return nil, ErrTransactionChangeAccount
			}
			return transactionChangeAddressHash(address)
		},
		Signer: func(ctx context.Context, transaction *Transaction) error {
			defer syncSuppliedTransactionValues(inputs, outputs, transaction)
			// wallet is intentionally captured by validation even though the
			// existing signing adapter derives the same wallet from the accounts.
			if wallet == nil {
				return ErrTransactionNoWallet
			}
			return transaction.SignWithAccounts(ctx, fundingAccounts, nil)
		},
		Release: func(ctx context.Context, transaction *Transaction) error {
			return ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
		},
		SkipSigning: !sign,
	})
}

func transactionChangeAddressHash(address string) ([]byte, error) {
	decoded, err := keys.DecodeBase58(address)
	if err != nil {
		return nil, err
	}
	if len(decoded) <= 1 {
		return []byte{}, nil
	}
	end := len(decoded)
	if end > 21 {
		end = 21
	}
	return append([]byte(nil), decoded[1:end]...), nil
}

func transactionCreateLedgerAndWallet(
	fundingAccounts []*Account, changeAccount *Account,
) (*Ledger, *Wallet, error) {
	var ledger *Ledger
	var wallet *Wallet
	for _, account := range fundingAccounts {
		if account == nil {
			return nil, nil, errors.New("funding account is nil")
		}
		// This intentionally re-establishes both baselines after a nil-ledger
		// account, matching the legacy `if ledger is None` edge behavior.
		if ledger == nil {
			ledger = account.ledger
			wallet = account.wallet
		}
		if ledger != account.ledger {
			return nil, nil, ErrTransactionFundingLedgerMismatch
		}
		if wallet != account.wallet {
			return nil, nil, ErrTransactionFundingWalletMismatch
		}
	}
	if changeAccount != nil {
		if changeAccount.ledger != ledger {
			return nil, nil, ErrTransactionChangeLedgerMismatch
		}
		if changeAccount.wallet != wallet {
			return nil, nil, ErrTransactionChangeWalletMismatch
		}
	}
	if ledger == nil {
		return nil, nil, ErrTransactionNoLedger
	}
	if wallet == nil {
		return nil, nil, ErrTransactionNoWallet
	}
	return ledger, wallet, nil
}

type transactionLedgerFeePolicy struct {
	ledger func() *Ledger
}

func (policy transactionLedgerFeePolicy) ledgerValue() (*Ledger, error) {
	if policy.ledger == nil || policy.ledger() == nil {
		return nil, ErrTransactionNoLedger
	}
	return policy.ledger(), nil
}

func (policy transactionLedgerFeePolicy) feePerByte() (int64, error) {
	ledger, err := policy.ledgerValue()
	if err != nil {
		return 0, err
	}
	return transactionLedgerFeeValue(ledger, ledger.FeePerByte, "fee_per_byte", 50)
}

func (policy transactionLedgerFeePolicy) feePerNameCharacter() (int64, error) {
	ledger, err := policy.ledgerValue()
	if err != nil {
		return 0, err
	}
	return transactionLedgerFeeValue(
		ledger, ledger.FeePerNameCharacter, "fee_per_name_char", 0,
	)
}

func (policy transactionLedgerFeePolicy) BaseFee(transaction *Transaction) (int64, error) {
	feePerByte, err := policy.feePerByte()
	if err != nil {
		return 0, err
	}
	return (LegacyTransactionFeePolicy{FeePerByte: feePerByte}).BaseFee(transaction)
}

func (policy transactionLedgerFeePolicy) InputFee(input *TransactionInput) (int64, error) {
	feePerByte, err := policy.feePerByte()
	if err != nil {
		return 0, err
	}
	return (LegacyTransactionFeePolicy{FeePerByte: feePerByte}).InputFee(input)
}

func (policy transactionLedgerFeePolicy) OutputFee(output *TransactionOutput) (int64, error) {
	feePerByte, err := policy.feePerByte()
	if err != nil {
		return 0, err
	}
	feePerNameCharacter := int64(0)
	if output != nil && output.Script.IsClaimName() {
		feePerNameCharacter, err = policy.feePerNameCharacter()
		if err != nil {
			return 0, err
		}
	}
	legacy := LegacyTransactionFeePolicy{
		FeePerByte: feePerByte, FeePerNameCharacter: feePerNameCharacter,
	}
	return legacy.OutputFee(output)
}

func transactionLedgerFeeValue(
	ledger *Ledger, captured any, configName string, fallback int64,
) (int64, error) {
	value := captured
	if value == nil && (ledger == nil || !ledger.transactionFeesSet) {
		value = any(fallback)
		if ledger != nil && ledger.Config != nil {
			if configured, exists := ledger.Config[configName]; exists {
				value = configured
			}
		}
	}
	integer, err := strictTransactionFeeInteger(value)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrInvalidTransactionLedgerFee, configName, err)
	}
	return integer, nil
}

func strictTransactionFeeInteger(value any) (int64, error) {
	if value == nil {
		return 0, errors.New("fee is None")
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		if integer, ok := new(big.Int).SetString(string(typed), 10); ok {
			if !integer.IsInt64() {
				return 0, errors.New("fee is outside int64")
			}
			return integer.Int64(), nil
		}
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, err
		}
		return strictTransactionFeeFloat(parsed)
	case *big.Int:
		if typed == nil {
			return 0, errors.New("fee integer is nil")
		}
		if !typed.IsInt64() {
			return 0, errors.New("fee is outside int64")
		}
		return typed.Int64(), nil
	case big.Int:
		if !typed.IsInt64() {
			return 0, errors.New("fee is outside int64")
		}
		return typed.Int64(), nil
	case float64:
		return strictTransactionFeeFloat(typed)
	case float32:
		return strictTransactionFeeFloat(float64(typed))
	case string:
		return 0, errors.New("fee has string type")
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if reflected.Uint() > math.MaxInt64 {
			return 0, errors.New("fee is outside int64")
		}
		return int64(reflected.Uint()), nil
	default:
		return 0, fmt.Errorf("fee has type %T", value)
	}
}

func strictTransactionFeeFloat(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
		value < math.MinInt64 || value > math.MaxInt64 {
		return 0, errors.New("fee is not an int64-valued number")
	}
	return int64(value), nil
}

func syncSuppliedTransactionValues(
	inputs []TransactionInput, outputs []TransactionOutput, transaction *Transaction,
) {
	if transaction == nil {
		return
	}
	inputCount := len(inputs)
	if inputCount > len(transaction.Inputs) {
		inputCount = len(transaction.Inputs)
	}
	copy(inputs[:inputCount], transaction.Inputs[:inputCount])
	outputCount := len(outputs)
	if outputCount > len(transaction.Outputs) {
		outputCount = len(transaction.Outputs)
	}
	copy(outputs[:outputCount], transaction.Outputs[:outputCount])
}

// GetSpendableTransactionInputs selects and reserves spendable outputs under
// the ledger-wide reservation lock used by the SDK.
func (ledger *Ledger) GetSpendableTransactionInputs(
	ctx context.Context, amount int64, fundingAccounts []*Account,
) ([]TransactionSpendable, error) {
	if ledger == nil || ledger.Database == nil {
		return nil, ErrTransactionReservationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	feePerByte, err := (transactionLedgerFeePolicy{
		ledger: func() *Ledger { return ledger },
	}).feePerByte()
	if err != nil {
		return nil, err
	}
	legacyPolicy := LegacyTransactionFeePolicy{FeePerByte: feePerByte}
	dummy := NewPayPubKeyHashOutput(uint64(TransactionCoin), make([]byte, 32))
	costOfChange, err := legacyPolicy.OutputFee(&dummy)
	if err != nil {
		return nil, err
	}

	if err := ledger.utxoReservationMu.Lock(ctx); err != nil {
		return nil, err
	}
	defer ledger.utxoReservationMu.Unlock()
	strategyValue := ledger.CoinSelectionStrategy
	if !pythonJSONTruthy(strategyValue) {
		strategyValue = nil
	}
	if strategy, ok := strategyValue.(string); ok && strategy == CoinSelectionStrategySQLite {
		return ledger.getSQLiteSpendableTransactionInputs(
			ctx, amount, costOfChange, legacyPolicy, fundingAccounts,
		)
	}

	estimators := make([]CoinSelectionEstimator, 0)
	for _, account := range fundingAccounts {
		if account == nil {
			return nil, errors.New("funding account is nil")
		}
		rows, err := ledger.Database.ListSpendableOutputs(ctx, []string{account.ID})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			estimator, err := transactionCoinSelectionEstimator(row, legacyPolicy)
			if err != nil {
				return nil, err
			}
			estimators = append(estimators, estimator)
		}
	}
	strategy := ""
	if strategyValue != nil {
		var ok bool
		strategy, ok = strategyValue.(string)
		if !ok {
			selector := NewCoinSelector(amount, costOfChange)
			if len(estimators) == 0 || selector.Target > coinSelectionAvailable(estimators) {
				return []TransactionSpendable{}, nil
			}
			return nil, fmt.Errorf("%w: %T", ErrTransactionCoinSelectionStrategy, strategyValue)
		}
	}
	selector := NewCoinSelector(amount, costOfChange)
	selected, err := selector.Select(estimators, strategy)
	if err != nil {
		return nil, err
	}
	spendables := make([]TransactionSpendable, len(selected))
	outputIDs := make([]string, len(selected))
	for index, estimator := range selected {
		spendables[index] = estimator.TransactionSpendable
		if estimator.Input.ResolvedOutput == nil {
			return nil, ErrUnattachedTransactionOutput
		}
		outputIDs[index] = currentTransactionOutput(estimator.Input.ResolvedOutput).ID()
	}
	if len(outputIDs) > 0 {
		if err := ledger.Database.ReserveOutputs(ctx, outputIDs, true); err != nil {
			return nil, err
		}
	}
	return spendables, nil
}

func transactionCoinSelectionEstimator(
	row ledgerdb.SpendableOutputRow, policy LegacyTransactionFeePolicy,
) (CoinSelectionEstimator, error) {
	output, err := transactionOutputFromStored(
		row.TXID, row.OutputPosition, row.Amount, row.Script,
	)
	if err != nil {
		return CoinSelectionEstimator{}, err
	}
	input, err := NewSpendInput(output)
	if err != nil {
		return CoinSelectionEstimator{}, err
	}
	fee, err := policy.InputFee(&input)
	if err != nil {
		return CoinSelectionEstimator{}, err
	}
	amount, err := transactionBalanceUint64(output.Amount)
	if err != nil {
		return CoinSelectionEstimator{}, err
	}
	effectiveAmount, err := subtractTransactionBalanceAmount(amount, fee)
	if err != nil {
		return CoinSelectionEstimator{}, err
	}
	return CoinSelectionEstimator{
		TransactionSpendable: TransactionSpendable{
			Input: input, EffectiveAmount: effectiveAmount,
		},
		Fee: fee, Height: row.Height,
	}, nil
}

func (ledger *Ledger) getSQLiteSpendableTransactionInputs(
	ctx context.Context,
	amount int64,
	costOfChange int64,
	policy LegacyTransactionFeePolicy,
	fundingAccounts []*Account,
) ([]TransactionSpendable, error) {
	accountIDs := make([]string, len(fundingAccounts))
	for index, account := range fundingAccounts {
		if account == nil {
			return nil, errors.New("funding account is nil")
		}
		accountIDs[index] = account.ID
	}
	reserveAmount, err := addTransactionBalanceAmount(amount, costOfChange)
	if err != nil {
		return nil, err
	}
	minimumAmount := int64(1)
	if amount/10 < minimumAmount {
		minimumAmount = amount / 10
	}
	parents := make(map[string]*Transaction)
	decodeParent := func(row ledgerdb.SpendableOutputRow) (*Transaction, error) {
		if parent := parents[row.TXID]; parent != nil {
			return parent, nil
		}
		parent, err := ParseTransaction(row.Raw)
		if err != nil {
			return nil, err
		}
		parent.Height = row.Height
		parent.IsVerified = row.IsVerified
		parents[row.TXID] = parent
		return parent, nil
	}
	rows, err := ledger.Database.GetAndReserveSpendableOutputs(
		ctx, accountIDs, reserveAmount, minimumAmount, policy.FeePerByte,
		true, false,
		func(row ledgerdb.SpendableOutputRow) (int64, error) {
			parent, err := decodeParent(row)
			if err != nil {
				return 0, err
			}
			if row.OutputPosition < 0 || row.OutputPosition > math.MaxUint32 ||
				uint64(row.OutputPosition) >= uint64(len(parent.Outputs)) {
				return 0, fmt.Errorf(
					"%w: %s:%d", ErrTransactionOutputOutOfRange,
					row.TXID, row.OutputPosition,
				)
			}
			input, err := NewSpendInput(&parent.Outputs[row.OutputPosition])
			if err != nil {
				return 0, err
			}
			fee, err := policy.InputFee(&input)
			if err != nil {
				return 0, err
			}
			return subtractTransactionBalanceAmount(row.Amount, fee)
		},
		func(row ledgerdb.SpendableOutputRow) error {
			_, err := decodeParent(row)
			return err
		},
	)
	if err != nil || len(rows) == 0 {
		return []TransactionSpendable{}, err
	}

	type parentGroup struct {
		parent    *Transaction
		positions []int64
	}
	type parentGroupKey struct {
		raw      string
		height   int64
		verified bool
	}
	groups := make([]parentGroup, 0)
	groupIndexes := make(map[parentGroupKey]int)
	for _, row := range rows {
		key := parentGroupKey{raw: string(row.Raw), height: row.Height, verified: row.IsVerified}
		groupIndex, exists := groupIndexes[key]
		if !exists {
			parent, err := decodeParent(row)
			if err != nil {
				return nil, err
			}
			groupIndex = len(groups)
			groupIndexes[key] = groupIndex
			groups = append(groups, parentGroup{parent: parent})
		}
		groups[groupIndex].positions = append(groups[groupIndex].positions, row.OutputPosition)
	}
	spendables := make([]TransactionSpendable, 0, len(rows))
	for _, group := range groups {
		for _, position := range group.positions {
			if position < 0 || position > math.MaxUint32 || uint64(position) >= uint64(len(group.parent.Outputs)) {
				return nil, fmt.Errorf("%w: output %d", ErrTransactionOutputOutOfRange, position)
			}
			input, err := NewSpendInput(&group.parent.Outputs[position])
			if err != nil {
				return nil, err
			}
			fee, err := policy.InputFee(&input)
			if err != nil {
				return nil, err
			}
			inputAmount, err := transactionBalanceUint64(group.parent.Outputs[position].Amount)
			if err != nil {
				return nil, err
			}
			effectiveAmount, err := subtractTransactionBalanceAmount(inputAmount, fee)
			if err != nil {
				return nil, err
			}
			spendables = append(spendables, TransactionSpendable{
				Input: input, EffectiveAmount: effectiveAmount,
			})
		}
	}
	return spendables, nil
}

// ReserveTransactionOutputs and ReleaseTransaction mirror the ledger helpers
// used by create and callers that abandon a successfully created transaction.
func (ledger *Ledger) ReserveTransactionOutputs(
	ctx context.Context, outputs []*TransactionOutput, isReserved bool,
) error {
	if ledger == nil || ledger.Database == nil {
		return ErrTransactionReservationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outputIDs := make([]string, len(outputs))
	for index, output := range outputs {
		if output == nil {
			return fmt.Errorf("%w: output %d is unresolved", ErrTransactionReservationUnavailable, index)
		}
		outputIDs[index] = currentTransactionOutput(output).ID()
	}
	return ledger.Database.ReserveOutputs(ctx, outputIDs, isReserved)
}

func (ledger *Ledger) ReleaseTransaction(ctx context.Context, transaction *Transaction) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is nil", ErrTransactionReservationUnavailable)
	}
	outputs := make([]*TransactionOutput, len(transaction.Inputs))
	for index := range transaction.Inputs {
		outputs[index] = transaction.Inputs[index].ResolvedOutput
	}
	return ledger.ReserveTransactionOutputs(ctx, outputs, false)
}
