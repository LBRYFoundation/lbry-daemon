package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTracemallocLifecycleAndResultShape(t *testing.T) {
	server := CreateServer()
	disabled := performRequest(server, "POST", "/", `{"method":"tracemalloc_top"}`, nil)
	assertRPCError(
		t, disabled, json.Number("-32500"),
		"Enable tracemalloc first! See 'tracemalloc set' command.",
	)

	if result := fileMutationRPCResult(t, server, "tracemalloc_enable", map[string]any{}); result != true {
		t.Fatalf("tracemalloc_enable = %#v", result)
	}
	result := fileMutationRPCResult(t, server, "tracemalloc_top", map[string]any{"items": 2})
	items, ok := result.([]any)
	if !ok || len(items) > 2 {
		t.Fatalf("tracemalloc_top = %#v", result)
	}
	for _, item := range items {
		record, recordOK := item.(map[string]any)
		if !recordOK || record["line"] == "" || record["code"] == "" ||
			record["size"] == nil || record["count"] == nil {
			t.Fatalf("tracemalloc record = %#v", item)
		}
		if strings.Count(record["line"].(string), string(filepathSeparator())) > 1 {
			t.Fatalf("tracemalloc line is not shortened: %q", record["line"])
		}
	}
	if result := fileMutationRPCResult(t, server, "tracemalloc_disable", map[string]any{}); result != false {
		t.Fatalf("tracemalloc_disable = %#v", result)
	}
}

func filepathSeparator() rune {
	return '/'
}
