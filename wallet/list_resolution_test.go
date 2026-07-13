package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResolveLocalTransactionOutputsUsesLastDuplicateAndLocalAnnotations(t *testing.T) {
	payload := claimWireOracleMustHex(t, transactionResolvedWireStream)
	localTransaction := transactionResolvedWireClaimTransaction(t, 0x31, "local", payload)
	firstLocal := localTransaction.Outputs[0]
	secondLocal := localTransaction.Outputs[0]
	firstSpent, secondSpent := true, false
	firstTips, secondTips := int64(5), int64(9)
	firstLocal.IsSpent, firstLocal.SentTips = &firstSpent, &firstTips
	secondLocal.IsSpent, secondLocal.SentTips = &secondSpent, &secondTips

	firstRemote := transactionResolvedWireClaimTransaction(t, 0x41, "first", payload)
	secondRemote := transactionResolvedWireClaimTransaction(t, 0x42, "second", payload)
	encoded := listResolutionPage(
		listResolutionOutputReference(firstRemote, "first"),
		listResolutionOutputReference(secondRemote, "second"),
	)
	source := &resolveWorkflowSource{results: []any{encoded}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	listResolutionCacheTransaction(t, ledger, firstRemote)
	listResolutionCacheTransaction(t, ledger, secondRemote)

	resolved, err := ledger.ResolveLocalTransactionOutputs(
		context.Background(), []*TransactionOutput{&firstLocal, &secondLocal},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[0] == nil || resolved[0] != resolved[1] ||
		resolved[0] == &secondRemote.Outputs[0] || resolved[0].IsSpent != &secondSpent ||
		resolved[0].SentTips != &secondTips {
		t.Fatalf("duplicate local resolve = %#v", resolved)
	}
	resolved[0].Meta["detached"] = true
	if secondRemote.Outputs[0].Meta["detached"] != nil ||
		secondRemote.Outputs[0].IsSpent != nil || secondRemote.Outputs[0].SentTips != nil {
		t.Fatalf("resolved annotations leaked into cache source: %#v", secondRemote.Outputs[0])
	}

	url, err := transactionOutputPermanentURL(&firstLocal)
	if err != nil {
		t.Fatal(err)
	}
	calls := source.snapshotCalls()
	if len(calls) != 1 || calls[0].method != SPVResolveMethod ||
		!reflect.DeepEqual(calls[0].params, []any{url, url}) {
		t.Fatalf("duplicate resolve calls = %#v", calls)
	}
}

func TestResolveLocalTransactionOutputsPreservesHubErrorOnLocalMeta(t *testing.T) {
	payload := claimWireOracleMustHex(t, transactionResolvedWireStream)
	local := transactionResolvedWireClaimTransaction(t, 0x51, "error", payload)
	source := &resolveWorkflowSource{results: []any{
		resolveWorkflowErrorPage(1, HubErrorInvalid, "invalid result"),
	}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	result, err := ledger.ResolveLocalTransactionOutputs(
		context.Background(), []*TransactionOutput{&local.Outputs[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	errorValue, ok := local.Outputs[0].Meta["error"].(map[string]any)
	if len(result) != 1 || result[0] != &local.Outputs[0] || !ok ||
		errorValue["name"] != "INVALID" || errorValue["text"] != "invalid result" {
		t.Fatalf("local resolve error = result %#v, meta %#v", result, local.Outputs[0].Meta)
	}
}

func TestResolveLocalTransactionOutputsMarksResolveStage(t *testing.T) {
	payload := claimWireOracleMustHex(t, transactionResolvedWireStream)
	local := transactionResolvedWireClaimTransaction(t, 0x52, "failure", payload)
	ledger := &Ledger{SPVNetwork: &claimSearchQuerySource{}}
	_, err := ledger.ResolveLocalTransactionOutputs(
		context.Background(), []*TransactionOutput{&local.Outputs[0]},
	)
	if !errors.Is(err, ErrLocalTransactionResolve) ||
		!errors.Is(err, ErrResolveUnavailable) {
		t.Fatalf("marked local resolve error = %T %v", err, err)
	}
}

func TestResolveLocalTransactionOutputsPreservesClaimNameUnicodeErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		claimName []byte
		message   string
	}{
		{
			name: "invalid start", claimName: []byte{0xff},
			message: "'utf-8' codec can't decode byte 0xff in position 0: invalid start byte",
		},
		{
			name: "truncated", claimName: []byte{0xe2, 0x82},
			message: "'utf-8' codec can't decode bytes in position 0-1: unexpected end of data",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := transactionResolvedWireClaimTransaction(
				t, 0x54, "valid", claimWireOracleMustHex(t, transactionResolvedWireStream),
			)
			local.Outputs[0].Script.ClaimName = append([]byte(nil), test.claimName...)
			source := &resolveWorkflowSource{}
			ledger := &Ledger{SPVNetwork: source}
			_, err := ledger.ResolveLocalTransactionOutputs(
				context.Background(), []*TransactionOutput{&local.Outputs[0]},
			)
			var named interface{ PythonErrorName() string }
			if !errors.As(err, &named) || named.PythonErrorName() != "UnicodeDecodeError" ||
				err.Error() != test.message || len(source.snapshotCalls()) != 0 {
				t.Fatalf("claim-name error = %T %v, calls %#v", err, err, source.snapshotCalls())
			}
		})
	}
}

func TestResolveLocalTransactionOutputsHydratesSignedSupportChannel(t *testing.T) {
	channelID := strings.Repeat("44", 20)
	channel := listResolutionClaimTransaction(t, channelID, 0x53)
	supportValue := append([]byte{1}, bytes.Repeat([]byte{0x44}, 20)...)
	supportValue = append(supportValue, bytes.Repeat([]byte{0x55}, 64)...)
	support, err := NewSupportDataOutput(
		1, "supported", strings.Repeat("66", 20), supportValue,
		bytes.Repeat([]byte{0x77}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	source := &claimSearchQuerySource{result: listResolutionPage(
		listResolutionOutputReference(channel, "@channel"),
	)}
	ledger := &Ledger{SPVNetwork: source}
	listResolutionCacheTransaction(t, ledger, channel)
	resolved, err := ledger.ResolveLocalTransactionOutputs(
		context.Background(), []*TransactionOutput{&support},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != &support || support.Channel == nil ||
		support.Channel == &channel.Outputs[0] {
		t.Fatalf("signed support resolution = %#v", resolved)
	}
	if len(source.calls) != 1 || !reflect.DeepEqual(
		source.calls[0].params, map[string]any{"claim_ids": []string{channelID}},
	) {
		t.Fatalf("support channel search = %#v", source.calls)
	}
}

func TestResolvePurchaseOutputsSwallowsAwaitFailureButNotCancellation(t *testing.T) {
	claimID := strings.Repeat("11", 20)
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	old := transactionResolvedEnrichmentClaim(t, 0x61)
	purchase := &TransactionOutput{Purchase: &purchaseData, PurchasedClaim: old}
	ordinary := errors.New("claim search failed")
	ledger := &Ledger{SPVNetwork: &claimSearchQuerySource{err: ordinary}}
	if err := ledger.ResolvePurchaseOutputs(
		context.Background(), []*TransactionOutput{purchase},
	); err != nil || purchase.PurchasedClaim != nil {
		t.Fatalf("swallowed purchase failure = claim %#v, error %v", purchase.PurchasedClaim, err)
	}

	purchase.PurchasedClaim = old
	ledger.SPVNetwork = &claimSearchQuerySource{err: context.Canceled}
	if err := ledger.ResolvePurchaseOutputs(
		context.Background(), []*TransactionOutput{purchase},
	); !errors.Is(err, context.Canceled) || purchase.PurchasedClaim != old {
		t.Fatalf("purchase cancellation = claim %#v, error %v", purchase.PurchasedClaim, err)
	}
}

func TestResolvePurchaseOutputsPropagatesReturnedMappingErrorWithoutMutation(t *testing.T) {
	claimID := strings.Repeat("22", 20)
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	old := transactionResolvedEnrichmentClaim(t, 0x62)
	purchase := &TransactionOutput{Purchase: &purchaseData, PurchasedClaim: old}
	source := &claimSearchQuerySource{result: resolveWorkflowErrorPage(
		1, HubErrorNotFound, "missing",
	)}
	ledger := &Ledger{SPVNetwork: source}
	err = ledger.ResolvePurchaseOutputs(context.Background(), []*TransactionOutput{purchase})
	var pythonError interface{ PythonErrorName() string }
	if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != "AttributeError" ||
		err.Error() != "'dict' object has no attribute 'claim_id'" ||
		purchase.PurchasedClaim != old {
		t.Fatalf("purchase mapping failure = claim %#v, %T %v", purchase.PurchasedClaim, err, err)
	}
}

func TestResolvePurchaseOutputsHydratesDetachedClaim(t *testing.T) {
	claim := listResolutionClaimTransaction(t, strings.Repeat("33", 20), 0x71)
	claimID, err := claim.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	source := &claimSearchQuerySource{result: listResolutionPage(
		listResolutionOutputReference(claim, "claim"),
	)}
	ledger := &Ledger{SPVNetwork: source}
	listResolutionCacheTransaction(t, ledger, claim)
	purchase := &TransactionOutput{Purchase: &purchaseData}
	if err := ledger.ResolvePurchaseOutputs(
		context.Background(), []*TransactionOutput{purchase},
	); err != nil {
		t.Fatal(err)
	}
	if purchase.PurchasedClaim == nil || purchase.PurchasedClaim == &claim.Outputs[0] {
		t.Fatalf("hydrated purchased claim = %#v", purchase.PurchasedClaim)
	}
	purchase.PurchasedClaim.Meta["detached"] = true
	if claim.Outputs[0].Meta["detached"] != nil {
		t.Fatalf("purchased claim leaked cache meta: %#v", claim.Outputs[0].Meta)
	}
	if len(source.calls) != 1 || !reflect.DeepEqual(
		source.calls[0].params, map[string]any{"claim_ids": []string{claimID}},
	) {
		t.Fatalf("purchase claim search = %#v", source.calls)
	}
}

func TestResolveCollectionClaimsOrdersMatchesAndSwallowsOnlyAwaitFailure(t *testing.T) {
	collectionPayload := claimWireOracleMustHex(t, transactionResolvedWireCollection)
	collection := transactionResolvedWireClaimTransaction(
		t, 0x81, "collection", collectionPayload,
	)
	claimIDs, err := transactionCollectionClaimIDs(&collection.Outputs[0], 2)
	if err != nil || len(claimIDs) != 2 {
		t.Fatalf("collection IDs = %v, %v", claimIDs, err)
	}
	claim := listResolutionClaimTransaction(t, claimIDs[0], 0x82)
	source := &claimSearchQuerySource{result: listResolutionPage(
		listResolutionOutputReference(claim, "member"),
	)}
	ledger := &Ledger{SPVNetwork: source}
	listResolutionCacheTransaction(t, ledger, claim)
	if err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 2,
	); err != nil {
		t.Fatal(err)
	}
	claims := collection.Outputs[0].Claims
	if len(claims) != 2 || claims[0] == nil || claims[1] != nil ||
		claims[0] == &claim.Outputs[0] {
		t.Fatalf("resolved collection claims = %#v", claims)
	}
	if !reflect.DeepEqual(
		source.calls[0].params, map[string]any{"claim_ids": claimIDs},
	) {
		t.Fatalf("collection claim search = %#v", source.calls)
	}

	old := []*TransactionOutput{claims[0]}
	collection.Outputs[0].Claims = old
	ledger.SPVNetwork = &claimSearchQuerySource{err: errors.New("search failed")}
	if err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 2,
	); err != nil || collection.Outputs[0].Claims == nil ||
		len(collection.Outputs[0].Claims) != 0 {
		t.Fatalf("swallowed collection failure = %#v, %v", collection.Outputs[0].Claims, err)
	}

	collection.Outputs[0].Claims = old
	ledger.SPVNetwork = &claimSearchQuerySource{err: context.Canceled}
	if err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 2,
	); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(collection.Outputs[0].Claims, old) {
		t.Fatalf("collection cancellation = %#v, %v", collection.Outputs[0].Claims, err)
	}
}

func TestResolveCollectionClaimsPropagatesReturnedMappingErrorWithoutMutation(t *testing.T) {
	collection := transactionResolvedWireClaimTransaction(
		t, 0x83, "collection",
		claimWireOracleMustHex(t, transactionResolvedWireCollection),
	)
	old := []*TransactionOutput{transactionResolvedEnrichmentClaim(t, 0x84)}
	collection.Outputs[0].Claims = old
	ledger := &Ledger{SPVNetwork: &claimSearchQuerySource{result: resolveWorkflowErrorPage(
		1, HubErrorNotFound, "missing member",
	)}}
	err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 1,
	)
	var pythonError interface{ PythonErrorName() string }
	if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != "AttributeError" ||
		err.Error() != "'dict' object has no attribute 'claim_id'" ||
		!reflect.DeepEqual(collection.Outputs[0].Claims, old) {
		t.Fatalf("collection mapping failure = claims %#v, %T %v", collection.Outputs[0].Claims, err, err)
	}
}

func TestResolveCollectionClaimsStopsBeforeUnusedHubErrors(t *testing.T) {
	collection := transactionResolvedWireClaimTransaction(
		t, 0x85, "collection",
		claimWireOracleMustHex(t, transactionResolvedWireCollection),
	)
	claimIDs, err := transactionCollectionClaimIDs(&collection.Outputs[0], 1)
	if err != nil || len(claimIDs) != 1 {
		t.Fatalf("collection IDs = %v, %v", claimIDs, err)
	}
	claim := listResolutionClaimTransaction(t, claimIDs[0], 0x86)
	source := &claimSearchQuerySource{result: listResolutionPage(
		listResolutionOutputReference(claim, "member"),
		resolveWorkflowErrorOutput(HubErrorNotFound, "unused trailing error"),
	)}
	ledger := &Ledger{SPVNetwork: source}
	listResolutionCacheTransaction(t, ledger, claim)

	if err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 1,
	); err != nil {
		t.Fatal(err)
	}
	if len(collection.Outputs[0].Claims) != 1 || collection.Outputs[0].Claims[0] == nil {
		t.Fatalf("first-match collection claims = %#v", collection.Outputs[0].Claims)
	}

	collection.Outputs[0].Claims = []*TransactionOutput{collection.Outputs[0].Claims[0]}
	if err := ledger.ResolveCollectionClaims(
		context.Background(), &collection.Outputs[0], 0,
	); err != nil {
		t.Fatal(err)
	}
	if collection.Outputs[0].Claims == nil || len(collection.Outputs[0].Claims) != 0 {
		t.Fatalf("empty requested collection claims = %#v", collection.Outputs[0].Claims)
	}
}

func listResolutionClaimTransaction(
	t *testing.T, claimID string, marker byte,
) *Transaction {
	t.Helper()
	claimHash, err := hex.DecodeString(claimID)
	if err != nil {
		t.Fatal(err)
	}
	output := TransactionOutput{Script: TransactionOutputScript{
		Template:  TransactionScriptUpdatePubKey,
		ClaimName: []byte("resolved"),
		ClaimID:   reverseTransactionBytes(claimHash),
		Claim:     claimWireOracleMustHex(t, transactionResolvedWireStream),
	}}
	return claimWireOracleTransaction(
		t, strings.Repeat(hex.EncodeToString([]byte{marker}), 32), 7, output,
	)
}

func listResolutionCacheTransaction(t *testing.T, ledger *Ledger, transaction *Transaction) {
	t.Helper()
	transaction.IsVerified = true
	cache := ledger.ledgerTransactionCache()
	if err := cache.insertPlaceholder(transaction.ID); err != nil {
		t.Fatal(err)
	}
	if err := cache.setExisting(transaction.ID, transaction); err != nil {
		t.Fatal(err)
	}
}

func listResolutionPage(outputs ...[]byte) string {
	return resolveWorkflowPage(outputs...)
}

func listResolutionOutputReference(transaction *Transaction, shortURL string) []byte {
	claim := resolveWorkflowBytesField(nil, 3, []byte(shortURL))
	output := resolveWorkflowBytesField(nil, 1, transaction.Hash[:])
	output = resolveWorkflowVarintField(output, 2, 0)
	output = resolveWorkflowVarintField(output, 3, uint64(transaction.Height))
	output = resolveWorkflowBytesField(output, 7, claim)
	return output
}
