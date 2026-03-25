package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// decodeClaim decodes a Claim protobuf into a JSON-friendly map.
// It flattens the type-specific sub-message (stream/channel/collection/repost)
// into the top level, matching the LBRY SDK's JSON encoding.
// Returns (value, value_type, error).
func decodeClaim(data []byte) (map[string]any, string, error) {
	if len(data) == 0 {
		return map[string]any{}, "", fmt.Errorf("empty claim data")
	}

	// Strip signing prefix (LBRY v2 format).
	// 0x00 = unsigned: 1-byte prefix
	// 0x01 = signed: 1 + 20 (channel_hash) + 64 (signature) = 85 bytes
	pb := data
	switch pb[0] {
	case 0:
		pb = pb[1:]
	case 1:
		if len(pb) < 85 {
			return map[string]any{}, "", fmt.Errorf("signed claim too short")
		}
		pb = pb[85:]
	case '{':
		// Legacy JSON v0 format
		var m map[string]any
		if err := json.Unmarshal(pb, &m); err == nil {
			return m, guessValueType(m), nil
		}
		return map[string]any{}, "", fmt.Errorf("invalid v0 JSON claim")
	}

	result := map[string]any{}
	var valueType string

	for b := pb; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		switch typ {
		case protowire.BytesType:
			val, _ := protowire.ConsumeBytes(b)
			switch num {
			case 1: // Stream
				valueType = "stream"
				for k, v := range decodeStream(val) {
					result[k] = v
				}
			case 2: // Channel
				valueType = "channel"
				for k, v := range decodeChannel(val) {
					result[k] = v
				}
			case 3: // Collection (ClaimList)
				valueType = "collection"
				for k, v := range decodeClaimList(val) {
					result[k] = v
				}
			case 4: // Repost (ClaimReference)
				valueType = "repost"
				for k, v := range decodeClaimReference(val) {
					result[k] = v
				}
			case 8: // title
				result["title"] = string(val)
			case 9: // description
				result["description"] = string(val)
			case 10: // thumbnail (Source)
				result["thumbnail"] = decodeSource(val)
			case 11: // tags (repeated string)
				tags, _ := result["tags"].([]string)
				result["tags"] = append(tags, string(val))
			}
		}

		b = b[fieldLen:]
	}

	return result, valueType, nil
}

func decodeStream(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		switch typ {
		case protowire.VarintType:
			val, _ := protowire.ConsumeVarint(b)
			if num == 5 { // release_time
				result["release_time"] = val
			}

		case protowire.BytesType:
			val, _ := protowire.ConsumeBytes(b)
			switch num {
			case 1: // source
				result["source"] = decodeSource(val)
			case 2: // author
				result["author"] = string(val)
			case 3: // license
				result["license"] = string(val)
			case 4: // license_url
				result["license_url"] = string(val)
			case 6: // fee
				result["fee"] = decodeFee(val)
			case 10: // image
				result["stream_type"] = "image"
				result["image"] = decodeImage(val)
			case 11: // video
				result["stream_type"] = "video"
				result["video"] = decodeVideo(val)
			case 12: // audio
				result["stream_type"] = "audio"
				result["audio"] = decodeAudio(val)
			case 13: // software
				result["stream_type"] = "software"
			}
		}

		b = b[fieldLen:]
	}

	if _, ok := result["stream_type"]; !ok {
		if _, hasSource := result["source"]; hasSource {
			result["stream_type"] = "document"
		} else {
			result["stream_type"] = "binary"
		}
	}

	return result
}

func decodeChannel(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.BytesType {
			val, _ := protowire.ConsumeBytes(b)
			switch num {
			case 1: // public_key
				result["public_key"] = hex.EncodeToString(val)
			case 2: // email
				result["email"] = string(val)
			case 3: // website_url
				result["website_url"] = string(val)
			case 4: // cover (Source)
				result["cover"] = decodeSource(val)
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeSource(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		switch typ {
		case protowire.VarintType:
			val, _ := protowire.ConsumeVarint(b)
			if num == 3 { // size
				result["size"] = val
			}
		case protowire.BytesType:
			val, _ := protowire.ConsumeBytes(b)
			switch num {
			case 1: // hash
				result["hash"] = hex.EncodeToString(val)
			case 2: // name
				result["name"] = string(val)
			case 4: // media_type
				result["media_type"] = string(val)
			case 5: // url
				result["url"] = string(val)
			case 6: // sd_hash
				result["sd_hash"] = hex.EncodeToString(val)
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeVideo(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.VarintType {
			val, _ := protowire.ConsumeVarint(b)
			switch num {
			case 1:
				result["width"] = val
			case 2:
				result["height"] = val
			case 3:
				result["duration"] = val
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeAudio(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.VarintType {
			val, _ := protowire.ConsumeVarint(b)
			if num == 1 { // duration
				result["duration"] = val
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeImage(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.VarintType {
			val, _ := protowire.ConsumeVarint(b)
			switch num {
			case 1:
				result["width"] = val
			case 2:
				result["height"] = val
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeFee(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		switch typ {
		case protowire.VarintType:
			val, _ := protowire.ConsumeVarint(b)
			switch num {
			case 1: // currency
				switch val {
				case 1:
					result["currency"] = "LBC"
				case 2:
					result["currency"] = "BTC"
				case 3:
					result["currency"] = "USD"
				}
			case 3: // amount
				result["amount"] = val
			}
		case protowire.BytesType:
			val, _ := protowire.ConsumeBytes(b)
			if num == 2 { // address
				result["address"] = hex.EncodeToString(val)
			}
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeClaimReference(data []byte) map[string]any {
	result := map[string]any{}

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.BytesType && num == 1 { // claim_hash
			val, _ := protowire.ConsumeBytes(b)
			result["claim_id"] = claimIDFromBytes(val)
		}

		b = b[fieldLen:]
	}

	return result
}

func decodeClaimList(data []byte) map[string]any {
	result := map[string]any{}
	var claims []string

	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]

		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}

		if typ == protowire.BytesType && num == 2 { // claim_references (repeated)
			val, _ := protowire.ConsumeBytes(b)
			ref := decodeClaimReference(val)
			if id, ok := ref["claim_id"].(string); ok {
				claims = append(claims, id)
			}
		}

		b = b[fieldLen:]
	}

	if len(claims) > 0 {
		result["claims"] = claims
	}
	return result
}

// guessValueType guesses value_type from a legacy JSON claim map.
func guessValueType(m map[string]any) string {
	if _, ok := m["stream"]; ok {
		return "stream"
	}
	if _, ok := m["channel"]; ok {
		return "channel"
	}
	if _, ok := m["collection"]; ok {
		return "collection"
	}
	if _, ok := m["repost"]; ok {
		return "repost"
	}
	return ""
}
