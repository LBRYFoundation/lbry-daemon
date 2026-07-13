package wallet

import "lbry/daemon/wallet/keys"

type transactionWireClaimValue struct {
	value    *ClaimValue
	legacyV1 *LegacyV1ClaimMetadata
}

// decodeTransactionWireClaimValue follows Claim.from_bytes' format fallback
// order without widening DecodeClaimValue's intentionally v2-only contract.
func decodeTransactionWireClaimValue(payload []byte) (transactionWireClaimValue, error) {
	if len(payload) == 0 || payload[0] == 0 || payload[0] == 1 {
		value, err := DecodeClaimValue(payload)
		return transactionWireClaimValue{value: value}, err
	}
	if payload[0] == '{' {
		value, err := DecodeLegacyV0ClaimValue(payload)
		return transactionWireClaimValue{value: value}, err
	}
	value, metadata, err := DecodeLegacyV1ClaimValueWithMetadata(payload)
	return transactionWireClaimValue{value: value, legacyV1: metadata}, err
}

func verifyTransactionWireClaimSignature(
	ledger *Ledger,
	output *TransactionOutput,
	value transactionWireClaimValue,
	channel *ClaimValue,
) (bool, error) {
	if value.legacyV1 == nil {
		return VerifyTransactionClaimSignature(value.value, output.owner, channel)
	}
	address, err := output.Address(ledger.Network)
	if err != nil {
		return false, err
	}
	digest, err := LegacyV1ClaimSignatureDigest(address, value.value, value.legacyV1)
	if err != nil {
		return false, err
	}
	publicKey, err := ClaimChannelPublicKey(channel)
	if err != nil {
		return false, err
	}
	return keys.VerifyCompactSignature(publicKey, value.value.Signature, digest[:])
}
