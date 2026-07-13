package wallet

import (
	"context"

	"lbry/daemon/wallet/ledgerdb"
)

var transactionClaimOutputTypes = []int64{
	TransactionOutputTypeStream,
	TransactionOutputTypeChannel,
	TransactionOutputTypeCollection,
	TransactionOutputTypeRepost,
}

// ClaimListOptions carries the wallet-specific channel-key context that the
// lower-level output query deliberately does not infer.
type ClaimListOptions struct {
	Query  ledgerdb.OutputQuery
	Wallet *Wallet
}

func (ledger *Ledger) GetClaims(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	query := transactionClaimOutputQuery(options.Query, nil)
	var channelKeyAccounts []*Account
	if options.Wallet != nil {
		channelKeyAccounts = options.Wallet.Accounts
	}
	return ledger.getTransactionOutputs(ctx, query, channelKeyAccounts)
}

func (ledger *Ledger) CountClaims(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	return ledger.CountTransactionOutputs(ctx, transactionClaimOutputQuery(query, nil))
}

func (ledger *Ledger) GetStreams(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(
		options.Query, []int64{TransactionOutputTypeStream},
	)
	return ledger.GetClaims(ctx, options)
}

func (ledger *Ledger) CountStreams(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	return ledger.CountClaims(ctx, transactionClaimOutputQuery(
		query, []int64{TransactionOutputTypeStream},
	))
}

func (ledger *Ledger) GetChannels(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(
		options.Query, []int64{TransactionOutputTypeChannel},
	)
	return ledger.GetClaims(ctx, options)
}

func (ledger *Ledger) CountChannels(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	return ledger.CountClaims(ctx, transactionClaimOutputQuery(
		query, []int64{TransactionOutputTypeChannel},
	))
}

func (ledger *Ledger) GetCollections(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(
		options.Query, []int64{TransactionOutputTypeCollection},
	)
	return ledger.GetClaims(ctx, options)
}

func (ledger *Ledger) CountCollections(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	return ledger.CountClaims(ctx, transactionClaimOutputQuery(
		query, []int64{TransactionOutputTypeCollection},
	))
}

func (ledger *Ledger) GetSupports(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(
		options.Query, []int64{TransactionOutputTypeSupport},
	)
	return ledger.GetClaims(ctx, options)
}

func (ledger *Ledger) CountSupports(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	return ledger.CountClaims(ctx, transactionClaimOutputQuery(
		query, []int64{TransactionOutputTypeSupport},
	))
}

func (account *Account) GetClaims(
	ctx context.Context, options ClaimListOptions,
) ([]*TransactionOutput, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return nil, err
	}
	options.Query.AccountIDs = []string{accountID}
	options.Wallet = account.wallet
	if options.Query.IncludeIsMyInput || options.Query.IncludeIsMyOutput {
		options.Query.AnnotationAccountIDs, err = transactionWalletAccountIDs(account.wallet)
		if err != nil {
			return nil, err
		}
	}
	return ledger.GetClaims(ctx, options)
}

func (account *Account) CountClaims(
	ctx context.Context, query ledgerdb.OutputQuery,
) (int64, error) {
	ledger, accountID, err := accountOutputQueryContext(account)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = []string{accountID}
	return ledger.CountClaims(ctx, query)
}

func (account *Account) GetStreams(ctx context.Context, options ClaimListOptions) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(options.Query, []int64{TransactionOutputTypeStream})
	return account.GetClaims(ctx, options)
}

func (account *Account) CountStreams(ctx context.Context, query ledgerdb.OutputQuery) (int64, error) {
	query = transactionClaimOutputQuery(query, []int64{TransactionOutputTypeStream})
	return account.CountClaims(ctx, query)
}

func (account *Account) GetChannels(ctx context.Context, options ClaimListOptions) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(options.Query, []int64{TransactionOutputTypeChannel})
	return account.GetClaims(ctx, options)
}

func (account *Account) CountChannels(ctx context.Context, query ledgerdb.OutputQuery) (int64, error) {
	query = transactionClaimOutputQuery(query, []int64{TransactionOutputTypeChannel})
	return account.CountClaims(ctx, query)
}

func (account *Account) GetCollections(ctx context.Context, options ClaimListOptions) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(options.Query, []int64{TransactionOutputTypeCollection})
	return account.GetClaims(ctx, options)
}

func (account *Account) CountCollections(ctx context.Context, query ledgerdb.OutputQuery) (int64, error) {
	query = transactionClaimOutputQuery(query, []int64{TransactionOutputTypeCollection})
	return account.CountClaims(ctx, query)
}

func (account *Account) GetSupports(ctx context.Context, options ClaimListOptions) ([]*TransactionOutput, error) {
	options.Query = transactionClaimOutputQuery(options.Query, []int64{TransactionOutputTypeSupport})
	return account.GetClaims(ctx, options)
}

func (account *Account) CountSupports(ctx context.Context, query ledgerdb.OutputQuery) (int64, error) {
	query = transactionClaimOutputQuery(query, []int64{TransactionOutputTypeSupport})
	return account.CountClaims(ctx, query)
}

func transactionClaimOutputQuery(
	query ledgerdb.OutputQuery, forcedTypes []int64,
) ledgerdb.OutputQuery {
	unspent := false
	query.IsSpent = &unspent
	if forcedTypes != nil {
		query.Types = append([]int64(nil), forcedTypes...)
	} else if query.Types == nil {
		query.Types = append([]int64(nil), transactionClaimOutputTypes...)
	}
	return query
}
