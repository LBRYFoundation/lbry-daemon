package rpc

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"

	"google.golang.org/protobuf/encoding/protowire"
)

type claimSearchPublicNamedCall struct {
	method     string
	params     map[string]any
	restricted bool
}

type claimSearchPublicValueCall struct {
	method     string
	params     []any
	restricted bool
}

type claimSearchPublicNetwork struct {
	mu             sync.Mutex
	searchResult   any
	namedErr       error
	transactionRaw map[string]string
	namedCalls     []claimSearchPublicNamedCall
	valueCalls     []claimSearchPublicValueCall
}

func (*claimSearchPublicNetwork) Start(context.Context) error { return nil }
func (*claimSearchPublicNetwork) Stop(context.Context) error  { return nil }
func (*claimSearchPublicNetwork) RemoteHeight() int           { return 0 }
func (*claimSearchPublicNetwork) IsConnected() bool           { return true }
func (*claimSearchPublicNetwork) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*claimSearchPublicNetwork) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*claimSearchPublicNetwork) SetConnectedHandler(func(context.Context)) {}
func (*claimSearchPublicNetwork) SubscribeAddresses(context.Context, []string) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}
func (*claimSearchPublicNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected header RPC")
}

func (network *claimSearchPublicNetwork) OneShotNamedValue(
	_ context.Context, method string, params map[string]any, restricted bool,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	cloned := make(map[string]any, len(params))
	for name, value := range params {
		cloned[name] = value
	}
	network.namedCalls = append(network.namedCalls, claimSearchPublicNamedCall{
		method: method, params: cloned, restricted: restricted,
	})
	return network.searchResult, network.namedErr
}

func (network *claimSearchPublicNetwork) RetriableValue(
	_ context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.valueCalls = append(network.valueCalls, claimSearchPublicValueCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	if method != walletpkg.SPVTransactionBatchMethod {
		return nil, errors.New("unexpected retriable method: " + method)
	}
	result := make(map[string]any, len(params))
	for _, value := range params {
		txid, ok := value.(string)
		if !ok {
			return nil, errors.New("transaction batch parameter is not a string")
		}
		raw, exists := network.transactionRaw[txid]
		if !exists {
			return nil, errors.New("unexpected transaction batch id: " + txid)
		}
		result[txid] = []any{raw, nil}
	}
	return result, nil
}

func (network *claimSearchPublicNetwork) snapshotCalls() ([]claimSearchPublicNamedCall, []claimSearchPublicValueCall) {
	network.mu.Lock()
	defer network.mu.Unlock()
	named := append([]claimSearchPublicNamedCall(nil), network.namedCalls...)
	values := append([]claimSearchPublicValueCall(nil), network.valueCalls...)
	return named, values
}

func TestPublicClaimSearchRoutesDefaultLedgerAndSelectedWalletEndToEnd(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	defaultLedger := fixture.manager.DefaultLedger()
	if defaultLedger == nil {
		t.Fatal("fixture has no default ledger")
	}
	selectedLedger := fixture.manager.Ledgers[fixture.accountB.Network]
	if selectedLedger == nil || selectedLedger == defaultLedger {
		t.Fatalf("selected account ledger = %p, default = %p", selectedLedger, defaultLedger)
	}

	alpha := fixture.outputs["alpha"]
	rows, err := defaultLedger.Database.ListTransactions(context.Background(), ledgerdb.TransactionQuery{
		TXID: &alpha.TXID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("alpha transaction rows = %d, want 1", len(rows))
	}
	transaction, err := walletpkg.ParseTransaction(rows[0].Raw)
	if err != nil {
		t.Fatal(err)
	}

	defaultNetwork := &claimSearchPublicNetwork{
		searchResult: claimSearchPublicResultBase64(transaction.Hash[:], 3, 7),
		transactionRaw: map[string]string{
			alpha.TXID: hex.EncodeToString(rows[0].Raw),
		},
	}
	selectedNetwork := &claimSearchPublicNetwork{namedErr: errors.New("selected ledger must not be queried")}
	defaultLedger.SPVNetwork = defaultNetwork
	selectedLedger.SPVNetwork = selectedNetwork

	payload := fixture.request(t, "claim_search", map[string]any{
		"text":                 "fixture",
		"wallet_id":            fixture.walletB.ID,
		"page":                 2,
		"page_size":            3,
		"include_is_my_output": true,
	})
	result := txoHandlerOracleResult(t, payload)
	if result["page"] != json.Number("2") || result["page_size"] != json.Number("3") ||
		result["total_items"] != json.Number("7") || result["total_pages"] != json.Number("3") {
		t.Fatalf("claim_search pagination = %#v", result)
	}
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 {
		t.Fatalf("claim_search items = %#v", result["items"])
	}
	item := items[0]
	if item["txid"] != alpha.TXID || item["claim_id"] != alpha.ClaimID ||
		item["is_my_output"] != false || item["short_url"] != "lbry://alpha#short" {
		t.Fatalf("selected-wallet annotated item = %#v", item)
	}
	blocked, ok := result["blocked"].(map[string]any)
	if !ok || blocked["total"] != json.Number("0") ||
		!reflect.DeepEqual(blocked["channels"], []any{}) {
		t.Fatalf("claim_search blocked = %#v", result["blocked"])
	}

	named, values := defaultNetwork.snapshotCalls()
	wantNamed := []claimSearchPublicNamedCall{{
		method: walletpkg.SPVClaimSearchMethod,
		params: map[string]any{
			"text": "fixture", "offset": json.Number("3"), "limit": json.Number("3"),
		},
		restricted: false,
	}}
	if !reflect.DeepEqual(named, wantNamed) {
		t.Fatalf("default-ledger named calls = %#v, want %#v", named, wantNamed)
	}
	wantValues := []claimSearchPublicValueCall{{
		method: walletpkg.SPVTransactionBatchMethod,
		params: []any{alpha.TXID}, restricted: true,
	}}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatalf("default-ledger value calls = %#v, want %#v", values, wantValues)
	}
	if selectedNamed, selectedValues := selectedNetwork.snapshotCalls(); len(selectedNamed) != 0 || len(selectedValues) != 0 {
		t.Fatalf("selected-ledger calls = named %#v, values %#v", selectedNamed, selectedValues)
	}
}

func TestPublicClaimSearchChecksWalletBeforePreprocessing(t *testing.T) {
	providerCalls := 0
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		providerCalls++
		return nil
	}))
	response := performRequest(
		server,
		http.MethodPost,
		"/",
		`{"method":"claim_search","params":{"page":"not-a-number"}}`,
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("claim_search HTTP status = %d, body %s", response.Code, response.Body.String())
	}
	txoHandlerOracleAssertErrorNameMessage(
		t,
		decodeResponse(t, response),
		"ComponentsNotStartedError",
		`the following required components have not yet started: ["wallet"]`,
	)
	if providerCalls != 1 {
		t.Fatalf("wallet manager provider calls = %d, want 1", providerCalls)
	}
}

func TestPublicClaimSearchAbsentProviderAndPositionalArgumentsMatchDecoratorOrder(t *testing.T) {
	withoutProvider := performRequest(
		CreateServer(), http.MethodPost, "/",
		`{"method":"claim_search","params":[[1],{}]}`, nil,
	)
	txoHandlerOracleAssertErrorNameMessage(
		t, decodeResponse(t, withoutProvider), "ComponentsNotStartedError",
		`the following required components have not yet started: ["wallet"]`,
	)

	fixture := newTXOHandlerOracleFixture(t)
	withProvider := performRequest(
		fixture.server, http.MethodPost, "/",
		`{"method":"claim_search","params":[[1],{}]}`, nil,
	)
	txoHandlerOracleAssertErrorNameMessage(
		t, decodeResponse(t, withProvider), "TypeError",
		"Daemon.jsonrpc_claim_search() takes 1 positional argument but 2 were given",
	)
}

func TestPublicClaimSearchZeroPageSizeTotalsFailsAfterHubCall(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	network := &claimSearchPublicNetwork{
		searchResult: claimSearchPublicEmptyResultBase64(7),
	}
	fixture.manager.DefaultLedger().SPVNetwork = network

	payload := fixture.request(t, "claim_search", map[string]any{"page_size": 0})
	txoHandlerOracleAssertErrorNameMessage(
		t, payload, "ZeroDivisionError", "division by zero",
	)

	named, values := network.snapshotCalls()
	wantNamed := []claimSearchPublicNamedCall{{
		method: walletpkg.SPVClaimSearchMethod,
		params: map[string]any{
			"offset": json.Number("0"), "limit": json.Number("0"),
		},
		restricted: false,
	}}
	if !reflect.DeepEqual(named, wantNamed) {
		t.Fatalf("named calls = %#v, want %#v", named, wantNamed)
	}
	if len(values) != 0 {
		t.Fatalf("transaction calls = %#v, want none", values)
	}
}

func TestPublicClaimSearchZeroPageSizeNoTotalsSucceeds(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	network := &claimSearchPublicNetwork{
		searchResult: claimSearchPublicEmptyResultBase64(7),
	}
	fixture.manager.DefaultLedger().SPVNetwork = network

	result := txoHandlerOracleResult(t, fixture.request(t, "claim_search", map[string]any{
		"page_size": 0,
		"no_totals": true,
	}))
	if result["page"] != json.Number("1") || result["page_size"] != json.Number("0") {
		t.Fatalf("claim_search pagination = %#v", result)
	}
	if items := txoHandlerOracleItems(t, result); len(items) != 0 {
		t.Fatalf("claim_search items = %#v, want none", items)
	}
	if _, exists := result["total_items"]; exists {
		t.Fatalf("no_totals result includes total_items: %#v", result)
	}
	if _, exists := result["total_pages"]; exists {
		t.Fatalf("no_totals result includes total_pages: %#v", result)
	}

	named, values := network.snapshotCalls()
	wantNamed := []claimSearchPublicNamedCall{{
		method: walletpkg.SPVClaimSearchMethod,
		params: map[string]any{
			"no_totals": true, "offset": json.Number("0"), "limit": json.Number("0"),
		},
		restricted: false,
	}}
	if !reflect.DeepEqual(named, wantNamed) {
		t.Fatalf("named calls = %#v, want %#v", named, wantNamed)
	}
	if len(values) != 0 {
		t.Fatalf("transaction calls = %#v, want none", values)
	}
}

func TestPublicClaimSearchMalformedBase64PreservesPythonError(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	network := &claimSearchPublicNetwork{searchResult: "YQ"}
	fixture.manager.DefaultLedger().SPVNetwork = network

	payload := fixture.request(t, "claim_search", nil)
	txoHandlerOracleAssertErrorNameMessage(t, payload, "Error", "Incorrect padding")
	named, values := network.snapshotCalls()
	if len(named) != 1 || named[0].method != walletpkg.SPVClaimSearchMethod {
		t.Fatalf("named calls = %#v, want one claim_search call", named)
	}
	if len(values) != 0 {
		t.Fatalf("transaction calls = %#v, want none", values)
	}
}

func TestPublicClaimSearchTotalValidationPrecedesGenericEncodingFailure(t *testing.T) {
	fixture, network := newClaimSearchPublicSuccessFixture(t)
	alpha := fixture.outputs["alpha"]
	raw, err := hex.DecodeString(network.transactionRaw[alpha.TXID])
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := walletpkg.ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	network.searchResult = claimSearchPublicCyclicResultBase64(transaction.Hash[:], 1)

	zeroPayload := fixture.request(t, "claim_search", map[string]any{"page_size": 0})
	txoHandlerOracleAssertErrorNameMessage(
		t, zeroPayload, "ZeroDivisionError", "division by zero",
	)

	fixture, network = newClaimSearchPublicSuccessFixture(t)
	alpha = fixture.outputs["alpha"]
	raw, err = hex.DecodeString(network.transactionRaw[alpha.TXID])
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = walletpkg.ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	network.searchResult = claimSearchPublicCyclicResultBase64(transaction.Hash[:], 1)
	body, err := json.Marshal(map[string]any{"method": "claim_search"})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.server, http.MethodPost, "/", string(body), nil)
	errorObject := assertRPCError(
		t, response, json.Number("-32500"),
		"After successfully executing the command, failed to encode result for JSON RPC response.",
	)
	data, ok := errorObject["data"].(map[string]any)
	if !ok {
		t.Fatalf("encoding error data = %#v", errorObject["data"])
	}
	if _, ok := data["traceback"].(string); !ok {
		t.Fatalf("encoding traceback = %#v", data["traceback"])
	}
}

func TestPublicClaimSearchTopLevelIncludeProtobufIsTransportOnly(t *testing.T) {
	fixture, network := newClaimSearchPublicSuccessFixture(t)
	result := txoHandlerOracleResult(t, fixture.request(t, "claim_search", map[string]any{
		"include_protobuf": true,
	}))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 || items[0]["protobuf"] != "000a00420746697874757265" {
		t.Fatalf("claim_search protobuf items = %#v", items)
	}

	named, values := network.snapshotCalls()
	wantNamed := []claimSearchPublicNamedCall{{
		method: walletpkg.SPVClaimSearchMethod,
		params: map[string]any{
			"offset": json.Number("0"), "limit": json.Number("20"),
		},
		restricted: false,
	}}
	if !reflect.DeepEqual(named, wantNamed) {
		t.Fatalf("named calls = %#v, want %#v", named, wantNamed)
	}
	if len(values) != 1 || values[0].method != walletpkg.SPVTransactionBatchMethod {
		t.Fatalf("transaction calls = %#v, want one batch", values)
	}
}

func TestPublicClaimSearchLegacyWrappedIncludeProtobufIsHubOnly(t *testing.T) {
	fixture, network := newClaimSearchPublicSuccessFixture(t)
	body, err := json.Marshal(map[string]any{
		"method": "claim_search",
		"params": []any{map[string]any{"include_protobuf": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.server, http.MethodPost, "/", string(body), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("claim_search HTTP status = %d, body %s", response.Code, response.Body.String())
	}
	result := txoHandlerOracleResult(t, decodeResponse(t, response))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 {
		t.Fatalf("claim_search items = %#v", items)
	}
	if _, exists := items[0]["protobuf"]; exists {
		t.Fatalf("legacy wrapped claim_search item includes protobuf: %#v", items[0])
	}

	named, values := network.snapshotCalls()
	wantNamed := []claimSearchPublicNamedCall{{
		method: walletpkg.SPVClaimSearchMethod,
		params: map[string]any{
			"include_protobuf": true,
			"offset":           json.Number("0"),
			"limit":            json.Number("20"),
		},
		restricted: false,
	}}
	if !reflect.DeepEqual(named, wantNamed) {
		t.Fatalf("named calls = %#v, want %#v", named, wantNamed)
	}
	if len(values) != 1 || values[0].method != walletpkg.SPVTransactionBatchMethod {
		t.Fatalf("transaction calls = %#v, want one batch", values)
	}
}

func newClaimSearchPublicSuccessFixture(
	t *testing.T,
) (*txoHandlerOracleFixture, *claimSearchPublicNetwork) {
	t.Helper()
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	if ledger == nil {
		t.Fatal("fixture has no default ledger")
	}
	alpha := fixture.outputs["alpha"]
	rows, err := ledger.Database.ListTransactions(context.Background(), ledgerdb.TransactionQuery{
		TXID: &alpha.TXID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("alpha transaction rows = %d, want 1", len(rows))
	}
	transaction, err := walletpkg.ParseTransaction(rows[0].Raw)
	if err != nil {
		t.Fatal(err)
	}
	network := &claimSearchPublicNetwork{
		searchResult: claimSearchPublicResultBase64(transaction.Hash[:], 0, 1),
		transactionRaw: map[string]string{
			alpha.TXID: hex.EncodeToString(rows[0].Raw),
		},
	}
	ledger.SPVNetwork = network
	return fixture, network
}

func claimSearchPublicResultBase64(transactionHash []byte, offset, total uint64) string {
	claim := protowire.AppendTag(nil, 3, protowire.BytesType)
	claim = protowire.AppendString(claim, "alpha#short")
	claim = protowire.AppendTag(claim, 4, protowire.BytesType)
	claim = protowire.AppendString(claim, "alpha#canonical")

	output := protowire.AppendTag(nil, 1, protowire.BytesType)
	output = protowire.AppendBytes(output, transactionHash)
	output = protowire.AppendTag(output, 7, protowire.BytesType)
	output = protowire.AppendBytes(output, claim)

	page := protowire.AppendTag(nil, 1, protowire.BytesType)
	page = protowire.AppendBytes(page, output)
	page = protowire.AppendTag(page, 3, protowire.VarintType)
	page = protowire.AppendVarint(page, total)
	page = protowire.AppendTag(page, 4, protowire.VarintType)
	page = protowire.AppendVarint(page, offset)
	return base64.StdEncoding.EncodeToString(page)
}

func claimSearchPublicCyclicResultBase64(transactionHash []byte, total uint64) string {
	repost := protowire.AppendTag(nil, 1, protowire.BytesType)
	repost = protowire.AppendBytes(repost, transactionHash)
	claim := protowire.AppendTag(nil, 2, protowire.BytesType)
	claim = protowire.AppendBytes(claim, repost)

	output := protowire.AppendTag(nil, 1, protowire.BytesType)
	output = protowire.AppendBytes(output, transactionHash)
	output = protowire.AppendTag(output, 7, protowire.BytesType)
	output = protowire.AppendBytes(output, claim)

	page := protowire.AppendTag(nil, 1, protowire.BytesType)
	page = protowire.AppendBytes(page, output)
	page = protowire.AppendTag(page, 3, protowire.VarintType)
	page = protowire.AppendVarint(page, total)
	return base64.StdEncoding.EncodeToString(page)
}

func claimSearchPublicEmptyResultBase64(total uint64) string {
	page := protowire.AppendTag(nil, 3, protowire.VarintType)
	page = protowire.AppendVarint(page, total)
	return base64.StdEncoding.EncodeToString(page)
}

var _ walletpkg.LedgerSPVNetwork = (*claimSearchPublicNetwork)(nil)
var _ walletpkg.LedgerSPVAddressSource = (*claimSearchPublicNetwork)(nil)
var _ walletpkg.LedgerSPVNamedValueSource = (*claimSearchPublicNetwork)(nil)
