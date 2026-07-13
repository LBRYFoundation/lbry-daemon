package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

const knownChannelClaimScriptHex = "b505406368616e4c5d00125a0a583056301006072a8648ce3d020106052b8104000a03420004d7fa13fd8e57f3a0b878eaaf3d179144d25ddbe4a3e4440a661f51b4134c6a13c9c98678ff8411932e60fd97d7baf03ea67ebcc21097230cfb2241348aadb55e6d7576a9149c6d700f89c77f0e8c650ba05656f8f2392782d388ac"

const legacyChannelClaimHex = "08011002225e0801100322583056301006072a8648ce3d020106052b8104000a034200043878b1edd4a1373149909ef03f4339f6da9c2bd2214c040fd2e530463ffe66098eca14fc70b50ff3aefd106049a815f595ed5a13eda7419ad78d9ed7ae473f17"

const knownChannelSPKIHex = "3056301006072a8648ce3d020106052b8104000a03420004d7fa13fd8e57f3a0b878eaaf3d179144d25ddbe4a3e4440a661f51b4134c6a13c9c98678ff8411932e60fd97d7baf03ea67ebcc21097230cfb2241348aadb55e"

func TestDecodeChannelClaimPublicKeyKnownSPKIFixture(t *testing.T) {
	script := mustChannelClaimHex(t, knownChannelClaimScriptHex)
	want := mustChannelClaimHex(t, "02d7fa13fd8e57f3a0b878eaaf3d179144d25ddbe4a3e4440a661f51b4134c6a13")
	got, isChannel, err := DecodeChannelClaimPublicKey(script)
	if err != nil || !isChannel || !bytes.Equal(got, want) {
		t.Fatalf("DecodeChannelClaimPublicKey() = %x, %v, %v; want %x, true, nil", got, isChannel, err, want)
	}
}

func TestDecodeChannelClaimPublicKeyCompactKeyIsVerbatim(t *testing.T) {
	// Channel.public_key_bytes returns every 33-byte value without asking
	// Coincurve to validate it as a point.
	want := bytes.Repeat([]byte{0xaa}, 33)
	claim := makeV2ChannelClaim(want)
	got, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, claim, false))
	if err != nil || !isChannel || !bytes.Equal(got, want) {
		t.Fatalf("compact key decode = %x, %v, %v", got, isChannel, err)
	}
	got[0] = 0
	if want[0] != 0xaa {
		t.Fatal("decoder returned an alias of claim data")
	}
}

func TestDecodeChannelClaimPublicKeyUpdateScriptAndSignedClaim(t *testing.T) {
	want := append([]byte{0x03}, bytes.Repeat([]byte{0x22}, 32)...)
	wrapped := make([]byte, 85)
	wrapped[0] = 1
	wrapped = append(wrapped, makeV2ChannelClaim(want)[1:]...)
	script := makeChannelClaimScript(channelUpdateClaimOpcode, wrapped, true)
	got, isChannel, err := DecodeChannelClaimPublicKey(script)
	if err != nil || !isChannel || !bytes.Equal(got, want) {
		t.Fatalf("signed update decode = %x, %v, %v", got, isChannel, err)
	}
}

func TestDecodeChannelClaimPublicKeyLegacyCertificate(t *testing.T) {
	want := mustChannelClaimHex(t, "033878b1edd4a1373149909ef03f4339f6da9c2bd2214c040fd2e530463ffe6609")
	claim := mustChannelClaimHex(t, legacyChannelClaimHex)
	got, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, claim, false))
	if err != nil || !isChannel || !bytes.Equal(got, want) {
		t.Fatalf("legacy certificate decode = %x, %v, %v; want %x", got, isChannel, err, want)
	}
}

func TestDecodeChannelClaimPublicKeySkipsCanDecodeClaimFailures(t *testing.T) {
	valid := makeChannelClaimScript(channelClaimNameOpcode, makeV2ChannelClaim(bytes.Repeat([]byte{2}, 33)), false)
	cases := map[string][]byte{
		"empty":              nil,
		"not claim":          {0x76, 0xa9, 0, 0x88, 0xac},
		"support":            {0xb6, 0, 0, 0x6d, 0x75, 0xa9, 0, 0x87},
		"wrong drop":         append(append([]byte(nil), valid[:len(valid)-27]...), append([]byte{0x75, 0x75}, valid[len(valid)-25:]...)...),
		"trailing script":    append(append([]byte(nil), valid...), 0),
		"truncated protobuf": makeChannelClaimScript(channelClaimNameOpcode, []byte{0, 0x12}, false),
		"malformed old JSON": makeChannelClaimScript(channelClaimNameOpcode, []byte(`{}`), false),
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			key, isChannel, err := DecodeChannelClaimPublicKey(script)
			if err != nil || isChannel || key != nil {
				t.Fatalf("decode = %x, %v, %v; want nil, false, nil", key, isChannel, err)
			}
		})
	}
}

func TestDecodeChannelClaimPublicKeyPropagatesAfterClaimDecode(t *testing.T) {
	streamMessage := protowire.AppendTag(nil, 1, protowire.BytesType)
	streamMessage = protowire.AppendBytes(streamMessage, nil)
	streamClaim := append([]byte{0}, streamMessage...)
	key, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, streamClaim, false))
	if !errors.Is(err, ErrDecodedClaimNotChannel) || isChannel || key != nil {
		t.Fatalf("stream decode = %x, %v, %v", key, isChannel, err)
	}

	legacyJSON := []byte(`{"sources":{"lbry_sd_hash":"00"}}`)
	key, isChannel, err = DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, legacyJSON, false))
	if !errors.Is(err, ErrDecodedClaimNotChannel) || isChannel || key != nil {
		t.Fatalf("legacy JSON stream decode = %x, %v, %v", key, isChannel, err)
	}

	badKeyClaim := makeV2ChannelClaim([]byte{1})
	key, isChannel, err = DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, badKeyClaim, false))
	if !errors.Is(err, ErrInvalidChannelPublicKey) || !isChannel || key != nil {
		t.Fatalf("bad key decode = %x, %v, %v", key, isChannel, err)
	}

	// An empty protobuf Claim decodes successfully. Accessing claim.channel
	// then creates the channel oneof and its empty key fails SPKI parsing.
	key, isChannel, err = DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, []byte{0}, false))
	if !errors.Is(err, ErrInvalidChannelPublicKey) || !isChannel || key != nil {
		t.Fatalf("empty claim decode = %x, %v, %v", key, isChannel, err)
	}
}

func TestDecodeChannelClaimPublicKeyValidatesNestedProtobuf(t *testing.T) {
	publicKey := append([]byte{0x02}, bytes.Repeat([]byte{0x11}, 32)...)
	baseChannel := protowire.AppendTag(nil, 1, protowire.BytesType)
	baseChannel = protowire.AppendBytes(baseChannel, publicKey)

	badSource := protowire.AppendTag(nil, 2, protowire.BytesType)
	badSource = protowire.AppendBytes(badSource, []byte{0xff})
	channel := append([]byte(nil), baseChannel...)
	channel = protowire.AppendTag(channel, 4, protowire.BytesType)
	channel = protowire.AppendBytes(channel, badSource)
	assertChannelClaimSkipped(t, makeV2ClaimMessage(2, channel), "channel cover invalid UTF-8")

	badStream := protowire.AppendTag(nil, 2, protowire.BytesType)
	badStream = protowire.AppendBytes(badStream, []byte{0xff})
	claimMessage := protowire.AppendTag(nil, 1, protowire.BytesType)
	claimMessage = protowire.AppendBytes(claimMessage, badStream)
	claimMessage = append(claimMessage, makeV2ClaimMessage(2, baseChannel)[1:]...)
	claimMessage = append([]byte{0}, claimMessage...)
	assertChannelClaimSkipped(t, claimMessage, "earlier stream invalid UTF-8")

	legacy := []byte{0x08, 0x01, 0x10, 0x02}
	badMetadata := protowire.AppendTag(nil, 3, protowire.BytesType)
	badMetadata = protowire.AppendBytes(badMetadata, []byte{0xff})
	legacyStream := protowire.AppendTag(nil, 2, protowire.BytesType)
	legacyStream = protowire.AppendBytes(legacyStream, badMetadata)
	legacy = protowire.AppendTag(legacy, 3, protowire.BytesType)
	legacy = protowire.AppendBytes(legacy, legacyStream)
	certificateClaim := mustChannelClaimHex(t, legacyChannelClaimHex)
	certificate := certificateClaim[6:]
	legacy = protowire.AppendTag(legacy, 4, protowire.BytesType)
	legacy = protowire.AppendBytes(legacy, certificate)
	assertChannelClaimSkipped(t, legacy, "legacy stream invalid UTF-8")
}

func TestDecodeChannelClaimPublicKeyTreatsMismatchedWireAsUnknown(t *testing.T) {
	channel := protowire.AppendTag(nil, 1, protowire.VarintType)
	channel = protowire.AppendVarint(channel, 1)
	claim := makeV2ClaimMessage(2, channel)
	key, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, claim, false))
	if !errors.Is(err, ErrInvalidChannelPublicKey) || !isChannel || key != nil {
		t.Fatalf("mismatched channel key wire decode = %x, %v, %v", key, isChannel, err)
	}

	message := protowire.AppendTag(nil, 2, protowire.VarintType)
	message = protowire.AppendVarint(message, 1)
	claim = append([]byte{0}, message...)
	key, isChannel, err = DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, claim, false))
	if !errors.Is(err, ErrInvalidChannelPublicKey) || !isChannel || key != nil {
		t.Fatalf("mismatched claim oneof wire decode = %x, %v, %v", key, isChannel, err)
	}
}

func TestDecodeChannelClaimPublicKeyAcceptsPinnedBERForms(t *testing.T) {
	der := mustChannelClaimHex(t, knownChannelSPKIHex)
	want := mustChannelClaimHex(t, "02d7fa13fd8e57f3a0b878eaaf3d179144d25ddbe4a3e4440a661f51b4134c6a13")

	outerIndefinite := append([]byte{0x30, 0x80}, der[2:]...)
	outerIndefinite = append(outerIndefinite, 0, 0)
	constructedBitString := append([]byte{0x30, 0x80}, der[2:20]...)
	constructedBitString = append(constructedBitString, 0x23, 0x80)
	constructedBitString = append(constructedBitString, der[20:]...)
	constructedBitString = append(constructedBitString, 0, 0, 0, 0)

	for name, publicKey := range map[string][]byte{
		"outer indefinite":       outerIndefinite,
		"constructed bit string": constructedBitString,
		"trailing data":          append(append([]byte(nil), der...), 0xff, 0x00),
	} {
		t.Run(name, func(t *testing.T) {
			got, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(
				channelClaimNameOpcode, makeV2ChannelClaim(publicKey), false,
			))
			if err != nil || !isChannel || !bytes.Equal(got, want) {
				t.Fatalf("BER decode = %x, %v, %v; want %x, true, nil", got, isChannel, err, want)
			}
		})
	}
}

func makeV2ChannelClaim(publicKey []byte) []byte {
	channel := protowire.AppendTag(nil, 1, protowire.BytesType)
	channel = protowire.AppendBytes(channel, publicKey)
	claim := protowire.AppendTag(nil, 2, protowire.BytesType)
	claim = protowire.AppendBytes(claim, channel)
	return append([]byte{0}, claim...)
}

func makeV2ClaimMessage(field protowire.Number, nested []byte) []byte {
	claim := protowire.AppendTag(nil, field, protowire.BytesType)
	claim = protowire.AppendBytes(claim, nested)
	return append([]byte{0}, claim...)
}

func assertChannelClaimSkipped(t *testing.T, claim []byte, reason string) {
	t.Helper()
	key, isChannel, err := DecodeChannelClaimPublicKey(makeChannelClaimScript(channelClaimNameOpcode, claim, false))
	if err != nil || isChannel || key != nil {
		t.Fatalf("%s decode = %x, %v, %v; want nil, false, nil", reason, key, isChannel, err)
	}
}

func makeChannelClaimScript(opcode byte, claim []byte, scriptHash bool) []byte {
	script := []byte{opcode}
	script = appendChannelClaimPush(script, []byte("name"))
	if opcode == channelUpdateClaimOpcode {
		script = appendChannelClaimPush(script, bytes.Repeat([]byte{1}, 20))
	}
	script = appendChannelClaimPush(script, claim)
	if opcode == channelClaimNameOpcode {
		script = append(script, 0x6d, 0x75)
	} else {
		script = append(script, 0x6d, 0x6d)
	}
	if scriptHash {
		script = append(script, 0xa9)
		script = appendChannelClaimPush(script, bytes.Repeat([]byte{3}, 20))
		return append(script, 0x87)
	}
	script = append(script, 0x76, 0xa9)
	script = appendChannelClaimPush(script, bytes.Repeat([]byte{4}, 20))
	return append(script, 0x88, 0xac)
}

func appendChannelClaimPush(script, data []byte) []byte {
	switch {
	case len(data) == 0:
		return append(script, 0)
	case len(data) < channelPushData1Opcode:
		script = append(script, byte(len(data)))
	case len(data) <= 0xff:
		script = append(script, channelPushData1Opcode, byte(len(data)))
	default:
		script = append(script, channelPushData2Opcode, byte(len(data)), byte(len(data)>>8))
	}
	return append(script, data...)
}

func mustChannelClaimHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
