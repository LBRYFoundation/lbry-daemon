package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type claimSearchQueryCall struct {
	ctx        context.Context
	method     string
	params     map[string]any
	restricted bool
}

type claimSearchQuerySource struct {
	result any
	err    error
	calls  []claimSearchQueryCall
}

func (*claimSearchQuerySource) Start(context.Context) error { return nil }
func (*claimSearchQuerySource) Stop(context.Context) error  { return nil }
func (*claimSearchQuerySource) RemoteHeight() int           { return 0 }
func (*claimSearchQuerySource) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected retriable call")
}
func (source *claimSearchQuerySource) OneShotNamedValue(
	ctx context.Context, method string, params map[string]any, restricted bool,
) (any, error) {
	source.calls = append(source.calls, claimSearchQueryCall{
		ctx: ctx, method: method, params: params, restricted: restricted,
	})
	return source.result, source.err
}

type claimSearchHeaderOnlySource struct{}

func (*claimSearchHeaderOnlySource) Start(context.Context) error { return nil }
func (*claimSearchHeaderOnlySource) Stop(context.Context) error  { return nil }
func (*claimSearchHeaderOnlySource) RemoteHeight() int           { return 0 }
func (*claimSearchHeaderOnlySource) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, nil
}

func TestQueryClaimSearchUsesConfiguredNamedSourceOnce(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte{0x18, 0x07})
	source := &claimSearchQuerySource{result: encoded}
	ledger := &Ledger{SPVNetwork: source}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	params := map[string]any{
		"text": "open source", "claim_type": []any{"stream", "repost"},
	}

	outputs, err := ledger.QueryClaimSearch(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if outputs == nil || outputs.Total != 7 {
		t.Fatalf("decoded outputs = %#v", outputs)
	}
	if len(source.calls) != 1 {
		t.Fatalf("named query calls = %d, want 1", len(source.calls))
	}
	call := source.calls[0]
	if call.ctx != ctx || call.method != SPVClaimSearchMethod || call.restricted ||
		!reflect.DeepEqual(call.params, params) {
		t.Fatalf("named query call = %#v", call)
	}
}

func TestQueryClaimSearchCoercesPythonFalseyResultsToEmptyBase64(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "null", value: nil},
		{name: "false", value: false},
		{name: "empty string", value: ""},
		{name: "integer zero", value: json.Number("0")},
		{name: "float zero", value: json.Number("0.0")},
		{name: "empty list", value: []any{}},
		{name: "empty object", value: map[string]any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &claimSearchQuerySource{result: test.value}
			outputs, err := (&Ledger{SPVNetwork: source}).QueryClaimSearch(
				context.Background(), nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if outputs == nil || len(outputs.TXOs) != 0 || outputs.Total != 0 {
				t.Fatalf("falsey result outputs = %#v", outputs)
			}
			if len(source.calls) != 1 || source.calls[0].params != nil {
				t.Fatalf("falsey result calls = %#v", source.calls)
			}
		})
	}
}

func TestQueryClaimSearchRejectsTruthyNonStringResults(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    any
		typeName string
	}{
		{name: "true", value: true, typeName: "bool"},
		{name: "integer", value: json.Number("1"), typeName: "int"},
		{name: "float", value: json.Number("1.5"), typeName: "float"},
		{name: "list", value: []any{1}, typeName: "list"},
		{name: "object", value: map[string]any{"result": 1}, typeName: "dict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &claimSearchQuerySource{result: test.value}
			_, err := (&Ledger{SPVNetwork: source}).QueryClaimSearch(context.Background(), nil)
			var resultErr *ClaimSearchResultError
			wantMessage := "argument should be a bytes-like object or ASCII string, not '" +
				test.typeName + "'"
			if !errors.Is(err, ErrClaimSearchResult) || !errors.As(err, &resultErr) ||
				resultErr.PythonErrorName() != "TypeError" || err.Error() != wantMessage {
				t.Fatalf("truthy %s error = %T %v", test.name, err, err)
			}
			if len(source.calls) != 1 {
				t.Fatalf("truthy %s calls = %d, want 1", test.name, len(source.calls))
			}
		})
	}
}

func TestQueryClaimSearchPropagatesNetworkAndDecoderErrors(t *testing.T) {
	networkErr := errors.New("claim search failed")
	source := &claimSearchQuerySource{err: networkErr}
	if _, err := (&Ledger{SPVNetwork: source}).QueryClaimSearch(context.Background(), nil); !errors.Is(err, networkErr) {
		t.Fatalf("network error = %v", err)
	}
	if len(source.calls) != 1 {
		t.Fatalf("network-error calls = %d, want 1", len(source.calls))
	}

	source = &claimSearchQuerySource{result: "YQ"}
	if _, err := (&Ledger{SPVNetwork: source}).QueryClaimSearch(context.Background(), nil); !errors.Is(err, ErrInvalidHubOutputsBase64) {
		t.Fatalf("base64 error = %v", err)
	}

	source = &claimSearchQuerySource{result: base64.StdEncoding.EncodeToString([]byte{0xff})}
	_, err := (&Ledger{SPVNetwork: source}).QueryClaimSearch(context.Background(), nil)
	var decodeErr *HubOutputsDecodeError
	if !errors.As(err, &decodeErr) || decodeErr.PythonErrorName() != "DecodeError" {
		t.Fatalf("protobuf error = %T %v", err, err)
	}
}

func TestQueryClaimSearchRequiresConfiguredNamedSource(t *testing.T) {
	var nilLedger *Ledger
	if _, err := nilLedger.QueryClaimSearch(context.Background(), nil); !errors.Is(err, ErrClaimSearchUnavailable) {
		t.Fatalf("nil ledger error = %v", err)
	}
	for name, ledger := range map[string]*Ledger{
		"missing":     {},
		"unsupported": {SPVNetwork: &claimSearchHeaderOnlySource{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ledger.QueryClaimSearch(context.Background(), nil); !errors.Is(err, ErrClaimSearchUnavailable) {
				t.Fatalf("unavailable error = %v", err)
			}
		})
	}

	var typedNil *claimSearchQuerySource
	if _, err := (&Ledger{SPVNetwork: typedNil}).QueryClaimSearch(context.Background(), nil); !errors.Is(err, ErrClaimSearchUnavailable) {
		t.Fatalf("typed-nil source error = %v", err)
	}
}

var _ LedgerSPVNetwork = (*claimSearchQuerySource)(nil)
var _ LedgerSPVNamedValueSource = (*claimSearchQuerySource)(nil)
var _ LedgerSPVNetwork = (*claimSearchHeaderOnlySource)(nil)
