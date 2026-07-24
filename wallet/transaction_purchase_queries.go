package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionPurchaseQuery = errors.New("wallet purchase query is unavailable")
	ErrPurchaseAccountsRequired = errors.New("'accounts' argument required to find purchases")
)

// PurchaseListOptions is the typed subset of Ledger.get_purchases constraints
// supported by the wallet transaction database. Resolve remains a separate
// hub-result inflation boundary.
type PurchaseListOptions struct {
	Query    ledgerdb.TransactionQuery
	Accounts []*Account
	Wallet   *Wallet
	Resolve  bool
}

// GetPurchases mirrors Database.get_purchases: only transactions whose inputs
// belong to the selected accounts qualify, and output zero is returned.
func (ledger *Ledger) GetPurchases(
	ctx context.Context, options PurchaseListOptions,
) ([]*TransactionOutput, error) {
	accountIDs, err := transactionPurchaseAccountIDs(options.Accounts)
	if err != nil {
		return nil, err
	}
	var channelKeyAccounts []*Account
	if options.Wallet != nil {
		channelKeyAccounts = append([]*Account(nil), options.Wallet.Accounts...)
	}
	purchases, err := ledger.getPurchasesByAccountIDs(
		ctx, options.Query, accountIDs, channelKeyAccounts,
	)
	if err != nil || !options.Resolve {
		return purchases, err
	}
	if err := ledger.ResolvePurchaseOutputs(ctx, purchases); err != nil {
		return nil, err
	}
	return purchases, nil
}

func (ledger *Ledger) getPurchasesByAccountIDs(
	ctx context.Context,
	query ledgerdb.TransactionQuery,
	accountIDs []string,
	channelKeyAccounts []*Account,
) ([]*TransactionOutput, error) {
	if len(accountIDs) == 0 {
		return nil, ErrPurchaseAccountsRequired
	}
	query.InputAccountIDs = append([]string(nil), accountIDs...)
	if query.PurchasedClaimID == nil && query.PurchasedClaimIDs == nil {
		query.RequirePurchasedClaimID = true
	}
	transactionOptions := TransactionListOptions{
		Query: query, ChannelKeyAccounts: append([]*Account(nil), channelKeyAccounts...),
	}
	transactions, err := ledger.GetTransactions(ctx, transactionOptions)
	if err != nil {
		return nil, err
	}
	purchases := make([]*TransactionOutput, len(transactions))
	for index, transaction := range transactions {
		if transaction == nil || len(transaction.Outputs) == 0 {
			return nil, fmt.Errorf(
				"%w: transaction %d has no outputs", ErrTransactionPurchaseQuery, index,
			)
		}
		purchases[index] = &transaction.Outputs[0]
	}
	return purchases, nil
}

// CountPurchases mirrors Database.get_purchase_count and deliberately ignores
// resolve because resolving changes presentation only.
func (ledger *Ledger) CountPurchases(
	ctx context.Context, query ledgerdb.TransactionQuery, accounts []*Account,
) (int64, error) {
	query, err := transactionPurchaseQuery(query, accounts)
	if err != nil {
		return 0, err
	}
	return ledger.CountTransactions(ctx, query)
}

func (account *Account) GetPurchases(
	ctx context.Context, query ledgerdb.TransactionQuery,
) ([]*TransactionOutput, error) {
	if account == nil || account.ledger == nil {
		return nil, fmt.Errorf("%w: account is unavailable", ErrTransactionPurchaseQuery)
	}
	return account.ledger.GetPurchases(ctx, PurchaseListOptions{
		Query: query, Accounts: []*Account{account}, Wallet: account.wallet,
	})
}

func (account *Account) CountPurchases(
	ctx context.Context, query ledgerdb.TransactionQuery,
) (int64, error) {
	if account == nil || account.ledger == nil {
		return 0, fmt.Errorf("%w: account is unavailable", ErrTransactionPurchaseQuery)
	}
	return account.ledger.CountPurchases(ctx, query, []*Account{account})
}

// TransactionPurchasedClaimID exposes the purchase relation used by daemon
// response enrichment while retaining the pinned decoder and error behavior.
func TransactionPurchasedClaimID(output *TransactionOutput) (string, error) {
	return transactionPurchasedClaimID(output)
}

func transactionPurchaseQuery(
	query ledgerdb.TransactionQuery, accounts []*Account,
) (ledgerdb.TransactionQuery, error) {
	accountIDs, err := transactionPurchaseAccountIDs(accounts)
	if err != nil {
		return ledgerdb.TransactionQuery{}, err
	}
	query.InputAccountIDs = accountIDs
	if query.PurchasedClaimID == nil && query.PurchasedClaimIDs == nil {
		query.RequirePurchasedClaimID = true
	}
	return query, nil
}

func transactionPurchaseAccountIDs(accounts []*Account) ([]string, error) {
	if len(accounts) == 0 {
		return nil, ErrPurchaseAccountsRequired
	}
	accountIDs := make([]string, len(accounts))
	for index, account := range accounts {
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
