package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"lbry/daemon/wallet/keys"
)

var ErrChannelSigningKeyUnavailable = errors.New("channel signing key is unavailable")

// SignChannelData mirrors Output.sign_data: salt, internal claim hash, then
// caller data are SHA-256 hashed and signed as compact r||s.
func SignChannelData(channel *TransactionOutput, data []byte, salt string) (string, error) {
	if channel == nil || channel.PrivateKey == nil {
		return "", ErrChannelSigningKeyUnavailable
	}
	claimID, err := channel.ClaimID()
	if err != nil {
		return "", err
	}
	claimHash, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return "", err
	}
	preimage := make([]byte, 0, len(salt)+len(claimHash)+len(data))
	preimage = append(preimage, salt...)
	preimage = append(preimage, claimHash...)
	preimage = append(preimage, data...)
	digest := sha256.Sum256(preimage)
	signature, err := channel.PrivateKey.SignCompact(digest[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(signature), nil
}

func ChannelHoldingPublicKey(
	ledger *Ledger, wallet *Wallet, channel *TransactionOutput,
) (*keys.PublicKey, error) {
	if ledger == nil || wallet == nil || channel == nil {
		return nil, ErrTransactionKeyLookupUnavailable
	}
	address, err := channel.Address(ledger.Network)
	if err != nil {
		return nil, err
	}
	privateKey, err := ledger.GetPrivateKeyForAddress(nil, wallet, address)
	if err != nil || privateKey == nil {
		return nil, err
	}
	return privateKey.PublicKey(), nil
}
