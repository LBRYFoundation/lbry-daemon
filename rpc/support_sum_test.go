package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSupportSumForwardsExperimentalServerRequest(t *testing.T) {
	var request map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, incoming *http.Request) {
		decoder := json.NewDecoder(incoming.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"result": []any{map[string]any{"channel_id": "abc", "support_amount": "3.0"}},
		})
	}))
	defer remote.Close()

	server := CreateServer()
	result := fileMutationRPCResult(t, server, "support_sum", map[string]any{
		"claim_id": "claim", "new_sdk_server": remote.URL,
		"include_channel_content": true, "page": -2, "page_size": 75,
		"order_by": "support_amount",
	}).(map[string]any)
	items := result["items"].([]any)
	if result["page"] != json.Number("2") || result["page_size"] != json.Number("50") ||
		len(items) != 1 || items[0].(map[string]any)["channel_id"] != "abc" {
		t.Fatalf("support_sum = %#v", result)
	}
	if request["method"] != "support_sum" {
		t.Fatalf("forwarded request = %#v", request)
	}
	forwarded := request["params"].(map[string]any)
	if forwarded["claim_id"] != "claim" || forwarded["include_channel_content"] != true ||
		forwarded["offset"] != json.Number("50") || forwarded["limit"] != json.Number("50") ||
		forwarded["order_by"] != "support_amount" {
		t.Fatalf("forwarded params = %#v", forwarded)
	}
}
