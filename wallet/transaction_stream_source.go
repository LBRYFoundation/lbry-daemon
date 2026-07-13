package wallet

import "errors"

var ErrTransactionStreamSource = errors.New("transaction output has no stream source")

func TransactionOutputStreamSource(
	output *TransactionOutput,
) (string, *ClaimValue, error) {
	output = currentTransactionOutput(output)
	if output == nil || (!output.Script.IsClaimName() && !output.Script.IsUpdateClaim()) {
		return "", nil, ErrTransactionStreamSource
	}
	decoded, err := decodeTransactionWireClaimValue(output.Script.Claim)
	if err != nil {
		return "", nil, err
	}
	if decoded.value == nil || decoded.value.Type != "stream" {
		return "", decoded.value, ErrTransactionStreamSource
	}
	source, ok := decoded.value.Value["source"].(map[string]any)
	if !ok {
		return "", decoded.value, ErrTransactionStreamSource
	}
	sdHash, _ := source["sd_hash"].(string)
	if sdHash == "" {
		return "", decoded.value, ErrTransactionStreamSource
	}
	return sdHash, decoded.value, nil
}
