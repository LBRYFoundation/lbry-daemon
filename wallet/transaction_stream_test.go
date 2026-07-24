package wallet

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildStreamClaimCreateAndUpdateMetadata(t *testing.T) {
	created, err := BuildStreamClaim(nil, false, map[string]any{
		"title": "Video", "author": "Author", "file_name": "video.mp4",
		"file_size": "123", "file_hash": strings.Repeat("11", 48),
		"sd_hash": strings.Repeat("22", 48), "width": "1920", "height": "1080",
		"duration": "60", "tags": []string{"one"}, "languages": []string{"en"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := BuildStreamClaim(created, false, map[string]any{
		"description": "Updated", "tags": []string{"two"}, "clear_languages": true,
		"languages": []string{"pl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeClaimValue(updated)
	if err != nil || value.Type != "stream" || value.Value["title"] != "Video" ||
		value.Value["description"] != "Updated" || value.Value["stream_type"] != "video" ||
		!reflect.DeepEqual(value.Value["tags"], []any{"one", "two"}) ||
		!reflect.DeepEqual(value.Value["languages"], []any{"pl"}) {
		t.Fatalf("stream value = %#v, %v", value, err)
	}
	source := value.Value["source"].(map[string]any)
	video := value.Value["video"].(map[string]any)
	if source["name"] != "video.mp4" || source["size"] != "123" ||
		source["sd_hash"] != strings.Repeat("22", 48) ||
		video["width"] != uint32(1920) || video["height"] != uint32(1080) || video["duration"] != uint32(60) {
		t.Fatalf("stream source/media = %#v / %#v", source, video)
	}
}

func TestBuildStreamClaimFee(t *testing.T) {
	claim, err := BuildStreamClaim(nil, false, map[string]any{
		"fee_currency": "usd", "fee_amount": "1.25", "fee_address": "15T",
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeClaimValue(claim)
	fee := value.Value["fee"].(map[string]any)
	if err != nil || fee["currency"] != "USD" || fee["amount"] != "1.25" || fee["address"] != "15T" {
		t.Fatalf("fee = %#v, %v", fee, err)
	}
}
