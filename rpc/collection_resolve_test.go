package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestCollectionResolveURLHydratesPaginatedMembers(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	alpha := fixture.outputs["alpha"]
	payload := collectionListClaimPayload(t, alpha.ClaimID)
	collection := txoHandlerOraclePersist(
		t, ledger, "resolved-collection", 710, 710, 710,
		walletpkg.NewClaimNameOutput(
			90_000_000, "ResolvedCollection", payload, bytes.Repeat([]byte{0x7b}, 20),
		),
		alpha.Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection,
			ClaimName:  "ResolvedCollection",
		},
	)
	collectionStored, err := ledger.Database.GetTransaction(context.Background(), collection.TXID)
	if err != nil || collectionStored == nil {
		t.Fatalf("collection transaction = %#v, %v", collectionStored, err)
	}
	collectionTransaction, err := walletpkg.ParseTransaction(collectionStored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	alphaStored, err := ledger.Database.GetTransaction(context.Background(), alpha.TXID)
	if err != nil || alphaStored == nil {
		t.Fatalf("member transaction = %#v, %v", alphaStored, err)
	}
	alphaTransaction, err := walletpkg.ParseTransaction(alphaStored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	network := &collectionListCombinedNetwork{
		resolvePublicNetwork: &resolvePublicNetwork{
			resolveResult: resolvePublicResultBase64(collectionTransaction.Hash[:]),
			transactionRaw: map[string]string{
				collection.TXID: hex.EncodeToString(collectionStored.Raw),
				alpha.TXID:      hex.EncodeToString(alphaStored.Raw),
			},
		},
		searchResult: claimSearchPublicResultBase64(alphaTransaction.Hash[:], 0, 1),
	}
	ledger.SPVNetwork = network

	result := txoHandlerOracleResult(t, fixture.request(t, "collection_resolve", map[string]any{
		"url":  "lbry://ResolvedCollection#" + collection.ClaimID,
		"page": -1, "page_size": 50,
	}))
	items := result["items"].([]any)
	if len(items) != 1 || result["total_items"] != json.Number("1") ||
		result["total_pages"] != json.Number("1") || result["page"] != json.Number("-1") ||
		items[0].(map[string]any)["claim_id"] != alpha.ClaimID {
		t.Fatalf("collection_resolve = %#v", result)
	}
}
