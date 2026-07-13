package wallet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	TransactionOutputTypeOther      int64 = 0
	TransactionOutputTypeStream     int64 = 1
	TransactionOutputTypeChannel    int64 = 2
	TransactionOutputTypeSupport    int64 = 3
	TransactionOutputTypePurchase   int64 = 4
	TransactionOutputTypeCollection int64 = 5
	TransactionOutputTypeRepost     int64 = 6
)

var ErrInvalidTransactionClaimName = errors.New("invalid transaction claim name")

// TransactionMetadata is the wallet-owned portion of Database.tx_to_row and
// Database.txo_to_row. Persistence code can consume it without knowing how
// claim, support, or purchase payloads are encoded.
type TransactionMetadata struct {
	PurchasedClaimID *string
	Outputs          []TransactionOutputMetadata
}

type TransactionOutputMetadata struct {
	TXOType         int64
	ClaimID         *string
	ClaimName       *string
	HasSource       bool
	ChannelID       *string
	RepostedClaimID *string
	Err             error
}

// ProjectTransactionMetadata performs the transaction-wide purchase lookup
// and retains output-local errors on the corresponding projection. The
// database only projects selected wallet outputs, so an invalid external
// output must not abort address synchronization.
func ProjectTransactionMetadata(transaction *Transaction) TransactionMetadata {
	metadata := TransactionMetadata{
		Outputs: make([]TransactionOutputMetadata, len(transaction.Outputs)),
	}

	var purchasedClaimID *string
	if len(transaction.Outputs) >= 2 {
		if claimID, ok := decodeTransactionPurchase(transaction.Outputs[1].Script); ok {
			metadata.PurchasedClaimID = transactionMetadataStringPointer(claimID)
			purchasedClaimID = metadata.PurchasedClaimID
		}
	}

	for index := range transaction.Outputs {
		linkedPurchase := (*string)(nil)
		if index == 0 {
			linkedPurchase = purchasedClaimID
		}
		projected, err := ProjectTransactionOutputMetadata(
			&transaction.Outputs[index], linkedPurchase,
		)
		projected.Err = err
		metadata.Outputs[index] = projected
	}
	return metadata
}

// ProjectTransactionOutputMetadata mirrors Database.txo_to_row for one output.
// Claim and support decode failures are intentionally non-fatal because
// Output.can_decode_claim/support catches them. Claim-name UTF-8 failures
// remain visible because Output.claim_name.decode is outside those guards.
func ProjectTransactionOutputMetadata(
	output *TransactionOutput, purchasedClaimID *string,
) (TransactionOutputMetadata, error) {
	projected := TransactionOutputMetadata{TXOType: TransactionOutputTypeOther}
	if purchasedClaimID != nil {
		projected.TXOType = TransactionOutputTypePurchase
		projected.ClaimID = transactionMetadataStringPointer(*purchasedClaimID)
	}

	switch {
	case output.Script.IsClaimName(), output.Script.IsUpdateClaim():
		projected.TXOType = TransactionOutputTypeStream
		projected.ClaimID = nil
		claim, ok := decodeTransactionClaim(output.Script.Claim)
		if ok {
			projected.TXOType = claim.TXOType
			projected.HasSource = claim.HasSource
			projected.ChannelID = claim.ChannelID
			projected.RepostedClaimID = claim.RepostedClaimID
		}
	case output.Script.IsSupportClaim():
		projected.TXOType = TransactionOutputTypeSupport
		projected.ClaimID = nil
		if output.Script.IsSupportData() {
			if channelID, ok := decodeTransactionSupport(output.Script.Support); ok {
				projected.ChannelID = channelID
			}
		}
	}

	if output.Script.IsClaimInvolved() {
		claimID, err := output.ClaimID()
		if err != nil {
			return TransactionOutputMetadata{}, err
		}
		if !utf8.Valid(output.Script.ClaimName) {
			return TransactionOutputMetadata{}, fmt.Errorf(
				"%w at output %d", ErrInvalidTransactionClaimName, output.Position,
			)
		}
		claimName := string(output.Script.ClaimName)
		projected.ClaimID = transactionMetadataStringPointer(claimID)
		projected.ClaimName = transactionMetadataStringPointer(claimName)
	}
	return projected, nil
}

type decodedTransactionClaim struct {
	TXOType         int64
	HasSource       bool
	ChannelID       *string
	RepostedClaimID *string
}

func decodeTransactionClaim(payload []byte) (decodedTransactionClaim, bool) {
	if len(payload) == 0 {
		return decodedTransactionClaim{}, false
	}
	if payload[0] == 0 || payload[0] == 1 {
		message, signingHash, signed := splitTransactionSignable(payload)
		claim, err := decodeV2TransactionClaim(message)
		if err != nil {
			return decodedTransactionClaim{}, false
		}
		if signed && len(signingHash) > 0 {
			claim.ChannelID = transactionMetadataStringPointer(reverseTransactionHex(signingHash))
		}
		return claim, true
	}

	if payload[0] == '{' {
		if !utf8.Valid(payload) {
			return decodedTransactionClaim{}, false
		}
		if _, _, err := decodeLegacyJSONClaim(payload); err != nil {
			return decodedTransactionClaim{}, false
		}
		return decodedTransactionClaim{
			TXOType:   TransactionOutputTypeStream,
			HasSource: true,
		}, true
	}

	_, claimType, err := decodeV1ChannelClaim(payload)
	if err != nil {
		return decodedTransactionClaim{}, false
	}
	claim := decodedTransactionClaim{TXOType: TransactionOutputTypeStream}
	if claimType == channelClaimTypeChannel {
		claim.TXOType = TransactionOutputTypeChannel
	} else {
		// compat.from_types_v1 always materializes Stream.source, even when the
		// old source submessage was absent.
		if !legacyV1TransactionStreamConversionValid(payload) {
			return decodedTransactionClaim{}, false
		}
		claim.HasSource = true
		// The v1 compatibility converter only copies publisherSignature in its
		// stream branch; a publisher signature on a channel claim is ignored.
		if signingChannelID, signed := decodeV1TransactionSigningChannel(payload); signed {
			// compat.from_types_v1 clears publisherSignature and serializes the
			// remaining proto2 message into unsigned_payload. Serialization checks
			// every required field still present in the message tree.
			if !legacyV1TransactionInitializedWithoutSignature(payload) {
				return decodedTransactionClaim{}, false
			}
			claim.ChannelID = signingChannelID
		}
	}
	return claim, true
}

func decodeV2TransactionClaim(message []byte) (decodedTransactionClaim, error) {
	// Reuse the source-pinned protobuf validator used by channel-key decoding.
	// Its return type has special Claim.channel accessor semantics, so metadata
	// type and oneof merge state are collected separately below.
	if _, _, err := decodeV2ChannelClaim(message); err != nil {
		return decodedTransactionClaim{}, err
	}

	claim := decodedTransactionClaim{TXOType: TransactionOutputTypeStream}
	var currentType protowire.Number
	var repostClaimHash []byte
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number >= 1 && number <= 4 && wireType == protowire.BytesType {
			nested, _ := protowire.ConsumeBytes(value)
			if currentType != number {
				claim.HasSource = false
				repostClaimHash = nil
			}
			currentType = number
			switch number {
			case 1:
				claim.TXOType = TransactionOutputTypeStream
				claim.HasSource = claim.HasSource || v2StreamHasSource(nested)
			case 2:
				claim.TXOType = TransactionOutputTypeChannel
			case 3:
				claim.TXOType = TransactionOutputTypeCollection
			case 4:
				claim.TXOType = TransactionOutputTypeRepost
				repostClaimHash = mergeV2TransactionClaimReference(nested, repostClaimHash)
				claim.HasSource = true
			}
		}
		message = value[valueLength:]
	}
	if currentType == 4 {
		claim.RepostedClaimID = transactionMetadataStringPointer(reverseTransactionHex(repostClaimHash))
	}
	return claim, nil
}

func v2StreamHasSource(message []byte) bool {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number == 1 && wireType == protowire.BytesType {
			return true
		}
		message = value[valueLength:]
	}
	return false
}

func mergeV2TransactionClaimReference(message, claimHash []byte) []byte {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number == 1 && wireType == protowire.BytesType {
			decoded, _ := protowire.ConsumeBytes(value)
			claimHash = append(claimHash[:0], decoded...)
		}
		message = value[valueLength:]
	}
	return claimHash
}

func decodeTransactionSupport(payload []byte) (*string, bool) {
	if len(payload) == 0 || (payload[0] != 0 && payload[0] != 1) {
		return nil, false
	}
	message, signingHash, signed := splitTransactionSignable(payload)
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, false
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return nil, false
		}
		if (number == 1 || number == 2) && wireType == protowire.BytesType {
			encoded, length := protowire.ConsumeBytes(value)
			if length < 0 || !utf8.Valid(encoded) {
				return nil, false
			}
		}
		message = value[valueLength:]
	}
	if signed && len(signingHash) > 0 {
		return transactionMetadataStringPointer(reverseTransactionHex(signingHash)), true
	}
	return nil, true
}

func decodeTransactionPurchase(script TransactionOutputScript) (string, bool) {
	if script.Template != TransactionScriptReturnData || len(script.Data) == 0 || script.Data[0] != 'P' {
		return "", false
	}
	message := script.Data[1:]
	var claimHash []byte
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return "", false
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return "", false
		}
		if number == 1 && wireType == protowire.BytesType {
			decoded, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return "", false
			}
			claimHash = append(claimHash[:0], decoded...)
		}
		message = value[valueLength:]
	}
	return reverseTransactionHex(claimHash), true
}

func splitTransactionSignable(payload []byte) (message, signingHash []byte, signed bool) {
	if payload[0] == 0 {
		return payload[1:], nil, false
	}
	if len(payload) > 1 {
		end := len(payload)
		if end > 21 {
			end = 21
		}
		signingHash = payload[1:end]
	}
	if len(payload) > 85 {
		message = payload[85:]
	}
	return message, signingHash, true
}

func decodeV1TransactionSigningChannel(message []byte) (*string, bool) {
	var certificateID []byte
	signed := false
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number == 5 && wireType == protowire.BytesType {
			signed = true
			nested, _ := protowire.ConsumeBytes(value)
			certificateID = mergeV1TransactionSignature(nested, certificateID)
		}
		message = value[valueLength:]
	}
	if !signed || len(certificateID) == 0 {
		return nil, signed
	}
	// compat.from_types_v1 reverses certificateId into signing_channel_hash;
	// Signable.signing_channel_id reverses it back for display.
	return transactionMetadataStringPointer(hex.EncodeToString(certificateID)), true
}

func legacyV1TransactionStreamConversionValid(message []byte) bool {
	stream, streamPresent := legacyV1MergedMessageField(message, 3)
	if !streamPresent {
		return true
	}
	metadata, metadataPresent := legacyV1MergedMessageField(stream, 2)
	if !metadataPresent {
		return true
	}
	fee, feePresent := legacyV1MergedMessageField(metadata, 8)
	if !feePresent {
		return true
	}

	currency := uint64(0)
	amount := float32(0)
	for len(fee) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(fee)
		value := fee[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		switch {
		case number == 2 && wireType == protowire.VarintType:
			decoded, _ := protowire.ConsumeVarint(value)
			// Unknown proto2 enum values remain unknown fields and do not replace
			// the last recognized currency value.
			if decoded <= 3 {
				currency = decoded
			}
		case number == 4 && wireType == protowire.Fixed32Type:
			bits, _ := protowire.ConsumeFixed32(value)
			amount = math.Float32frombits(bits)
		}
		fee = value[valueLength:]
	}
	if currency < 1 || currency > 3 {
		return false
	}
	scale, roundAway := int64(100_000_000), false
	if currency == 3 {
		scale, roundAway = 100, true
	}
	return legacyScaledFloatFitsUint64(float64(amount), scale, roundAway)
}

func legacyScaledFloatFitsUint64(value float64, scale int64, roundAway bool) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	rational := new(big.Rat)
	if rational.SetFloat64(value) == nil {
		return false
	}
	rational.Mul(rational, new(big.Rat).SetInt64(scale))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(rational.Num(), rational.Denom(), remainder)
	if roundAway && remainder.Sign() != 0 {
		if rational.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	return quotient.Sign() >= 0 && quotient.BitLen() <= 64
}

func legacyV1MergedMessageField(message []byte, wanted protowire.Number) ([]byte, bool) {
	var merged []byte
	present := false
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number == wanted && wireType == protowire.BytesType {
			nested, _ := protowire.ConsumeBytes(value)
			merged = append(merged, nested...)
			present = true
		}
		message = value[valueLength:]
	}
	return merged, present
}

type legacyV1InitializationMessage uint8

const (
	legacyV1InitializeClaim legacyV1InitializationMessage = iota
	legacyV1InitializeStream
	legacyV1InitializeMetadata
	legacyV1InitializeSource
	legacyV1InitializeFee
	legacyV1InitializeCertificate
)

type legacyV1InitializationField struct {
	wire       protowire.Type
	required   bool
	nested     legacyV1InitializationMessage
	hasNested  bool
	enumMax    uint64
	hasEnumMax bool
}

func legacyV1TransactionInitializedWithoutSignature(message []byte) bool {
	return legacyV1TransactionMessageInitialized(message, legacyV1InitializeClaim)
}

func legacyV1TransactionMessageInitialized(
	message []byte, messageType legacyV1InitializationMessage,
) bool {
	seen := make(map[protowire.Number]bool)
	nestedMessages := make(map[protowire.Number][]byte)
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		field, known := legacyV1TransactionInitializationField(messageType, number)
		if known && wireType == field.wire {
			recognized := true
			if field.hasEnumMax {
				decoded, _ := protowire.ConsumeVarint(value)
				recognized = decoded <= field.enumMax
			}
			if recognized {
				seen[number] = true
				if field.hasNested {
					nested, _ := protowire.ConsumeBytes(value)
					nestedMessages[number] = append(nestedMessages[number], nested...)
				}
			}
		}
		message = value[valueLength:]
	}

	for number := protowire.Number(1); number <= 13; number++ {
		field, known := legacyV1TransactionInitializationField(messageType, number)
		if !known {
			continue
		}
		if field.required && !seen[number] {
			return false
		}
		if field.hasNested && seen[number] &&
			!legacyV1TransactionMessageInitialized(nestedMessages[number], field.nested) {
			return false
		}
	}
	return true
}

func legacyV1TransactionInitializationField(
	messageType legacyV1InitializationMessage, number protowire.Number,
) (legacyV1InitializationField, bool) {
	field := func(wire protowire.Type, required bool) legacyV1InitializationField {
		return legacyV1InitializationField{wire: wire, required: required}
	}
	enum := func(required bool, maximum uint64) legacyV1InitializationField {
		return legacyV1InitializationField{
			wire: protowire.VarintType, required: required, enumMax: maximum, hasEnumMax: true,
		}
	}
	nested := func(required bool, kind legacyV1InitializationMessage) legacyV1InitializationField {
		return legacyV1InitializationField{
			wire: protowire.BytesType, required: required, nested: kind, hasNested: true,
		}
	}

	switch messageType {
	case legacyV1InitializeClaim:
		switch number {
		case 1:
			return enum(true, 1), true
		case 2:
			return enum(true, 2), true
		case 3:
			return nested(false, legacyV1InitializeStream), true
		case 4:
			return nested(false, legacyV1InitializeCertificate), true
			// Field 5 is publisherSignature and is cleared before serialization.
		}
	case legacyV1InitializeStream:
		switch number {
		case 1:
			return enum(true, 1), true
		case 2:
			return nested(true, legacyV1InitializeMetadata), true
		case 3:
			return nested(true, legacyV1InitializeSource), true
		}
	case legacyV1InitializeMetadata:
		switch number {
		case 1:
			return enum(true, 4), true
		case 2:
			return enum(true, 184), true
		case 3, 4, 5, 6:
			return field(protowire.BytesType, true), true
		case 7:
			return field(protowire.VarintType, true), true
		case 8:
			return nested(false, legacyV1InitializeFee), true
		case 9, 10, 11:
			return field(protowire.BytesType, false), true
		}
	case legacyV1InitializeSource:
		switch number {
		case 1, 2:
			return enum(true, 1), true
		case 3, 4:
			return field(protowire.BytesType, true), true
		}
	case legacyV1InitializeFee:
		switch number {
		case 1:
			return enum(true, 1), true
		case 2:
			return enum(true, 3), true
		case 3:
			return field(protowire.BytesType, true), true
		case 4:
			return field(protowire.Fixed32Type, true), true
		}
	case legacyV1InitializeCertificate:
		switch number {
		case 1:
			return enum(true, 1), true
		case 2:
			return enum(true, 3), true
		case 4:
			return field(protowire.BytesType, true), true
		}
	}
	return legacyV1InitializationField{}, false
}

func mergeV1TransactionSignature(message, certificateID []byte) []byte {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if number == 4 && wireType == protowire.BytesType {
			decoded, _ := protowire.ConsumeBytes(value)
			certificateID = append(certificateID[:0], decoded...)
		}
		message = value[valueLength:]
	}
	return certificateID
}

func reverseTransactionHex(value []byte) string {
	return hex.EncodeToString(reverseTransactionBytes(value))
}

func transactionMetadataStringPointer(value string) *string {
	return &value
}
