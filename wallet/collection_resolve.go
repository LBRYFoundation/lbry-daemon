package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"
)

var ErrCollectionReferenceNotFound = errors.New("collection reference was not found")

func (ledger *Ledger) ResolveCollectionReference(
	ctx context.Context, claimID, url string, offset, limit int,
) ([]*TransactionOutput, int, error) {
	collection, err := ledger.resolveCollectionOutput(ctx, claimID, url)
	if err != nil {
		return nil, 0, err
	}
	allClaimIDs, err := transactionCollectionClaimIDs(collection, math.MaxInt)
	if err != nil {
		return nil, 0, err
	}
	total := len(allClaimIDs)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	requested := append([]string(nil), allClaimIDs[offset:end]...)
	results, err := ledger.claimSearchDetachedHubResults(ctx, requested)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, 0, err
		}
		return []*TransactionOutput{}, total, nil
	}
	if err := hydrateTransactionCollectionHubResults(collection, requested, results); err != nil {
		return nil, 0, err
	}
	return collection.Claims, total, nil
}

func (ledger *Ledger) resolveCollectionOutput(
	ctx context.Context, claimID, url string,
) (*TransactionOutput, error) {
	var results []HubInflatedResult
	var err error
	if claimID != "" {
		results, err = ledger.claimSearchDetachedHubResults(ctx, []string{claimID})
	} else {
		results, err = ledger.resolveDetachedHubResults(ctx, []string{url})
	}
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		output, outputErr := transactionOutputFromHubResult(result)
		if outputErr != nil {
			continue
		}
		if output != nil && (output.Script.IsClaimName() || output.Script.IsUpdateClaim()) {
			return output, nil
		}
	}
	return nil, fmt.Errorf("%w", ErrCollectionReferenceNotFound)
}
