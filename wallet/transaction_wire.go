package wallet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"

	"lbry/daemon/wallet/keys"
)

var ErrTransactionWireUnavailable = errors.New("transaction wire encoding is unavailable")

var ErrTransactionWireRelationCycle = errors.New("transaction output relation cycle")

type TransactionWireRelationCycleError struct{}

func (*TransactionWireRelationCycleError) Error() string { return "maximum recursion depth exceeded" }

func (*TransactionWireRelationCycleError) PythonErrorName() string { return "RecursionError" }

func (*TransactionWireRelationCycleError) Unwrap() error { return ErrTransactionWireRelationCycle }

type LegacyTransactionJSONOptions struct {
	IncludeProtobuf bool
}

// LegacyTransactionJSON projects the basic transaction, input, output, header,
// and amount fields emitted by JSONResponseEncoder.encode_transaction.
func (ledger *Ledger) LegacyTransactionJSON(transaction *Transaction) (map[string]any, error) {
	return ledger.LegacyTransactionJSONWithOptions(transaction, LegacyTransactionJSONOptions{})
}

func (ledger *Ledger) LegacyTransactionJSONWithOptions(
	transaction *Transaction, options LegacyTransactionJSONOptions,
) (map[string]any, error) {
	_ = options
	if ledger == nil || ledger.Headers == nil {
		return nil, fmt.Errorf("%w: ledger headers are unavailable", ErrTransactionWireUnavailable)
	}
	if transaction == nil {
		return nil, fmt.Errorf("%w: transaction is nil", ErrTransactionWireUnavailable)
	}

	inputs := make([]any, len(transaction.Inputs))
	inputSum := new(big.Int)
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.ResolvedOutput == nil {
			inputs[index] = map[string]any{
				"txid": input.PreviousTxID,
				"nout": input.PreviousIndex,
			}
			continue
		}
		resolved := currentTransactionOutput(input.ResolvedOutput)
		inputSum.Add(inputSum, new(big.Int).SetUint64(resolved.Amount))
		encoded, err := ledger.legacyTransactionOutputJSON(resolved, options, false)
		if err != nil {
			return nil, err
		}
		inputs[index] = encoded
	}

	outputs := make([]any, len(transaction.Outputs))
	outputSum := new(big.Int)
	for index := range transaction.Outputs {
		output := currentTransactionOutput(&transaction.Outputs[index])
		outputSum.Add(outputSum, new(big.Int).SetUint64(output.Amount))
		encoded, err := ledger.legacyTransactionOutputJSON(output, options, true)
		if err != nil {
			return nil, err
		}
		outputs[index] = encoded
	}
	fee := new(big.Int).Sub(new(big.Int).Set(inputSum), outputSum)
	height := any(transaction.Height)
	if transaction.HeightMissing {
		height = nil
	}
	return map[string]any{
		"txid":         transaction.ID,
		"height":       height,
		"inputs":       inputs,
		"outputs":      outputs,
		"total_input":  transactionHistoryDewies(inputSum),
		"total_output": transactionHistoryDewies(outputSum),
		"total_fee":    transactionHistoryDewies(fee),
		"hex":          hex.EncodeToString(transaction.Raw),
	}, nil
}

func (ledger *Ledger) legacyTransactionOutputJSON(
	output *TransactionOutput, options LegacyTransactionJSONOptions, checkSignature bool,
) (map[string]any, error) {
	return ledger.legacyTransactionOutputJSONState(
		output, options, checkSignature, make(map[*TransactionOutput]struct{}),
	)
}

func (ledger *Ledger) legacyTransactionOutputJSONState(
	output *TransactionOutput,
	options LegacyTransactionJSONOptions,
	checkSignature bool,
	active map[*TransactionOutput]struct{},
) (map[string]any, error) {
	if output == nil {
		return nil, fmt.Errorf("%w: output is nil", ErrTransactionWireUnavailable)
	}
	output = currentTransactionOutput(output)
	if _, cyclic := active[output]; cyclic {
		return nil, &TransactionWireRelationCycleError{}
	}
	active[output] = struct{}{}
	defer delete(active, output)
	transactionID := output.TransactionID
	height := output.TransactionHeight()
	if output.owner != nil {
		if output.owner.HeightMissing {
			return nil, errors.New("'>' not supported between instances of 'NoneType' and 'int'")
		}
		if output.owner.ID == "" {
			if err := output.owner.RebuildDerived(); err != nil {
				return nil, err
			}
		}
		transactionID = output.owner.ID
	}
	var address any
	if output.Script.HasAddress() {
		encoded, err := output.Address(ledger.Network)
		if err != nil {
			return nil, err
		}
		address = encoded
	}
	var timestamp any
	if estimated, ok := ledger.Headers.EstimatedTimestamp(int(height), true); ok {
		timestamp = estimated
	}
	confirmations := height
	if height > 0 {
		confirmations = int64(ledger.Headers.Height()+1) - height
	}
	encoded := map[string]any{
		"txid":          transactionID,
		"nout":          output.Position,
		"height":        height,
		"amount":        transactionHistoryDewies(new(big.Int).SetUint64(output.Amount)),
		"address":       address,
		"confirmations": confirmations,
		"timestamp":     timestamp,
		"type":          legacyTransactionOutputType(output),
	}
	if output.IsSpent != nil {
		encoded["is_spent"] = *output.IsSpent
	}
	if output.IsMyOutput != nil {
		encoded["is_my_output"] = *output.IsMyOutput
	}
	if output.IsMyInput != nil {
		encoded["is_my_input"] = *output.IsMyInput
	}
	if output.SentSupports != nil {
		encoded["sent_supports"] = transactionHistoryDewies(big.NewInt(*output.SentSupports))
	}
	if output.SentTips != nil {
		encoded["sent_tips"] = transactionHistoryDewies(big.NewInt(*output.SentTips))
	}
	if output.ReceivedTips != nil {
		encoded["received_tips"] = transactionHistoryDewies(big.NewInt(*output.ReceivedTips))
	}
	if output.IsInternalTransfer != nil {
		encoded["is_internal_transfer"] = *output.IsInternalTransfer
	}
	if output.Purchase != nil {
		if claimID, ok := decodeTransactionPurchase(output.Purchase.Script); ok {
			encoded["claim_id"] = claimID
		}
		if output.PurchasedClaim != nil {
			claim, err := ledger.legacyTransactionOutputJSONState(
				currentTransactionOutput(output.PurchasedClaim), options, true, active,
			)
			if err != nil {
				return nil, err
			}
			encoded["claim"] = claim
		}
	}
	if output.Script.IsClaimName() {
		encoded["claim_op"] = "create"
	} else if output.Script.IsUpdateClaim() {
		encoded["claim_op"] = "update"
	}
	if output.Script.IsClaimInvolved() {
		if !utf8.Valid(output.Script.ClaimName) {
			return nil, errors.New("'utf-8' codec can't decode claim name")
		}
		name := string(output.Script.ClaimName)
		claimID, err := output.ClaimID()
		if err != nil {
			return nil, err
		}
		encoded["name"] = name
		encoded["normalized_name"] = normalizeClaimName(name)
		encoded["claim_id"] = claimID
		encoded["permanent_url"] = "lbry://" + name + "#" + claimID
		meta := ledger.legacyTransactionClaimMeta(output.Meta)
		meta, err = ledger.legacyTransactionClaimMetaRelations(meta, options, active)
		if err != nil {
			return nil, err
		}
		encoded["meta"] = meta
		if shortURL, exists := meta["short_url"]; exists {
			encoded["short_url"] = shortURL
			delete(meta, "short_url")
		}
		if canonicalURL, exists := meta["canonical_url"]; exists {
			encoded["canonical_url"] = canonicalURL
			delete(meta, "canonical_url")
		}
		if output.Claims != nil {
			claims := make([]any, len(output.Claims))
			for index, claim := range output.Claims {
				if claim == nil {
					claims[index] = nil
					continue
				}
				claims[index], err = ledger.legacyTransactionOutputJSONState(
					currentTransactionOutput(claim), options, true, active,
				)
				if err != nil {
					return nil, err
				}
			}
			encoded["claims"] = claims
		}
		if output.RepostedClaim != nil {
			repostedClaim, err := ledger.legacyTransactionOutputJSONState(
				currentTransactionOutput(output.RepostedClaim), options, true, active,
			)
			if err != nil {
				return nil, err
			}
			encoded["reposted_claim"] = repostedClaim
		}
	}
	if output.Script.IsClaimName() || output.Script.IsUpdateClaim() {
		decodedValue, err := decodeTransactionWireClaimValue(output.Script.Claim)
		if err != nil {
			if transactionWirePythonErrorName(err) == "DecodeError" {
				return encoded, nil
			}
			return nil, err
		}
		value := decodedValue.value
		encoded["value"] = value.Value
		encoded["value_type"] = value.Type
		if value.Type == "channel" {
			encoded["has_signing_key"] = output.PrivateKey != nil
			if publicKey, ok := value.Value["public_key"].(string); ok {
				publicKeyBytes, err := hex.DecodeString(publicKey)
				if err != nil {
					return nil, err
				}
				publicKeyID, err := keys.AddressFromPublicKeyBytes(ledger.Network, publicKeyBytes)
				if err != nil {
					return nil, err
				}
				value.Value["public_key_id"] = publicKeyID
			}
		}
		if options.IncludeProtobuf {
			encoded["protobuf"] = hex.EncodeToString(value.Canonical)
		}
		if output.PurchaseReceipt != nil {
			purchaseReceipt, err := ledger.legacyTransactionOutputJSONState(
				currentTransactionOutput(output.PurchaseReceipt), options, true, active,
			)
			if err != nil {
				return nil, err
			}
			encoded["purchase_receipt"] = purchaseReceipt
		}
		if checkSignature && value.IsSigned() {
			if output.Channel != nil {
				channelOutput := currentTransactionOutput(output.Channel)
				signingChannel, err := ledger.legacyTransactionOutputJSONState(
					channelOutput, options, true, active,
				)
				if err != nil {
					return nil, err
				}
				encoded["signing_channel"] = signingChannel
				decodedChannel, err := decodeTransactionWireClaimValue(channelOutput.Script.Claim)
				if err != nil {
					if transactionWirePythonErrorName(err) == "DecodeError" {
						return encoded, nil
					}
					return nil, err
				}
				valid, err := verifyTransactionWireClaimSignature(
					ledger, output, decodedValue, decodedChannel.value,
				)
				if err != nil {
					return nil, err
				}
				encoded["is_channel_signature_valid"] = valid
			} else {
				var channelID any
				if signingChannelID := value.SigningChannelID(); signingChannelID != nil {
					channelID = *signingChannelID
				}
				encoded["signing_channel"] = map[string]any{"channel_id": channelID}
				encoded["is_channel_signature_valid"] = false
			}
		}
	}
	if output.Script.IsSupportData() {
		value, err := DecodeSupportValue(output.Script.Support)
		if err != nil {
			if transactionWirePythonErrorName(err) == "DecodeError" {
				return encoded, nil
			}
			return nil, err
		}
		encoded["value"] = value.Value()
		if options.IncludeProtobuf {
			encoded["protobuf"] = hex.EncodeToString(value.Canonical)
		}
		if checkSignature && value.IsSigned() {
			if output.Channel != nil {
				channelOutput := currentTransactionOutput(output.Channel)
				signingChannel, err := ledger.legacyTransactionOutputJSONState(
					channelOutput, options, true, active,
				)
				if err != nil {
					return nil, err
				}
				encoded["signing_channel"] = signingChannel
				decodedChannel, err := decodeTransactionWireClaimValue(channelOutput.Script.Claim)
				if err != nil {
					if transactionWirePythonErrorName(err) == "DecodeError" {
						return encoded, nil
					}
					return nil, err
				}
				valid, err := VerifyTransactionSupportSignature(
					value, output.owner, decodedChannel.value,
				)
				if err != nil {
					return nil, err
				}
				encoded["is_channel_signature_valid"] = valid
			} else {
				var channelID any
				if signingChannelID := value.SigningChannelID(); signingChannelID != nil {
					channelID = *signingChannelID
				}
				encoded["signing_channel"] = map[string]any{"channel_id": channelID}
				encoded["is_channel_signature_valid"] = false
			}
		}
	}
	return encoded, nil
}

type transactionWirePythonError interface {
	PythonErrorName() string
}

func transactionWirePythonErrorName(err error) string {
	var pythonError transactionWirePythonError
	if errors.As(err, &pythonError) {
		return pythonError.PythonErrorName()
	}
	return ""
}

func legacyTransactionOutputType(output *TransactionOutput) string {
	switch {
	case output.Script.IsClaimName(), output.Script.IsUpdateClaim():
		return "claim"
	case output.Script.IsSupportClaim():
		return "support"
	case output.Script.Template == TransactionScriptReturnData:
		return "data"
	case output.Purchase != nil:
		return "purchase"
	default:
		return "payment"
	}
}
