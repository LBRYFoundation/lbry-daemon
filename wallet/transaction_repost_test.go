package wallet

import (
	"strings"
	"testing"
)

func TestBuildRepostClaimEncodesDisplayClaimID(t *testing.T) {
	claimID := "131211100f0e0d0c0b0a09080706050403020100"
	claim, err := BuildRepostClaim(claimID, map[string]any{
		"title": "Repost", "description": "Reference", "tags": []string{"one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeClaimValue(claim)
	if err != nil || value.Type != "repost" || value.Value["claim_id"] != claimID ||
		value.Value["title"] != "Repost" {
		t.Fatalf("repost = %#v, %v", value, err)
	}
	if _, err := BuildRepostClaim(strings.Repeat("z", 40), nil); err == nil {
		t.Fatal("non-hex claim id was accepted")
	}
}
