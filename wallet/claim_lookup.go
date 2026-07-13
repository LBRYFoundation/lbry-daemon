package wallet

import (
	"context"
	"errors"
)

var ErrClaimLookupMissing = errors.New("claim lookup returned no output")

// GetClaimByClaimID mirrors Ledger.get_claim_by_claim_id's remote lookup and
// applies the same wallet-owned annotations used by resolve.
func (ledger *Ledger) GetClaimByClaimID(
	ctx context.Context,
	claimID string,
	options ResolvedTransactionOutputAnnotationOptions,
) (*TransactionOutput, error) {
	outputs, err := ledger.QueryClaimSearch(ctx, map[string]any{
		"claim_ids": []string{claimID},
	})
	if err != nil {
		return nil, err
	}
	page, err := ledger.inflateDetachedHubOutputs(ctx, outputs)
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 || page.Items[0].Output == nil {
		return nil, ErrClaimLookupMissing
	}
	enriched, err := ledger.EnrichResolvedTransactionOutputAnnotations(
		ctx, []*TransactionOutput{page.Items[0].Output}, options,
	)
	if err != nil {
		return nil, err
	}
	if len(enriched) == 0 || enriched[0] == nil {
		return nil, ErrClaimLookupMissing
	}
	return enriched[0], nil
}
