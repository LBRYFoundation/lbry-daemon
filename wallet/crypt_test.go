package wallet

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const (
	accountEncryptionSeed = "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent"
	accountEncryptionXPrv = "xprv9s21ZrQH143K42ovpZygnjfHdAqSd9jo7zceDfPRogM7bkkoNVv7DRNLEoB8HoirMgH969NrgL8jNzLEegqFzPRWM37GXd4uE8uuRkx4LAe"
	encryptedAccountSeed  = "MDAwMDAwMDAwMDAwMDAwMJ4e4W4pE6nQtPiD6MujNIQ7aFPhUBl63GwPziAgGNMBTMoaSjZfyyvw7ELMCqAYTWJ61aV7K4lmd2hR11g9dpdnnpCb9f9j3zLZHRv7+bIkZ//trah9AIkmrc/ZvNkC0Q=="
	encryptedAccountXPrv  = "MDAwMDAwMDAwMDAwMDAwMLkWikOLScA/ZxlFSGU7dl8pqVjgdpu1S3MWQF3IJ5HOXPAQcgnhHldVq98uP7Q8JqSWOv1p4gpxGSYnA4w5Gbuh0aUD4hmV70m7nVTj7T15+Pu30DCspndru59pee/S+mShoK68q7t7r32leaVIfzw="
)

func TestAccountSecretEncryptionMatchesPinnedSDKVectors(t *testing.T) {
	initializationVector := []byte("0000000000000000")
	for _, test := range []struct {
		name      string
		plaintext string
		encoded   string
	}{
		{name: "seed", plaintext: accountEncryptionSeed, encoded: encryptedAccountSeed},
		{name: "extended private key", plaintext: accountEncryptionXPrv, encoded: encryptedAccountXPrv},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := EncryptAccountSecret("password", test.plaintext, initializationVector)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.encoded {
				t.Fatalf("encrypted value = %q, want %q", got, test.encoded)
			}
			plaintext, gotIV, err := DecryptAccountSecret("password", got)
			if err != nil {
				t.Fatal(err)
			}
			if plaintext != test.plaintext || !bytes.Equal(gotIV, initializationVector) {
				t.Fatalf("decrypted value = (%q, %q)", plaintext, gotIV)
			}
		})
	}
}

func TestAccountSecretEncryptionUsesRandomIV(t *testing.T) {
	encoded, err := EncryptAccountSecret("password", "value", nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 32 {
		t.Fatalf("encrypted length = %d, want 32", len(decoded))
	}
	plaintext, initializationVector, err := DecryptAccountSecret("password", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "value" || !bytes.Equal(initializationVector, decoded[:16]) {
		t.Fatalf("decrypted value = (%q, %x)", plaintext, initializationVector)
	}
}

func TestAccountSecretDecryptionMatchesLegacyFailures(t *testing.T) {
	if _, _, err := DecryptAccountSecret("wrong", encryptedAccountSeed); !errors.Is(err, ErrInvalidAccountPassword) {
		t.Fatalf("wrong-password error = %v", err)
	}
	if _, _, err := DecryptAccountSecret("password", base64.StdEncoding.EncodeToString([]byte("short"))); !errors.Is(err, ErrInvalidAccountIV) {
		t.Fatalf("short-IV error = %v", err)
	}
	partialBlock := base64.StdEncoding.EncodeToString(append([]byte("0000000000000000"), 1))
	if _, _, err := DecryptAccountSecret("password", partialBlock); !errors.Is(err, ErrInvalidAccountPassword) {
		t.Fatalf("partial-block error = %v", err)
	}
	if _, err := EncryptAccountSecret("password", "value", []byte("short")); !errors.Is(err, ErrInvalidAccountIV) {
		t.Fatalf("encrypt IV error = %v", err)
	}
}

func TestAccountSecretDecryptionPreservesMissingFinalizeBug(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(encryptedAccountSeed)
	if err != nil {
		t.Fatal(err)
	}
	encodedWithPartialBlock := base64.StdEncoding.EncodeToString(append(decoded, 0xff))
	plaintext, _, err := DecryptAccountSecret("password", encodedWithPartialBlock)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != accountEncryptionSeed {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestAccountSecretBase64UsesPythonValidationMode(t *testing.T) {
	decorated := "!" + strings.ReplaceAll(encryptedAccountSeed, "M", "M \n") + "?"
	plaintext, _, err := DecryptAccountSecret("password", decorated)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != accountEncryptionSeed {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestPythonBase64DecoderPreservesNonStrictPadding(t *testing.T) {
	tests := []struct {
		encoded string
		want    []byte
		wantErr bool
	}{
		{encoded: "!", want: []byte{}},
		{encoded: "====", want: []byte{}},
		{encoded: "AA==", want: []byte{0}},
		{encoded: "AAAA===", want: []byte{0, 0, 0}},
		{encoded: "MDAw-_MDAw", want: []byte("000000")},
		{encoded: "A===Gk=", want: []byte{0, 'i'}},
		{encoded: "AA==BBBB", want: []byte{0}},
		{encoded: "A===", wantErr: true},
		{encoded: "aGk", wantErr: true},
	}
	for _, test := range tests {
		got, err := decodePythonBase64(test.encoded)
		if test.wantErr {
			if err == nil {
				t.Errorf("decodePythonBase64(%q) succeeded with %x", test.encoded, got)
			}
			continue
		}
		if err != nil || !bytes.Equal(got, test.want) {
			t.Errorf("decodePythonBase64(%q) = %x, %v; want %x", test.encoded, got, err, test.want)
		}
	}
}

func TestAccountSecretEncryptionPadsWholeBlock(t *testing.T) {
	encoded, err := EncryptAccountSecret("", "", []byte("0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 32 {
		t.Fatalf("empty plaintext encrypted length = %d, want 32", len(decoded))
	}
	plaintext, _, err := DecryptAccountSecret("", encoded)
	if err != nil || plaintext != "" {
		t.Fatalf("empty plaintext round trip = %q, %v", plaintext, err)
	}
}
