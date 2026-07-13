package wallet

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"lbry/daemon/wallet/keys"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func BuildStreamClaim(existing []byte, replace bool, fields map[string]any) ([]byte, error) {
	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	claim := dynamicpb.NewMessage(descriptor)
	var previous *dynamicpb.Message
	if len(existing) > 0 {
		if existing[0] == 1 {
			if len(existing) < 85 {
				return nil, fmt.Errorf("%w: malformed signed stream", ErrInvalidClaimValue)
			}
			existing = append([]byte{0}, existing[85:]...)
		} else if existing[0] != 0 {
			return nil, fmt.Errorf("%w: unsupported stream wrapper", ErrInvalidClaimValue)
		}
		target := claim
		if replace {
			previous = dynamicpb.NewMessage(descriptor)
			target = previous
		}
		if err := proto.Unmarshal(existing[1:], target); err != nil {
			return nil, err
		}
	}
	streamField := descriptor.Fields().ByName("stream")
	stream := claim.Mutable(streamField).Message()
	if previous != nil && previous.Has(streamField) {
		oldStream := previous.Get(streamField).Message()
		for _, name := range []protoreflect.Name{"source", "image", "video", "audio", "software"} {
			field := oldStream.Descriptor().Fields().ByName(name)
			if field != nil && oldStream.Has(field) {
				proto.Merge(stream.Mutable(field).Message().Interface(), oldStream.Get(field).Message().Interface())
			}
		}
	}
	setOptionalProtoString(claim, "title", fields["title"])
	setOptionalProtoString(claim, "description", fields["description"])
	setSourceURL(claim, "thumbnail", fields["thumbnail_url"])
	setOptionalProtoString(stream, "author", fields["author"])
	setOptionalProtoString(stream, "license", fields["license"])
	setOptionalProtoString(stream, "license_url", fields["license_url"])
	if err := setStreamInteger(stream, "release_time", fields["release_time"]); err != nil {
		return nil, err
	}
	if err := applyClaimLanguages(claim, fields); err != nil {
		return nil, err
	}
	if err := applyClaimLocations(claim, fields); err != nil {
		return nil, err
	}
	if transactionQueryBoolValue(fields["clear_tags"]) {
		claim.Clear(descriptor.Fields().ByName("tags"))
	}
	if raw, ok := fields["tags"]; ok && raw != nil {
		values, parseErr := channelMutationStrings(raw)
		if parseErr != nil {
			return nil, parseErr
		}
		list := claim.Mutable(descriptor.Fields().ByName("tags")).List()
		for _, value := range values {
			list.Append(protoreflect.ValueOfString(value))
		}
	}
	if err := applyStreamSource(stream, fields); err != nil {
		return nil, err
	}
	if err := applyStreamFee(stream, fields); err != nil {
		return nil, err
	}
	encoded, err := marshalClaimProtoMessage(claim)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, encoded...), nil
}

func applyStreamSource(stream protoreflect.Message, fields map[string]any) error {
	source := stream.Mutable(stream.Descriptor().Fields().ByName("source")).Message()
	setOptionalProtoString(source, "name", fields["file_name"])
	setOptionalProtoString(source, "media_type", fields["media_type"])
	for key, fieldName := range map[string]string{"file_hash": "hash", "sd_hash": "sd_hash", "bt_infohash": "bt_infohash"} {
		text, ok := fields[key].(string)
		if !ok {
			continue
		}
		decoded, err := hex.DecodeString(text)
		if err != nil {
			return fmt.Errorf("invalid %s %q", key, text)
		}
		setProtoBytes(source, protoreflect.Name(fieldName), decoded)
	}
	if err := setStreamUnsigned(source, "size", fields["file_size"]); err != nil {
		return err
	}
	mediaType, _ := fields["media_type"].(string)
	if mediaType == "" {
		if name, ok := fields["file_name"].(string); ok {
			mediaType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
			if separator := strings.IndexByte(mediaType, ';'); separator >= 0 {
				mediaType = mediaType[:separator]
			}
			if mediaType != "" {
				setOptionalProtoString(source, "media_type", mediaType)
			}
		}
	}
	kind := strings.SplitN(mediaType, "/", 2)[0]
	if kind != "image" && kind != "video" && kind != "audio" {
		return nil
	}
	media := stream.Mutable(stream.Descriptor().Fields().ByName(protoreflect.Name(kind))).Message()
	if kind != "audio" {
		if err := setStreamUnsigned(media, "width", fields["width"]); err != nil {
			return err
		}
		if err := setStreamUnsigned(media, "height", fields["height"]); err != nil {
			return err
		}
	}
	if kind != "image" {
		if err := setStreamUnsigned(media, "duration", fields["duration"]); err != nil {
			return err
		}
	}
	return nil
}

func applyStreamFee(stream protoreflect.Message, fields map[string]any) error {
	feeField := stream.Descriptor().Fields().ByName("fee")
	if transactionQueryBoolValue(fields["clear_fee"]) {
		stream.Clear(feeField)
		return nil
	}
	amountRaw, amountSet := fields["fee_amount"]
	currencyRaw, currencySet := fields["fee_currency"]
	address, addressSet := fields["fee_address"].(string)
	if !amountSet && !currencySet && !addressSet {
		return nil
	}
	fee := stream.Mutable(feeField).Message()
	if amountSet && transactionQueryBoolValue(amountRaw) {
		currency := ""
		if text, ok := currencyRaw.(string); ok {
			currency = strings.ToUpper(text)
		} else if current := fee.Get(fee.Descriptor().Fields().ByName("currency")).Enum(); current != 0 {
			currency = string(fee.Descriptor().Fields().ByName("currency").Enum().Values().ByNumber(current).Name())
		}
		scale := int64(100_000_000)
		if currency == "USD" {
			scale = 100
		}
		if currency != "LBC" && currency != "BTC" && currency != "USD" {
			return fmt.Errorf("Missing or unknown currency provided: %s", strings.ToLower(currency))
		}
		amount, err := scaledStreamDecimal(fmt.Sprint(amountRaw), scale)
		if err != nil {
			return err
		}
		if err := setEnumName(fee, "currency", protoreflect.Name(currency)); err != nil {
			return err
		}
		fee.Set(fee.Descriptor().Fields().ByName("amount"), protoreflect.ValueOfUint64(amount))
	} else if currencySet {
		return fmt.Errorf("In order to set a fee currency, please specify a fee amount")
	}
	if addressSet && address != "" {
		if fee.Get(fee.Descriptor().Fields().ByName("currency")).Enum() == 0 {
			return fmt.Errorf("In order to set a fee address, please specify a fee amount and currency")
		}
		decoded, err := keys.DecodeBase58(address)
		if err != nil {
			return err
		}
		setProtoBytes(fee, "address", decoded)
	}
	return nil
}

func scaledStreamDecimal(text string, scale int64) (uint64, error) {
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Sign() < 0 {
		return 0, fmt.Errorf("invalid fee amount %q", text)
	}
	value.Mul(value, new(big.Rat).SetInt64(scale))
	integer := new(big.Int).Quo(value.Num(), value.Denom())
	if !integer.IsUint64() {
		return 0, fmt.Errorf("invalid fee amount %q", text)
	}
	return integer.Uint64(), nil
}

func setStreamInteger(message protoreflect.Message, name string, raw any) error {
	if raw == nil {
		return nil
	}
	value, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid %s %q", name, raw)
	}
	message.Set(message.Descriptor().Fields().ByName(protoreflect.Name(name)), protoreflect.ValueOfInt64(value))
	return nil
}

func setStreamUnsigned(message protoreflect.Message, name string, raw any) error {
	if raw == nil {
		return nil
	}
	value, err := strconv.ParseUint(fmt.Sprint(raw), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid %s %q", name, raw)
	}
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	if field.Kind() == protoreflect.Uint32Kind {
		message.Set(field, protoreflect.ValueOfUint32(uint32(value)))
	} else {
		message.Set(field, protoreflect.ValueOfUint64(value))
	}
	return nil
}

func CreateStreamTransaction(
	ctx context.Context, name string, amount uint64, address string, funding []*Account,
	fields map[string]any, channel *TransactionOutput,
) (*Transaction, error) {
	claim, err := BuildStreamClaim(nil, false, fields)
	if err != nil {
		return nil, err
	}
	return createStreamClaimTransaction(ctx, nil, name, "", claim, amount, address, funding, channel)
}

func CreateStreamUpdateTransaction(
	ctx context.Context, previous *TransactionOutput, amount uint64, address string,
	funding []*Account, fields map[string]any, replace bool, channel *TransactionOutput,
) (*Transaction, error) {
	claim, err := BuildStreamClaim(previous.Script.Claim, replace, fields)
	if err != nil {
		return nil, err
	}
	claimID, err := previous.ClaimID()
	if err != nil {
		return nil, err
	}
	return createStreamClaimTransaction(
		ctx, previous, string(previous.Script.ClaimName), claimID, claim, amount, address, funding, channel,
	)
}

func createStreamClaimTransaction(
	ctx context.Context, previous *TransactionOutput, name, claimID string, claim []byte,
	amount uint64, address string, funding []*Account, channel *TransactionOutput,
) (*Transaction, error) {
	if len(funding) == 0 || funding[0] == nil {
		return nil, ErrPurchaseFundingAccount
	}
	var err error
	if channel != nil {
		claim, err = placeholderSignedValue(claim, channel)
		if err != nil {
			return nil, err
		}
	}
	hash, err := transactionChangeAddressHash(address)
	if err != nil {
		return nil, err
	}
	var output TransactionOutput
	var inputs []TransactionInput
	if previous == nil {
		output = NewClaimNameOutput(amount, name, claim, hash)
	} else {
		output, err = NewUpdateClaimOutput(amount, name, claimID, claim, hash)
		if err == nil {
			var input TransactionInput
			input, err = NewSpendInput(previous)
			inputs = []TransactionInput{input}
		}
	}
	if err != nil {
		return nil, err
	}
	transaction, err := CreateTransaction(ctx, inputs, []TransactionOutput{output}, funding, funding[0], channel == nil)
	if err != nil || channel == nil {
		return transaction, err
	}
	if err = finalizeSignedTransaction(ctx, transaction, funding, channel, false); err != nil {
		return nil, releaseFailedSignedTransaction(ctx, funding, transaction, err)
	}
	return transaction, nil
}
