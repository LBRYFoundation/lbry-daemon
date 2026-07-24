package wallet

import (
	"errors"
	"reflect"
	"testing"
)

func TestTransactionCollectionClaimMappingPreservesPinnedOrder(t *testing.T) {
	firstA := transactionResolvedEnrichmentClaim(t, 0x11)
	secondA := transactionResolvedEnrichmentClaim(t, 0x11)
	claimB := transactionResolvedEnrichmentClaim(t, 0x22)
	claimAID := transactionResolvedEnrichmentClaimID(t, firstA)
	claimBID := transactionResolvedEnrichmentClaimID(t, claimB)

	collection := transactionResolvedEnrichmentClaim(t, 0x33)
	err := hydrateTransactionCollectionClaims(
		collection,
		[]string{claimBID, "missing", claimAID, claimAID},
		[]*TransactionOutput{firstA, claimB, secondA},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []*TransactionOutput{claimB, nil, firstA, firstA}
	if !reflect.DeepEqual(collection.Claims, want) {
		t.Fatalf("resolved collection claims = %#v, want %#v", collection.Claims, want)
	}
}

func TestTransactionCollectionClaimsDistinguishUnresolvedAndEmpty(t *testing.T) {
	collection := transactionResolvedEnrichmentClaim(t, 0x41)
	if collection.Claims != nil {
		t.Fatalf("new collection claims = %#v, want nil", collection.Claims)
	}
	if err := hydrateTransactionCollectionClaims(collection, nil, nil); err != nil {
		t.Fatal(err)
	}
	if collection.Claims == nil || len(collection.Claims) != 0 {
		t.Fatalf("resolved empty collection claims = %#v", collection.Claims)
	}

	collection.Claims = []*TransactionOutput{transactionResolvedEnrichmentClaim(t, 0x42)}
	if err := clearTransactionCollectionClaimsAfterResolveError(collection); err != nil {
		t.Fatal(err)
	}
	if collection.Claims == nil || len(collection.Claims) != 0 {
		t.Fatalf("failed collection resolve claims = %#v", collection.Claims)
	}
}

func TestTransactionResolvedClaimMappingUsesLastDuplicate(t *testing.T) {
	first := transactionResolvedEnrichmentClaim(t, 0x51)
	last := transactionResolvedEnrichmentClaim(t, 0x51)
	other := transactionResolvedEnrichmentClaim(t, 0x52)
	claimID := transactionResolvedEnrichmentClaimID(t, first)
	otherID := transactionResolvedEnrichmentClaimID(t, other)

	purchases := []*TransactionOutput{{}, {}, {}}
	if err := hydrateTransactionPurchasedClaims(
		purchases, []string{claimID, "missing", claimID},
		[]*TransactionOutput{first, other, last},
	); err != nil {
		t.Fatal(err)
	}
	if purchases[0].PurchasedClaim != last || purchases[1].PurchasedClaim != nil ||
		purchases[2].PurchasedClaim != last {
		t.Fatalf("purchased claims = %#v", purchases)
	}

	claims := []*TransactionOutput{{}, {}, {}}
	if err := hydrateTransactionPurchaseReceipts(
		claims, []string{otherID, claimID, claimID},
		[]*TransactionOutput{first, other, last},
	); err != nil {
		t.Fatal(err)
	}
	if claims[0].PurchaseReceipt != other || claims[1].PurchaseReceipt != last ||
		claims[2].PurchaseReceipt != last {
		t.Fatalf("purchase receipts = %#v", claims)
	}
}

func TestTransactionResolvedClaimMappingBoundaries(t *testing.T) {
	claim := transactionResolvedEnrichmentClaim(t, 0x61)
	if err := hydrateTransactionPurchasedClaims([]*TransactionOutput{{}}, nil, nil); !errors.Is(
		err, ErrTransactionResolvedClaim,
	) {
		t.Fatalf("mismatched purchase mapping error = %v", err)
	}
	if _, err := mapTransactionCollectionClaims(
		[]string{"claim"}, []*TransactionOutput{nil},
	); !errors.Is(err, ErrTransactionResolvedClaim) {
		t.Fatalf("nil collection result error = %v", err)
	}
	if _, err := mapTransactionResolvedClaims(
		nil, []*TransactionOutput{{}},
	); !errors.Is(err, ErrTransactionResolvedClaim) {
		t.Fatalf("non-claim resolved output error = %v", err)
	} else if named, ok := err.(interface{ PythonErrorName() string }); !ok || named.PythonErrorName() != "ValueError" || err.Error() != "No claim_id associated." {
		t.Fatalf("non-claim Python error = %T %v", err, err)
	}
	if err := hydrateTransactionPurchaseReceipts(
		[]*TransactionOutput{nil}, []string{transactionResolvedEnrichmentClaimID(t, claim)},
		[]*TransactionOutput{claim},
	); !errors.Is(err, ErrTransactionResolvedClaim) {
		t.Fatalf("nil receipt target error = %v", err)
	}
}

func TestTransactionAnnotationCopyDoesNotLeakResolvedRelationships(t *testing.T) {
	related := transactionResolvedEnrichmentClaim(t, 0x71)
	annotated := &TransactionOutput{
		PurchasedClaim: related, PurchaseReceipt: related,
		RepostedClaim: related, Claims: []*TransactionOutput{related},
	}
	target := &TransactionOutput{}
	copyTransactionOutputAnnotations(target, annotated)
	if target.PurchasedClaim != nil || target.PurchaseReceipt != nil ||
		target.RepostedClaim != nil || target.Claims != nil {
		t.Fatalf("database annotations leaked resolved relationships: %#v", target)
	}
}

func transactionResolvedEnrichmentClaim(t *testing.T, hashByte byte) *TransactionOutput {
	t.Helper()
	var transactionHash [32]byte
	for index := range transactionHash {
		transactionHash[index] = hashByte
	}
	return &TransactionOutput{
		TransactionHash: transactionHash,
		Position:        0,
		Script: TransactionOutputScript{
			Template:  TransactionScriptClaimPubKeyHash,
			ClaimName: []byte("claim"),
		},
	}
}

func transactionResolvedEnrichmentClaimID(t *testing.T, output *TransactionOutput) string {
	t.Helper()
	claimID, err := output.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	return claimID
}
