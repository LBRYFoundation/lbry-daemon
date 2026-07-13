package rpc

import (
	"errors"
	"testing"

	walletpkg "lbry/daemon/wallet"
	spvpkg "lbry/daemon/wallet/spv"
)

func TestClaimSearchRPCErrorMatchesPythonBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    string
		wantErr string
	}{
		{
			name:    "connection",
			err:     spvpkg.ErrConnection,
			want:    "ConnectionError",
			wantErr: "Attempting to send rpc request when connection is not available.",
		},
		{
			name:    "unavailable",
			err:     walletpkg.ErrClaimSearchUnavailable,
			want:    "AttributeError",
			wantErr: "'NoneType' object has no attribute 'claim_search'",
		},
		{
			name: "timeout", err: spvpkg.ErrRequestTimeout,
			want: "TimeoutError", wantErr: "",
		},
		{
			name:    "hub rpc",
			err:     &spvpkg.RPCError{Code: -32602, Message: "bad 'filter'"},
			want:    "RPCError",
			wantErr: "(-32602, \"bad 'filter'\")",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := claimSearchRPCError(test.err)
			if recoveredErrorName(err) != test.want || err.Error() != test.wantErr {
				t.Fatalf("error = %s: %q, want %s: %q",
					recoveredErrorName(err), err, test.want, test.wantErr)
			}
		})
	}

	sentinel := errors.New("sentinel")
	if claimSearchRPCError(sentinel) != sentinel || claimSearchRPCError(nil) != nil {
		t.Fatal("unrecognized claim search error was not preserved")
	}
}
