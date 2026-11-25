package rpc

import (
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"lbry/daemon/wallet"
)

// A throwaway channel key, its public key, and the signature of deadbeef computed with an independent
// implementation (pure Python secp256k1, RFC 6979, low s) for the claim hash 00 01 .. 13 and the salt below.
const (
	signTestPrivateKey = "8257020d54902465c5fbefa169309c61aebe12913e43c20e943d72c1deafc0a3"
	signTestPublicKey  = "02c9f0f75acebc5419d784b3f66f87e37464c8fbe63bbdda98f86d18a534711e67"
	signTestSignature  = "44ce1cc94109514c08350f36b2be49dad3fff92f9bb6cfe72f7d3f0d2f961da20b03a224106f38065c3d342f3eae807729c53455cb4352e32979e226d1295b54"
	signTestSalt       = "1700000000"
)

func signTestChannel(t *testing.T, withKey bool) ownedChannel {
	t.Helper()
	raw, _ := hex.DecodeString(signTestPrivateKey)
	key := secp256k1.PrivKeyFromBytes(raw)
	address := wallet.AddressFromPublicKey(key.PubKey().SerializeCompressed())

	account := &wallet.Account{}
	if withKey {
		der, err := asn1.Marshal(struct {
			Version    int
			PrivateKey []byte
			Curve      asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
		}{1, key.Serialize(), asn1.ObjectIdentifier{1, 3, 132, 0, 10}})
		if err != nil {
			t.Fatal(err)
		}
		certificate, _ := json.Marshal(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})))
		account.Certificates = map[string]json.RawMessage{address: certificate}
	}
	return ownedChannel{
		item: map[string]any{
			"claim_id": signTestClaimIDFull(),
			"value":    map[string]any{"public_key": signTestPublicKey},
		},
		account: account,
	}
}

// signTestClaimIDFull is the 20 byte claim hash 00 01 .. 13 reversed, as 40 hexadecimal characters.
func signTestClaimIDFull() string {
	hash := make([]byte, 20)
	for i := range hash {
		hash[19-i] = byte(i)
	}
	return hex.EncodeToString(hash)
}

func TestSignWithChannelGivesTheSignatureOfLbrySDK(t *testing.T) {
	result, err := signWithChannel([]ownedChannel{signTestChannel(t, true)}, signTestClaimIDFull(), "", []byte{0xde, 0xad, 0xbe, 0xef}, signTestSalt, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result["signature"] != signTestSignature || result["salt"] != signTestSalt || result["signing_ts"] != signTestSalt {
		t.Fatalf("result = %v", result)
	}
}

func TestSignWithChannelSaltsWithTheTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	result, err := signWithChannel([]ownedChannel{signTestChannel(t, true)}, "", "@name", []byte{0xde, 0xad, 0xbe, 0xef}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if result["signature"] != signTestSignature || result["salt"] != signTestSalt {
		t.Fatalf("result = %v", result)
	}
}

func TestSignWithChannelErrors(t *testing.T) {
	withKey := signTestChannel(t, true)
	withoutKey := signTestChannel(t, false)
	tests := []struct {
		name     string
		channels []ownedChannel
		id, nam  string
		want     string
	}{
		{"nothing to find it by", nil, "", "", "channel_id or channel_name was not provided"},
		{"unknown id", nil, "abc", "", "Couldn't find channel with channel_id 'abc'."},
		{"unknown name", nil, "", "@x", "Couldn't find channel with channel_name '@x'."},
		{"two with the name", []ownedChannel{withKey, withKey}, "", "@x", "Multiple channels found with channel_name '@x'"},
		{"no key", []ownedChannel{withoutKey}, "", "@x", "Couldn't find private key for name '@x'."},
	}
	for _, test := range tests {
		_, err := signWithChannel(test.channels, test.id, test.nam, []byte{1}, "s", time.Now())
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", test.name, err, test.want)
		}
	}
}
