package wallet

import (
	"errors"
	"strings"
)

var ErrHubOutputsInflation = errors.New("hub outputs inflation failed")

type HubInflatedResultKind string

const (
	HubInflatedOutput  HubInflatedResultKind = "output"
	HubInflatedError   HubInflatedResultKind = "error"
	HubInflatedMissing HubInflatedResultKind = "missing"
)

// HubInflatedResult represents the three values returned by message_to_txo:
// a transaction output, an encoded resolve error, or Python None.
type HubInflatedResult struct {
	Output *TransactionOutput
	Error  *HubResolveError
}

func (result HubInflatedResult) Kind() HubInflatedResultKind {
	if result.Error != nil {
		return HubInflatedError
	}
	if result.Output != nil {
		return HubInflatedOutput
	}
	return HubInflatedMissing
}

func (result HubInflatedResult) IsMissing() bool {
	return result.Output == nil && result.Error == nil
}

// HubResolveError is the typed equivalent of the error dictionary returned by
// Outputs.message_to_txo. Censor is present for BLOCKED even when it resolves
// to a missing or another error result.
type HubResolveError struct {
	Name   string
	Text   string
	Censor *HubInflatedResult
}

type HubBlockedChannelSummary struct {
	Channel HubInflatedResult
	Blocked uint32
}

type HubBlockedSummary struct {
	Total    uint32
	Channels []HubBlockedChannelSummary
}

// HubOutputsInflateError preserves the built-in Python exception raised by
// Outputs.inflate after protobuf decoding has succeeded.
type HubOutputsInflateError struct {
	Name    string
	Message string
}

func (err *HubOutputsInflateError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *HubOutputsInflateError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *HubOutputsInflateError) Unwrap() error { return ErrHubOutputsInflation }

// Inflate mirrors schema.result.Outputs.inflate and inflate_blocked against a
// caller-supplied transaction set. It mutates the supplied transaction outputs
// in place, preserving the SDK's shared-cache aliases and partial side effects.
func (outputs *HubOutputs) Inflate(
	transactions []*Transaction,
) ([]HubInflatedResult, HubBlockedSummary, error) {
	if outputs == nil {
		return nil, HubBlockedSummary{}, hubOutputsInflateError(
			"AttributeError", "'NoneType' object has no attribute 'extra_txos'",
		)
	}
	transactionMap := make(map[string]*Transaction, len(transactions))
	for _, transaction := range transactions {
		if transaction == nil {
			return nil, HubBlockedSummary{}, hubOutputsInflateError(
				"AttributeError", "'NoneType' object has no attribute 'hash'",
			)
		}
		transactionMap[string(transaction.Hash[:])] = transaction
	}

	for _, message := range outputs.ExtraTXOs {
		if _, err := inflateHubOutput(message, transactionMap); err != nil {
			return nil, HubBlockedSummary{}, err
		}
	}

	inflated := make([]HubInflatedResult, len(outputs.TXOs))
	for index, message := range outputs.TXOs {
		result, err := inflateHubOutput(message, transactionMap)
		if err != nil {
			return nil, HubBlockedSummary{}, err
		}
		inflated[index] = result
	}

	blocked := HubBlockedSummary{
		Total:    outputs.BlockedTotal,
		Channels: make([]HubBlockedChannelSummary, len(outputs.Blocked)),
	}
	for index, entry := range outputs.Blocked {
		if entry == nil {
			entry = &HubBlocked{}
		}
		channel, err := inflateHubOutput(entry.Channel, transactionMap)
		if err != nil {
			return nil, HubBlockedSummary{}, err
		}
		blocked.Channels[index] = HubBlockedChannelSummary{
			Channel: channel,
			Blocked: entry.Count,
		}
	}
	return inflated, blocked, nil
}

func inflateHubOutput(
	message *HubOutput, transactions map[string]*Transaction,
) (HubInflatedResult, error) {
	if message == nil {
		message = &HubOutput{}
	}
	if message.Error != nil {
		name, err := message.Error.Code.Name()
		if err != nil {
			return HubInflatedResult{}, hubOutputsInflateError("ValueError", err.Error())
		}
		resolvedError := &HubResolveError{Name: name, Text: message.Error.Text}
		if name == "BLOCKED" {
			var censorMessage *HubOutput
			if message.Error.Blocked != nil {
				censorMessage = message.Error.Blocked.Channel
			}
			censor, err := inflateHubOutput(censorMessage, transactions)
			if err != nil {
				return HubInflatedResult{}, err
			}
			resolvedError.Censor = &censor
		}
		return HubInflatedResult{Error: resolvedError}, nil
	}

	transaction := transactions[string(message.TransactionHash)]
	if transaction == nil {
		return HubInflatedResult{}, nil
	}
	if uint64(message.Position) >= uint64(len(transaction.Outputs)) {
		return HubInflatedResult{}, hubOutputsInflateError(
			"IndexError", "list index out of range",
		)
	}
	output := &transaction.Outputs[message.Position]
	if message.Claim != nil {
		claim := message.Claim
		canonicalURL := claim.CanonicalURL
		if canonicalURL == "" {
			canonicalURL = claim.ShortURL
		}
		output.Meta = map[string]any{
			"short_url":         "lbry://" + claim.ShortURL,
			"canonical_url":     "lbry://" + canonicalURL,
			"reposted":          claim.Reposted,
			"is_controlling":    claim.IsControlling,
			"take_over_height":  claim.TakeOverHeight,
			"creation_height":   claim.CreationHeight,
			"activation_height": claim.ActivationHeight,
			"expiration_height": claim.ExpirationHeight,
			"effective_amount":  claim.EffectiveAmount,
			"support_amount":    claim.SupportAmount,
		}
		if claim.Channel != nil {
			channel, err := inflateHubRelation(claim.Channel, transactions)
			if err != nil {
				return HubInflatedResult{}, err
			}
			output.Channel = channel
		}
		if claim.Repost != nil {
			repost, err := inflateHubRelation(claim.Repost, transactions)
			if err != nil {
				return HubInflatedResult{}, err
			}
			output.RepostedClaim = repost
		}
		if hubInflatedOutputIsChannel(output) {
			output.Meta["claims_in_channel"] = claim.ClaimsInChannel
		}
	}
	return HubInflatedResult{Output: output}, nil
}

func inflateHubRelation(
	reference *HubOutput, transactions map[string]*Transaction,
) (*TransactionOutput, error) {
	if reference == nil {
		reference = &HubOutput{}
	}
	transaction, exists := transactions[string(reference.TransactionHash)]
	if !exists || transaction == nil {
		return nil, hubOutputsInflateError(
			"KeyError", pythonHubBytesRepr(reference.TransactionHash),
		)
	}
	if uint64(reference.Position) >= uint64(len(transaction.Outputs)) {
		return nil, hubOutputsInflateError("IndexError", "list index out of range")
	}
	return &transaction.Outputs[reference.Position], nil
}

func hubInflatedOutputIsChannel(output *TransactionOutput) bool {
	if output == nil || (!output.Script.IsClaimName() && !output.Script.IsUpdateClaim()) {
		return false
	}
	decoded, ok := decodeTransactionClaim(output.Script.Claim)
	return ok && decoded.TXOType == TransactionOutputTypeChannel
}

func hubOutputsInflateError(name, message string) error {
	return &HubOutputsInflateError{Name: name, Message: message}
}

func pythonHubBytesRepr(value []byte) string {
	quote := byte('\'')
	if strings.ContainsRune(string(value), '\'') &&
		!strings.ContainsRune(string(value), '"') {
		quote = '"'
	}
	var result strings.Builder
	result.Grow(3 + len(value)*4)
	result.WriteByte('b')
	result.WriteByte(quote)
	const hexadecimal = "0123456789abcdef"
	for _, character := range value {
		switch character {
		case '\\':
			result.WriteString("\\\\")
		case '\t':
			result.WriteString("\\t")
		case '\n':
			result.WriteString("\\n")
		case '\r':
			result.WriteString("\\r")
		default:
			switch {
			case character == quote:
				result.WriteByte('\\')
				result.WriteByte(character)
			case character >= 0x20 && character < 0x7f:
				result.WriteByte(character)
			default:
				result.WriteString("\\x")
				result.WriteByte(hexadecimal[character>>4])
				result.WriteByte(hexadecimal[character&0x0f])
			}
		}
	}
	result.WriteByte(quote)
	return result.String()
}
