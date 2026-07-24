package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	walletpkg "lbry/daemon/wallet"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestCollectionListNonResolvingHandlerMatchesPinnedOracle(t *testing.T) {
	oracle := runListWrapperOracle(t)
	oracleCases := make(map[string]listWrapperOracleCase, len(oracle.Cases))
	for _, fixture := range oracle.Cases {
		oracleCases[fixture.Name] = fixture
	}
	fixture := newTXOHandlerOracleFixture(t)
	fixture.server.handlers["collection_list"] = fixture.server.handleCollectionList

	result := txoHandlerOracleResult(t, fixture.request(t, "collection_list", nil))
	oracleResult := listWrapperDecodeObject(t, oracleCases["collection defaults"].Result)
	if !reflect.DeepEqual(txoHandlerOracleMapKeys(result), txoHandlerOracleMapKeys(oracleResult)) ||
		result["page"] != json.Number("1") || result["page_size"] != json.Number("20") ||
		result["total_items"] != json.Number("1") || result["total_pages"] != json.Number("1") {
		t.Fatalf("collection defaults = %#v, oracle keys %v", result, txoHandlerOracleMapKeys(oracleResult))
	}
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 {
		t.Fatalf("collection defaults returned %d items", len(items))
	}
	for key, want := range map[string]any{
		"name": "Gamma", "type": "claim", "value_type": "collection",
		"amount": "2.0", "claim_op": "create",
	} {
		if items[0][key] != want {
			t.Fatalf("collection output %s = %#v, want %#v; output %#v", key, items[0][key], want, items[0])
		}
	}

	result = txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"page": 2, "page_size": 1, "resolve_claims": -3,
	}))
	if len(txoHandlerOracleItems(t, result)) != 0 || result["total_items"] != json.Number("1") ||
		result["page"] != json.Number("2") || result["page_size"] != json.Number("1") {
		t.Fatalf("negative resolve_claims pagination = %#v", result)
	}

	protobuf := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"include_protobuf": true,
	}))
	protobufItems := txoHandlerOracleItems(t, protobuf)
	if len(protobufItems) != 1 || protobufItems[0]["protobuf"] == nil {
		t.Fatalf("collection protobuf output = %#v", protobufItems)
	}
}

func TestCollectionListUsesDefaultAndAccountLedgers(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	fixture.server.handlers["collection_list"] = fixture.server.handleCollectionList
	payload := append([]byte(nil), []byte{0x00, 0x1a, 0x00}...)
	claimHash := bytes.Repeat([]byte{0x75}, 20)

	mainCollection := txoHandlerOraclePersist(
		t, fixture.manager.DefaultLedger(), "b-main-collection", 501, 40, 40,
		walletpkg.NewClaimNameOutput(90_000_000, "BMainCollection", payload, claimHash),
		fixture.outputs["b-main"].Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection, ClaimName: "BMainCollection",
		},
	)
	fixture.labels[mainCollection.TXID] = "b-main-collection"
	testLedger := fixture.manager.Ledgers[fixture.accountB.Network]
	testCollection := txoHandlerOraclePersist(
		t, testLedger, "b-test-collection", 502, 41, 41,
		walletpkg.NewClaimNameOutput(91_000_000, "BTestCollection", payload, claimHash),
		fixture.outputs["b-test"].Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection, ClaimName: "BTestCollection",
		},
	)
	fixture.labels[testCollection.TXID] = "b-test-collection"

	walletWide := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"wallet_id": fixture.walletB.ID,
	}))
	if got := fixture.resultLabels(t, walletWide); !reflect.DeepEqual(got, []string{"b-main-collection"}) {
		t.Fatalf("wallet-wide collections = %v, want default-ledger collection", got)
	}
	accountScoped := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"wallet_id": fixture.walletB.ID, "account_id": fixture.accountB.ID,
	}))
	if got := fixture.resultLabels(t, accountScoped); !reflect.DeepEqual(got, []string{"b-test-collection"}) {
		t.Fatalf("account collections = %v, want account-ledger collection", got)
	}
}

func TestCollectionListResolutionValidationAfterSelection(t *testing.T) {
	oracle := runListWrapperOracle(t)
	oracleCases := make(map[string]listWrapperOracleCase, len(oracle.Cases))
	for _, fixture := range oracle.Cases {
		oracleCases[fixture.Name] = fixture
	}
	fixture := newTXOHandlerOracleFixture(t)
	fixture.server.handlers["collection_list"] = fixture.server.handleCollectionList

	resolvedMembers := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"resolve_claims": 1,
	}))
	memberItems := txoHandlerOracleItems(t, resolvedMembers)
	if len(memberItems) != 1 || !reflect.DeepEqual(memberItems[0]["claims"], []any{}) {
		t.Fatalf("resolved empty collection members = %#v", memberItems)
	}
	txoHandlerOracleAssertErrorNameMessage(
		t, fixture.request(t, "collection_list", map[string]any{"resolve_claims": 0.5}),
		"TypeError", "slice indices must be integers or None or have an __index__ method",
	)
	emptyFloatPage := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"resolve_claims": 0.5, "page": 2, "page_size": 1,
	}))
	if len(txoHandlerOracleItems(t, emptyFloatPage)) != 0 ||
		emptyFloatPage["total_items"] != json.Number("1") {
		t.Fatalf("empty float resolve_claims page = %#v", emptyFloatPage)
	}
	positional := performRequest(
		fixture.server, "POST", "/",
		`{"method":"collection_list","params":[[1],{}]}`, nil,
	)
	if positional.Code != 200 {
		t.Fatalf("positional resolve_claims HTTP status = %d", positional.Code)
	}
	positionalResult := txoHandlerOracleResult(t, decodeResponse(t, positional))
	if len(txoHandlerOracleItems(t, positionalResult)) != 1 {
		t.Fatalf("positional resolve_claims result = %#v", positionalResult)
	}

	for name, value := range map[string]any{"null": nil, "string": "1"} {
		t.Run("resolve claims "+name, func(t *testing.T) {
			payload := fixture.request(t, "collection_list", map[string]any{"resolve_claims": value})
			txoHandlerOracleAssertErrorNameMessage(
				t, payload, "TypeError",
				"'>' not supported between instances of '"+pythonTypeName(value)+"' and 'int'",
			)
		})
	}

	payload := fixture.request(t, "collection_list", map[string]any{
		"wallet_id": "missing", "resolve": true,
	})
	want := oracleCases["collection missing wallet"].Error
	if want == nil {
		t.Fatal("pinned collection missing-wallet case has no error")
	}
	txoHandlerOracleAssertErrorNameMessage(t, payload, want.Data.Name, want.Message)
}

func TestCollectionListResolveClaimsHydratesMembersEndToEnd(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	alpha := fixture.outputs["alpha"]
	stored, err := ledger.Database.GetTransaction(context.Background(), alpha.TXID)
	if err != nil || stored == nil {
		t.Fatalf("load alpha transaction = %#v, %v", stored, err)
	}
	transaction, err := walletpkg.ParseTransaction(stored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	ledger.SPVNetwork = &claimSearchPublicNetwork{
		searchResult: claimSearchPublicResultBase64(transaction.Hash[:], 0, 1),
		transactionRaw: map[string]string{
			alpha.TXID: hex.EncodeToString(stored.Raw),
		},
	}
	payload := collectionListClaimPayload(t, alpha.ClaimID)
	collection := txoHandlerOraclePersist(
		t, ledger, "member-collection", 610, 610, 610,
		walletpkg.NewClaimNameOutput(
			90_000_000, "MemberCollection", payload, bytes.Repeat([]byte{0x79}, 20),
		),
		alpha.Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection,
			ClaimName:  "MemberCollection",
		},
	)
	fixture.labels[collection.TXID] = "member-collection"

	result := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"resolve_claims": 1, "include_protobuf": true,
	}))
	var found map[string]any
	for _, item := range txoHandlerOracleItems(t, result) {
		if item["name"] == "MemberCollection" {
			found = item
			break
		}
	}
	if found == nil {
		t.Fatalf("member collection missing from %#v", result["items"])
	}
	claims, ok := found["claims"].([]any)
	if !ok || len(claims) != 1 {
		t.Fatalf("resolved collection claims = %#v", found["claims"])
	}
	claim, ok := claims[0].(map[string]any)
	if !ok || claim["claim_id"] != alpha.ClaimID || claim["name"] != "Alpha" ||
		claim["protobuf"] == nil {
		t.Fatalf("resolved collection member = %#v", claims[0])
	}
}

type collectionListCombinedNetwork struct {
	*resolvePublicNetwork
	searchResult any
	namedCalls   []claimSearchPublicNamedCall
}

func (network *collectionListCombinedNetwork) OneShotNamedValue(
	_ context.Context, method string, params map[string]any, restricted bool,
) (any, error) {
	cloned := make(map[string]any, len(params))
	for name, value := range params {
		cloned[name] = value
	}
	network.namedCalls = append(network.namedCalls, claimSearchPublicNamedCall{
		method: method, params: cloned, restricted: restricted,
	})
	return network.searchResult, nil
}

func TestCollectionListResolveThenMembersUsesResolvedCollection(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	selectedLedger := fixture.manager.Ledgers[fixture.accountB.Network]
	alpha := fixture.outputs["alpha"]
	payload := collectionListClaimPayload(t, alpha.ClaimID)
	local := txoHandlerOraclePersist(
		t, selectedLedger, "combined-collection", 630, 12, 12,
		walletpkg.NewClaimNameOutput(
			91_000_000, "CombinedCollection", payload, bytes.Repeat([]byte{0x7a}, 20),
		),
		fixture.outputs["b-test"].Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection,
			ClaimName:  "CombinedCollection",
		},
	)
	collectionStored, err := selectedLedger.Database.GetTransaction(
		context.Background(), local.TXID,
	)
	if err != nil || collectionStored == nil {
		t.Fatalf("load collection transaction = %#v, %v", collectionStored, err)
	}
	collectionTransaction, err := walletpkg.ParseTransaction(collectionStored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	alphaStored, err := fixture.manager.DefaultLedger().Database.GetTransaction(
		context.Background(), alpha.TXID,
	)
	if err != nil || alphaStored == nil {
		t.Fatalf("load alpha transaction = %#v, %v", alphaStored, err)
	}
	alphaTransaction, err := walletpkg.ParseTransaction(alphaStored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	network := &collectionListCombinedNetwork{
		resolvePublicNetwork: &resolvePublicNetwork{
			resolveResult: resolvePublicResultBase64(collectionTransaction.Hash[:]),
			transactionRaw: map[string]string{
				local.TXID: hex.EncodeToString(collectionStored.Raw),
				alpha.TXID: hex.EncodeToString(alphaStored.Raw),
			},
		},
		searchResult: claimSearchPublicResultBase64(alphaTransaction.Hash[:], 0, 1),
	}
	selectedLedger.SPVNetwork = network

	result := txoHandlerOracleResult(t, fixture.request(t, "collection_list", map[string]any{
		"wallet_id": fixture.walletB.ID, "account_id": fixture.accountB.ID,
		"resolve": true, "resolve_claims": 1, "include_protobuf": true,
	}))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 || items[0]["txid"] != local.TXID ||
		items[0]["short_url"] != "lbry://alpha#short" {
		t.Fatalf("combined resolved collection = %#v", items)
	}
	claims, ok := items[0]["claims"].([]any)
	if !ok || len(claims) != 1 {
		t.Fatalf("combined collection claims = %#v", items[0]["claims"])
	}
	claim, ok := claims[0].(map[string]any)
	if !ok || claim["claim_id"] != alpha.ClaimID || claim["protobuf"] == nil {
		t.Fatalf("combined collection member = %#v", claims[0])
	}
	resolveCalls := resolvePublicCallsForMethod(
		network.snapshotCalls(), walletpkg.SPVResolveMethod,
	)
	if len(resolveCalls) != 1 || !reflect.DeepEqual(resolveCalls[0].params, []any{
		"lbry://CombinedCollection#" + local.ClaimID,
	}) {
		t.Fatalf("combined collection resolve calls = %#v", resolveCalls)
	}
	if len(network.namedCalls) != 1 || !reflect.DeepEqual(
		network.namedCalls[0].params, map[string]any{"claim_ids": []string{alpha.ClaimID}},
	) {
		t.Fatalf("combined collection member calls = %#v", network.namedCalls)
	}
}

func collectionListClaimPayload(t *testing.T, claimID string) []byte {
	t.Helper()
	displayHash, err := hex.DecodeString(claimID)
	if err != nil {
		t.Fatal(err)
	}
	claimHash := make([]byte, len(displayHash))
	for index := range displayHash {
		claimHash[len(displayHash)-1-index] = displayHash[index]
	}
	reference := protowire.AppendTag(nil, 1, protowire.BytesType)
	reference = protowire.AppendBytes(reference, claimHash)
	collection := protowire.AppendTag(nil, 2, protowire.BytesType)
	collection = protowire.AppendBytes(collection, reference)
	claim := protowire.AppendTag(nil, 3, protowire.BytesType)
	claim = protowire.AppendBytes(claim, collection)
	return append([]byte{0}, claim...)
}
