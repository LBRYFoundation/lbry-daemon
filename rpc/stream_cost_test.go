package rpc

import (
	"encoding/json"
	"testing"
)

func TestStreamCostEstimateResolvesAndConvertsAdvertisedFee(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "stream_cost_estimate", map[string]any{
		"uri": "lbry://paid",
	})
	if result != json.Number("1") {
		t.Fatalf("stream_cost_estimate = %#v", result)
	}
}
