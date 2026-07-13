package wallet

import (
	"context"

	"lbry/daemon/wallet/ledgerdb"
)

type CollectionPageOptions struct {
	AccountID              *string
	WalletID               *string
	Page                   int
	PageSize               int
	Offset                 *int
	Resolve                bool
	ResolveClaimsEnabled   bool
	ResolveClaimsLimit     int
	ResolveClaimsError     error
	ResolveClaimsItemError error
}

// GetCollectionPage supplies the pinned wallet/account ledger split and keeps
// local and member resolution between the records and count queries.
func (manager *WalletManager) GetCollectionPage(
	ctx context.Context, options CollectionPageOptions,
) (TransactionOutputPage, error) {
	unspent := false
	return manager.GetTransactionOutputPage(ctx, TransactionOutputPageOptions{
		AccountID: options.AccountID,
		WalletID:  options.WalletID,
		Page:      options.Page,
		PageSize:  options.PageSize,
		Offset:    options.Offset,
		Resolve:   options.Resolve,
		BeforeCount: func(
			ctx context.Context, ledger *Ledger, collections []*TransactionOutput,
		) error {
			if options.ResolveClaimsError != nil {
				return options.ResolveClaimsError
			}
			if !options.ResolveClaimsEnabled {
				return nil
			}
			for _, collection := range collections {
				if options.ResolveClaimsItemError != nil {
					if _, err := transactionCollectionClaimIDs(collection, 0); err != nil {
						return err
					}
					return options.ResolveClaimsItemError
				}
				if err := ledger.ResolveCollectionClaims(
					ctx, collection, options.ResolveClaimsLimit,
				); err != nil {
					return err
				}
			}
			return nil
		},
		Query: ledgerdb.OutputQuery{
			Types:   []int64{TransactionOutputTypeCollection},
			IsSpent: &unspent,
		},
	})
}
