package wallet

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// A throwaway key, and the signature of the data below computed with an independent implementation (pure
// Python secp256k1 with RFC 6979 and a low s, which is what libsecp256k1 produces for lbry-sdk).
const (
	throwawayPrivateKey = "8257020d54902465c5fbefa169309c61aebe12913e43c20e943d72c1deafc0a3"
	throwawayPublicKey  = "02c9f0f75acebc5419d784b3f66f87e37464c8fbe63bbdda98f86d18a534711e67"
	throwawaySignature  = "44ce1cc94109514c08350f36b2be49dad3fff92f9bb6cfe72f7d3f0d2f961da20b03a224106f38065c3d342f3eae807729c53455cb4352e32979e226d1295b54"
)

var secp256k1OID = asn1.ObjectIdentifier{1, 3, 132, 0, 10}

func throwawayKey(t *testing.T) *secp256k1.PrivateKey {
	t.Helper()
	raw, err := hex.DecodeString(throwawayPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return secp256k1.PrivKeyFromBytes(raw)
}

// sec1PEM writes the key as lbry-sdk's `to_pem` does.
func sec1PEM(t *testing.T, key *secp256k1.PrivateKey) string {
	t.Helper()
	der, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
		Curve      asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
	}{1, key.Serialize(), secp256k1OID})
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func pkcs8PEM(t *testing.T, key *secp256k1.PrivateKey) string {
	t.Helper()
	inner, _ := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
	}{1, key.Serialize()})
	curve, _ := asn1.Marshal(secp256k1OID)
	der, err := asn1.Marshal(struct {
		Version   int
		Algorithm pkix.AlgorithmIdentifier
		Key       []byte
	}{0, pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}, Parameters: asn1.RawValue{FullBytes: curve}}, inner})
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func accountWithCertificate(t *testing.T, address, certificate string) *Account {
	t.Helper()
	encoded, _ := json.Marshal(certificate)
	return &Account{Certificates: map[string]json.RawMessage{address: encoded}}
}

func TestSignChannelDataMatchesAnIndependentImplementation(t *testing.T) {
	claimHash := make([]byte, 20)
	for i := range claimHash {
		claimHash[i] = byte(i)
	}
	got := SignChannelData(throwawayKey(t), claimHash, []byte{0xde, 0xad, 0xbe, 0xef}, "1700000000")
	if got != throwawaySignature {
		t.Fatalf("signature = %s, want %s", got, throwawaySignature)
	}
}

func TestChannelPrivateKeyReadsTheCertificate(t *testing.T) {
	key := throwawayKey(t)
	publicKey, _ := hex.DecodeString(throwawayPublicKey)
	address := AddressFromPublicKey(publicKey)

	for name, certificate := range map[string]string{"sec1": sec1PEM(t, key), "pkcs8": pkcs8PEM(t, key)} {
		found, err := accountWithCertificate(t, address, certificate).ChannelPrivateKey(publicKey)
		if err != nil || found == nil || !found.Key.Equals(&key.Key) {
			t.Errorf("%s: key = %v, err = %v", name, found, err)
		}
	}
}

func TestChannelPrivateKeyAcceptsAKeyInfoPublicKey(t *testing.T) {
	key := throwawayKey(t)
	publicKey, _ := hex.DecodeString(throwawayPublicKey)
	curve, _ := asn1.Marshal(secp256k1OID)
	info, err := asn1.Marshal(struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}{
		pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}, Parameters: asn1.RawValue{FullBytes: curve}},
		asn1.BitString{Bytes: key.PubKey().SerializeUncompressed(), BitLength: 65 * 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	found, err := accountWithCertificate(t, AddressFromPublicKey(publicKey), sec1PEM(t, key)).ChannelPrivateKey(info)
	if err != nil || found == nil {
		t.Fatalf("key = %v, err = %v", found, err)
	}
}

func TestChannelPrivateKeyWithoutAKey(t *testing.T) {
	publicKey, _ := hex.DecodeString(throwawayPublicKey)

	if found, err := (&Account{}).ChannelPrivateKey(publicKey); found != nil || err != nil {
		t.Errorf("an account with no keys: key = %v, err = %v", found, err)
	}
	// Locked: the account's own key is encrypted, so the derived keys are out of reach.
	if found, err := (&Account{Locked: true, PrivateKey: "encrypted"}).ChannelPrivateKey(publicKey); found != nil || err != nil {
		t.Errorf("a locked account: key = %v, err = %v", found, err)
	}
	if _, err := (&Account{}).ChannelPrivateKey([]byte{1, 2, 3}); err == nil {
		t.Error("a public key that is not a key was accepted")
	}
}

func TestChannelPrivateKeyRejectsACertificateOfAnotherKey(t *testing.T) {
	publicKey, _ := hex.DecodeString(throwawayPublicKey)
	other := secp256k1.PrivKeyFromBytes(append(make([]byte, 31), 7))

	_, err := accountWithCertificate(t, AddressFromPublicKey(publicKey), sec1PEM(t, other)).ChannelPrivateKey(publicKey)
	if err == nil {
		t.Fatal("a certificate that holds another key was accepted")
	}
}

// BIP32 test vector 1: the child 1 of m/0H, which is what a non-hardened private derivation has to give.
func TestExtendedPrivateKeyChildMatchesBIP32(t *testing.T) {
	parent, err := parseExtendedPrivateKey("xprv9uHRZZhk6KAJC1avXpDAp4MDc3sQKNxDiPvvkX8Br5ngLNv1TxvUxt4cV1rGL5hj6KCesnDYUhd7oWgT11eZG7XnxHrnYeSvkzY7d2bhkJ7")
	if err != nil {
		t.Fatal(err)
	}
	want, err := parseExtendedPrivateKey("xprv9wTYmMFdV23N2TdNG573QoEsfRrWKQgWeibmLntzniatZvR9BmLnvSxqu53Kw1UmYPxLgboyZQaXwTCg8MSY3H2EU4pWcQDnRnrVA1xe8fs")
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.child(1)
	if err != nil {
		t.Fatal(err)
	}
	if !child.key.Key.Equals(&want.key.Key) || hex.EncodeToString(child.chainCode) != hex.EncodeToString(want.chainCode) {
		t.Fatal("child 1 of m/0H differs from the BIP32 vector")
	}
}

func TestChannelPrivateKeyDerivesTheAccountsChannelKeys(t *testing.T) {
	const master = "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"
	root, err := parseExtendedPrivateKey(master)
	if err != nil {
		t.Fatal(err)
	}
	channels, _ := root.child(channelChain)
	third, _ := channels.child(3)
	publicKey := third.key.PubKey().SerializeCompressed()

	found, err := (&Account{PrivateKey: master}).ChannelPrivateKey(publicKey)
	if err != nil || found == nil || !found.Key.Equals(&third.key.Key) {
		t.Fatalf("key = %v, err = %v", found, err)
	}
	// A key that is not one of the account's.
	other, _ := hex.DecodeString(throwawayPublicKey)
	if found, err := (&Account{PrivateKey: master}).ChannelPrivateKey(other); found != nil || err != nil {
		t.Fatalf("a foreign key: key = %v, err = %v", found, err)
	}
}
