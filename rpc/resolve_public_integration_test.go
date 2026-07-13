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

	daemonconfig "lbry/daemon/config"
	walletpkg "lbry/daemon/wallet"
	spvpkg "lbry/daemon/wallet/spv"

	"google.golang.org/protobuf/encoding/protowire"
)

type resolvePublicCall struct {
	method     string
	params     []any
	restricted bool
}

type resolvePublicNetwork struct {
	mu             sync.Mutex
	resolveResult  any
	resolveErr     error
	transactionRaw map[string]string
	calls          []resolvePublicCall
}

func (*resolvePublicNetwork) Start(context.Context) error { return nil }
func (*resolvePublicNetwork) Stop(context.Context) error  { return nil }
func (*resolvePublicNetwork) RemoteHeight() int           { return 0 }
func (*resolvePublicNetwork) IsConnected() bool           { return true }
func (*resolvePublicNetwork) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*resolvePublicNetwork) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*resolvePublicNetwork) SetConnectedHandler(func(context.Context)) {}
func (*resolvePublicNetwork) SubscribeAddresses(context.Context, []string) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}
func (*resolvePublicNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected header RPC")
}

func (network *resolvePublicNetwork) RetriableValue(
	_ context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.calls = append(network.calls, resolvePublicCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	switch method {
	case walletpkg.SPVResolveMethod:
		return network.resolveResult, network.resolveErr
	case walletpkg.SPVTransactionBatchMethod:
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
	default:
		return nil, errors.New("unexpected retriable method: " + method)
	}
}

func (network *resolvePublicNetwork) snapshotCalls() []resolvePublicCall {
	network.mu.Lock()
	defer network.mu.Unlock()
	calls := make([]resolvePublicCall, len(network.calls))
	for index, call := range network.calls {
		calls[index] = resolvePublicCall{
			method: call.method, params: append([]any(nil), call.params...), restricted: call.restricted,
		}
	}
	return calls
}

func TestPublicResolveScalarUsesDefaultLedgerAndSelectedWalletAnnotations(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	defaultLedger := fixture.manager.DefaultLedger()
	selectedLedger := fixture.manager.Ledgers[fixture.accountB.Network]
	if defaultLedger == nil || selectedLedger == nil || selectedLedger == defaultLedger {
		t.Fatalf("resolve ledgers = default %p selected %p", defaultLedger, selectedLedger)
	}
	selectedNetwork := &resolvePublicNetwork{}
	selectedLedger.SPVNetwork = selectedNetwork

	defaultResult := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
		"urls": "alpha", "include_is_my_output": true,
	}), "alpha")
	if defaultResult["txid"] != fixture.outputs["alpha"].TXID ||
		defaultResult["claim_id"] != fixture.outputs["alpha"].ClaimID ||
		defaultResult["is_my_output"] != true ||
		defaultResult["short_url"] != "lbry://alpha#short" {
		t.Fatalf("default-wallet resolve output = %#v", defaultResult)
	}

	selectedResult := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
		"urls": "alpha", "wallet_id": fixture.walletB.ID, "include_is_my_output": true,
	}), "alpha")
	if selectedResult["txid"] != fixture.outputs["alpha"].TXID ||
		selectedResult["is_my_output"] != false {
		t.Fatalf("selected-wallet resolve output = %#v", selectedResult)
	}

	resolveCalls := resolvePublicCallsForMethod(network.snapshotCalls(), walletpkg.SPVResolveMethod)
	wantResolveCalls := []resolvePublicCall{
		{method: walletpkg.SPVResolveMethod, params: []any{"alpha"}, restricted: false},
		{method: walletpkg.SPVResolveMethod, params: []any{"alpha"}, restricted: false},
	}
	if !reflect.DeepEqual(resolveCalls, wantResolveCalls) {
		t.Fatalf("default-ledger resolve calls = %#v, want %#v", resolveCalls, wantResolveCalls)
	}
	if calls := selectedNetwork.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("selected account ledger was queried: %#v", calls)
	}
}

func TestPublicResolveFiltersInvalidURLsAndDeduplicatesLocally(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	payload := fixture.request(t, "resolve", map[string]any{
		"urls": []any{"not a url", "alpha", "alpha"},
	})
	result := txoHandlerOracleResult(t, payload)
	if len(result) != 2 {
		t.Fatalf("resolve result keys = %#v, want invalid and alpha", result)
	}
	invalid, ok := result["not a url"].(map[string]any)
	if !ok || invalid["error"] != "not a url is not a valid url" {
		t.Fatalf("invalid URL result = %#v", result["not a url"])
	}
	alpha, ok := result["alpha"].(map[string]any)
	if !ok || alpha["txid"] != fixture.outputs["alpha"].TXID {
		t.Fatalf("valid URL result = %#v", result["alpha"])
	}

	resolveCalls := resolvePublicCallsForMethod(network.snapshotCalls(), walletpkg.SPVResolveMethod)
	want := []resolvePublicCall{{
		method: walletpkg.SPVResolveMethod, params: []any{"alpha"}, restricted: false,
	}}
	if !reflect.DeepEqual(resolveCalls, want) {
		t.Fatalf("deduplicated resolve calls = %#v, want %#v", resolveCalls, want)
	}
}

func TestPublicResolveAbsentChannelBecomesInvalidResult(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	const url = "lbry://@chan/alpha"
	result := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
		"urls": url,
	}), url)
	want := map[string]any{"error": map[string]any{
		"name": "INVALID", "text": url + " has invalid channel signature",
	}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("absent-channel resolve result = %#v, want %#v", result, want)
	}
	resolveCalls := resolvePublicCallsForMethod(network.snapshotCalls(), walletpkg.SPVResolveMethod)
	if len(resolveCalls) != 1 || !reflect.DeepEqual(resolveCalls[0].params, []any{url}) {
		t.Fatalf("absent-channel resolve calls = %#v", resolveCalls)
	}
}

func TestPublicResolveUnknownKeywordRequiresAtLeastOneValidURL(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	valid := fixture.request(t, "resolve", map[string]any{
		"urls": "alpha", "unknown_option": true,
	})
	txoHandlerOracleAssertErrorNameMessage(
		t, valid, "TypeError",
		"Ledger._inflate_outputs() got an unexpected keyword argument 'unknown_option'",
	)
	if calls := network.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("unknown valid resolve reached Hub: %#v", calls)
	}

	allInvalid := txoHandlerOracleResult(t, fixture.request(t, "resolve", map[string]any{
		"urls": []any{"not a url", "also not a url"}, "unknown_option": true,
	}))
	for _, url := range []string{"not a url", "also not a url"} {
		entry, ok := allInvalid[url].(map[string]any)
		if !ok || entry["error"] != url+" is not a valid url" {
			t.Fatalf("all-invalid result[%q] = %#v", url, allInvalid[url])
		}
	}
	if calls := network.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("all-invalid resolve reached Hub: %#v", calls)
	}
}

func TestPublicResolveResultCountMismatchIsGlobalAssertion(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	network.resolveResult = ""
	payload := fixture.request(t, "resolve", map[string]any{"urls": "alpha"})
	txoHandlerOracleAssertErrorNameMessage(
		t, payload, "AssertionError",
		"Mismatch between urls requested for resolve and responses received.",
	)
	calls := network.snapshotCalls()
	want := []resolvePublicCall{{
		method: walletpkg.SPVResolveMethod, params: []any{"alpha"}, restricted: false,
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("count-mismatch calls = %#v, want %#v", calls, want)
	}
}

func TestPublicResolveStoppedNetworkAbortsResponse(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	network.resolveErr = spvpkg.ErrNetworkStopped
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		performRequest(
			fixture.server, http.MethodPost, "/",
			`{"method":"resolve","params":{"urls":"alpha"}}`, nil,
		)
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("stopped resolve panic = %#v, want http.ErrAbortHandler", recovered)
	}

	fixture, network = newResolvePublicSuccessFixture(t, nil)
	network.resolveErr = spvpkg.ErrConnection
	payload := fixture.request(t, "resolve", map[string]any{"urls": "alpha"})
	txoHandlerOracleAssertErrorNameMessage(
		t, payload, "ConnectionError",
		"Attempting to send rpc request when connection is not available.",
	)
}

func TestPublicResolveComponentAndWalletErrorsPrecedeURLValidation(t *testing.T) {
	withoutProvider := performRequest(
		CreateServer(), http.MethodPost, "/",
		`{"method":"resolve","params":{"urls":"not a url","wallet_id":"missing","self":null}}`, nil,
	)
	txoHandlerOracleAssertErrorNameMessage(
		t, decodeResponse(t, withoutProvider), "ComponentsNotStartedError",
		`the following required components have not yet started: ["wallet"]`,
	)

	fixture := newTXOHandlerOracleFixture(t)
	missingWallet := fixture.request(t, "resolve", map[string]any{
		"urls": "not a url", "wallet_id": "missing",
	})
	txoHandlerOracleAssertErrorNameMessage(
		t, missingWallet, "WalletNotLoadedError", "Wallet missing is not loaded.",
	)

	duplicateSelf := fixture.request(t, "resolve", map[string]any{
		"urls": "not a url", "wallet_id": "missing", "self": nil,
	})
	txoHandlerOracleAssertErrorNameMessage(
		t, duplicateSelf, "TypeError",
		"Daemon.jsonrpc_resolve() got multiple values for argument 'self'",
	)
}

func TestPublicResolveTopLevelAndWrappedIncludeProtobufRemainDistinct(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	result := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
		"urls": "alpha", "include_protobuf": true,
	}), "alpha")
	if result["protobuf"] != "000a00420746697874757265" {
		t.Fatalf("top-level include_protobuf output = %#v", result)
	}
	if resolveCalls := resolvePublicCallsForMethod(
		network.snapshotCalls(), walletpkg.SPVResolveMethod,
	); len(resolveCalls) != 1 {
		t.Fatalf("top-level include_protobuf resolve calls = %#v", resolveCalls)
	}

	fixture, network = newResolvePublicSuccessFixture(t, nil)
	body, err := json.Marshal(map[string]any{
		"method": "resolve",
		"params": []any{map[string]any{"urls": "alpha", "include_protobuf": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.server, http.MethodPost, "/", string(body), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("wrapped resolve HTTP status = %d, body %s", response.Code, response.Body.String())
	}
	txoHandlerOracleAssertErrorNameMessage(
		t, decodeResponse(t, response), "TypeError",
		"Ledger._inflate_outputs() got an unexpected keyword argument 'include_protobuf'",
	)
	if calls := network.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("wrapped include_protobuf reached Hub: %#v", calls)
	}
}

func TestPublicResolveSaverCallbackAndErrorSuppression(t *testing.T) {
	t.Run("saves resolved outputs", func(t *testing.T) {
		saver := &resolvePublicSaver{}
		fixture, _ := newResolvePublicSuccessFixture(t, saver)
		result := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
			"urls": "alpha",
		}), "alpha")
		if result["txid"] != fixture.outputs["alpha"].TXID {
			t.Fatalf("saved resolve result = %#v", result)
		}
		calls := saver.snapshotCalls()
		if len(calls) != 1 || calls[0].ledger != fixture.manager.DefaultLedger() ||
			len(calls[0].outputs) != 1 || string(calls[0].outputs[0].Script.ClaimName) != "Alpha" {
			t.Fatalf("resolved saver calls = %#v", calls)
		}
	})

	t.Run("suppresses DecodeError", func(t *testing.T) {
		saver := &resolvePublicSaver{err: resolvePublicNamedError{
			name: "DecodeError", message: "fixture decode failure",
		}}
		fixture, _ := newResolvePublicSuccessFixture(t, saver)
		result := resolvePublicResultEntry(t, fixture.request(t, "resolve", map[string]any{
			"urls": "alpha",
		}), "alpha")
		if result["txid"] != fixture.outputs["alpha"].TXID || len(saver.snapshotCalls()) != 1 {
			t.Fatalf("DecodeError-suppressed resolve = %#v, calls %#v", result, saver.snapshotCalls())
		}
	})

	t.Run("propagates other errors", func(t *testing.T) {
		saver := &resolvePublicSaver{err: resolvePublicNamedError{
			name: "StorageFailure", message: "offline storage failure",
		}}
		fixture, _ := newResolvePublicSuccessFixture(t, saver)
		payload := fixture.request(t, "resolve", map[string]any{"urls": "alpha"})
		txoHandlerOracleAssertErrorNameMessage(
			t, payload, "StorageFailure", "offline storage failure",
		)
		if len(saver.snapshotCalls()) != 1 {
			t.Fatalf("storage failure saver calls = %#v", saver.snapshotCalls())
		}
	})
}

func TestResolveSaverSettingIsReadAfterResolution(t *testing.T) {
	settings := daemonconfig.NewMemory()
	saver := &resolvePublicSaver{}
	server := CreateServer(
		WithSettingsStore(settings), WithResolvedClaimSaver(saver),
	)
	callback := server.resolveBeforeEncoding(context.Background(), &walletpkg.Ledger{})
	if callback == nil {
		t.Fatal("configured resolve saver callback = nil")
	}
	if _, err := settings.Set("save_resolved_claims", false); err != nil {
		t.Fatal(err)
	}
	if err := callback([]*walletpkg.TransactionOutput{}); err != nil {
		t.Fatal(err)
	}
	if calls := saver.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("disabled late resolve saver calls = %#v", calls)
	}
	if _, err := settings.Set("save_resolved_claims", true); err != nil {
		t.Fatal(err)
	}
	if err := callback([]*walletpkg.TransactionOutput{}); err != nil {
		t.Fatal(err)
	}
	if calls := saver.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("enabled late resolve saver calls = %#v", calls)
	}
}

type resolvePublicSaverCall struct {
	ledger  *walletpkg.Ledger
	outputs []*walletpkg.TransactionOutput
}

type resolvePublicSaver struct {
	mu    sync.Mutex
	err   error
	calls []resolvePublicSaverCall
}

func (saver *resolvePublicSaver) SaveResolvedClaims(
	_ context.Context, ledger *walletpkg.Ledger, outputs []*walletpkg.TransactionOutput,
) error {
	saver.mu.Lock()
	defer saver.mu.Unlock()
	saver.calls = append(saver.calls, resolvePublicSaverCall{
		ledger: ledger, outputs: append([]*walletpkg.TransactionOutput(nil), outputs...),
	})
	return saver.err
}

func (saver *resolvePublicSaver) snapshotCalls() []resolvePublicSaverCall {
	saver.mu.Lock()
	defer saver.mu.Unlock()
	calls := make([]resolvePublicSaverCall, len(saver.calls))
	for index, call := range saver.calls {
		calls[index] = resolvePublicSaverCall{
			ledger:  call.ledger,
			outputs: append([]*walletpkg.TransactionOutput(nil), call.outputs...),
		}
	}
	return calls
}

type resolvePublicNamedError struct {
	name    string
	message string
}

func (err resolvePublicNamedError) Error() string           { return err.message }
func (err resolvePublicNamedError) PythonErrorName() string { return err.name }

func newResolvePublicSuccessFixture(
	t *testing.T, saver ResolvedClaimSaver,
) (*txoHandlerOracleFixture, *resolvePublicNetwork) {
	t.Helper()
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	if ledger == nil {
		t.Fatal("fixture has no default ledger")
	}
	alpha := fixture.outputs["alpha"]
	stored, err := ledger.Database.GetTransaction(context.Background(), alpha.TXID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatalf("alpha transaction %s is not persisted", alpha.TXID)
	}
	transaction, err := walletpkg.ParseTransaction(stored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	network := &resolvePublicNetwork{
		resolveResult: resolvePublicResultBase64(transaction.Hash[:]),
		transactionRaw: map[string]string{
			alpha.TXID: hex.EncodeToString(stored.Raw),
		},
	}
	ledger.SPVNetwork = network
	options := []ServerOption{WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return fixture.manager
	})}
	if saver != nil {
		options = append(options, WithResolvedClaimSaver(saver))
	}
	fixture.server = CreateServer(options...)
	return fixture, network
}

func resolvePublicResultBase64(transactionHash []byte) string {
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
	page = protowire.AppendVarint(page, 1)
	return base64.StdEncoding.EncodeToString(page)
}

func resolvePublicResultEntry(
	t *testing.T, payload map[string]any, url string,
) map[string]any {
	t.Helper()
	result := txoHandlerOracleResult(t, payload)
	entry, ok := result[url].(map[string]any)
	if !ok {
		t.Fatalf("resolve result[%q] = %#v", url, result[url])
	}
	return entry
}

func resolvePublicCallsForMethod(calls []resolvePublicCall, method string) []resolvePublicCall {
	filtered := make([]resolvePublicCall, 0, len(calls))
	for _, call := range calls {
		if call.method == method {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

var _ walletpkg.LedgerSPVNetwork = (*resolvePublicNetwork)(nil)
var _ walletpkg.LedgerSPVAddressSource = (*resolvePublicNetwork)(nil)
