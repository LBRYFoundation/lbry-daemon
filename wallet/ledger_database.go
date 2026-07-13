package wallet

import (
	"context"
	"fmt"

	"lbry/daemon/wallet/keys"
)

// ChannelKeyUsage binds the database query to a ledger and context while
// retaining the callback shape used by DeterministicChannelKeyManager.
func (ledger *Ledger) ChannelKeyUsage(ctx context.Context) ChannelKeyUsage {
	if ctx == nil {
		ctx = context.Background()
	}
	return func(account *Account, publicKey *keys.PublicKey) (bool, error) {
		if ledger == nil || ledger.Database == nil || !ledger.Database.IsOpen() {
			return false, ErrChannelKeyUsageUnavailable
		}
		if account == nil || account.PublicKey == nil {
			return false, fmt.Errorf("%w: account public key is unavailable", ErrInvalidAccountData)
		}
		if publicKey == nil {
			return false, fmt.Errorf("%w: channel public key is nil", ErrInvalidAccountData)
		}
		return ledger.Database.IsChannelKeyUsed(
			ctx, account.PublicKey.Address(), publicKey.CompressedBytes(),
			DecodeChannelClaimPublicKey,
		)
	}
}

func (account *Account) ensureDeterministicChannelCachePrimed(ctx context.Context) error {
	if account == nil || account.DeterministicChannelKeys == nil {
		return nil
	}
	// Isolated wallets can exist before their ledger lifecycle starts. Python's
	// normal daemon path opens the DB first; once this Go DB is open the same
	// unlock-time priming becomes mandatory and its errors remain observable.
	if account.ledger == nil || account.ledger.Database == nil || !account.ledger.Database.IsOpen() {
		return nil
	}
	return account.DeterministicChannelKeys.EnsureCachePrimed(
		account.ledger.ChannelKeyUsage(ctx),
	)
}
