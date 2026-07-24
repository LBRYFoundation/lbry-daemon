package wallet

import (
	"errors"
	"fmt"
)

var ErrTransactionResolvedClaim = errors.New("invalid resolved transaction claim")

// mapTransactionCollectionClaims mirrors Ledger.resolve_collection's ordered
// nested scan. It preserves duplicate requested IDs, uses the first resolved
// output with a matching ID, and leaves misses as nil entries.
func mapTransactionCollectionClaims(
	claimIDs []string, resolved []*TransactionOutput,
) ([]*TransactionOutput, error) {
	claims := make([]*TransactionOutput, len(claimIDs))
	for index, claimID := range claimIDs {
		for _, candidate := range resolved {
			resolvedID, err := transactionResolvedOutputClaimID(candidate)
			if err != nil {
				return nil, err
			}
			if resolvedID == claimID {
				claims[index] = candidate
				break
			}
		}
	}
	return claims, nil
}

// mapTransactionResolvedClaims mirrors the dict comprehensions used for
// purchased claims and purchase receipts. Later duplicate resolved outputs
// replace earlier ones, while requested order and duplicates are retained.
func mapTransactionResolvedClaims(
	claimIDs []string, resolved []*TransactionOutput,
) ([]*TransactionOutput, error) {
	lookup := make(map[string]*TransactionOutput, len(resolved))
	for _, candidate := range resolved {
		claimID, err := transactionResolvedOutputClaimID(candidate)
		if err != nil {
			return nil, err
		}
		lookup[claimID] = candidate
	}
	claims := make([]*TransactionOutput, len(claimIDs))
	for index, claimID := range claimIDs {
		claims[index] = lookup[claimID]
	}
	return claims, nil
}

// hydrateTransactionCollectionClaims applies a successful collection resolve.
// In particular, an empty requested slice becomes a non-nil empty Claims list;
// nil remains reserved for a collection whose claims were not resolved.
func hydrateTransactionCollectionClaims(
	collection *TransactionOutput, claimIDs []string, resolved []*TransactionOutput,
) error {
	if collection == nil {
		return fmt.Errorf("%w: collection output is nil", ErrTransactionResolvedClaim)
	}
	claims, err := mapTransactionCollectionClaims(claimIDs, resolved)
	if err != nil {
		return err
	}
	collection.Claims = claims
	return nil
}

// clearTransactionCollectionClaimsAfterResolveError mirrors
// Ledger.resolve_collection's broad exception handler: the caller records a
// completed resolution with an empty list, rather than the unresolved nil.
func clearTransactionCollectionClaimsAfterResolveError(collection *TransactionOutput) error {
	if collection == nil {
		return fmt.Errorf("%w: collection output is nil", ErrTransactionResolvedClaim)
	}
	collection.Claims = make([]*TransactionOutput, 0)
	return nil
}

// hydrateTransactionPurchasedClaims applies the last-result-wins lookup used
// by Ledger.get_purchases(resolve=True). claimIDs must align with purchases.
func hydrateTransactionPurchasedClaims(
	purchases []*TransactionOutput, claimIDs []string, resolved []*TransactionOutput,
) error {
	if len(purchases) != len(claimIDs) {
		return fmt.Errorf(
			"%w: %d purchases for %d claim IDs",
			ErrTransactionResolvedClaim, len(purchases), len(claimIDs),
		)
	}
	mapped, err := mapTransactionResolvedClaims(claimIDs, resolved)
	if err != nil {
		return err
	}
	for index, purchase := range purchases {
		if purchase == nil {
			return fmt.Errorf(
				"%w: purchase output %d is nil", ErrTransactionResolvedClaim, index,
			)
		}
		purchase.PurchasedClaim = mapped[index]
	}
	return nil
}

// hydrateTransactionPurchaseReceipts applies the same last-result-wins map to
// claim outputs selected by _inflate_outputs(include_purchase_receipt=True).
func hydrateTransactionPurchaseReceipts(
	claims []*TransactionOutput, claimIDs []string, receipts []*TransactionOutput,
) error {
	if len(claims) != len(claimIDs) {
		return fmt.Errorf(
			"%w: %d claims for %d claim IDs",
			ErrTransactionResolvedClaim, len(claims), len(claimIDs),
		)
	}
	mapped, err := mapTransactionResolvedClaims(claimIDs, receipts)
	if err != nil {
		return err
	}
	for index, claim := range claims {
		if claim == nil {
			return fmt.Errorf(
				"%w: claim output %d is nil", ErrTransactionResolvedClaim, index,
			)
		}
		claim.PurchaseReceipt = mapped[index]
	}
	return nil
}

func transactionResolvedOutputClaimID(output *TransactionOutput) (string, error) {
	if output == nil {
		return "", fmt.Errorf("%w: resolved output is nil", ErrTransactionResolvedClaim)
	}
	claimID, err := currentTransactionOutput(output).ClaimID()
	if err != nil {
		if errors.Is(err, ErrTransactionHasNoClaimID) {
			return "", localResolutionPythonError{
				name: "ValueError", message: "No claim_id associated.",
				cause: ErrTransactionResolvedClaim,
			}
		}
		return "", fmt.Errorf("%w: %v", ErrTransactionResolvedClaim, err)
	}
	return claimID, nil
}
