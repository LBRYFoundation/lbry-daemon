package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type resolveQueryCall struct {
	ctx        context.Context
	method     string
	params     []any
	restricted bool
}

type resolveQuerySource struct {
	result any
	err    error
	calls  []resolveQueryCall
}

func (*resolveQuerySource) Start(context.Context) error { return nil }
func (*resolveQuerySource) Stop(context.Context) error  { return nil }
func (*resolveQuerySource) RemoteHeight() int           { return 0 }
func (*resolveQuerySource) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected mapping call")
}
func (source *resolveQuerySource) RetriableValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	source.calls = append(source.calls, resolveQueryCall{
		ctx: ctx, method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	return source.result, source.err
}

type resolveQueryHeaderOnlySource struct{}

func (*resolveQueryHeaderOnlySource) Start(context.Context) error { return nil }
func (*resolveQueryHeaderOnlySource) Stop(context.Context) error  { return nil }
func (*resolveQueryHeaderOnlySource) RemoteHeight() int           { return 0 }
func (*resolveQueryHeaderOnlySource) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, nil
}

func TestQueryResolveBatchUsesOneConfiguredRetriableCall(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte{0x18, 0x07})
	source := &resolveQuerySource{result: encoded}
	ledger := &Ledger{SPVNetwork: source}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	urls := make([]string, 101)
	wantParams := make([]any, len(urls))
	for index := range urls {
		urls[index] = "lbry://claim-" + strconv.Itoa(index)
		wantParams[index] = urls[index]
	}

	outputs, err := ledger.QueryResolveBatch(ctx, urls)
	if err != nil {
		t.Fatal(err)
	}
	if outputs == nil || outputs.Total != 7 {
		t.Fatalf("decoded outputs = %#v", outputs)
	}
	if len(source.calls) != 1 {
		t.Fatalf("resolve query calls = %d, want 1", len(source.calls))
	}
	call := source.calls[0]
	if call.ctx != ctx || call.method != SPVResolveMethod || call.restricted ||
		!reflect.DeepEqual(call.params, wantParams) {
		t.Fatalf("resolve query call = %#v", call)
	}
}

func TestQueryResolveBatchCoercesPythonFalseyResultsToEmptyBase64(t *testing.T) {
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
			source := &resolveQuerySource{result: test.value}
			outputs, err := (&Ledger{SPVNetwork: source}).QueryResolveBatch(
				context.Background(), []string{"lbry://example"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if outputs == nil || len(outputs.TXOs) != 0 || outputs.Total != 0 {
				t.Fatalf("falsey result outputs = %#v", outputs)
			}
			if len(source.calls) != 1 || !reflect.DeepEqual(
				source.calls[0].params, []any{"lbry://example"},
			) {
				t.Fatalf("falsey result calls = %#v", source.calls)
			}
		})
	}
}

func TestQueryResolveBatchRejectsTruthyNonStringResults(t *testing.T) {
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
			source := &resolveQuerySource{result: test.value}
			_, err := (&Ledger{SPVNetwork: source}).QueryResolveBatch(
				context.Background(), []string{"lbry://example"},
			)
			var resultErr *ResolveResultError
			wantMessage := "argument should be a bytes-like object or ASCII string, not '" +
				test.typeName + "'"
			if !errors.Is(err, ErrResolveResult) || !errors.As(err, &resultErr) ||
				resultErr.PythonErrorName() != "TypeError" || err.Error() != wantMessage {
				t.Fatalf("truthy %s error = %T %v", test.name, err, err)
			}
			if len(source.calls) != 1 {
				t.Fatalf("truthy %s calls = %d, want 1", test.name, len(source.calls))
			}
		})
	}
}

func TestQueryResolveBatchPropagatesNetworkAndDecoderErrors(t *testing.T) {
	networkErr := errors.New("resolve failed")
	source := &resolveQuerySource{err: networkErr}
	if _, err := (&Ledger{SPVNetwork: source}).QueryResolveBatch(
		context.Background(), []string{"lbry://example"},
	); !errors.Is(err, networkErr) {
		t.Fatalf("network error = %v", err)
	}
	if len(source.calls) != 1 {
		t.Fatalf("network-error calls = %d, want 1", len(source.calls))
	}

	source = &resolveQuerySource{result: "YQ"}
	_, err := (&Ledger{SPVNetwork: source}).QueryResolveBatch(
		context.Background(), []string{"lbry://example"},
	)
	var base64Err *HubOutputsBase64DecodeError
	if !errors.Is(err, ErrInvalidHubOutputsBase64) || !errors.As(err, &base64Err) ||
		base64Err.PythonErrorName() != "Error" || err.Error() != "Incorrect padding" {
		t.Fatalf("base64 error = %T %v", err, err)
	}

	source = &resolveQuerySource{result: "YQ==\u00e9"}
	_, err = (&Ledger{SPVNetwork: source}).QueryResolveBatch(
		context.Background(), []string{"lbry://example"},
	)
	if !errors.Is(err, ErrInvalidHubOutputsBase64) || !errors.As(err, &base64Err) ||
		base64Err.PythonErrorName() != "ValueError" ||
		err.Error() != "string argument should contain only ASCII characters" {
		t.Fatalf("non-ASCII base64 error = %T %v", err, err)
	}

	source = &resolveQuerySource{
		result: base64.StdEncoding.EncodeToString([]byte{0xff}),
	}
	_, err = (&Ledger{SPVNetwork: source}).QueryResolveBatch(
		context.Background(), []string{"lbry://example"},
	)
	var decodeErr *HubOutputsDecodeError
	if !errors.As(err, &decodeErr) || decodeErr.PythonErrorName() != "DecodeError" {
		t.Fatalf("protobuf error = %T %v", err, err)
	}
}

func TestQueryResolveBatchRequiresConfiguredRetriableSource(t *testing.T) {
	var nilLedger *Ledger
	if _, err := nilLedger.QueryResolveBatch(context.Background(), nil); !errors.Is(
		err, ErrResolveUnavailable,
	) {
		t.Fatalf("nil ledger error = %v", err)
	}
	for name, ledger := range map[string]*Ledger{
		"missing":     {},
		"unsupported": {SPVNetwork: &resolveQueryHeaderOnlySource{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ledger.QueryResolveBatch(context.Background(), nil); !errors.Is(
				err, ErrResolveUnavailable,
			) {
				t.Fatalf("unavailable error = %v", err)
			}
		})
	}

	var typedNil *resolveQuerySource
	if _, err := (&Ledger{SPVNetwork: typedNil}).QueryResolveBatch(
		context.Background(), nil,
	); !errors.Is(err, ErrResolveUnavailable) {
		t.Fatalf("typed-nil source error = %v", err)
	}
}

var _ LedgerSPVNetwork = (*resolveQuerySource)(nil)
var _ LedgerSPVRetriableValueSource = (*resolveQuerySource)(nil)
var _ LedgerSPVNetwork = (*resolveQueryHeaderOnlySource)(nil)
