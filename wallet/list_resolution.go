package wallet

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

var (
	ErrLocalTransactionResolve = errors.New("local transaction output resolve failed")
	ErrLocalSupportClaimSearch = errors.New("local support channel search failed")
)

// ResolveLocalTransactionOutputs mirrors Ledger._resolve_for_local_results.
// Claim replacements retain the local output annotations, and signed supports
// receive signing channels from the subsequent one-shot claim search.
func (ledger *Ledger) ResolveLocalTransactionOutputs(
	ctx context.Context, outputs []*TransactionOutput,
) ([]*TransactionOutput, error) {
	urls := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if output == nil {
			return nil, localResolutionAttributeError(
				"'NoneType' object has no attribute 'can_decode_claim'",
			)
		}
		if !transactionOutputCanDecodeClaim(output) {
			continue
		}
		url, err := transactionOutputPermanentURL(output)
		if err != nil {
			return nil, err
		}
		urls = append(urls, url)
	}

	resolved, err := ledger.resolveDetachedHubResults(ctx, urls)
	if err != nil {
		return nil, localResolutionStageError(ErrLocalTransactionResolve, err)
	}
	byURL := make(map[string]HubInflatedResult, len(urls))
	for index, url := range urls {
		byURL[url] = resolved[index]
	}

	result := make([]*TransactionOutput, len(outputs))
	for index, output := range outputs {
		result[index] = output
		if !transactionOutputCanDecodeClaim(output) {
			continue
		}
		url, err := transactionOutputPermanentURL(output)
		if err != nil {
			return nil, err
		}
		remote := byURL[url]
		if remote.Output != nil {
			copyLocalTransactionOutputAnnotations(remote.Output, output)
			result[index] = remote.Output
			continue
		}
		if remote.Error != nil {
			if output.Meta == nil {
				output.Meta = make(map[string]any)
			}
			output.Meta["error"] = hubResolveErrorValue(remote.Error)
		}
	}

	channelIDs := make([]string, 0)
	signedSupports := make([]*TransactionOutput, 0)
	seenChannels := make(map[string]struct{})
	for _, output := range result {
		if output == nil || !output.Script.IsSupportData() {
			continue
		}
		support, err := DecodeSupportValue(output.Script.Support)
		if err != nil {
			continue
		}
		channelID := support.SigningChannelID()
		if channelID == nil {
			continue
		}
		signedSupports = append(signedSupports, output)
		if _, exists := seenChannels[*channelID]; !exists {
			seenChannels[*channelID] = struct{}{}
			channelIDs = append(channelIDs, *channelID)
		}
	}
	if len(channelIDs) == 0 {
		return result, nil
	}

	channels, err := ledger.claimSearchDetachedHubResults(ctx, channelIDs)
	if err != nil {
		return nil, localResolutionStageError(ErrLocalSupportClaimSearch, err)
	}
	channelOutputs, err := transactionOutputsFromHubResults(channels)
	if err != nil {
		return nil, err
	}
	channelLookup := make(map[string]*TransactionOutput, len(channelOutputs))
	for _, channel := range channelOutputs {
		claimID, err := transactionResolvedOutputClaimID(channel)
		if err != nil {
			return nil, err
		}
		channelLookup[claimID] = channel
	}
	for _, support := range signedSupports {
		decoded, err := DecodeSupportValue(support.Script.Support)
		if err != nil {
			continue
		}
		channelID := decoded.SigningChannelID()
		if channelID != nil {
			support.Channel = channelLookup[*channelID]
		}
	}
	return result, nil
}

// ResolvePurchaseOutputs mirrors Ledger.get_purchases(resolve=True). Failures
// while awaiting or inflating claim_search are logged and swallowed by Python;
// this method represents that outcome by clearing every PurchasedClaim.
func (ledger *Ledger) ResolvePurchaseOutputs(
	ctx context.Context, purchases []*TransactionOutput,
) error {
	claimIDs := make([]string, len(purchases))
	for index, purchase := range purchases {
		claimID, err := transactionPurchasedClaimID(purchase)
		if err != nil {
			return err
		}
		claimIDs[index] = claimID
	}

	results, err := ledger.claimSearchDetachedHubResults(ctx, claimIDs)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		for _, purchase := range purchases {
			if purchase != nil {
				purchase.PurchasedClaim = nil
			}
		}
		return nil
	}
	resolved, err := transactionOutputsFromHubResults(results)
	if err != nil {
		return err
	}
	return hydrateTransactionPurchasedClaims(purchases, claimIDs, resolved)
}

// ResolveCollectionClaims resolves the first limit collection references in
// their original order. Await/inflate failures become a completed empty
// resolution, while malformed returned items still propagate.
func (ledger *Ledger) ResolveCollectionClaims(
	ctx context.Context, collection *TransactionOutput, limit int,
) error {
	claimIDs, err := transactionCollectionClaimIDs(collection, limit)
	if err != nil {
		return err
	}
	results, err := ledger.claimSearchDetachedHubResults(ctx, claimIDs)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return clearTransactionCollectionClaimsAfterResolveError(collection)
	}
	return hydrateTransactionCollectionHubResults(collection, claimIDs, results)
}

func hydrateTransactionCollectionHubResults(
	collection *TransactionOutput, claimIDs []string, results []HubInflatedResult,
) error {
	if collection == nil {
		return fmt.Errorf("%w: collection output is nil", ErrTransactionResolvedClaim)
	}
	claims := make([]*TransactionOutput, len(claimIDs))
	for index, claimID := range claimIDs {
		for _, result := range results {
			candidate, err := transactionOutputFromHubResult(result)
			if err != nil {
				return err
			}
			resolvedID, err := transactionResolvedOutputClaimID(candidate)
			if err != nil {
				return err
			}
			if resolvedID == claimID {
				claims[index] = candidate
				break
			}
		}
	}
	collection.Claims = claims
	return nil
}

func (ledger *Ledger) claimSearchDetachedHubResults(
	ctx context.Context, claimIDs []string,
) ([]HubInflatedResult, error) {
	outputs, err := ledger.QueryClaimSearch(ctx, map[string]any{
		"claim_ids": append([]string(nil), claimIDs...),
	})
	if err != nil {
		return nil, err
	}
	page, err := ledger.inflateDetachedHubOutputs(ctx, outputs)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (ledger *Ledger) resolveDetachedHubResults(
	ctx context.Context, urls []string,
) ([]HubInflatedResult, error) {
	results := make([]HubInflatedResult, 0, len(urls))
	for start := 0; start < len(urls); start += ResolveBatchSize {
		end := min(start+ResolveBatchSize, len(urls))
		outputs, err := ledger.QueryResolveBatch(ctx, urls[start:end])
		if err != nil {
			return nil, err
		}
		page, err := ledger.inflateDetachedHubOutputs(ctx, outputs)
		if err != nil {
			return nil, err
		}
		results = append(results, page.Items...)
	}
	if len(results) != len(urls) {
		return nil, &ResolveResultCountError{}
	}
	for index := range results {
		if results[index].IsMissing() {
			results[index].Error = &HubResolveError{
				Name: "NOT_FOUND", Text: urls[index] + " did not resolve to a claim",
			}
			continue
		}
		if results[index].Output == nil {
			continue
		}
		parsed, err := ParseLBRYURL(urls[index])
		if err != nil {
			return nil, err
		}
		if !parsed.HasStreamInChannel {
			continue
		}
		valid := false
		if results[index].Output.Channel != nil {
			valid, err = ledger.resolveChannelSignatureValid(results[index].Output)
			if err != nil {
				return nil, err
			}
		}
		if !valid {
			results[index] = HubInflatedResult{Error: &HubResolveError{
				Name: "INVALID", Text: urls[index] + " has invalid channel signature",
			}}
		}
	}
	return results, nil
}

func transactionOutputsFromHubResults(
	results []HubInflatedResult,
) ([]*TransactionOutput, error) {
	outputs := make([]*TransactionOutput, len(results))
	for index, result := range results {
		output, err := transactionOutputFromHubResult(result)
		if err != nil {
			return nil, err
		}
		outputs[index] = output
	}
	return outputs, nil
}

func transactionOutputFromHubResult(result HubInflatedResult) (*TransactionOutput, error) {
	switch {
	case result.Output != nil:
		return result.Output, nil
	case result.Error != nil:
		return nil, localResolutionAttributeError(
			"'dict' object has no attribute 'claim_id'",
		)
	default:
		return nil, localResolutionAttributeError(
			"'NoneType' object has no attribute 'claim_id'",
		)
	}
}

func transactionOutputPermanentURL(output *TransactionOutput) (string, error) {
	output = currentTransactionOutput(output)
	if output == nil || !output.Script.IsClaimInvolved() {
		return "", errors.New("No claim associated.")
	}
	if !utf8.Valid(output.Script.ClaimName) {
		return "", localResolutionUTF8Error(output.Script.ClaimName)
	}
	claimID, err := output.ClaimID()
	if err != nil {
		return "", err
	}
	return "lbry://" + string(output.Script.ClaimName) + "#" + claimID, nil
}

func transactionPurchasedClaimID(output *TransactionOutput) (string, error) {
	output = currentTransactionOutput(output)
	if output == nil {
		return "", localResolutionAttributeError(
			"'NoneType' object has no attribute 'purchased_claim_id'",
		)
	}
	if output.Purchase != nil {
		if claimID, ok := decodeTransactionPurchase(
			currentTransactionOutput(output.Purchase).Script,
		); ok {
			return claimID, nil
		}
		return "", localResolutionPythonError{
			name: "DecodeError", message: "invalid purchase data",
		}
	}
	if output.PurchasedClaim != nil {
		return currentTransactionOutput(output.PurchasedClaim).ClaimID()
	}
	return "", nil
}

func transactionCollectionClaimIDs(
	collection *TransactionOutput, limit int,
) ([]string, error) {
	collection = currentTransactionOutput(collection)
	if collection == nil {
		return nil, localResolutionAttributeError(
			"'NoneType' object has no attribute 'claim'",
		)
	}
	decoded, err := decodeTransactionWireClaimValue(collection.Script.Claim)
	if err != nil {
		return nil, err
	}
	if decoded.value == nil || decoded.value.Type != "collection" {
		return nil, localResolutionAttributeError(
			"'Claim' object has no attribute 'collection'",
		)
	}
	raw, exists := decoded.value.Value["claims"]
	if !exists {
		return make([]string, 0), nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: collection claims are not a list", ErrTransactionResolvedClaim)
	}
	end := limit
	if end < 0 {
		end = len(values) + end
	}
	if end < 0 {
		end = 0
	}
	if end > len(values) {
		end = len(values)
	}
	claimIDs := make([]string, end)
	for index := range end {
		claimID, ok := values[index].(string)
		if !ok {
			return nil, fmt.Errorf(
				"%w: collection claim %d is not a string",
				ErrTransactionResolvedClaim, index,
			)
		}
		claimIDs[index] = claimID
	}
	return claimIDs, nil
}

func copyLocalTransactionOutputAnnotations(target, source *TransactionOutput) {
	target.IsInternalTransfer = source.IsInternalTransfer
	target.IsSpent = source.IsSpent
	target.IsMyOutput = source.IsMyOutput
	target.IsMyInput = source.IsMyInput
	target.SentSupports = source.SentSupports
	target.SentTips = source.SentTips
	target.ReceivedTips = source.ReceivedTips
	target.Channel = source.Channel
	target.PrivateKey = source.PrivateKey
}

func hubResolveErrorValue(resolved *HubResolveError) map[string]any {
	value := map[string]any{"name": resolved.Name, "text": resolved.Text}
	if resolved.Censor != nil {
		value["censor"] = hubInflatedResultValue(*resolved.Censor)
	}
	return value
}

func hubInflatedResultValue(result HubInflatedResult) any {
	if result.Output != nil {
		return result.Output
	}
	if result.Error != nil {
		return map[string]any{"error": hubResolveErrorValue(result.Error)}
	}
	return nil
}

type localResolutionPythonError struct {
	name    string
	message string
	cause   error
}

func (err localResolutionPythonError) Error() string           { return err.message }
func (err localResolutionPythonError) PythonErrorName() string { return err.name }
func (err localResolutionPythonError) Unwrap() error           { return err.cause }

func localResolutionAttributeError(message string) error {
	return localResolutionPythonError{name: "AttributeError", message: message}
}

func localResolutionUTF8Error(encoded []byte) error {
	start, end, reason := invalidSupportUTF8(encoded)
	location := fmt.Sprintf("bytes in position %d-%d", start, end-1)
	if end == start+1 {
		location = fmt.Sprintf("byte 0x%02x in position %d", encoded[start], start)
	}
	return localResolutionPythonError{
		name:    "UnicodeDecodeError",
		message: "'utf-8' codec can't decode " + location + ": " + reason,
	}
}

type localResolutionStage struct {
	stage error
	err   error
}

func (err localResolutionStage) Error() string { return err.err.Error() }
func (err localResolutionStage) Unwrap() []error {
	return []error{err.stage, err.err}
}

func (err localResolutionStage) LocalResolutionCause() error { return err.err }

func localResolutionStageError(stage, err error) error {
	if err == nil {
		return nil
	}
	return localResolutionStage{stage: stage, err: err}
}
