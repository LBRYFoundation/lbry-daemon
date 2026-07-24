package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
)

const ResolveBatchSize = 100

var ErrResolveResultCount = errors.New("resolve result count mismatch")

// ResolveRequest preserves the original public URL spelling sent to the Hub
// and used in result/error text.
type ResolveRequest struct {
	URL string
}

// ResolveResultCountError preserves Ledger.resolve's global assertion after
// every batch has already been queried, inflated, and annotated.
type ResolveResultCountError struct{}

func (*ResolveResultCountError) Error() string {
	return "Mismatch between urls requested for resolve and responses received."
}

func (*ResolveResultCountError) PythonErrorName() string { return "AssertionError" }
func (*ResolveResultCountError) Unwrap() error           { return ErrResolveResultCount }

// ResolveAndSnapshot mirrors Ledger.resolve while keeping shallow relation
// aliases protected until signature replacement and recursive encoding finish.
func (ledger *Ledger) ResolveAndSnapshot(
	ctx context.Context,
	requests []ResolveRequest,
	annotationOptions ResolvedTransactionOutputAnnotationOptions,
	wireOptions LegacyTransactionJSONOptions,
	beforeEncoding func([]*TransactionOutput) error,
) ([]any, error) {
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger is nil", ErrResolveUnavailable)
	}
	results := make([]HubInflatedResult, 0, len(requests))
	locked := false
	defer func() {
		if locked {
			ledger.hubOutputsInflateMu.Unlock()
		}
	}()

	for start := 0; start < len(requests); start += ResolveBatchSize {
		end := min(start+ResolveBatchSize, len(requests))
		urls := make([]string, end-start)
		for index := start; index < end; index++ {
			urls[index-start] = requests[index].URL
		}
		outputs, err := ledger.QueryResolveBatch(ctx, urls)
		if err != nil {
			return nil, err
		}
		transactions, err := ledger.prepareHubOutputs(ctx, outputs)
		if err != nil {
			return nil, err
		}
		if !locked {
			ledger.hubOutputsInflateMu.Lock()
			locked = true
		}
		page, err := ledger.inflatePreparedHubOutputs(outputs, transactions)
		if err != nil {
			return nil, err
		}
		primary := make([]*TransactionOutput, len(page.Items))
		for index, result := range page.Items {
			primary[index] = result.Output
		}
		enriched, err := ledger.EnrichResolvedTransactionOutputAnnotations(
			ctx, primary, annotationOptions,
		)
		if err != nil {
			return nil, err
		}
		for index := range page.Items {
			if page.Items[index].Output != nil {
				page.Items[index].Output = enriched[index]
			}
		}
		results = append(results, page.Items...)
	}

	if len(results) != len(requests) {
		return nil, &ResolveResultCountError{}
	}
	for index := range results {
		if results[index].Output == nil {
			continue
		}
		parsed, err := ParseLBRYURL(requests[index].URL)
		if err != nil {
			return nil, err
		}
		if !parsed.HasStreamInChannel {
			continue
		}
		valid := false
		if results[index].Output.Channel != nil {
			var err error
			valid, err = ledger.resolveChannelSignatureValid(results[index].Output)
			if err != nil {
				return nil, err
			}
		}
		if !valid {
			results[index] = HubInflatedResult{Error: &HubResolveError{
				Name: "INVALID", Text: requests[index].URL + " has invalid channel signature",
			}}
		}
	}

	if beforeEncoding != nil && len(requests) > 0 {
		resolvedOutputs := make([]*TransactionOutput, 0, len(results))
		for _, result := range results {
			if result.Output != nil {
				resolvedOutputs = append(resolvedOutputs, result.Output)
			}
		}
		if err := beforeEncoding(resolvedOutputs); err != nil {
			return nil, err
		}
	}

	encoded := make([]any, len(results))
	for index, result := range results {
		if result.IsMissing() {
			encoded[index] = map[string]any{"error": map[string]any{
				"name": "NOT_FOUND", "text": requests[index].URL + " did not resolve to a claim",
			}}
			continue
		}
		var err error
		encoded[index], err = ledger.snapshotHubInflatedResult(result, wireOptions)
		if err != nil {
			return nil, &HubOutputsSnapshotEncodingError{Err: err}
		}
	}
	return encoded, nil
}

func (ledger *Ledger) resolveChannelSignatureValid(output *TransactionOutput) (bool, error) {
	value, err := decodeTransactionWireClaimValue(output.Script.Claim)
	if err != nil {
		return false, err
	}
	channel, err := decodeTransactionWireClaimValue(output.Channel.Script.Claim)
	if err != nil {
		return false, err
	}
	valid, err := verifyTransactionWireClaimSignature(ledger, output, value, channel.value)
	if errors.Is(err, ErrUnsignedClaimValue) || errors.Is(err, keys.ErrInvalidSignatureLength) {
		return false, &ResolveSignatureError{
			Name: "ValueError", Message: "Signature must be 64 bytes long.", Err: err,
		}
	}
	if errors.Is(err, keys.ErrInvalidDigestLength) {
		return false, &ResolveSignatureError{
			Name: "ValueError", Message: "Digest must be 32 bytes long.", Err: err,
		}
	}
	if errors.Is(err, keys.ErrInvalidCompactSignature) {
		return false, &ResolveSignatureError{
			Name: "AssertionError", Message: "", Err: err,
		}
	}
	return valid, err
}

// ResolveSignatureError supplies Python's exception class at the public RPC
// boundary while retaining the underlying verification failure.
type ResolveSignatureError struct {
	Name    string
	Message string
	Err     error
}

func (err *ResolveSignatureError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *ResolveSignatureError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *ResolveSignatureError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}
