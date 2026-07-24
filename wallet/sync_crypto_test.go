package wallet

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestBetterAESDecryptsPinnedSDKVector(t *testing.T) {
	encoded := []byte("czo4MTkyOjE2OjE6VrwsN8FSJlegxHVEQePoyjWT1k8yAXBCUbbGCFKcsNY=")
	plaintext, err := BetterAESDecrypt("super secret", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(plaintext), "valuable value"; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}

	wrongPassword, err := BetterAESDecrypt("super secret but wrong", encoded)
	if err != nil {
		t.Fatalf("pinned valid-padding wrong-password vector failed: %v", err)
	}
	if bytes.Equal(wrongPassword, plaintext) {
		t.Fatal("wrong password returned the original plaintext")
	}
}

func TestBetterAESEnvelopeRoundTripAndLayout(t *testing.T) {
	initializationVector := bytes.Repeat([]byte{'d'}, 16)
	encoded, err := betterAESEncrypt(
		"super secret", []byte("valuable value"), bytes.NewReader(initializationVector),
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := append([]byte("s:8192:16:1:"), initializationVector...)
	if !bytes.HasPrefix(decoded, wantPrefix) {
		t.Fatalf("decoded envelope prefix = %x, want prefix %x", decoded, wantPrefix)
	}
	if got := len(decoded) - len(wantPrefix); got == 0 || got%16 != 0 {
		t.Fatalf("ciphertext length = %d, want a positive AES block multiple", got)
	}
	if got, want := string(encoded), "czo4MTkyOjE2OjE6ZGRkZGRkZGRkZGRkZGRkZOe10OyeLVMYoPfF8/1tquo="; got != want {
		t.Fatalf("fixed-IV envelope = %q, want %q", got, want)
	}
	plaintext, err := BetterAESDecrypt("super secret", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(plaintext), "valuable value"; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}
	if _, err := BetterAESDecrypt("super secret but wrong", encoded); !errors.Is(err, ErrInvalidWalletPassword) {
		t.Fatalf("wrong-password error = %v, want ErrInvalidWalletPassword", err)
	}
}

func TestBetterAESPreservesPinnedParsingAndPartialBlockBehavior(t *testing.T) {
	encoded, err := betterAESEncrypt("password", []byte("payload"), bytes.NewReader(bytes.Repeat([]byte{3}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	payload := decoded[len("s:8192:16:1:"):]
	legacyFields := append([]byte("ignored:8_192:+16:01:"), payload...)
	legacyFields = append(legacyFields, 0xff)
	legacyEncoded := []byte(base64.StdEncoding.EncodeToString(legacyFields))
	plaintext, err := BetterAESDecrypt("password", legacyEncoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(plaintext), "payload"; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}

	// base64.b64decode(validate=False) ignores non-alphabet bytes.
	permissive := append([]byte(" \n"), encoded...)
	permissive = append(permissive, '\t')
	if plaintext, err := BetterAESDecrypt("password", permissive); err != nil || string(plaintext) != "payload" {
		t.Fatalf("permissive base64 decode = %q, %v", plaintext, err)
	}
}

func TestBetterAESRejectsMalformedEnvelopeWithoutReclassifyingItAsPassword(t *testing.T) {
	for _, encoded := range [][]byte{
		[]byte("not base64"),
		[]byte(base64.StdEncoding.EncodeToString([]byte("s:8192:16:1:"))),
		[]byte(base64.StdEncoding.EncodeToString([]byte("s:bad:16:1:0123456789abcdef"))),
	} {
		if _, err := BetterAESDecrypt("password", encoded); err == nil || errors.Is(err, ErrInvalidWalletPassword) {
			t.Fatalf("malformed envelope error = %v", err)
		}
	}
	if _, err := betterAESEncrypt("password", nil, bytes.NewReader([]byte("short"))); err == nil {
		t.Fatal("short entropy source succeeded")
	}

	encoded, err := betterAESEncrypt("password", []byte("payload"), bytes.NewReader(bytes.Repeat([]byte{4}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	payload := decoded[len("s:8192:16:1:"):]
	nonASCIIHeader := append([]byte("s:\xc2\xa08192:16:1:"), payload...)
	if _, err := BetterAESDecrypt("password", []byte(base64.StdEncoding.EncodeToString(nonASCIIHeader))); err == nil {
		t.Fatal("non-ASCII whitespace in a bytes int field succeeded")
	}
}
