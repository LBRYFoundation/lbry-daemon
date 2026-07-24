package keys

import (
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const (
	pemVectorPrivate = "16514d9eed1e76021f7204f660a8f79e3ae8bfd28581615c6ae3992305270f1e"
	pemVectorPublic  = "039ae7283f3f6723e0a166b7e19e1d1167f6dc5f4af61b4a58066a0d2a8bed2b35"
	pemVectorPKCS8   = "-----BEGIN PRIVATE KEY-----\n" +
		"MIGEAgEAMBAGByqGSM49AgEGBSuBBAAKBG0wawIBAQQgFlFNnu0edgIfcgT2YKj3\n" +
		"njrov9KFgWFcauOZIwUnDx6hRANCAASa5yg/P2cj4KFmt+GeHRFn9txfSvYbSlgG\n" +
		"ag0qi+0rNcZrzLTsProxaxapem1qSo7/0p10iQG7l4k1JRnNALE9\n" +
		"-----END PRIVATE KEY-----\n"
	pemVectorSEC1 = "-----BEGIN EC PRIVATE KEY-----\n" +
		"MHQCAQEEIBZRTZ7tHnYCH3IE9mCo95466L/ShYFhXGrjmSMFJw8eoAcGBSuBBAAK\n" +
		"oUQDQgAEmucoPz9nI+ChZrfhnh0RZ/bcX0r2G0pYBmoNKovtKzXGa8y07D66MWsW\n" +
		"qXptakqO/9KddIkBu5eJNSUZzQCxPQ==\n" +
		"-----END EC PRIVATE KEY-----\n"
)

func TestPrivateKeyPEMMatchesCoincurve15CanonicalVector(t *testing.T) {
	key, err := NewPrivateKey(
		RegTest, mustPEMHex(t, pemVectorPrivate), make([]byte, 32), 7, 8, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := key.ToPEM()
	if err != nil {
		t.Fatal(err)
	}
	if encoded != pemVectorPKCS8 {
		t.Fatalf("PKCS#8 PEM differs\nGo:\n%s\nPinned:\n%s", encoded, pemVectorPKCS8)
	}
	if got, want := len(encoded), 237; got != want {
		t.Fatalf("PEM length = %d, want %d", got, want)
	}
}

func TestPrivateKeyFromPEMAcceptsPKCS8AndLegacySEC1(t *testing.T) {
	for name, encoded := range map[string]string{
		"PKCS8": pemVectorPKCS8,
		"SEC1":  pemVectorSEC1,
	} {
		t.Run(name, func(t *testing.T) {
			key, err := PrivateKeyFromPEM(RegTest, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(key.PrivateKeyBytes()); got != pemVectorPrivate {
				t.Fatalf("private key = %s, want %s", got, pemVectorPrivate)
			}
			if got := hex.EncodeToString(key.PublicKey().CompressedBytes()); got != pemVectorPublic {
				t.Fatalf("public key = %s, want %s", got, pemVectorPublic)
			}
			if got, want := key.Address(), "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8"; got != want {
				t.Fatalf("address = %s, want %s", got, want)
			}
			if key.Depth() != 0 || key.ChildNumber() != 0 || key.Parent() != nil || key.ChainCode() != [32]byte{} {
				t.Fatalf("parsed root metadata = depth %d child %d parent %p chain %x",
					key.Depth(), key.ChildNumber(), key.Parent(), key.ChainCode())
			}
			canonical, err := key.ToPEM()
			if err != nil || canonical != pemVectorPKCS8 {
				t.Fatalf("canonical PEM differs: %v\n%s", err, canonical)
			}
		})
	}
}

func TestPrivateKeyFromPEMPreservesPinnedPermissiveEnvelopeRules(t *testing.T) {
	permissive := " \r\n-----BEGIN IGNORED LABEL-----\r\n" +
		strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(
			pemVectorPKCS8, "-----BEGIN PRIVATE KEY-----\n"),
			"-----END PRIVATE KEY-----\n"), "\n", "!\r\n") +
		"-----END ANOTHER LABEL-----\r\n\t"
	key, err := PrivateKeyFromPEM(TestNet, permissive)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := key.Address(), "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8"; got != want {
		t.Fatalf("permissive PEM address = %s, want %s", got, want)
	}

	for _, network := range []Network{TestNet, RegTest} {
		parsed, err := PrivateKeyFromPEM(network, pemVectorPKCS8)
		if err != nil || parsed.Address() != "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8" {
			t.Fatalf("network %s parse = %v, %v", network.ID(), parsed, err)
		}
	}
	main, err := PrivateKeyFromPEM(MainNet, pemVectorPKCS8)
	if err != nil {
		t.Fatal(err)
	}
	if main.Address() == "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8" {
		t.Fatal("mainnet PEM retained the testnet address prefix")
	}

	// Python's non-strict decoder ignores padding which is followed by more
	// alphabet data. This is observably different from Go's standard decoder.
	misplacedPadding := strings.Replace(pemVectorPKCS8, "MIGE", "MIG=E", 1)
	parsed, err := PrivateKeyFromPEM(RegTest, misplacedPadding)
	if err != nil || hex.EncodeToString(parsed.PrivateKeyBytes()) != pemVectorPrivate {
		t.Fatalf("misplaced Base64 padding parse = %v, %v", parsed, err)
	}
}

func TestPrivateKeyFromPEMIgnoresEmbeddedPointAndPadsScalar(t *testing.T) {
	legacyDER := mustPEMBody(t, pemVectorSEC1)
	legacyDER[len(legacyDER)-1] ^= 0xff
	mutated := pemWrap("EC PRIVATE KEY", legacyDER)
	key, err := PrivateKeyFromPEM(RegTest, mutated)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(key.PrivateKeyBytes()); got != pemVectorPrivate {
		t.Fatalf("embedded-point mutation changed scalar: %s", got)
	}

	shortDER, err := asn1.Marshal(ecPrivateKeyDER{Version: 1, PrivateKey: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	short, err := PrivateKeyFromPEM(MainNet, pemWrap("ANY", shortDER))
	if err != nil {
		t.Fatal(err)
	}
	if got := short.PrivateKeyBytes(); len(got) != 32 || got[31] != 1 {
		t.Fatalf("padded scalar = %x", got)
	}
	leadingZeroDER, err := asn1.Marshal(ecPrivateKeyDER{
		Version: 1, PrivateKey: append([]byte{0}, mustPEMHex(t, pemVectorPrivate)...),
	})
	if err != nil {
		t.Fatal(err)
	}
	leadingZero, err := PrivateKeyFromPEM(RegTest, pemWrap("EC PRIVATE KEY", leadingZeroDER))
	if err != nil || hex.EncodeToString(leadingZero.PrivateKeyBytes()) != pemVectorPrivate {
		t.Fatalf("leading-zero scalar parse = %v, %v", leadingZero, err)
	}

	withTrailing := append(mustPEMBody(t, pemVectorPKCS8), []byte("ignored trailing DER")...)
	trailing, err := PrivateKeyFromPEM(MainNet, pemWrap("PRIVATE KEY", withTrailing))
	if err != nil || hex.EncodeToString(trailing.PrivateKeyBytes()) != pemVectorPrivate {
		t.Fatalf("trailing DER parse = %v, %v", trailing, err)
	}
}

func TestPrivateKeyFromPEMAcceptsPKCS8AttributesAndBER(t *testing.T) {
	canonicalDER := mustPEMBody(t, pemVectorPKCS8)
	withAttributes := appendASN1SequenceField(t, canonicalDER, []byte{
		0xa0, 0x09, // [0] IMPLICIT Attributes
		0x30, 0x07, // Attribute SEQUENCE
		0x06, 0x03, 0x2a, 0x03, 0x04, // 1.2.3.4
		0x31, 0x00, // empty SET OF values
	})
	secret := mustPEMHex(t, pemVectorPrivate)
	constructedScalar := indefiniteASN1Value(0x24,
		asn1TLV(0x04, secret[:16]), asn1TLV(0x04, secret[16:]),
	)
	constructedPublic := indefiniteASN1Value(0xa1,
		indefiniteASN1Value(0x23, asn1TLV(0x03, []byte{0, 4, 1})),
	)
	constructedSEC1 := indefiniteASN1Value(0x30,
		asn1TLV(0x02, []byte{1}), constructedScalar, constructedPublic,
	)
	canonicalFields, err := parseLegacyASN1Sequence(canonicalDER)
	if err != nil {
		t.Fatal(err)
	}
	attribute := asn1TLV(0x30, append(
		asn1TLV(0x06, []byte{0x2a, 0x03, 0x04}),
		indefiniteASN1Value(0x31, asn1TLV(0x0c, []byte("ok")))...,
	))
	constructedPKCS8 := indefiniteASN1Value(0x30,
		asn1TLV(0x02, []byte{0}), canonicalFields[1].FullBytes,
		indefiniteASN1Value(0x24,
			asn1TLV(0x04, constructedSEC1[:7]), asn1TLV(0x04, constructedSEC1[7:]),
		),
		indefiniteASN1Value(0xa0, attribute),
	)
	for name, der := range map[string][]byte{
		"PKCS8 attributes":         withAttributes,
		"PKCS8 indefinite":         indefiniteASN1Sequence(t, canonicalDER),
		"SEC1 indefinite":          indefiniteASN1Sequence(t, mustPEMBody(t, pemVectorSEC1)),
		"SEC1 constructed values":  constructedSEC1,
		"nested constructed PKCS8": constructedPKCS8,
	} {
		t.Run(name, func(t *testing.T) {
			key, err := PrivateKeyFromPEM(RegTest, pemWrap("IGNORED", der))
			if err != nil || hex.EncodeToString(key.PrivateKeyBytes()) != pemVectorPrivate {
				t.Fatalf("parse = %v, %v", key, err)
			}
		})
	}
}

func TestPrivateKeyFromPEMValidatesOptionalASN1Fields(t *testing.T) {
	// Each SEC1 fixture contains scalar 1 followed by an invalid optional field.
	for name, der := range map[string][]byte{
		"parameters are an integer": mustPEMHex(t, "300b020101040101a003020101"),
		"public key is an octet":    mustPEMHex(t, "300b020101040101a103040100"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrivateKeyFromPEM(MainNet, pemWrap("EC PRIVATE KEY", der)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
				t.Fatalf("error = %v, want ErrInvalidPrivateKeyPEM", err)
			}
		})
	}
	unknownTrailing, err := PrivateKeyFromPEM(
		MainNet, pemWrap("EC PRIVATE KEY", mustPEMHex(t, "3008020101040101a200")),
	)
	if err != nil || unknownTrailing.PrivateKeyBytes()[31] != 1 {
		t.Fatalf("unknown trailing SEC1 field = %v, %v", unknownTrailing, err)
	}
	finalUnusedBits, err := PrivateKeyFromPEM(
		MainNet, pemWrap("EC PRIVATE KEY", mustPEMHex(t, "3010020101040101a1082380030201020000")),
	)
	if err != nil || finalUnusedBits.PrivateKeyBytes()[31] != 1 {
		t.Fatalf("constructed final unused bits = %v, %v", finalUnusedBits, err)
	}
	for name, der := range map[string][]byte{
		"definite constructed scalar": mustPEMHex(t, "30080201012403040101"),
		"non-minimal high tag":        mustPEMHex(t, "3f1006020101040101"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrivateKeyFromPEM(MainNet, pemWrap("EC PRIVATE KEY", der)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
				t.Fatalf("error = %v, want ErrInvalidPrivateKeyPEM", err)
			}
		})
	}

	canonicalDER := mustPEMBody(t, pemVectorPKCS8)
	var info privateKeyInfoDER
	if _, err := asn1.Unmarshal(canonicalDER, &info); err != nil {
		t.Fatal(err)
	}
	info.Algorithm.Parameters = asn1.RawValue{FullBytes: []byte{0x02, 0x01, 0x01}}
	malformedParameters, err := asn1.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateKeyFromPEM(MainNet, pemWrap("PRIVATE KEY", malformedParameters)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
		t.Fatalf("malformed EC parameters error = %v", err)
	}

	malformedAttributes := appendASN1SequenceField(t, canonicalDER, []byte{0xa0, 0x02, 0x05, 0x00})
	if _, err := PrivateKeyFromPEM(MainNet, pemWrap("PRIVATE KEY", malformedAttributes)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
		t.Fatalf("malformed attributes error = %v", err)
	}
}

func TestPrivateKeyFromPEMRejectsMalformedAndInvalidScalars(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty":       "",
		"bad padding": "header\nA\nfooter",
		"bad DER":     "header\nYWJjZA==\nfooter",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrivateKeyFromPEM(MainNet, encoded); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
				t.Fatalf("error = %v, want ErrInvalidPrivateKeyPEM", err)
			}
		})
	}
	for name, secret := range map[string][]byte{
		"zero":  make([]byte, 32),
		"order": secp256k1OrderBytes(),
		"long":  make([]byte, 33),
	} {
		t.Run(name, func(t *testing.T) {
			der, err := asn1.Marshal(ecPrivateKeyDER{Version: 1, PrivateKey: secret})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PrivateKeyFromPEM(MainNet, pemWrap("EC PRIVATE KEY", der)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
				t.Fatalf("error = %v, want ErrInvalidPrivateKeyPEM", err)
			}
		})
	}

	canonicalDER := mustPEMBody(t, pemVectorPKCS8)
	var info privateKeyInfoDER
	if _, err := asn1.Unmarshal(canonicalDER, &info); err != nil {
		t.Fatal(err)
	}
	info.Algorithm.Algorithm = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	rsaLabeled, err := asn1.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateKeyFromPEM(MainNet, pemWrap("PRIVATE KEY", rsaLabeled)); !errors.Is(err, ErrInvalidPrivateKeyPEM) {
		t.Fatalf("non-EC PKCS#8 error = %v", err)
	}
}

func mustPEMHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustPEMBody(t *testing.T, value string) []byte {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(value), "\n")
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func pemWrap(label string, der []byte) string {
	encoded := base64.StdEncoding.EncodeToString(der)
	var result strings.Builder
	result.WriteString("-----BEGIN " + label + "-----\n")
	for len(encoded) > 64 {
		result.WriteString(encoded[:64] + "\n")
		encoded = encoded[64:]
	}
	result.WriteString(encoded + "\n-----END " + label + "-----\n")
	return result.String()
}

func appendASN1SequenceField(t *testing.T, der, field []byte) []byte {
	t.Helper()
	var sequence asn1.RawValue
	rest, err := asn1.Unmarshal(der, &sequence)
	if err != nil || len(rest) != 0 || sequence.Tag != asn1.TagSequence {
		t.Fatalf("parse sequence = rest %x, %v", rest, err)
	}
	contents := append(append([]byte(nil), sequence.Bytes...), field...)
	encoded, err := asn1.Marshal(asn1.RawValue{
		Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: contents,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func indefiniteASN1Sequence(t *testing.T, der []byte) []byte {
	t.Helper()
	var sequence asn1.RawValue
	rest, err := asn1.Unmarshal(der, &sequence)
	if err != nil || len(rest) != 0 || sequence.Tag != asn1.TagSequence {
		t.Fatalf("parse sequence = rest %x, %v", rest, err)
	}
	encoded := []byte{0x30, 0x80}
	encoded = append(encoded, sequence.Bytes...)
	return append(encoded, 0x00, 0x00)
}

func asn1TLV(tag byte, content []byte) []byte {
	encoded := appendBERLength([]byte{tag}, len(content))
	return append(encoded, content...)
}

func indefiniteASN1Value(tag byte, children ...[]byte) []byte {
	encoded := []byte{tag, 0x80}
	for _, child := range children {
		encoded = append(encoded, child...)
	}
	return append(encoded, 0, 0)
}

func secp256k1OrderBytes() []byte {
	return secp256k1.Params().N.Bytes()
}
