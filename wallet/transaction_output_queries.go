package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/ledgerdb"
)

var ErrTransactionOutputQueryUnavailable = errors.New("transaction output query is unavailable")

type AccountBalanceOptions struct {
	Confirmations int64
	IncludeClaims bool
	Query         ledgerdb.OutputQuery
}

type TransactionOutputListOptions struct {
	Query         ledgerdb.OutputQuery
	Wallet        *Wallet
	Resolve       bool
	NoTransaction bool
}

// GetTransactionOutputs is the unqualified Database.get_txos path. Unlike
// GetUTXOs it preserves claim/support types and the caller's spent predicate.
func (ledger *Ledger) GetTransactionOutputs(
	ctx context.Context, options TransactionOutputListOptions,
) ([]*TransactionOutput, error) {
	query := options.Query
	query.NoTransaction = query.NoTransaction || options.NoTransaction
	var channelKeyAccounts []*Account
	if options.Wallet != nil {
		channelKeyAccounts = append([]*Account(nil), options.Wallet.Accounts...)
		if query.IncludeIsMyInput || query.IncludeIsMyOutput || query.IncludeReceivedTips {
			accountIDs, err := transactionWalletAccountIDs(options.Wallet)
			if err != nil {
				return nil, err
			}
			query.AnnotationAccountIDs = accountIDs
		}
	}
	outputs, err := ledger.getTransactionOutputs(ctx, query, channelKeyAccounts)
	if err != nil || !options.Resolve {
		return outputs, err
	}
	return ledger.ResolveLocalTransactionOutputs(ctx, outputs)
}

// GetUTXOs returns unspent ordinary and purchase outputs. The type and spent
// constraints overwrite caller values exactly as Ledger.get_utxos does.
func (ledger *Ledger) GetUTXOs(
	ctx context.Context, query ledgerdb.OutputQuery,
) ([]*TransactionOutput, error) {
	query = spendingUTXOQuery(query)
	return ledger.getTransactionOutputs(ctx, query, nil)
}

func (ledger *Ledger) GetUTXOCount(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.CountOutputs(ctx, spendingUTXOQuery(query))
}

func (ledger *Ledger) CountTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.CountOutputs(ctx, query)
}

func (ledger *Ledger) SumTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.SumOutputs(ctx, query)
}

func (ledger *Ledger) ReleaseAllOutputs(ctx context.Context) error {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.ReleaseAllOutputs(ctx, nil)
}

func (ledger *Ledger) getTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery, channelKeyAccounts []*Account,
) ([]*TransactionOutput, error) {
	if err := validateTransactionOutputQuery(ledger); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := ledger.Database.ListOutputs(ctx, query)
	if err != nil {
		return nil, err
	}
	transactions := make(map[string]*Transaction)
	outputs := make([]*TransactionOutput, 0, len(rows))
	outputsByID := make(map[string]*TransactionOutput, len(rows))
	for _, row := range rows {
		if query.NoTransaction {
			output, outputErr := transactionOutputFromStored(
				row.TXID, row.OutputPosition, row.Amount, row.Script,
			)
			if outputErr != nil {
				return nil, fmt.Errorf(
					"%w: output %s: %v", ErrInvalidStoredTransaction, row.TXOID, outputErr,
				)
			}
			height := row.Height
			output.transactionHeight = &height
			applyTransactionOutputRowAnnotations(
				output, row, query.IncludeIsMyInput, query.IncludeIsMyOutput,
			)
			outputs = append(outputs, output)
			outputsByID[output.ID()] = output
			continue
		}
		transaction := transactions[row.TXID]
		if transaction == nil {
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
			output, row, query.IncludeIsMyInput, query.IncludeIsMyOutput,
		)
		outputs = append(outputs, output)
		outputsByID[output.ID()] = output
	}
	if err := newTransactionChannelHydrationState(
		ledger, ctx, channelKeyAccounts,
	).HydrateRows(rows, outputsByID); err != nil {
		return nil, err
	}
	return outputs, nil
}

func (account *Account) GetUTXOs(
	ctx context.Context, query ledgerdb.OutputQuery,
) ([]*TransactionOutput, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return nil, err
	}
	query.AccountIDs = []string{accountID}
	if query.IncludeIsMyInput || query.IncludeIsMyOutput || query.IncludeReceivedTips {
		query.AnnotationAccountIDs, err = transactionWalletAccountIDs(account.wallet)
		if err != nil {
			return nil, err
		}
	}
	var channelKeyAccounts []*Account
	if account.wallet != nil {
		channelKeyAccounts = append([]*Account(nil), account.wallet.Accounts...)
	}
	return ledger.getTransactionOutputs(ctx, spendingUTXOQuery(query), channelKeyAccounts)
}

func (account *Account) GetTransactionOutputs(
	ctx context.Context, options TransactionOutputListOptions,
) ([]*TransactionOutput, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return nil, err
	}
	options.Query.AccountIDs = []string{accountID}
	options.Wallet = account.wallet
	return ledger.GetTransactionOutputs(ctx, options)
}

func (account *Account) CountTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.CountTransactionOutputs(ctx, query)
}

func (account *Account) SumTransactionOutputs(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.SumTransactionOutputs(ctx, query)
}

func (account *Account) GetUTXOCount(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.GetUTXOCount(ctx, query)
}

func (account *Account) GetBalance(
	ctx context.Context, options AccountBalanceOptions,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query := options.Query
	query.AccountIDs = []string{accountID}
	unspent := false
	query.IsSpent = &unspent
	if !options.IncludeClaims {
		query.Types = spendingUTXOTypes()
	}
	if options.Confirmations > 0 {
		if ledger.Headers == nil {
			return 0, fmt.Errorf("%w: ledger headers are unavailable", ErrTransactionOutputQueryUnavailable)
		}
		maximumHeight := int64(ledger.Headers.Height()) - (options.Confirmations - 1)
		zero := int64(0)
		query.HeightLTE = &maximumHeight
		query.HeightGT = &zero
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.SumOutputs(ctx, query)
}

func (account *Account) ReleaseAllOutputs(ctx context.Context) error {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ledger.Database.ReleaseAllOutputs(ctx, &accountID)
}

func spendingUTXOQuery(query ledgerdb.OutputQuery) ledgerdb.OutputQuery {
	unspent := false
	query.IsSpent = &unspent
	query.Types = spendingUTXOTypes()
	return query
}

func spendingUTXOTypes() []int64 {
	return []int64{TransactionOutputTypeOther, TransactionOutputTypePurchase}
}

func validateTransactionOutputQuery(ledger *Ledger) error {
	if ledger == nil || ledger.Database == nil {
		return fmt.Errorf(
			"%w: wallet ledger database is unavailable", ErrTransactionOutputQueryUnavailable,
		)
	}
	return nil
}

func accountOutputQueryContext(account *Account) (*Ledger, string, error) {
	if account == nil {
		return nil, "", fmt.Errorf("%w: account is nil", ErrTransactionOutputQueryUnavailable)
	}
	if err := validateTransactionOutputQuery(account.ledger); err != nil {
		return nil, "", err
	}
	accountID := account.ID
	if account.PublicKey != nil {
		accountID = account.PublicKey.Address()
	}
	return account.ledger, accountID, nil
}

func cloneTransactionQueryInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
