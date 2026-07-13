package wallet

import (
	"context"
	"errors"
	"fmt"
)

const (
	SPVHeaderRPCMethod              = "blockchain.block.headers"
	SPVHeaderRPCRestrictionDistance = 100
)

var (
	ErrSPVHeaderRPCUnavailable = errors.New("SPV header RPC caller is unavailable")
	ErrSPVHeaderBase64Missing  = errors.New("SPV header response is missing base64")
	ErrSPVHeaderBase64Type     = errors.New("SPV header response base64 is not a string")
)

// SPVHeaderRPC is the retry/server-selection boundary used by checkpoint and
// live-tip synchronization. The result is the unwrapped Electrum method result.
type SPVHeaderRPC interface {
	RetriableCall(context.Context, string, []any, bool) (map[string]any, error)
}

// NewSPVHeaderChunkGetter binds the header store to the exact request emitted
// by Network.get_headers through WalletNetwork.retriable_call in SDK 0.113.0.
func NewSPVHeaderChunkGetter(
	caller SPVHeaderRPC, remoteHeight func() int,
) (HeaderChunkGetter, error) {
	if caller == nil {
		return nil, ErrSPVHeaderRPCUnavailable
	}
	if remoteHeight == nil {
		return nil, errors.New("SPV remote-height provider is unavailable")
	}
	return func(ctx context.Context, start int) (HeaderChunkResponse, error) {
		restricted := start >= remoteHeight()-SPVHeaderRPCRestrictionDistance
		result, err := caller.RetriableCall(ctx, SPVHeaderRPCMethod, []any{
			start, CheckpointChunkHeaders, 0, true,
		}, restricted)
		if err != nil {
			return HeaderChunkResponse{}, err
		}
		value, exists := result["base64"]
		if !exists {
			return HeaderChunkResponse{}, ErrSPVHeaderBase64Missing
		}
		encoded, ok := value.(string)
		if !ok {
			return HeaderChunkResponse{}, fmt.Errorf("%w: got %T", ErrSPVHeaderBase64Type, value)
		}
		return HeaderChunkResponse{Base64: encoded}, nil
	}, nil
}
