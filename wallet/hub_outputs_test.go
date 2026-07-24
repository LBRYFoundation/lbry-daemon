package wallet

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDecodeHubOutputsBytesProjectsTypedSchema(t *testing.T) {
	channel := hubOutputsTestOutput([]byte{0xaa}, 2, 3, nil)
	repost := hubOutputsTestOutput([]byte{0xbb}, 4, 5, nil)
	claim := hubOutputsTestBytesField(nil, 1, channel)
	claim = hubOutputsTestBytesField(claim, 2, repost)
	claim = hubOutputsTestStringField(claim, 3, "short#1")
	claim = hubOutputsTestStringField(claim, 4, "@channel#2/short#1")
	claim = hubOutputsTestVarintField(claim, 5, 1)
	for field, value := range map[protowire.Number]uint64{
		6: 11, 7: 12, 8: 13, 9: 14, 10: 15, 11: 16, 20: 17, 21: 18,
	} {
		claim = hubOutputsTestVarintField(claim, field, value)
	}
	claim = hubOutputsTestFixed64Field(claim, 22, math.Float64bits(19.5))

	root := hubOutputsTestOutput([]byte{1, 2, 3}, 6, 7,
		hubOutputsTestBytesField(nil, 7, claim))
	errorBlocked := hubOutputsTestVarintField(nil, 1, 8)
	errorBlocked = hubOutputsTestBytesField(errorBlocked, 2,
		hubOutputsTestOutput([]byte{0xcc}, 9, 10, nil))
	errorMessage := hubOutputsTestVarintField(nil, 1, uint64(HubErrorBlocked))
	errorMessage = hubOutputsTestStringField(errorMessage, 2, "blocked result")
	errorMessage = hubOutputsTestBytesField(errorMessage, 3, errorBlocked)
	errorOutput := hubOutputsTestOutput([]byte{9, 8, 7}, 1, 2,
		hubOutputsTestBytesField(nil, 15, errorMessage))

	page := hubOutputsTestBytesField(nil, 1, root)
	page = hubOutputsTestBytesField(page, 1, errorOutput)
	page = hubOutputsTestBytesField(page, 2, channel)
	page = hubOutputsTestVarintField(page, 3, 23)
	page = hubOutputsTestVarintField(page, 4, 4)
	page = hubOutputsTestBytesField(page, 5, errorBlocked)
	page = hubOutputsTestVarintField(page, 6, 8)
	page = hubOutputsTestStringField(page, 99, "ignored")

	outputs, err := DecodeHubOutputsBytes(page)
	if err != nil {
		t.Fatal(err)
	}
	if outputs.Total != 23 || outputs.Offset != 4 || outputs.BlockedTotal != 8 ||
		len(outputs.TXOs) != 2 || len(outputs.ExtraTXOs) != 1 || len(outputs.Blocked) != 1 {
		t.Fatalf("page projection = %#v", outputs)
	}
	got := outputs.TXOs[0]
	if !reflect.DeepEqual(got.TransactionHash, []byte{1, 2, 3}) || got.Position != 6 ||
		got.Height != 7 || got.Error != nil || got.Claim == nil {
		t.Fatalf("root output = %#v", got)
	}
	if got.Claim.ShortURL != "short#1" || got.Claim.CanonicalURL != "@channel#2/short#1" ||
		!got.Claim.IsControlling || got.Claim.TakeOverHeight != 11 ||
		got.Claim.CreationHeight != 12 || got.Claim.ActivationHeight != 13 ||
		got.Claim.ExpirationHeight != 14 || got.Claim.ClaimsInChannel != 15 ||
		got.Claim.Reposted != 16 || got.Claim.EffectiveAmount != 17 ||
		got.Claim.SupportAmount != 18 || got.Claim.TrendingScore != 19.5 ||
		got.Claim.Channel == nil || got.Claim.Channel.Position != 2 ||
		got.Claim.Repost == nil || got.Claim.Repost.Position != 4 {
		t.Fatalf("claim projection = %#v", got.Claim)
	}
	gotError := outputs.TXOs[1].Error
	if gotError == nil || gotError.Code != HubErrorBlocked || gotError.Text != "blocked result" ||
		gotError.Blocked == nil || gotError.Blocked.Count != 8 ||
		gotError.Blocked.Channel == nil || gotError.Blocked.Channel.Position != 9 {
		t.Fatalf("error projection = %#v", gotError)
	}
	if outputs.Blocked[0].Count != 8 || outputs.Blocked[0].Channel == nil {
		t.Fatalf("blocked summary = %#v", outputs.Blocked[0])
	}
}

func TestHubOutputsTransactionRequestsOnlyUseTopLevelNonErrors(t *testing.T) {
	hash := []byte{1, 2, 3}
	plain := hubOutputsTestOutput(hash, 0, 5, nil)
	errorMeta := hubOutputsTestBytesField(nil, 15,
		hubOutputsTestVarintField(nil, 1, uint64(HubErrorNotFound)))
	errorOutput := hubOutputsTestOutput([]byte{4, 5, 6}, 0, 6, errorMeta)
	nested := hubOutputsTestOutput([]byte{7, 8, 9}, 0, 7, nil)
	claim := hubOutputsTestBytesField(nil, 1, nested)
	withNested := hubOutputsTestOutput([]byte{10, 11, 12}, 0, 8,
		hubOutputsTestBytesField(nil, 7, claim))

	page := hubOutputsTestBytesField(nil, 1, plain)
	page = hubOutputsTestBytesField(page, 1, errorOutput)
	page = hubOutputsTestBytesField(page, 1, withNested)
	page = hubOutputsTestBytesField(page, 2, plain)
	page = hubOutputsTestBytesField(page, 2, hubOutputsTestOutput(hash, 0, 9, nil))

	outputs, err := DecodeHubOutputsBytes(page)
	if err != nil {
		t.Fatal(err)
	}
	want := []TransactionFetchRequest{
		{TxID: "030201", Height: 5},
		{TxID: "0c0b0a", Height: 8},
		{TxID: "030201", Height: 9},
	}
	if got := outputs.TransactionRequests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("transaction requests = %#v, want %#v", got, want)
	}
}

func TestDecodeHubOutputsPreservesEmptyMessagesAndOneofOrder(t *testing.T) {
	emptyClaim := hubOutputsTestBytesField(nil, 7, nil)
	claimRelations := hubOutputsTestBytesField(nil, 1, nil)
	claimRelations = hubOutputsTestBytesField(claimRelations, 2, nil)
	claimWithEmptyRelations := hubOutputsTestBytesField(nil, 7, claimRelations)
	errorMeta := hubOutputsTestBytesField(nil, 15, nil)

	page := hubOutputsTestBytesField(nil, 1,
		hubOutputsTestOutput(nil, 0, 0, emptyClaim))
	page = hubOutputsTestBytesField(page, 1,
		hubOutputsTestOutput(nil, 0, 0, claimWithEmptyRelations))
	page = hubOutputsTestBytesField(page, 1,
		hubOutputsTestOutput(nil, 0, 0, append(emptyClaim, errorMeta...)))
	page = hubOutputsTestBytesField(page, 1,
		hubOutputsTestOutput(nil, 0, 0, append(errorMeta, emptyClaim...)))

	outputs, err := DecodeHubOutputsBytes(page)
	if err != nil {
		t.Fatal(err)
	}
	if outputs.TXOs[0].Claim == nil || outputs.TXOs[0].Error != nil {
		t.Fatalf("empty claim presence = %#v", outputs.TXOs[0])
	}
	if outputs.TXOs[1].Claim == nil || outputs.TXOs[1].Claim.Channel == nil ||
		outputs.TXOs[1].Claim.Repost == nil {
		t.Fatalf("empty relation presence = %#v", outputs.TXOs[1])
	}
	if outputs.TXOs[2].Error == nil || outputs.TXOs[2].Claim != nil {
		t.Fatalf("claim then error oneof = %#v", outputs.TXOs[2])
	}
	if outputs.TXOs[3].Claim == nil || outputs.TXOs[3].Error != nil {
		t.Fatalf("error then claim oneof = %#v", outputs.TXOs[3])
	}
}

func TestDecodeHubOutputsProtobufAndBase64Boundaries(t *testing.T) {
	wrapped := hubOutputsTestVarintField(nil, 3, uint64(math.MaxUint32)+2)
	outputs, err := DecodeHubOutputsBytes(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if outputs.Total != 1 {
		t.Fatalf("overflowed uint32 total = %d, want 1", outputs.Total)
	}

	invalidUTF8 := hubOutputsTestBytesField(nil, 1,
		hubOutputsTestOutput(nil, 0, 0, hubOutputsTestBytesField(nil, 7,
			hubOutputsTestBytesField(nil, 3, []byte{0xff}))))
	_, err = DecodeHubOutputsBytes(invalidUTF8)
	var unicodeError *HubOutputsDecodeError
	if !errors.As(err, &unicodeError) ||
		unicodeError.PythonErrorName() != "UnicodeDecodeError" {
		t.Fatalf("invalid UTF-8 error = %#v", err)
	}

	for name, raw := range map[string][]byte{
		"truncated":       {0x0a, 0x02, 0x01},
		"eleven byte int": append([]byte{0x18}, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeHubOutputsBytes(raw)
			if !errors.Is(err, ErrInvalidHubOutputs) {
				t.Fatalf("error = %v, want ErrInvalidHubOutputs", err)
			}
			var decodeError *HubOutputsDecodeError
			if !errors.As(err, &decodeError) || decodeError.PythonErrorName() != "DecodeError" {
				t.Fatalf("typed error = %#v", err)
			}
		})
	}

	empty, err := DecodeHubOutputsBase64("!!!!")
	if err != nil || len(empty.TXOs) != 0 || empty.Total != 0 {
		t.Fatalf("junk-only base64 = %#v, %v", empty, err)
	}
	encoded := base64.StdEncoding.EncodeToString(hubOutputsTestVarintField(nil, 3, 42))
	decoded, err := DecodeHubOutputsBase64(" \n" + encoded + "\t!!")
	if err != nil || decoded.Total != 42 {
		t.Fatalf("noisy base64 = %#v, %v", decoded, err)
	}
	_, err = DecodeHubOutputsBase64("YQ")
	var base64Error *HubOutputsBase64DecodeError
	if !errors.Is(err, ErrInvalidHubOutputsBase64) ||
		!errors.As(err, &base64Error) || base64Error.PythonErrorName() != "Error" ||
		err.Error() != "Incorrect padding" {
		t.Fatalf("missing-padding error = %T %v", err, err)
	}
	_, err = DecodeHubOutputsBase64("A")
	if !errors.As(err, &base64Error) || err.Error() !=
		"Invalid base64-encoded string: number of data characters (1) cannot be 1 more than a multiple of 4" {
		t.Fatalf("invalid-length error = %T %v", err, err)
	}
}

func TestHubErrorCodeNames(t *testing.T) {
	want := []string{"UNKNOWN_CODE", "NOT_FOUND", "INVALID", "BLOCKED"}
	for code, name := range want {
		got, err := HubErrorCode(code).Name()
		if err != nil || got != name {
			t.Fatalf("code %d name = %q, %v", code, got, err)
		}
	}
	if _, err := HubErrorCode(99).Name(); err == nil {
		t.Fatal("unknown enum code unexpectedly has a name")
	}
}

func hubOutputsTestOutput(
	hash []byte, position uint32, height uint32, meta []byte,
) []byte {
	message := hubOutputsTestBytesField(nil, 1, hash)
	message = hubOutputsTestVarintField(message, 2, uint64(position))
	message = hubOutputsTestVarintField(message, 3, uint64(height))
	return append(message, meta...)
}

func hubOutputsTestBytesField(
	message []byte, field protowire.Number, value []byte,
) []byte {
	message = protowire.AppendTag(message, field, protowire.BytesType)
	return protowire.AppendBytes(message, value)
}

func hubOutputsTestStringField(
	message []byte, field protowire.Number, value string,
) []byte {
	return hubOutputsTestBytesField(message, field, []byte(value))
}

func hubOutputsTestVarintField(
	message []byte, field protowire.Number, value uint64,
) []byte {
	message = protowire.AppendTag(message, field, protowire.VarintType)
	return protowire.AppendVarint(message, value)
}

func hubOutputsTestFixed64Field(
	message []byte, field protowire.Number, value uint64,
) []byte {
	message = protowire.AppendTag(message, field, protowire.Fixed64Type)
	return protowire.AppendFixed64(message, value)
}
