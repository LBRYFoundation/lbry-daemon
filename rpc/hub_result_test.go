package rpc

import (
	"errors"
	"testing"
)

func TestHubResult(t *testing.T) {
	if result, err := hubResult("m", map[string]any{"result": 5.0}, nil); err != nil || result != 5.0 {
		t.Errorf("result: %v %v", result, err)
	}
	if _, err := hubResult("m", map[string]any{"error": map[string]any{"message": "boom"}}, nil); err == nil {
		t.Error("an error reply must be an error")
	}
	if _, err := hubResult("m", map[string]any{}, nil); err == nil {
		t.Error("a reply with neither result nor error must be an error")
	}
	if _, err := hubResult("m", nil, errors.New("down")); err == nil {
		t.Error("a transport failure must be an error")
	}
}
