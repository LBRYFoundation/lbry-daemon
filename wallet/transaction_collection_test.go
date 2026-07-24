package wallet

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildCollectionClaimUpdateClearAndReplace(t *testing.T) {
	first := strings.Repeat("11", 20)
	second := strings.Repeat("22", 20)
	created, err := BuildCollectionClaim(nil, false, map[string]any{
		"title": "Old", "description": "Keep", "claims": []string{first}, "tags": []string{"old"},
		"languages": []string{"en", "zh-Hans", "es-ES"},
		"locations": []any{
			map[string]any{"country": "US", "state": "NH"},
			"42.990605:-71.460989",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := BuildCollectionClaim(created, false, map[string]any{
		"title": "New", "clear_claims": true, "tags": []string{"new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeClaimValue(updated)
	if err != nil || value.Value["title"] != "New" || value.Value["description"] != "Keep" ||
		value.Value["claims"] != nil || !reflect.DeepEqual(value.Value["tags"], []any{"new"}) ||
		!reflect.DeepEqual(value.Value["languages"], []any{"en", "zh-Hans", "es-ES"}) ||
		!reflect.DeepEqual(value.Value["locations"], []any{
			map[string]any{"country": "US", "state": "NH"},
			map[string]any{"latitude": "42.990605", "longitude": "-71.460989"},
		}) {
		t.Fatalf("updated collection = %#v, %v", value, err)
	}
	replaced, err := BuildCollectionClaim(updated, true, map[string]any{"claims": []string{second}})
	if err != nil {
		t.Fatal(err)
	}
	value, err = DecodeClaimValue(replaced)
	if err != nil || value.Value["title"] != nil ||
		!reflect.DeepEqual(value.Value["claims"], []any{second}) {
		t.Fatalf("replaced collection = %#v, %v", value, err)
	}
}

func TestBuildCollectionClaimAppendsAndClearsRepeatedMetadata(t *testing.T) {
	created, err := BuildCollectionClaim(nil, false, map[string]any{
		"languages": []string{"en"},
		"locations": []any{"US:NH:Manchester:03101:42.990605:-71.460989"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := BuildCollectionClaim(created, false, map[string]any{
		"languages": []string{"pl"}, "clear_locations": true,
		"locations": []any{map[string]any{"country": "PL", "city": "Warsaw"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeClaimValue(updated)
	if err != nil || !reflect.DeepEqual(value.Value["languages"], []any{"en", "pl"}) ||
		!reflect.DeepEqual(value.Value["locations"], []any{map[string]any{"country": "PL", "city": "Warsaw"}}) {
		t.Fatalf("updated collection = %#v, %v", value, err)
	}
}

func TestBuildCollectionClaimRejectsInvalidLocation(t *testing.T) {
	for _, location := range []any{
		map[string]any{"country": "XX"},
		map[string]any{"latitude": "90.0000001"},
		map[string]any{"longitude": -181.0},
		"US:NH:Manchester:03101:1:2:extra",
	} {
		if _, err := BuildCollectionClaim(nil, false, map[string]any{"locations": []any{location}}); err == nil {
			t.Fatalf("location %#v was accepted", location)
		}
	}
}
