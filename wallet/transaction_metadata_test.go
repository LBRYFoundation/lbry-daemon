package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestTransactionOutputTypeValuesMatchPinnedDatabase(t *testing.T) {
	got := []int64{
		TransactionOutputTypeOther,
		TransactionOutputTypeStream,
		TransactionOutputTypeChannel,
		TransactionOutputTypeSupport,
		TransactionOutputTypePurchase,
		TransactionOutputTypeCollection,
		TransactionOutputTypeRepost,
	}
	want := []int64{0, 1, 2, 3, 4, 5, 6}
	if !equalMetadataInt64s(got, want) {
		t.Fatalf("transaction output types = %v, want %v", got, want)
	}
}

func TestProjectTransactionMetadataV2ClaimTypes(t *testing.T) {
	source := protowire.AppendTag(nil, 1, protowire.BytesType)
	source = protowire.AppendBytes(source, nil)
	reference := protowire.AppendTag(nil, 1, protowire.BytesType)
	reference = protowire.AppendBytes(reference, []byte{1, 2, 3})
	invalidStream := protowire.AppendTag(nil, 1, protowire.BytesType)
	invalidStream = protowire.AppendBytes(invalidStream, []byte{0x0a})
	invalidUTF8 := protowire.AppendTag(nil, 8, protowire.BytesType)
	invalidUTF8 = protowire.AppendBytes(invalidUTF8, []byte{0xff})
	mergedStream := append([]byte{0}, metadataV2Message(1, source)...)
	mergedStream = append(mergedStream, metadataV2Message(1, nil)...)
	resetStream := append([]byte{0}, metadataV2Message(1, source)...)
	resetStream = append(resetStream, metadataV2Message(2, nil)...)
	resetStream = append(resetStream, metadataV2Message(1, nil)...)
	mergedRepost := append([]byte{0}, metadataV2Message(4, reference)...)
	mergedRepost = append(mergedRepost, metadataV2Message(4, nil)...)

	tests := []struct {
		name              string
		claim             []byte
		wantType          int64
		wantSource        bool
		wantRepostedClaim *string
	}{
		{"unset", []byte{0}, TransactionOutputTypeStream, false, nil},
		{"stream without source", metadataV2Claim(1, nil), TransactionOutputTypeStream, false, nil},
		{"stream with source", metadataV2Claim(1, source), TransactionOutputTypeStream, true, nil},
		{"merged stream retains source", mergedStream, TransactionOutputTypeStream, true, nil},
		{"oneof switch clears stream", resetStream, TransactionOutputTypeStream, false, nil},
		{"channel", metadataV2Claim(2, nil), TransactionOutputTypeChannel, false, nil},
		{"collection", metadataV2Claim(3, nil), TransactionOutputTypeCollection, false, nil},
		{"repost without reference", metadataV2Claim(4, nil), TransactionOutputTypeRepost, true, metadataString("")},
		{"repost", metadataV2Claim(4, reference), TransactionOutputTypeRepost, true, metadataString("030201")},
		{"merged repost retains reference", mergedRepost, TransactionOutputTypeRepost, true, metadataString("030201")},
		{"invalid nested message", append([]byte{0}, invalidStream...), TransactionOutputTypeStream, false, nil},
		{"invalid claim utf8", append([]byte{0}, invalidUTF8...), TransactionOutputTypeStream, false, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := metadataClaimOutput(2, []byte("name"), test.claim)
			got, err := ProjectTransactionOutputMetadata(&output, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.TXOType != test.wantType || got.HasSource != test.wantSource {
				t.Fatalf("metadata = type %d, source %v; want type %d, source %v",
					got.TXOType, got.HasSource, test.wantType, test.wantSource)
			}
			assertMetadataString(t, "reposted claim ID", got.RepostedClaimID, test.wantRepostedClaim)
			assertMetadataString(t, "claim ID", got.ClaimID, metadataString("bbaa"))
			assertMetadataString(t, "claim name", got.ClaimName, metadataString("name"))
		})
	}
}

func TestProjectTransactionMetadataSignedV2Claim(t *testing.T) {
	signingHash := make([]byte, 20)
	for index := range signingHash {
		signingHash[index] = byte(index + 1)
	}
	claim := metadataSignedPayload(signingHash, metadataV2Message(2, nil))
	output := metadataClaimOutput(0, []byte("signed"), claim)

	got, err := ProjectTransactionOutputMetadata(&output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeChannel {
		t.Fatalf("txo type = %d, want channel", got.TXOType)
	}
	wantChannel := hex.EncodeToString(reverseTransactionBytes(signingHash))
	assertMetadataString(t, "channel ID", got.ChannelID, &wantChannel)

	invalid := metadataSignedPayload(signingHash, []byte{0x0a})
	output = metadataClaimOutput(0, []byte("invalid"), invalid)
	got, err = ProjectTransactionOutputMetadata(&output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeStream || got.ChannelID != nil {
		t.Fatalf("invalid signed claim = type %d, channel %#v; want stream, nil", got.TXOType, got.ChannelID)
	}
}

func TestProjectTransactionMetadataLegacySignatureOnlyAppliesToStream(t *testing.T) {
	signature := protowire.AppendTag(nil, 4, protowire.BytesType)
	signature = protowire.AppendBytes(signature, []byte{0x10, 0x20})

	legacyClaim := func(claimType uint64) []byte {
		message := protowire.AppendTag(nil, 1, protowire.VarintType)
		message = protowire.AppendVarint(message, 1)
		message = protowire.AppendTag(message, 2, protowire.VarintType)
		message = protowire.AppendVarint(message, claimType)
		message = protowire.AppendTag(message, 5, protowire.BytesType)
		return protowire.AppendBytes(message, signature)
	}

	stream := metadataClaimOutput(0, []byte("legacy"), legacyClaim(1))
	got, err := ProjectTransactionOutputMetadata(&stream, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeStream || !got.HasSource {
		t.Fatalf("legacy stream = type %d, source %v; want stream with source", got.TXOType, got.HasSource)
	}
	assertMetadataString(t, "legacy stream channel ID", got.ChannelID, metadataString("1020"))

	channel := metadataClaimOutput(0, []byte("legacy-channel"), legacyClaim(2))
	got, err = ProjectTransactionOutputMetadata(&channel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeChannel || got.ChannelID != nil {
		t.Fatalf("legacy channel = type %d, channel ID %#v; want channel without signing channel",
			got.TXOType, got.ChannelID)
	}
}

func TestProjectTransactionMetadataLegacySignedStreamRequiresInitializedRemainder(t *testing.T) {
	signature := protowire.AppendTag(nil, 4, protowire.BytesType)
	signature = protowire.AppendBytes(signature, []byte{0x10, 0x20})
	appendSignature := func(message []byte) []byte {
		message = protowire.AppendTag(message, 5, protowire.BytesType)
		return protowire.AppendBytes(message, signature)
	}
	appendClaimType := func(message []byte) []byte {
		message = protowire.AppendTag(message, 2, protowire.VarintType)
		return protowire.AppendVarint(message, 1)
	}

	missingVersion := appendSignature(appendClaimType(nil))
	output := metadataClaimOutput(0, []byte("missing-version"), missingVersion)
	got, err := ProjectTransactionOutputMetadata(&output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeStream || got.HasSource || got.ChannelID != nil {
		t.Fatalf("signed stream missing version = %#v, want undecodable stream fallback", got)
	}

	incompleteStream := protowire.AppendTag(nil, 1, protowire.VarintType)
	incompleteStream = protowire.AppendVarint(incompleteStream, 1)
	incompleteStream = appendClaimType(incompleteStream)
	incompleteStream = protowire.AppendTag(incompleteStream, 3, protowire.BytesType)
	incompleteStream = protowire.AppendBytes(incompleteStream, nil)
	output = metadataClaimOutput(0, []byte("incomplete-stream"), appendSignature(incompleteStream))
	got, err = ProjectTransactionOutputMetadata(&output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasSource || got.ChannelID != nil {
		t.Fatalf("signed stream with uninitialized nested message = %#v, want fallback", got)
	}

	unsigned := appendClaimType(nil)
	unsigned = protowire.AppendTag(unsigned, 3, protowire.BytesType)
	unsigned = protowire.AppendBytes(unsigned, nil)
	output = metadataClaimOutput(0, []byte("unsigned"), unsigned)
	got, err = ProjectTransactionOutputMetadata(&output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasSource || got.ChannelID != nil {
		t.Fatalf("unsigned uninitialized stream = %#v, want decoded source without channel", got)
	}
}

func TestProjectTransactionMetadataLegacyV1FeeConversionFailures(t *testing.T) {
	tests := []struct {
		name       string
		currency   uint64
		amount     float32
		wantSource bool
	}{
		{"lbc", 1, 1.25, true},
		{"btc", 2, 0.5, true},
		{"usd rounds away", 3, 0.001, true},
		{"unknown currency", 0, 1, false},
		{"tiny negative lbc truncates to zero", 1, -1e-10, true},
		{"negative lbc", 1, -1, false},
		{"negative usd", 3, -0.001, false},
		{"nan", 1, float32(math.NaN()), false},
		{"infinity", 1, float32(math.Inf(1)), false},
		{"overflow", 1, math.MaxFloat32, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := metadataLegacyV1FeeClaim(test.currency, test.amount)
			output := metadataClaimOutput(0, []byte("legacy-fee"), claim)
			got, err := ProjectTransactionOutputMetadata(&output, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.TXOType != TransactionOutputTypeStream || got.HasSource != test.wantSource {
				t.Fatalf("legacy fee metadata = type %d, source %v; want stream, source %v",
					got.TXOType, got.HasSource, test.wantSource)
			}
		})
	}
}

func TestProjectTransactionMetadataLegacyJSONSingleCurrencyFee(t *testing.T) {
	tests := []struct {
		name       string
		fee        string
		wantSource bool
	}{
		{"lbc string", `"LBC":{"amount":"1","address":"1"}`, true},
		{"lbc number", `"LBC":{"amount":1.25,"address":"123"}`, true},
		{"lbc boolean", `"LBC":{"amount":true,"address":"1"}`, true},
		{"lbc uint64 maximum", `"LBC":{"amount":"184467440737.09551615","address":"1"}`, true},
		{"usd rounds away", `"USD":{"amount":"0.001","address":"1"}`, true},
		{"invalid decimal", `"LBC":{"amount":"not-a-number","address":"1"}`, false},
		{"lbc overflow", `"LBC":{"amount":"184467440737.09551616","address":"1"}`, false},
		{"negative lbc", `"LBC":{"amount":"-1","address":"1"}`, false},
		{"negative usd", `"USD":{"amount":"-0.001","address":"1"}`, false},
		{"empty address", `"LBC":{"amount":"1","address":""}`, false},
		{"invalid base58 zero", `"LBC":{"amount":"1","address":"0"}`, false},
		{"invalid base58 letter", `"LBC":{"amount":"1","address":"O"}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := []byte(`{"sources":{"lbry_sd_hash":"00"},"fee":{` + test.fee + `}}`)
			output := metadataClaimOutput(0, []byte("legacy-json"), claim)
			got, err := ProjectTransactionOutputMetadata(&output, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.TXOType != TransactionOutputTypeStream || got.HasSource != test.wantSource {
				t.Fatalf("legacy JSON metadata = type %d, source %v; want stream, source %v",
					got.TXOType, got.HasSource, test.wantSource)
			}
		})
	}
}

func TestProjectTransactionMetadataSupport(t *testing.T) {
	signingHash := bytes.Repeat([]byte{0x24}, 20)
	message := protowire.AppendTag(nil, 2, protowire.BytesType)
	message = protowire.AppendBytes(message, []byte("good support"))
	valid := metadataSupportOutput(metadataSignedPayload(signingHash, message))

	got, err := ProjectTransactionOutputMetadata(&valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeSupport {
		t.Fatalf("txo type = %d, want support", got.TXOType)
	}
	wantChannel := hex.EncodeToString(reverseTransactionBytes(signingHash))
	assertMetadataString(t, "channel ID", got.ChannelID, &wantChannel)
	assertMetadataString(t, "claim ID", got.ClaimID, metadataString("0201"))

	badMessage := protowire.AppendTag(nil, 2, protowire.BytesType)
	badMessage = protowire.AppendBytes(badMessage, []byte{0xff})
	invalid := metadataSupportOutput(metadataSignedPayload(signingHash, badMessage))
	got, err = ProjectTransactionOutputMetadata(&invalid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TXOType != TransactionOutputTypeSupport || got.ChannelID != nil {
		t.Fatalf("invalid support = type %d, channel %#v; want support, nil", got.TXOType, got.ChannelID)
	}

	plain := metadataSupportOutput(nil)
	plain.Script.Template = TransactionScriptSupportPubKey
	got, err = ProjectTransactionOutputMetadata(&plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChannelID != nil {
		t.Fatalf("plain support channel ID = %q, want nil", *got.ChannelID)
	}
}

func TestProjectTransactionMetadataPurchaseLinksOutputZero(t *testing.T) {
	claimHash := []byte{0x10, 0x20, 0x30, 0x40}
	purchase := protowire.AppendTag([]byte{'P'}, 1, protowire.BytesType)
	purchase = protowire.AppendBytes(purchase, claimHash)
	transaction := &Transaction{Outputs: []TransactionOutput{
		{Position: 0, Script: TransactionOutputScript{Template: TransactionScriptPayPubKeyHash}},
		{Position: 1, Script: TransactionOutputScript{Template: TransactionScriptReturnData, Data: purchase}},
	}}

	got := ProjectTransactionMetadata(transaction)
	wantClaimID := "40302010"
	assertMetadataString(t, "transaction purchased claim ID", got.PurchasedClaimID, &wantClaimID)
	if got.Outputs[0].TXOType != TransactionOutputTypePurchase {
		t.Fatalf("output zero type = %d, want purchase", got.Outputs[0].TXOType)
	}
	assertMetadataString(t, "output zero claim ID", got.Outputs[0].ClaimID, &wantClaimID)
	if got.Outputs[1].TXOType != TransactionOutputTypeOther || got.Outputs[1].ClaimID != nil {
		t.Fatalf("purchase-data output metadata = %#v, want other without claim ID", got.Outputs[1])
	}

	transaction.Outputs[1].Script.Data = []byte{'P', 0x0a}
	got = ProjectTransactionMetadata(transaction)
	if got.PurchasedClaimID != nil || got.Outputs[0].TXOType != TransactionOutputTypeOther {
		t.Fatalf("invalid purchase metadata = %#v, want no purchase annotation", got)
	}

	transaction.Outputs[1].Script.Data = []byte{'P'}
	got = ProjectTransactionMetadata(transaction)
	assertMetadataString(t, "empty purchase claim ID", got.PurchasedClaimID, metadataString(""))
	if got.Outputs[0].TXOType != TransactionOutputTypePurchase {
		t.Fatalf("empty purchase output type = %d, want purchase", got.Outputs[0].TXOType)
	}
}

func TestProjectTransactionMetadataPurchaseDoesNotReplaceClaimID(t *testing.T) {
	purchase := protowire.AppendTag([]byte{'P'}, 1, protowire.BytesType)
	purchase = protowire.AppendBytes(purchase, []byte{0x99})
	claim := metadataClaimOutput(0, []byte("claim"), metadataV2Claim(1, nil))
	transaction := &Transaction{Outputs: []TransactionOutput{
		claim,
		{Position: 1, Script: TransactionOutputScript{Template: TransactionScriptReturnData, Data: purchase}},
	}}

	got := ProjectTransactionMetadata(transaction)
	assertMetadataString(t, "transaction purchased claim ID", got.PurchasedClaimID, metadataString("99"))
	if got.Outputs[0].TXOType != TransactionOutputTypeStream {
		t.Fatalf("claim output type = %d, want stream", got.Outputs[0].TXOType)
	}
	assertMetadataString(t, "claim output own claim ID", got.Outputs[0].ClaimID, metadataString("bbaa"))
}

func TestProjectTransactionMetadataRetainsClaimNameUTF8ErrorPerOutput(t *testing.T) {
	invalid := metadataClaimOutput(7, []byte{0xff}, metadataV2Claim(1, nil))
	valid := TransactionOutput{
		Position: 8,
		Script:   TransactionOutputScript{Template: TransactionScriptPayPubKeyHash},
	}
	transaction := &Transaction{Outputs: []TransactionOutput{invalid, valid}}

	got := ProjectTransactionMetadata(transaction)
	if len(got.Outputs) != 2 || !errors.Is(got.Outputs[0].Err, ErrInvalidTransactionClaimName) {
		t.Fatalf("invalid output error = %#v, want ErrInvalidTransactionClaimName", got.Outputs[0].Err)
	}
	if got.Outputs[1].Err != nil {
		t.Fatalf("unrelated output error = %v, want nil", got.Outputs[1].Err)
	}
	if _, err := ProjectTransactionOutputMetadata(&invalid, nil); !errors.Is(err, ErrInvalidTransactionClaimName) {
		t.Fatalf("selected output error = %v, want ErrInvalidTransactionClaimName", err)
	}
}

func metadataClaimOutput(position uint32, name, claim []byte) TransactionOutput {
	return TransactionOutput{
		Position: position,
		Script: TransactionOutputScript{
			Template:  TransactionScriptUpdatePubKey,
			ClaimName: append([]byte(nil), name...),
			ClaimID:   []byte{0xaa, 0xbb},
			Claim:     append([]byte(nil), claim...),
		},
	}
}

func metadataSupportOutput(support []byte) TransactionOutput {
	return TransactionOutput{
		Position: 1,
		Script: TransactionOutputScript{
			Template:       TransactionScriptSupportDataKey,
			ClaimName:      []byte("supported"),
			ClaimID:        []byte{1, 2},
			Support:        append([]byte(nil), support...),
			HasSupportData: true,
		},
	}
}

func metadataLegacyV1FeeClaim(currency uint64, amount float32) []byte {
	fee := protowire.AppendTag(nil, 2, protowire.VarintType)
	fee = protowire.AppendVarint(fee, currency)
	fee = protowire.AppendTag(fee, 4, protowire.Fixed32Type)
	fee = protowire.AppendFixed32(fee, math.Float32bits(amount))
	metadata := protowire.AppendTag(nil, 8, protowire.BytesType)
	metadata = protowire.AppendBytes(metadata, fee)
	stream := protowire.AppendTag(nil, 2, protowire.BytesType)
	stream = protowire.AppendBytes(stream, metadata)
	claim := protowire.AppendTag(nil, 2, protowire.VarintType)
	claim = protowire.AppendVarint(claim, 1)
	claim = protowire.AppendTag(claim, 3, protowire.BytesType)
	return protowire.AppendBytes(claim, stream)
}

func metadataV2Claim(field protowire.Number, nested []byte) []byte {
	return append([]byte{0}, metadataV2Message(field, nested)...)
}

func metadataV2Message(field protowire.Number, nested []byte) []byte {
	message := protowire.AppendTag(nil, field, protowire.BytesType)
	return protowire.AppendBytes(message, nested)
}

func metadataSignedPayload(signingHash, message []byte) []byte {
	payload := []byte{1}
	payload = append(payload, signingHash...)
	payload = append(payload, make([]byte, 64)...)
	return append(payload, message...)
}

func metadataString(value string) *string {
	return &value
}

func assertMetadataString(t *testing.T, name string, got, want *string) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s = %q, want %q", name, *got, *want)
	}
}

func equalMetadataInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
