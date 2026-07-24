package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/ledgerdb"
)

const TransactionQueryBatchSize = 900

var ErrTransactionQueryUnavailable = errors.New("wallet transaction query is unavailable")

type TransactionListOptions struct {
	Query                ledgerdb.TransactionQuery
	AnnotationAccountIDs []string
	ChannelKeyAccounts   []*Account
	IncludeIsSpent       bool
	IncludeIsMyInput     bool
	IncludeIsMyOutput    bool
}

// GetTransactions selects wallet-visible rows, parses their raw transactions,
// hydrates referenced inputs, and applies nullable wallet annotations.
func (ledger *Ledger) GetTransactions(
	ctx context.Context, options TransactionListOptions,
) ([]*Transaction, error) {
	if err := validateTransactionQuery(ledger); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := ledger.Database.ListTransactions(ctx, options.Query)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []*Transaction{}, nil
	}

	transactions := make([]*Transaction, 0, len(rows))
	storedTransactionIDs := make([]string, 0, len(rows))
	referencedOutputIDs := make([]string, 0)
	for _, row := range rows {
		transaction, err := ParseTransaction(row.Raw)
		if err != nil {
			return nil, fmt.Errorf("%w %s: %v", ErrInvalidStoredTransaction, row.TXID, err)
		}
		transaction.Height = row.Height
		transaction.Position = row.Position
		transaction.IsVerified = row.IsVerified
		transactions = append(transactions, transaction)
		storedTransactionIDs = append(storedTransactionIDs, row.TXID)
		for index := range transaction.Inputs {
			referencedOutputIDs = append(
				referencedOutputIDs, transaction.Inputs[index].PreviousOutputID(),
			)
		}
	}

	annotatedOutputs, err := ledger.loadTransactionQueryOutputs(
		ctx, storedTransactionIDs, true, options,
		newTransactionChannelHydrationState(ledger, ctx, options.ChannelKeyAccounts),
	)
	if err != nil {
		return nil, err
	}
	referenceOptions := TransactionListOptions{
		AnnotationAccountIDs: options.AnnotationAccountIDs,
		IncludeIsMyOutput:    options.IncludeIsMyOutput,
	}
	referencedOutputs, err := ledger.loadTransactionQueryOutputs(
		ctx, referencedOutputIDs, false, referenceOptions,
		newTransactionChannelHydrationState(ledger, ctx, options.ChannelKeyAccounts),
	)
	if err != nil {
		return nil, err
	}

	for _, transaction := range transactions {
		for index := range transaction.Inputs {
			input := &transaction.Inputs[index]
			if output := referencedOutputs[input.PreviousOutputID()]; output != nil {
				input.ResolvedOutput = output
			}
		}
		for index := range transaction.Outputs {
			output := &transaction.Outputs[index]
			if annotated := annotatedOutputs[output.ID()]; annotated != nil {
				copyTransactionOutputAnnotations(output, annotated)
			} else {
				markTransactionOutputNotMine(output)
			}
		}
		if len(transaction.Outputs) >= 2 {
			if _, ok := decodeTransactionPurchase(transaction.Outputs[1].Script); ok {
				transaction.Outputs[0].Purchase = &transaction.Outputs[1]
			}
		}
	}
	return transactions, nil
}

func (ledger *Ledger) CountTransactions(
	ctx context.Context, query ledgerdb.TransactionQuery,
) (int64, error) {
	if err := validateTransactionQuery(ledger); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.CountTransactions(ctx, query)
}

func (account *Account) GetTransactions(
	ctx context.Context, options TransactionListOptions,
) ([]*Transaction, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return nil, err
	}
	options.Query.AccountIDs = []string{accountID}
	options.AnnotationAccountIDs, err = transactionWalletAccountIDs(account.wallet)
	if err != nil {
		return nil, err
	}
	if account.wallet != nil {
		options.ChannelKeyAccounts = append([]*Account(nil), account.wallet.Accounts...)
	}
	return ledger.GetTransactions(ctx, options)
}

func (account *Account) CountTransactions(
	ctx context.Context, query ledgerdb.TransactionQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.CountTransactions(ctx, query)
}

func (ledger *Ledger) loadTransactionQueryOutputs(
	ctx context.Context,
	identifiers []string,
	byTransactionID bool,
	options TransactionListOptions,
	channelHydration *transactionChannelHydrationState,
) (map[string]*TransactionOutput, error) {
	outputs := make(map[string]*TransactionOutput)
	hydrationRows := make([]ledgerdb.OutputRow, 0)
	for offset := 0; offset < len(identifiers); offset += TransactionQueryBatchSize {
		end := min(offset+TransactionQueryBatchSize, len(identifiers))
		batch := append([]string(nil), identifiers[offset:end]...)
		query := ledgerdb.OutputQuery{
			AnnotationAccountIDs: options.AnnotationAccountIDs,
			IncludeIsSpent:       options.IncludeIsSpent,
			IncludeIsMyInput:     options.IncludeIsMyInput,
			IncludeIsMyOutput:    options.IncludeIsMyOutput,
		}
		if byTransactionID {
			query.TXIDs = batch
			query.Order = ledgerdb.OutputOrderTransactionID
		} else {
			query.TXOIDs = batch
			query.Order = ledgerdb.OutputOrderOutputID
		}
		rows, err := ledger.Database.ListOutputs(ctx, query)
		if err != nil {
			return nil, err
		}
		hydrationRows = append(hydrationRows, rows...)
		hydrated, err := hydrateTransactionQueryOutputRows(rows, options)
		if err != nil {
			return nil, err
		}
		for outputID, output := range hydrated {
			outputs[outputID] = output
		}
	}
	if channelHydration != nil {
		if err := channelHydration.HydrateRows(hydrationRows, outputs); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

func hydrateTransactionQueryOutputRows(
	rows []ledgerdb.OutputRow, options TransactionListOptions,
) (map[string]*TransactionOutput, error) {
	transactions := make(map[string]*Transaction)
	outputs := make(map[string]*TransactionOutput, len(rows))
	for _, row := range rows {
		transaction := transactions[row.TXID]
		if transaction == nil {
			var err error
			transaction, err = ParseTransaction(row.Raw)
			if err != nil {
				return nil, fmt.Errorf("%w %s: %v", ErrInvalidStoredTransaction, row.TXID, err)
			}
			transaction.Height = row.Height
			transaction.Position = row.TXPosition
			transaction.IsVerified = row.IsVerified
			transactions[row.TXID] = transaction
		}
		if row.OutputPosition < 0 || uint64(row.OutputPosition) >= uint64(len(transaction.Outputs)) {
			return nil, fmt.Errorf(
				"%w: %s:%d", ErrTransactionOutputOutOfRange, row.TXID, row.OutputPosition,
			)
		}
		output := &transaction.Outputs[row.OutputPosition]
		applyTransactionOutputRowAnnotations(
			output, row, options.IncludeIsMyInput, options.IncludeIsMyOutput,
		)
		outputs[output.ID()] = output
	}
	return outputs, nil
}

func applyTransactionOutputRowAnnotations(
	output *TransactionOutput,
	row ledgerdb.OutputRow,
	includeIsMyInput bool,
	includeIsMyOutput bool,
) {
	output.IsSpent = cloneTransactionQueryBool(row.IsSpent)
	output.IsMyOutput = cloneTransactionQueryBool(row.IsMyOutput)
	output.IsMyInput = cloneTransactionQueryBool(row.IsMyInput)
	output.ReceivedTips = cloneTransactionQueryInt64(row.ReceivedTips)
	if includeIsMyInput && includeIsMyOutput {
		internal := transactionQueryBool(row.IsMyInput) &&
			transactionQueryBool(row.IsMyOutput) && row.TXOType == TransactionOutputTypeOther
		output.IsInternalTransfer = transactionQueryBoolPointer(internal)
	}
}

func copyTransactionOutputAnnotations(output, annotated *TransactionOutput) {
	output.IsInternalTransfer = cloneTransactionQueryBool(annotated.IsInternalTransfer)
	output.IsSpent = cloneTransactionQueryBool(annotated.IsSpent)
	output.IsMyOutput = cloneTransactionQueryBool(annotated.IsMyOutput)
	output.IsMyInput = cloneTransactionQueryBool(annotated.IsMyInput)
	output.SentSupports = cloneTransactionQueryInt64(annotated.SentSupports)
	output.SentTips = cloneTransactionQueryInt64(annotated.SentTips)
	output.ReceivedTips = cloneTransactionQueryInt64(annotated.ReceivedTips)
	output.Channel = annotated.Channel
	output.PrivateKey = annotated.PrivateKey
}

func markTransactionOutputNotMine(output *TransactionOutput) {
	output.IsInternalTransfer = nil
	output.IsSpent = nil
	output.IsMyOutput = transactionQueryBoolPointer(false)
	output.IsMyInput = nil
	output.SentSupports = nil
	output.SentTips = nil
	output.ReceivedTips = nil
	output.Channel = nil
	output.PrivateKey = nil
}

func transactionWalletAccountIDs(wallet *Wallet) ([]string, error) {
	if wallet == nil {
		return nil, nil
	}
	accountIDs := make([]string, len(wallet.Accounts))
	for index, account := range wallet.Accounts {
		if account == nil {
			return nil, ErrNilWalletAccount
		}
		accountID := account.ID
		if account.PublicKey != nil {
			accountID = account.PublicKey.Address()
		}
		accountIDs[index] = accountID
	}
	return accountIDs, nil
}

func validateTransactionQuery(ledger *Ledger) error {
	if ledger == nil || ledger.Database == nil {
		return fmt.Errorf("%w: wallet ledger database is unavailable", ErrTransactionQueryUnavailable)
	}
	return nil
}

func transactionQueryBool(value *bool) bool {
	return value != nil && *value
}

func transactionQueryBoolPointer(value bool) *bool {
	return &value
}

func cloneTransactionQueryBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	return transactionQueryBoolPointer(*value)
}
