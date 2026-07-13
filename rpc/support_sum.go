package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

func (rpcServer *RPCServer) handleSupportSum(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	server, ok := normalized.named["new_sdk_server"].(string)
	if !ok {
		panic(errors.New("new_sdk_server must be a string"))
	}
	page := supportSumInteger(normalized.named["page"], 1)
	pageSize := supportSumInteger(normalized.named["page_size"], 20)
	if page < 0 {
		page = -page
	}
	if pageSize < 0 {
		pageSize = -pageSize
	}
	pageSize = min(pageSize, 50)
	forwarded := make(map[string]any, len(normalized.kwargs)+4)
	for name, value := range normalized.kwargs {
		switch name {
		case "new_sdk_server", "page", "page_size":
			continue
		default:
			forwarded[name] = value
		}
	}
	forwarded["claim_id"] = normalized.named["claim_id"]
	forwarded["include_channel_content"] = normalized.named["include_channel_content"]
	forwarded["offset"] = pageSize * (page - 1)
	forwarded["limit"] = pageSize
	body, err := json.Marshal(map[string]any{"method": "support_sum", "params": forwarded})
	if err != nil {
		panic(err)
	}
	request, err := http.NewRequestWithContext(normalized.ctx, http.MethodPost, server, bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Content-Type", "application/json")
	remote, err := http.DefaultClient.Do(request)
	if err != nil {
		panic(err)
	}
	defer remote.Body.Close()
	decoder := json.NewDecoder(remote.Body)
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		panic(err)
	}
	items, exists := envelope["result"]
	if !exists {
		panic(errors.New("support_sum response is missing result"))
	}
	sendResultResponse(response, map[string]any{
		"items": items, "page": page, "page_size": pageSize,
	})
}

func supportSumInteger(value any, fallback int) int {
	if value == nil {
		return fallback
	}
	parsed, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil {
		panic(err)
	}
	return parsed
}
