package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
	spvpkg "lbry/daemon/wallet/spv"
)

func TestTXOOutputQueryNormalizesPinnedFiltersAndPrecedence(t *testing.T) {
	normalized := normalizedRPCParams{
		kwargs: map[string]any{
			"type": []any{"stream", "support"}, "txid": []any{"one", "two"},
			"claim_id": "claim", "channel_id": "channel",
			"not_channel_id": []any{"blocked-a", "blocked-b"}, "name": "name",
			"reposted_claim_id": "repost", "is_spent": true, "is_not_spent": true,
			"has_source": true, "has_no_source": true,
			"is_my_input_or_output": true, "is_my_input": true,
			"is_not_my_input": true, "is_my_output": true, "is_not_my_output": true,
			"exclude_internal_transfers": true,
		},
	}
	normalized.named = normalized.kwargs
	query, err := txoOutputQuery(normalized, txoListParameterNames)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(query.Types, []int64{1, 3}) ||
		!reflect.DeepEqual(query.TXIDs, []string{"one", "two"}) ||
		!reflect.DeepEqual(query.ClaimIDs, []string{"claim"}) ||
		!reflect.DeepEqual(query.ChannelIDs, []string{"channel"}) ||
		!reflect.DeepEqual(query.NotChannelIDs, []string{"blocked-a", "blocked-b"}) ||
		!reflect.DeepEqual(query.ClaimNames, []string{"name"}) ||
		!reflect.DeepEqual(query.RepostedClaimIDs, []string{"repost"}) {
		t.Fatalf("normalized TXO query = %#v", query)
	}
	if query.IsSpent == nil || !*query.IsSpent || query.HasSource == nil || !*query.HasSource ||
		!query.IsMyInputOrOutput || !query.SkipAccountOutputConstraint ||
		query.IsMyInput != nil || query.IsMyOutput != nil || !query.ExcludeInternalTransfers {
		t.Fatalf("TXO precedence = %#v", query)
	}
}

func TestTXOOutputQueryRequiresExactTrueForOwnershipFlags(t *testing.T) {
	normalized := normalizedRPCParams{
		kwargs: map[string]any{
			"is_my_input_or_output": 1, "is_my_input": 1, "is_not_my_input": 1,
			"is_my_output": 1, "is_not_my_output": 1,
			"is_not_spent": "yes", "has_no_source": []any{true},
		},
	}
	normalized.named = normalized.kwargs
	query, err := txoOutputQuery(normalized, txoSumParameterNames)
	if err != nil {
		t.Fatal(err)
	}
	if query.IsMyInputOrOutput || query.IsMyInput != nil || query.IsMyOutput != nil ||
		query.IsSpent == nil || *query.IsSpent || query.HasSource == nil || *query.HasSource {
		t.Fatalf("exact ownership booleans = %#v", query)
	}
}

func TestTXOOutputQueryEmptyListsErrorsAndOrders(t *testing.T) {
	empty := normalizedRPCParams{
		kwargs: map[string]any{
			"type": []any{}, "txid": []any{}, "claim_id": []any{},
		},
	}
	empty.named = empty.kwargs
	query, err := txoOutputQuery(empty, txoListParameterNames)
	if err != nil || query.Types != nil || query.TXIDs != nil || query.ClaimIDs != nil {
		t.Fatalf("empty TXO lists = %#v, %v", query, err)
	}

	invalidType := normalizedRPCParams{
		kwargs: map[string]any{"type": "video"}, named: map[string]any{"type": "video"},
	}
	_, err = txoOutputQuery(invalidType, txoListParameterNames)
	var application transactionListApplicationError
	if !errors.As(err, &application) || application.name != "KeyError" || application.message != "'video'" {
		t.Fatalf("invalid TXO type error = %#v", err)
	}

	unknown := normalizedRPCParams{
		kwargs: map[string]any{"height": 10}, named: map[string]any{"height": 10},
	}
	_, err = txoOutputQuery(unknown, txoListParameterNames)
	if !errors.As(err, &application) || application.name != "TypeError" ||
		application.message != "Daemon._constrain_txo_from_kwargs() got an unexpected keyword argument 'height'" {
		t.Fatalf("unknown TXO filter error = %#v", err)
	}

	wantOrders := map[string]ledgerdb.OutputOrder{
		"name": ledgerdb.OutputOrderName, "height": ledgerdb.OutputOrderHeight,
		"amount": ledgerdb.OutputOrderAmount, "none": ledgerdb.OutputOrderNone,
	}
	for value, want := range wantOrders {
		got, err := txoOutputOrder(value)
		if err != nil || got != want {
			t.Fatalf("TXO order %q = %d, %v, want %d", value, got, err, want)
		}
	}
	if _, err := txoOutputOrder("txid"); !errors.As(err, &application) ||
		application.name != "ValueError" || application.message != "'txid' is not a valid --order_by value." {
		t.Fatalf("invalid TXO order error = %#v", err)
	}
}

func TestTXOResolveRunsAfterLegacyValidation(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	result := txoHandlerOracleResult(t, fixture.request(t, "txo_list", map[string]any{
		"resolve": true, "type": "stream", "name": "Alpha",
	}))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 || items[0]["short_url"] != "lbry://alpha#short" ||
		items[0]["canonical_url"] != "lbry://alpha#canonical" ||
		items[0]["is_my_output"] != true {
		t.Fatalf("resolved TXO item = %#v", items)
	}
	resolveCalls := resolvePublicCallsForMethod(network.snapshotCalls(), walletpkg.SPVResolveMethod)
	if len(resolveCalls) != 1 || len(resolveCalls[0].params) != 1 ||
		resolveCalls[0].params[0] != "lbry://Alpha#"+fixture.outputs["alpha"].ClaimID {
		t.Fatalf("TXO resolve calls = %#v", resolveCalls)
	}
	txoHandlerOracleAssertErrorNameMessage(
		t, fixture.request(t, "txo_list", map[string]any{
			"wallet_id": "missing", "resolve": true,
		}),
		"WalletNotLoadedError", "Wallet missing is not loaded.",
	)
	txoHandlerOracleAssertErrorNameMessage(
		t, fixture.request(t, "txo_list", map[string]any{
			"order_by": "txid", "resolve": true,
		}),
		"ValueError", "'txid' is not a valid --order_by value.",
	)
}

func TestTXOListResolutionNetworkErrorsPreserveTheirStage(t *testing.T) {
	fixture, network := newResolvePublicSuccessFixture(t, nil)
	network.resolveErr = spvpkg.ErrNetworkStopped
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		performRequest(
			fixture.server, http.MethodPost, "/",
			`{"method":"txo_list","params":{"resolve":true,"type":"stream","name":"Alpha"}}`, nil,
		)
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("stopped TXO resolve panic = %#v, want http.ErrAbortHandler", recovered)
	}

	fixture, network = newResolvePublicSuccessFixture(t, nil)
	network.resolveResult = ""
	countMismatch := fixture.request(t, "txo_list", map[string]any{
		"resolve": true, "type": "stream", "name": "Alpha",
	})
	txoHandlerOracleAssertErrorNameMessage(
		t, countMismatch, "AssertionError",
		"Mismatch between urls requested for resolve and responses received.",
	)

	connection := txoListRPCError(
		fmt.Errorf("%w: %w", walletpkg.ErrLocalTransactionResolve, spvpkg.ErrConnection),
		"get_txos",
	)
	var application transactionListApplicationError
	if !errors.As(connection, &application) || application.name != "ConnectionError" {
		t.Fatalf("TXO resolve connection error = %T %v", connection, connection)
	}
	support := txoListRPCError(
		fmt.Errorf("%w: %w", walletpkg.ErrLocalSupportClaimSearch, spvpkg.ErrRequestTimeout),
		"get_txos",
	)
	if !errors.As(support, &application) || application.name != "TimeoutError" {
		t.Fatalf("TXO support search timeout = %T %v", support, support)
	}
	supportCanceled := txoListRPCError(
		fmt.Errorf("%w: %w", walletpkg.ErrLocalSupportClaimSearch, context.Canceled),
		"get_txos",
	)
	var cancellation *rpcRequestCancellation
	if !errors.As(supportCanceled, &cancellation) {
		t.Fatalf("TXO support cancellation = %T %v", supportCanceled, supportCanceled)
	}
	canceled := purchaseListRPCError(context.Canceled)
	if !errors.As(canceled, &cancellation) {
		t.Fatalf("purchase cancellation = %T %v", canceled, canceled)
	}
}
