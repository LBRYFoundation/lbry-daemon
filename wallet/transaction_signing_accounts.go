package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionKeyLookupUnavailable = errors.New("transaction private-key lookup is unavailable")
	ErrTransactionAddressChain         = errors.New("transaction address has an unknown derivation chain")
	ErrTransactionSigningLedger        = errors.New("transaction signing ledger is unavailable or inconsistent")
	ErrTransactionSigningWallet        = errors.New("transaction signing wallet is unavailable or inconsistent")
)

// GetPrivateKeyForAddress mirrors Ledger.get_private_key_for_address: restrict
// the persisted address lookup to the supplied wallet's accounts, then derive
// the private child from the stored chain and index. A missing address or a
// watch-only single-address key returns nil without an error.
func (ledger *Ledger) GetPrivateKeyForAddress(
	ctx context.Context, wallet *Wallet, address string,
) (*keys.PrivateKey, error) {
	if ledger == nil || ledger.Database == nil {
		return nil, ErrTransactionKeyLookupUnavailable
	}
	if wallet == nil {
		return nil, fmt.Errorf("%w: wallet is nil", ErrTransactionKeyLookupUnavailable)
	}
	if !ledger.Database.IsOpen() {
		return nil, ledgerdb.ErrNotOpen
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit := 1
	for _, account := range wallet.Accounts {
		if account == nil {
			return nil, ErrNilWalletAccount
		}
		if account.PublicKey == nil {
			return nil, fmt.Errorf("%w: account public key is unavailable", ErrInvalidAccountData)
		}
		records, err := ledger.Database.GetAddresses(ctx, ledgerdb.AddressQuery{
			Account: account.PublicKey.Address(), Address: &address, Limit: &limit,
		})
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			continue
		}
		if account.Encrypted {
			return nil, ErrEncryptedAccountPrivateKey
		}
		record := records[0]
		var manager *AddressManager
		switch {
		case account.Receiving != nil && record.Chain == account.Receiving.ChainNumber:
			manager = account.Receiving
		case account.Change != nil && record.Chain == account.Change.ChainNumber:
			manager = account.Change
		default:
			return nil, fmt.Errorf("%w: %d", ErrTransactionAddressChain, record.Chain)
		}
		return manager.GetPrivateKey(record.N)
	}
	return nil, nil
}

// SignWithAccounts mirrors Transaction.sign's account-facing key selection.
// Extra keys are a slice because Python selects the first insertion-ordered
// dictionary value, while Go map iteration cannot preserve that contract.
func (transaction *Transaction) SignWithAccounts(
	ctx context.Context, fundingAccounts []*Account, extraKeys []*keys.PrivateKey,
) error {
	if transaction == nil {
		return fmt.Errorf("%w: nil transaction", ErrTransactionSigning)
	}
	// Python resets before validating account ownership, so failed validation
	// still invalidates the transaction's byte and ID caches.
	transaction.ResetDerived()
	ledger, wallet, err := transactionSigningLedgerAndWallet(fundingAccounts)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTransactionSigning, err)
	}
	return transaction.Sign(ctx, func(
		ctx context.Context, _ int, _ *TransactionInput, output *TransactionOutput,
	) (*keys.PrivateKey, error) {
		if output.Script.HasPubKeyHash {
			address, err := output.Address(ledger.Network)
			if err != nil {
				return nil, err
			}
			return ledger.GetPrivateKeyForAddress(ctx, wallet, address)
		}
		if len(extraKeys) == 0 {
			return nil, nil
		}
		return extraKeys[0], nil
	})
}

func transactionSigningLedgerAndWallet(accounts []*Account) (*Ledger, *Wallet, error) {
	var ledger *Ledger
	var wallet *Wallet
	for index, account := range accounts {
		if account == nil {
			return nil, nil, fmt.Errorf("%w: account %d is nil", ErrTransactionSigning, index)
		}
		if ledger == nil {
			ledger = account.ledger
			wallet = account.wallet
		}
		if ledger != account.ledger {
			return nil, nil, fmt.Errorf("%w: account %d", ErrTransactionSigningLedger, index)
		}
		if wallet != account.wallet {
			return nil, nil, fmt.Errorf("%w: account %d", ErrTransactionSigningWallet, index)
		}
	}
	if ledger == nil {
		return nil, nil, ErrTransactionSigningLedger
	}
	if wallet == nil {
		return nil, nil, ErrTransactionSigningWallet
	}
	return ledger, wallet, nil
}
