package wallet

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var (
	ErrInvalidHubOutputs       = errors.New("invalid hub outputs")
	ErrInvalidHubOutputsBase64 = errors.New("invalid hub outputs base64")
)

// HubOutputsDecodeError preserves the protobuf/Unicode exception boundary
// exposed by Outputs.from_bytes in the pinned SDK.
type HubOutputsDecodeError struct {
	Name    string
	Message string
}

// HubOutputsBase64DecodeError preserves binascii.Error from Python's
// base64.b64decode boundary.
type HubOutputsBase64DecodeError struct {
	Name    string
	Message string
}

func (err *HubOutputsBase64DecodeError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *HubOutputsBase64DecodeError) PythonErrorName() string {
	if err == nil || err.Name == "" {
		return "Error"
	}
	return err.Name
}

func (err *HubOutputsBase64DecodeError) Unwrap() error {
	return ErrInvalidHubOutputsBase64
}

func (err *HubOutputsDecodeError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *HubOutputsDecodeError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	if err.Name == "" {
		return "DecodeError"
	}
	return err.Name
}

func (err *HubOutputsDecodeError) Unwrap() error { return ErrInvalidHubOutputs }

type HubErrorCode int32

const (
	HubErrorUnknownCode HubErrorCode = iota
	HubErrorNotFound
	HubErrorInvalid
	HubErrorBlocked
)

func (code HubErrorCode) Name() (string, error) {
	switch code {
	case HubErrorUnknownCode:
		return "UNKNOWN_CODE", nil
	case HubErrorNotFound:
		return "NOT_FOUND", nil
	case HubErrorInvalid:
		return "INVALID", nil
	case HubErrorBlocked:
		return "BLOCKED", nil
	default:
		return "", fmt.Errorf("Enum Code has no name defined for value %d", code)
	}
}

type HubOutputs struct {
	TXOs         []*HubOutput
	ExtraTXOs    []*HubOutput
	Total        uint32
	Offset       uint32
	Blocked      []*HubBlocked
	BlockedTotal uint32
}

type HubOutput struct {
	TransactionHash []byte
	Position        uint32
	Height          uint32
	Claim           *HubClaimMeta
	Error           *HubError
}

type HubClaimMeta struct {
	Channel          *HubOutput
	Repost           *HubOutput
	ShortURL         string
	CanonicalURL     string
	IsControlling    bool
	TakeOverHeight   uint32
	CreationHeight   uint32
	ActivationHeight uint32
	ExpirationHeight uint32
	ClaimsInChannel  uint32
	Reposted         uint32
	EffectiveAmount  uint64
	SupportAmount    uint64
	TrendingScore    float64
}

type HubError struct {
	Code    HubErrorCode
	Text    string
	Blocked *HubBlocked
}

type HubBlocked struct {
	Count   uint32
	Channel *HubOutput
}

// TransactionRequests mirrors Outputs.from_bytes' top-level tx set. Python
// stores these pairs in a set, so this method preserves the first wire
// occurrence while deduplicating exact transaction ID and height pairs.
func (outputs *HubOutputs) TransactionRequests() []TransactionFetchRequest {
	if outputs == nil {
		return nil
	}
	requests := make([]TransactionFetchRequest, 0, len(outputs.TXOs)+len(outputs.ExtraTXOs))
	seen := make(map[TransactionFetchRequest]struct{}, cap(requests))
	appendOutput := func(output *HubOutput) {
		if output == nil || output.Error != nil {
			return
		}
		reversed := make([]byte, len(output.TransactionHash))
		for index := range output.TransactionHash {
			reversed[len(reversed)-1-index] = output.TransactionHash[index]
		}
		request := TransactionFetchRequest{
			TxID:   hex.EncodeToString(reversed),
			Height: int64(output.Height),
		}
		if _, exists := seen[request]; exists {
			return
		}
		seen[request] = struct{}{}
		requests = append(requests, request)
	}
	for _, output := range outputs.TXOs {
		appendOutput(output)
	}
	for _, output := range outputs.ExtraTXOs {
		appendOutput(output)
	}
	return requests
}

func DecodeHubOutputsBase64(encoded string) (*HubOutputs, error) {
	if !isASCIIHubOutputsBase64(encoded) {
		return nil, &HubOutputsBase64DecodeError{
			Name: "ValueError", Message: "string argument should contain only ASCII characters",
		}
	}
	raw, err := decodeHubOutputsBase64(encoded)
	if err != nil {
		return nil, &HubOutputsBase64DecodeError{Message: hubOutputsBase64ErrorMessage(err)}
	}
	return DecodeHubOutputsBytes(raw)
}

func isASCIIHubOutputsBase64(encoded string) bool {
	for index := 0; index < len(encoded); index++ {
		if encoded[index] >= 0x80 {
			return false
		}
	}
	return true
}

func hubOutputsBase64ErrorMessage(err error) string {
	message := err.Error()
	if strings.Contains(message, "number of data characters") {
		start := strings.Index(message, "number of data characters")
		return "Invalid base64-encoded string: " + message[start:]
	}
	if strings.Contains(message, "incorrect padding") {
		return "Incorrect padding"
	}
	return message
}

func DecodeHubOutputsBytes(raw []byte) (*HubOutputs, error) {
	descriptor, err := hubOutputsMessageDescriptor()
	if err != nil {
		return nil, err
	}
	message := dynamicpb.NewMessage(descriptor)
	if err := proto.Unmarshal(raw, message); err != nil {
		name := "DecodeError"
		if strings.Contains(strings.ToLower(err.Error()), "utf-8") {
			name = "UnicodeDecodeError"
		}
		return nil, &HubOutputsDecodeError{Name: name, Message: err.Error()}
	}
	return projectHubOutputs(message.ProtoReflect()), nil
}

func projectHubOutputs(message protoreflect.Message) *HubOutputs {
	result := &HubOutputs{
		TXOs:      projectHubOutputList(message, "txos"),
		ExtraTXOs: projectHubOutputList(message, "extra_txos"),
		Blocked:   projectHubBlockedList(message, "blocked"),
	}
	result.Total = uint32(hubProtoUint(message, "total"))
	result.Offset = uint32(hubProtoUint(message, "offset"))
	result.BlockedTotal = uint32(hubProtoUint(message, "blocked_total"))
	return result
}

func projectHubOutputList(message protoreflect.Message, name protoreflect.Name) []*HubOutput {
	field := message.Descriptor().Fields().ByName(name)
	list := message.Get(field).List()
	outputs := make([]*HubOutput, list.Len())
	for index := range outputs {
		outputs[index] = projectHubOutput(list.Get(index).Message())
	}
	return outputs
}

func projectHubBlockedList(message protoreflect.Message, name protoreflect.Name) []*HubBlocked {
	field := message.Descriptor().Fields().ByName(name)
	list := message.Get(field).List()
	blocked := make([]*HubBlocked, list.Len())
	for index := range blocked {
		blocked[index] = projectHubBlocked(list.Get(index).Message())
	}
	return blocked
}

func projectHubOutput(message protoreflect.Message) *HubOutput {
	output := &HubOutput{
		TransactionHash: append([]byte(nil), hubProtoBytes(message, "tx_hash")...),
		Position:        uint32(hubProtoUint(message, "nout")),
		Height:          uint32(hubProtoUint(message, "height")),
	}
	meta := message.Descriptor().Oneofs().ByName("meta")
	switch selected := message.WhichOneof(meta); {
	case selected == nil:
	case selected.Name() == "claim":
		output.Claim = projectHubClaimMeta(message.Get(selected).Message())
	case selected.Name() == "error":
		output.Error = projectHubError(message.Get(selected).Message())
	}
	return output
}

func projectHubClaimMeta(message protoreflect.Message) *HubClaimMeta {
	claim := &HubClaimMeta{
		ShortURL:         hubProtoString(message, "short_url"),
		CanonicalURL:     hubProtoString(message, "canonical_url"),
		IsControlling:    hubProtoBool(message, "is_controlling"),
		TakeOverHeight:   uint32(hubProtoUint(message, "take_over_height")),
		CreationHeight:   uint32(hubProtoUint(message, "creation_height")),
		ActivationHeight: uint32(hubProtoUint(message, "activation_height")),
		ExpirationHeight: uint32(hubProtoUint(message, "expiration_height")),
		ClaimsInChannel:  uint32(hubProtoUint(message, "claims_in_channel")),
		Reposted:         uint32(hubProtoUint(message, "reposted")),
		EffectiveAmount:  hubProtoUint(message, "effective_amount"),
		SupportAmount:    hubProtoUint(message, "support_amount"),
		TrendingScore:    hubProtoFloat(message, "trending_score"),
	}
	if nested, ok := hubProtoMessage(message, "channel"); ok {
		claim.Channel = projectHubOutput(nested)
	}
	if nested, ok := hubProtoMessage(message, "repost"); ok {
		claim.Repost = projectHubOutput(nested)
	}
	return claim
}

func projectHubError(message protoreflect.Message) *HubError {
	result := &HubError{
		Code: HubErrorCode(hubProtoEnum(message, "code")),
		Text: hubProtoString(message, "text"),
	}
	if nested, ok := hubProtoMessage(message, "blocked"); ok {
		result.Blocked = projectHubBlocked(nested)
	}
	return result
}

func projectHubBlocked(message protoreflect.Message) *HubBlocked {
	blocked := &HubBlocked{Count: uint32(hubProtoUint(message, "count"))}
	if nested, ok := hubProtoMessage(message, "channel"); ok {
		blocked.Channel = projectHubOutput(nested)
	}
	return blocked
}

func hubProtoMessage(message protoreflect.Message, name protoreflect.Name) (protoreflect.Message, bool) {
	field := message.Descriptor().Fields().ByName(name)
	if field == nil || !message.Has(field) {
		return nil, false
	}
	return message.Get(field).Message(), true
}

func hubProtoBytes(message protoreflect.Message, name protoreflect.Name) []byte {
	return message.Get(message.Descriptor().Fields().ByName(name)).Bytes()
}

func hubProtoString(message protoreflect.Message, name protoreflect.Name) string {
	return message.Get(message.Descriptor().Fields().ByName(name)).String()
}

func hubProtoBool(message protoreflect.Message, name protoreflect.Name) bool {
	return message.Get(message.Descriptor().Fields().ByName(name)).Bool()
}

func hubProtoUint(message protoreflect.Message, name protoreflect.Name) uint64 {
	return message.Get(message.Descriptor().Fields().ByName(name)).Uint()
}

func hubProtoEnum(message protoreflect.Message, name protoreflect.Name) protoreflect.EnumNumber {
	return message.Get(message.Descriptor().Fields().ByName(name)).Enum()
}

func hubProtoFloat(message protoreflect.Message, name protoreflect.Name) float64 {
	return message.Get(message.Descriptor().Fields().ByName(name)).Float()
}

func decodeHubOutputsBase64(encoded string) ([]byte, error) {
	filtered := make([]byte, 0, len(encoded))
	trailingPads := 0
	for index := 0; index < len(encoded); index++ {
		value := encoded[index]
		if value == '=' {
			trailingPads++
			continue
		}
		if !hubBase64Digit(value) {
			continue
		}
		filtered = append(filtered, value)
		trailingPads = 0
	}
	switch len(filtered) % 4 {
	case 0:
	case 1:
		return nil, fmt.Errorf(
			"invalid base64 data: number of data characters (%d) cannot be 1 more than a multiple of 4",
			len(filtered),
		)
	case 2:
		if trailingPads < 2 {
			return nil, errors.New("invalid base64 data: incorrect padding")
		}
		filtered = append(filtered, '=', '=')
	case 3:
		if trailingPads < 1 {
			return nil, errors.New("invalid base64 data: incorrect padding")
		}
		filtered = append(filtered, '=')
	}
	return base64.StdEncoding.DecodeString(string(filtered))
}

func hubBase64Digit(value byte) bool {
	return value >= 'A' && value <= 'Z' ||
		value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '+' || value == '/'
}

var hubOutputsDescriptor struct {
	sync.Once
	message protoreflect.MessageDescriptor
	err     error
}

func hubOutputsMessageDescriptor() (protoreflect.MessageDescriptor, error) {
	hubOutputsDescriptor.Do(func() {
		raw, err := base64.StdEncoding.DecodeString(hubOutputsDescriptorBase64)
		if err != nil {
			hubOutputsDescriptor.err = fmt.Errorf("decode result.proto descriptor: %w", err)
			return
		}
		fileProto := new(descriptorpb.FileDescriptorProto)
		if err := proto.Unmarshal(raw, fileProto); err != nil {
			hubOutputsDescriptor.err = fmt.Errorf("parse result.proto descriptor: %w", err)
			return
		}
		file, err := protodesc.NewFile(fileProto, nil)
		if err != nil {
			hubOutputsDescriptor.err = fmt.Errorf("build result.proto descriptor: %w", err)
			return
		}
		hubOutputsDescriptor.message = file.Messages().ByName("Outputs")
		if hubOutputsDescriptor.message == nil {
			hubOutputsDescriptor.err = errors.New("result.proto descriptor has no Outputs message")
		}
	})
	return hubOutputsDescriptor.message, hubOutputsDescriptor.err
}

const hubOutputsDescriptorBase64 = "CgxyZXN1bHQucHJvdG8SAnBiIpcBCgdPdXRwdXRzEhgKBHR4b3MYASADKAsyCi5wYi5PdXRwdXQSHgoKZXh0cmFfdHhvcxgCIAMoCzIKLnBiLk91dHB1dBINCgV0b3RhbBgDIAEoDRIOCgZvZmZzZXQYBCABKA0SHAoHYmxvY2tlZBgFIAMoCzILLnBiLkJsb2NrZWQSFQoNYmxvY2tlZF90b3RhbBgGIAEoDSJ7CgZPdXRwdXQSDwoHdHhfaGFzaBgBIAEoDBIMCgRub3V0GAIgASgNEg4KBmhlaWdodBgDIAEoDRIeCgVjbGFpbRgHIAEoCzINLnBiLkNsYWltTWV0YUgAEhoKBWVycm9yGA8gASgLMgkucGIuRXJyb3JIAEIGCgRtZXRhIuYCCglDbGFpbU1ldGESGwoHY2hhbm5lbBgBIAEoCzIKLnBiLk91dHB1dBIaCgZyZXBvc3QYAiABKAsyCi5wYi5PdXRwdXQSEQoJc2hvcnRfdXJsGAMgASgJEhUKDWNhbm9uaWNhbF91cmwYBCABKAkSFgoOaXNfY29udHJvbGxpbmcYBSABKAgSGAoQdGFrZV9vdmVyX2hlaWdodBgGIAEoDRIXCg9jcmVhdGlvbl9oZWlnaHQYByABKA0SGQoRYWN0aXZhdGlvbl9oZWlnaHQYCCABKA0SGQoRZXhwaXJhdGlvbl9oZWlnaHQYCSABKA0SGQoRY2xhaW1zX2luX2NoYW5uZWwYCiABKA0SEAoIcmVwb3N0ZWQYCyABKA0SGAoQZWZmZWN0aXZlX2Ftb3VudBgUIAEoBBIWCg5zdXBwb3J0X2Ftb3VudBgVIAEoBBIWCg50cmVuZGluZ19zY29yZRgWIAEoASKUAQoFRXJyb3ISHAoEY29kZRgBIAEoDjIOLnBiLkVycm9yLkNvZGUSDAoEdGV4dBgCIAEoCRIcCgdibG9ja2VkGAMgASgLMgsucGIuQmxvY2tlZCJBCgRDb2RlEhAKDFVOS05PV05fQ09ERRAAEg0KCU5PVF9GT1VORBABEgsKB0lOVkFMSUQQAhILCgdCTE9DS0VEEAMiNQoHQmxvY2tlZBINCgVjb3VudBgBIAEoDRIbCgdjaGFubmVsGAIgASgLMgoucGIuT3V0cHV0QiZaJGdpdGh1Yi5jb20vbGJyeWlvL2h1Yi9wcm90b2J1Zi9nby9wYmIGcHJvdG8z"
