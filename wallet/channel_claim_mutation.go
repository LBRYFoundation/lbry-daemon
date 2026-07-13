package wallet

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func BuildChannelClaim(
	existing []byte,
	publicKey []byte,
	replace bool,
	fields map[string]any,
) ([]byte, error) {
	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	claim := dynamicpb.NewMessage(descriptor)
	if len(existing) > 0 && !replace {
		if existing[0] != 0 {
			return nil, fmt.Errorf("%w: signed channel mutation is unsupported", ErrInvalidClaimValue)
		}
		if err := proto.Unmarshal(existing[1:], claim); err != nil {
			return nil, fmt.Errorf("%w: decode channel claim: %v", ErrInvalidClaimValue, err)
		}
	}
	channelField := descriptor.Fields().ByName("channel")
	channel := claim.Mutable(channelField).Message()
	setProtoBytes(channel, "public_key", publicKey)
	setOptionalProtoString(claim, "title", fields["title"])
	setOptionalProtoString(claim, "description", fields["description"])
	setOptionalProtoString(channel, "email", fields["email"])
	setOptionalProtoString(channel, "website_url", fields["website_url"])
	setSourceURL(claim, "thumbnail", fields["thumbnail_url"])
	if err := applyClaimLanguages(claim, fields); err != nil {
		return nil, err
	}
	if err := applyClaimLocations(claim, fields); err != nil {
		return nil, err
	}
	setSourceURL(channel, "cover", fields["cover_url"])
	if transactionQueryBoolValue(fields["clear_tags"]) {
		claim.Clear(descriptor.Fields().ByName("tags"))
	}
	if tags, exists := fields["tags"]; exists && tags != nil {
		values, err := channelMutationStrings(tags)
		if err != nil {
			return nil, err
		}
		tagField := descriptor.Fields().ByName("tags")
		claim.Clear(tagField)
		list := claim.Mutable(tagField).List()
		for _, tag := range values {
			list.Append(protoreflect.ValueOfString(tag))
		}
	}
	featuredField := channel.Descriptor().Fields().ByName("featured")
	if transactionQueryBoolValue(fields["clear_featured"]) {
		channel.Clear(featuredField)
	}
	if featured, exists := fields["featured"]; exists && featured != nil {
		values, err := channelMutationStrings(featured)
		if err != nil {
			return nil, err
		}
		featuredList := channel.Mutable(featuredField).Message()
		if err := setClaimReferenceList(featuredList, values); err != nil {
			return nil, err
		}
	}
	encoded, err := marshalClaimProtoMessage(claim)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, encoded...), nil
}

func applyClaimLanguages(claim protoreflect.Message, fields map[string]any) error {
	field := claim.Descriptor().Fields().ByName("languages")
	if transactionQueryBoolValue(fields["clear_languages"]) {
		claim.Clear(field)
	}
	raw, exists := fields["languages"]
	if !exists || raw == nil {
		return nil
	}
	values, err := channelMutationStrings(raw)
	if err != nil {
		return err
	}
	list := claim.Mutable(field).List()
	for _, encoded := range values {
		parts := strings.Split(encoded, "-")
		if len(parts) < 1 || len(parts) > 3 || parts[0] == "" {
			return fmt.Errorf("invalid language %q", encoded)
		}
		language := dynamicpb.NewMessage(field.Message())
		if err := setEnumName(language, "language", protoreflect.Name(parts[0])); err != nil {
			return fmt.Errorf("invalid language %q: %w", encoded, err)
		}
		index := 1
		if index < len(parts) && len(parts[index]) == 4 {
			script := strings.ToUpper(parts[index][:1]) + strings.ToLower(parts[index][1:])
			if err := setEnumName(language, "script", protoreflect.Name(script)); err != nil {
				return fmt.Errorf("invalid language %q: %w", encoded, err)
			}
			index++
		}
		if index < len(parts) {
			region := strings.ToUpper(parts[index])
			if err := setEnumName(language, "region", protoreflect.Name(region)); err != nil {
				if err = setEnumName(language, "region", protoreflect.Name("R"+region)); err != nil {
					return fmt.Errorf("invalid language %q: %w", encoded, err)
				}
			}
			index++
		}
		if index != len(parts) {
			return fmt.Errorf("invalid language %q", encoded)
		}
		list.Append(protoreflect.ValueOfMessage(language))
	}
	return nil
}

func applyClaimLocations(claim protoreflect.Message, fields map[string]any) error {
	field := claim.Descriptor().Fields().ByName("locations")
	if transactionQueryBoolValue(fields["clear_locations"]) {
		claim.Clear(field)
	}
	raw, exists := fields["locations"]
	if !exists || raw == nil {
		return nil
	}
	values := []any{raw}
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []map[string]any:
		values = make([]any, len(typed))
		for index := range typed {
			values[index] = typed[index]
		}
	}
	list := claim.Mutable(field).List()
	for _, value := range values {
		location := dynamicpb.NewMessage(field.Message())
		if err := setClaimLocation(location, value); err != nil {
			return err
		}
		list.Append(protoreflect.ValueOfMessage(location))
	}
	return nil
}

func setClaimLocation(location protoreflect.Message, value any) error {
	if text, ok := value.(string); ok {
		if strings.HasPrefix(text, "{") {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(text), &decoded); err != nil {
				return fmt.Errorf("invalid location %q: %w", text, err)
			}
			value = decoded
		} else {
			return setClaimLocationString(location, text)
		}
	}
	values, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid location %#v", value)
	}
	for key, raw := range values {
		switch key {
		case "country":
			country, ok := raw.(string)
			if !ok || setEnumName(location, "country", protoreflect.Name(country)) != nil {
				return fmt.Errorf("invalid location country %q", raw)
			}
		case "state", "city", "code":
			text, ok := raw.(string)
			if !ok {
				return fmt.Errorf("invalid location %s %q", key, raw)
			}
			setOptionalProtoString(location, protoreflect.Name(key), text)
		case "latitude":
			if err := setLocationCoordinate(location, "latitude", raw, 90); err != nil {
				return err
			}
		case "longitude":
			if err := setLocationCoordinate(location, "longitude", raw, 180); err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid location field %q", key)
		}
	}
	return nil
}

func setClaimLocationString(location protoreflect.Message, encoded string) error {
	parts := strings.Split(encoded, ":")
	coordinatesOnly := len(parts) <= 2 && (parts[0] == "" ||
		(parts[0][0] < 'A' || parts[0][0] > 'Z') && (parts[0][0] < 'a' || parts[0][0] > 'z'))
	fields := []string{"country", "state", "city", "code", "latitude", "longitude"}
	if coordinatesOnly {
		fields = fields[4:]
	}
	if len(parts) > len(fields) {
		return fmt.Errorf("invalid location %q", encoded)
	}
	values := make(map[string]any)
	for index, part := range parts {
		if part != "" {
			values[fields[index]] = part
		}
	}
	return setClaimLocation(location, values)
}

func setLocationCoordinate(location protoreflect.Message, name string, raw any, limit int64) error {
	var text string
	switch value := raw.(type) {
	case string:
		text = value
	case float64:
		text = strconv.FormatFloat(value, 'g', -1, 64)
	case json.Number:
		text = value.String()
	default:
		return fmt.Errorf("invalid location %s %q", name, raw)
	}
	coordinate, ok := new(big.Rat).SetString(text)
	if !ok {
		return fmt.Errorf("invalid location %s %q", name, raw)
	}
	bound := new(big.Rat).SetInt64(limit)
	if coordinate.Cmp(new(big.Rat).Neg(bound)) < 0 || coordinate.Cmp(bound) > 0 {
		return fmt.Errorf("location %s must be between -%d and %d degrees", name, limit, limit)
	}
	scaled := new(big.Rat).Mul(coordinate, new(big.Rat).SetInt64(10_000_000))
	encoded := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	field := location.Descriptor().Fields().ByName(protoreflect.Name(name))
	location.Set(field, protoreflect.ValueOfInt32(int32(encoded.Int64())))
	return nil
}

func setEnumName(message protoreflect.Message, fieldName, enumName protoreflect.Name) error {
	field := message.Descriptor().Fields().ByName(fieldName)
	value := field.Enum().Values().ByName(enumName)
	if value == nil {
		return fmt.Errorf("unknown %s %q", fieldName, enumName)
	}
	message.Set(field, protoreflect.ValueOfEnum(value.Number()))
	return nil
}

func setClaimReferenceList(message protoreflect.Message, claimIDs []string) error {
	field := message.Descriptor().Fields().ByName("claim_references")
	message.Clear(field)
	list := message.Mutable(field).List()
	for _, claimID := range claimIDs {
		display, err := hex.DecodeString(claimID)
		if err != nil || len(display) != 20 {
			return fmt.Errorf("invalid claim id %q", claimID)
		}
		for left, right := 0, len(display)-1; left < right; left, right = left+1, right-1 {
			display[left], display[right] = display[right], display[left]
		}
		reference := dynamicpb.NewMessage(field.Message())
		setProtoBytes(reference, "claim_hash", display)
		list.Append(protoreflect.ValueOfMessage(reference))
	}
	return nil
}

func setProtoBytes(message protoreflect.Message, name protoreflect.Name, value []byte) {
	field := message.Descriptor().Fields().ByName(name)
	message.Set(field, protoreflect.ValueOfBytes(append([]byte(nil), value...)))
}

func setOptionalProtoString(message protoreflect.Message, name protoreflect.Name, value any) {
	text, ok := value.(string)
	if !ok {
		return
	}
	field := message.Descriptor().Fields().ByName(name)
	message.Set(field, protoreflect.ValueOfString(text))
}

func setSourceURL(message protoreflect.Message, name protoreflect.Name, value any) {
	text, ok := value.(string)
	if !ok {
		return
	}
	field := message.Descriptor().Fields().ByName(name)
	source := message.Mutable(field).Message()
	url := source.Descriptor().Fields().ByName("url")
	source.Set(url, protoreflect.ValueOfString(text))
}

func channelMutationStrings(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		result := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("tag has type %T", item)
			}
			result[index] = text
		}
		return result, nil
	case string:
		return []string{typed}, nil
	default:
		return nil, fmt.Errorf("tags has type %T", value)
	}
}

func transactionQueryBoolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case nil:
		return false
	default:
		return pythonJSONTruthy(typed)
	}
}
