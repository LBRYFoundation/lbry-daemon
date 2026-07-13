package wallet

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type spvHeaderRPCCall struct {
	Method     string
	Params     []any
	Restricted bool
}

type spvHeaderRPCStub struct {
	Calls  []spvHeaderRPCCall
	Result map[string]any
	Err    error
}

func (stub *spvHeaderRPCStub) RetriableCall(
	_ context.Context, method string, params []any, restricted bool,
) (map[string]any, error) {
	stub.Calls = append(stub.Calls, spvHeaderRPCCall{
		Method: method, Params: append([]any(nil), params...), Restricted: restricted,
	})
	return stub.Result, stub.Err
}

func TestSPVHeaderChunkGetterRequestAndRestrictionContract(t *testing.T) {
	stub := &spvHeaderRPCStub{Result: map[string]any{"base64": "encoded"}}
	remoteHeight := 10_000
	getter, err := NewSPVHeaderChunkGetter(stub, func() int { return remoteHeight })
	if err != nil {
		t.Fatal(err)
	}
	response, err := getter(context.Background(), 9_000)
	if err != nil || response.Base64 != "encoded" {
		t.Fatalf("response = %#v, %v", response, err)
	}
	remoteHeight = 9_050
	if _, err := getter(context.Background(), 9_000); err != nil {
		t.Fatal(err)
	}
	want := []spvHeaderRPCCall{
		{Method: SPVHeaderRPCMethod, Params: []any{9_000, 1_000, 0, true}, Restricted: false},
		{Method: SPVHeaderRPCMethod, Params: []any{9_000, 1_000, 0, true}, Restricted: true},
	}
	if !reflect.DeepEqual(stub.Calls, want) {
		t.Fatalf("SPV calls = %#v, want %#v", stub.Calls, want)
	}
}

func TestSPVHeaderChunkGetterErrors(t *testing.T) {
	if _, err := NewSPVHeaderChunkGetter(nil, func() int { return 0 }); !errors.Is(err, ErrSPVHeaderRPCUnavailable) {
		t.Fatalf("nil caller error = %v", err)
	}
	if _, err := NewSPVHeaderChunkGetter(&spvHeaderRPCStub{}, nil); err == nil {
		t.Fatal("nil remote-height provider was accepted")
	}
	wantErr := errors.New("RPC failed")
	stub := &spvHeaderRPCStub{Err: wantErr}
	getter, err := NewSPVHeaderChunkGetter(stub, func() int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := getter(context.Background(), 0); !errors.Is(err, wantErr) {
		t.Fatalf("RPC error = %v", err)
	}
	stub.Err = nil
	stub.Result = map[string]any{}
	if _, err := getter(context.Background(), 0); !errors.Is(err, ErrSPVHeaderBase64Missing) {
		t.Fatalf("missing base64 error = %v", err)
	}
	stub.Result = map[string]any{"base64": 1}
	if _, err := getter(context.Background(), 0); !errors.Is(err, ErrSPVHeaderBase64Type) {
		t.Fatalf("base64 type error = %v", err)
	}
}
