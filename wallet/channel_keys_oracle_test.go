package wallet

import (
	"bytes"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	channelKeysOraclePinnedCommit      = "e7666f489418e96b6d2104974e93915b539235c5"
	channelKeysOraclePinnedVersion     = "0.113.0"
	channelKeysOraclePEMPrivateHex     = "16514d9eed1e76021f7204f660a8f79e3ae8bfd28581615c6ae3992305270f1e"
	channelKeysOraclePEMPublicHex      = "039ae7283f3f6723e0a166b7e19e1d1167f6dc5f4af61b4a58066a0d2a8bed2b35"
	channelKeysOracleCompactPrivateHex = "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c"
	channelKeysOracleCompactPublicHex  = "0243671cb26d01375c75dca6c4a2adc57fdbb55e69c32db9db38c7d23f8ed5538b"
	channelKeysOracleDigestHex         = "9fc0b0a4a1e7a2aa2b0cd0a5566f4847ed9f66f92c7f0fc3cc4e3cea6f29a0ff"
	channelKeysOracleSignatureHex      = "100f7542643e64d9efa3c78c60210de67585889e5efa715eb2b30ae5d047d809" +
		"1a9f9d0e13182b030eefcb567f3a6c5597259bd21ac0275a2b394c28a6c5e61e"
	channelKeysOracleHighSHex = "100f7542643e64d9efa3c78c60210de67585889e5efa715eb2b30ae5d047d809" +
		"e56062f1ece7d4fcf11034a980c593a923894114948878e19499126429705b23"
	channelKeysOracleOrderHex = "fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141"
	channelKeysOracleSeedHex  = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	channelKeysOraclePKCS8 = "-----BEGIN PRIVATE KEY-----\n" +
		"MIGEAgEAMBAGByqGSM49AgEGBSuBBAAKBG0wawIBAQQgFlFNnu0edgIfcgT2YKj3\n" +
		"njrov9KFgWFcauOZIwUnDx6hRANCAASa5yg/P2cj4KFmt+GeHRFn9txfSvYbSlgG\n" +
		"ag0qi+0rNcZrzLTsProxaxapem1qSo7/0p10iQG7l4k1JRnNALE9\n" +
		"-----END PRIVATE KEY-----\n"
	channelKeysOracleSEC1 = "-----BEGIN EC PRIVATE KEY-----\n" +
		"MHQCAQEEIBZRTZ7tHnYCH3IE9mCo95466L/ShYFhXGrjmSMFJw8eoAcGBSuBBAAK\n" +
		"oUQDQgAEmucoPz9nI+ChZrfhnh0RZ/bcX0r2G0pYBmoNKovtKzXGa8y07D66MWsW\n" +
		"qXptakqO/9KddIkBu5eJNSUZzQCxPQ==\n" +
		"-----END EC PRIVATE KEY-----\n"
)

var channelKeysOracleStateFields = []string{
	"certificates", "cache", "last_known", "manager_private_key_loaded",
	"manager_private_key", "usage_calls", "usage_remaining", "save_calls", "save_remaining",
}

func TestChannelKeysMatchPinnedPythonOracle(t *testing.T) {
	seed := channelKeysOracleMustHex(t, channelKeysOracleSeedHex)
	root, err := keys.PrivateKeyFromSeed(keys.RegTest, seed)
	if err != nil {
		t.Fatal(err)
	}
	channelRoot, err := root.Child(ChannelChain)
	if err != nil {
		t.Fatal(err)
	}
	children := make([]*keys.PrivateKey, 4)
	for index := range children {
		children[index], err = channelRoot.Child(int64(index))
		if err != nil {
			t.Fatal(err)
		}
	}
	imported, err := keys.PrivateKeyFromPEM(keys.RegTest, channelKeysOraclePKCS8)
	if err != nil {
		t.Fatal(err)
	}
	other, err := keys.NewPrivateKey(
		keys.RegTest, bytes.Repeat([]byte{2}, 32), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	mutatedSEC1DER := channelKeysOraclePEMBody(t, channelKeysOracleSEC1)
	mutatedSEC1DER[len(mutatedSEC1DER)-1] ^= 0xff
	shortDER, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
	}{Version: 1, PrivateKey: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	leadingZeroDER, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
	}{Version: 1, PrivateKey: append(
		[]byte{0}, channelKeysOracleMustHex(t, channelKeysOraclePEMPrivateHex)...,
	)})
	if err != nil {
		t.Fatal(err)
	}
	zeroDER, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
	}{Version: 1, PrivateKey: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	orderDER, err := asn1.Marshal(struct {
		Version    int
		PrivateKey []byte
	}{Version: 1, PrivateKey: channelKeysOracleMustHex(t, channelKeysOracleOrderHex)})
	if err != nil {
		t.Fatal(err)
	}
	trailingDER := append(channelKeysOraclePEMBody(t, channelKeysOraclePKCS8), []byte("ignored suffix")...)
	pkcs8DER := channelKeysOraclePEMBody(t, channelKeysOraclePKCS8)
	attributesDER := channelKeysOracleAppendSequenceField(t, pkcs8DER, []byte{0xa0, 0x00})
	indefinitePKCS8 := channelKeysOracleIndefiniteSequence(t, pkcs8DER)
	permissivePEM := " \r\n-----BEGIN IGNORED LABEL-----\r\n" +
		strings.ReplaceAll(channelKeysOraclePEMContents(channelKeysOraclePKCS8), "\n", "!\r\n") +
		"-----END ANOTHER LABEL-----\r\n\t"

	signCases := []map[string]any{
		{
			"name": "fixed compact signature", "private_key_hex": channelKeysOracleCompactPrivateHex,
			"digest_hex": channelKeysOracleDigestHex,
			"expected_result": map[string]any{
				"signature_hex":  channelKeysOracleSignatureHex,
				"public_key_hex": channelKeysOracleCompactPublicHex,
			},
		},
		{
			"name": "long compact signing buffer", "private_key_hex": channelKeysOracleCompactPrivateHex,
			"digest_hex": channelKeysOracleDigestHex + "deadbeef",
			"expected_result": map[string]any{
				"signature_hex":  channelKeysOracleSignatureHex,
				"public_key_hex": channelKeysOracleCompactPublicHex,
			},
		},
		{
			"name": "invalid digest length", "private_key_hex": channelKeysOracleCompactPrivateHex,
			"digest_hex": strings.Repeat("00", 31),
		},
		{
			"name": "zero private scalar", "private_key_hex": strings.Repeat("00", 32),
			"digest_hex": channelKeysOracleDigestHex,
		},
	}
	verifyCases := []map[string]any{
		{
			"name": "valid low S", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": channelKeysOracleSignatureHex, "digest_hex": channelKeysOracleDigestHex,
		},
		{
			"name": "valid high S", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": channelKeysOracleHighSHex, "digest_hex": channelKeysOracleDigestHex,
		},
		{
			"name": "zero R", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": strings.Repeat("00", 32) + channelKeysOracleSignatureHex[64:],
			"digest_hex":    channelKeysOracleDigestHex,
		},
		{
			"name": "overflow R", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": channelKeysOracleOrderHex + channelKeysOracleSignatureHex[64:],
			"digest_hex":    channelKeysOracleDigestHex,
		},
		{
			"name": "signature length wins", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": strings.Repeat("00", 63), "digest_hex": strings.Repeat("00", 31),
		},
		{
			"name": "digest length", "public_key_hex": channelKeysOracleCompactPublicHex,
			"signature_hex": channelKeysOracleSignatureHex, "digest_hex": strings.Repeat("00", 31),
		},
		{
			"name": "public key validation wins", "public_key_hex": strings.Repeat("00", 32),
			"signature_hex": strings.Repeat("00", 63), "digest_hex": strings.Repeat("00", 31),
		},
	}
	pemCases := []map[string]any{
		{"name": "canonical encode", "operation": "encode", "private_key_hex": channelKeysOraclePEMPrivateHex},
		{"name": "legacy SEC1", "operation": "decode", "pem": channelKeysOracleSEC1},
		{"name": "canonical PKCS8", "operation": "round_trip", "pem": channelKeysOraclePKCS8},
		{"name": "ignored labels and junk", "operation": "decode", "pem": permissivePEM},
		{
			"name": "misplaced base64 padding", "operation": "decode",
			"pem": strings.Replace(channelKeysOraclePKCS8, "MIGE", "MIG=E", 1),
		},
		{
			"name": "trailing DER", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("PRIVATE KEY", trailingDER),
		},
		{
			"name": "ignored embedded point", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("EC PRIVATE KEY", mutatedSEC1DER),
		},
		{
			"name": "short scalar padding", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("ANY", shortDER),
		},
		{
			"name": "leading zero scalar", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("ANY", leadingZeroDER),
		},
		{
			"name": "PKCS8 attributes", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("PRIVATE KEY", attributesDER),
		},
		{
			"name": "indefinite PKCS8", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("PRIVATE KEY", indefinitePKCS8),
		},
		{
			"name": "unknown trailing SEC1 field", "operation": "decode",
			"pem": channelKeysOracleWrapPEM(
				"EC PRIVATE KEY", channelKeysOracleMustHex(t, "3008020101040101a200"),
			),
		},
		{
			"name": "malformed SEC1 parameters", "operation": "decode",
			"pem": channelKeysOracleWrapPEM(
				"EC PRIVATE KEY", channelKeysOracleMustHex(t, "300b020101040101a003020101"),
			),
		},
		{
			"name": "definite constructed scalar", "operation": "decode",
			"pem": channelKeysOracleWrapPEM(
				"EC PRIVATE KEY", channelKeysOracleMustHex(t, "30080201012403040101"),
			),
		},
		{
			"name": "constructed final unused bits", "operation": "decode",
			"pem": channelKeysOracleWrapPEM(
				"EC PRIVATE KEY", channelKeysOracleMustHex(t, "3010020101040101a1082380030201020000"),
			),
		},
		{
			"name": "non-minimal ASN1 high tag", "operation": "decode",
			"pem": channelKeysOracleWrapPEM(
				"EC PRIVATE KEY", channelKeysOracleMustHex(t, "3f1006020101040101"),
			),
		},
		{"name": "empty PEM", "operation": "decode", "pem": ""},
		{"name": "bad base64 padding", "operation": "decode", "pem": "header\nA\nfooter"},
		{
			"name": "bad DER", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("PRIVATE KEY", []byte("abcd")),
		},
		{
			"name": "zero scalar", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("EC PRIVATE KEY", zeroDER),
		},
		{
			"name": "order scalar", "operation": "decode",
			"pem": channelKeysOracleWrapPEM("EC PRIVATE KEY", orderDER),
		},
	}

	accountCases := []map[string]any{
		{
			"name": "imported precedence migration and fallback", "address_prefix_hex": "6f",
			"seed_hex": channelKeysOracleSeedHex,
			"certificates": []any{
				[]any{"noise", "not valid key"},
				[]any{"wrong-address", channelKeysOracleSEC1},
				[]any{"duplicate", channelKeysOracleSEC1},
				[]any{"non-string", 1},
				[]any{"leading-space", " " + channelKeysOracleSEC1},
			},
			"usage":       []any{false},
			"save_errors": []any{nil},
			"actions": []any{
				map[string]any{"action": "migrate"},
				map[string]any{"action": "get", "public_key_hex": channelKeysOraclePEMPublicHex},
				map[string]any{"action": "add", "private_key_hex": channelKeysOraclePEMPrivateHex},
				map[string]any{
					"action": "set_certificates", "certificates": []any{
						[]any{other.Address(), channelKeysOraclePKCS8},
					},
				},
				map[string]any{"action": "get", "public_key_hex": hex.EncodeToString(other.PublicKey().CompressedBytes())},
				map[string]any{
					"action": "set_certificates", "certificates": []any{
						[]any{imported.Address(), ""},
					},
				},
				map[string]any{"action": "get", "public_key_hex": channelKeysOraclePEMPublicHex},
				map[string]any{"action": "generate"},
				map[string]any{"action": "get", "public_key_hex": hex.EncodeToString(children[0].PublicKey().CompressedBytes())},
				map[string]any{
					"action": "set_certificates", "certificates": []any{
						[]any{children[0].Address(), "not PEM"},
					},
				},
				map[string]any{"action": "get", "public_key_hex": hex.EncodeToString(children[0].PublicKey().CompressedBytes())},
			},
		},
		{
			"name": "migration rollback and save failure", "address_prefix_hex": "6f",
			"seed_hex": channelKeysOracleSeedHex,
			"certificates": []any{
				[]any{"first", channelKeysOracleSEC1},
				[]any{"bad", "-----BEGIN malformed"},
			},
			"save_errors": []any{map[string]any{"error": "injected save failure"}},
			"actions": []any{
				map[string]any{"action": "migrate"},
				map[string]any{
					"action": "set_certificates", "certificates": []any{
						[]any{"wrong", channelKeysOracleSEC1},
					},
				},
				map[string]any{"action": "migrate"},
				map[string]any{"action": "migrate"},
			},
		},
	}
	managerCases := []map[string]any{
		{
			"name": "used scan observation and idempotence", "address_prefix_hex": "6f",
			"seed_hex": channelKeysOracleSeedHex, "usage": []any{true, true, false},
			"actions": []any{
				map[string]any{"action": "prime"},
				map[string]any{"action": "maybe", "public_key_hex": hex.EncodeToString(children[3].PublicKey().CompressedBytes())},
				map[string]any{"action": "maybe", "public_key_hex": hex.EncodeToString(children[2].PublicKey().CompressedBytes())},
				map[string]any{"action": "set_usage", "usage": []any{false}},
				map[string]any{"action": "generate"},
				map[string]any{"action": "get_cached", "address": children[1].Address()},
			},
		},
		{
			"name": "probe error preserves cached candidate", "address_prefix_hex": "6f",
			"seed_hex": channelKeysOracleSeedHex,
			"usage":    []any{map[string]any{"error": "usage failed"}},
			"actions": []any{
				map[string]any{"action": "generate"},
				map[string]any{"action": "set_usage", "usage": []any{false}},
				map[string]any{"action": "generate"},
			},
		},
		{
			"name": "watch only", "address_prefix_hex": "55", "usage": []any{},
			"actions": []any{
				map[string]any{"action": "prime"},
				map[string]any{"action": "maybe", "public_key_hex": channelKeysOraclePEMPublicHex},
				map[string]any{"action": "generate"},
				map[string]any{"action": "get_cached", "address": "missing"},
			},
		},
	}

	payload := map[string]any{
		"sign_cases":    signCases,
		"verify_cases":  verifyCases,
		"pem_cases":     pemCases,
		"account_cases": accountCases,
		"manager_cases": managerCases,
	}
	oracle := runChannelKeysOracle(t, payload)
	channelKeysOracleAssertReference(t, oracle)
	channelKeysOracleAssertOutcomes(t, signCases, oracle["sign_cases"], channelKeysOracleRunSign)
	channelKeysOracleAssertOutcomes(t, verifyCases, oracle["verify_cases"], channelKeysOracleRunVerify)
	channelKeysOracleAssertOutcomes(t, pemCases, oracle["pem_cases"], channelKeysOracleRunPEM)
	channelKeysOracleAssertStateful(t, accountCases, oracle["account_cases"])
	channelKeysOracleAssertStateful(t, managerCases, oracle["manager_cases"])
}

type channelKeysOracleExecutor func(map[string]any) (any, error)

func channelKeysOracleAssertOutcomes(
	t *testing.T, fixtures []map[string]any, rawOracle any, execute channelKeysOracleExecutor,
) {
	t.Helper()
	oracle, ok := rawOracle.([]any)
	if !ok || len(oracle) != len(fixtures) {
		t.Fatalf("channel-key oracle outcome shape = %T/%d, want %d", rawOracle, len(oracle), len(fixtures))
	}
	for index, fixture := range fixtures {
		name := fixture["name"].(string)
		t.Run(name, func(t *testing.T) {
			want := oracle[index].(map[string]any)
			got, err := execute(fixture)
			channelKeysOracleAssertErrorParity(t, err, want["error_type"])
			if err == nil {
				if fixed, ok := fixture["expected_result"]; ok {
					channelKeysOracleAssertJSON(t, want["result"], fixed)
					channelKeysOracleAssertJSON(t, got, fixed)
				}
				channelKeysOracleAssertJSON(t, got, want["result"])
			}
		})
	}
}

func channelKeysOracleRunSign(fixture map[string]any) (any, error) {
	privateKey, err := channelKeysOraclePrivateKey(
		keys.MainNet, fixture["private_key_hex"].(string), make([]byte, 32),
	)
	if err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(fixture["digest_hex"].(string))
	if err != nil {
		return nil, err
	}
	signature, err := privateKey.SignCompact(digest)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"signature_hex":  hex.EncodeToString(signature),
		"public_key_hex": hex.EncodeToString(privateKey.PublicKey().CompressedBytes()),
	}, nil
}

func channelKeysOracleRunVerify(fixture map[string]any) (any, error) {
	publicKey, err := hex.DecodeString(fixture["public_key_hex"].(string))
	if err != nil {
		return nil, err
	}
	signature, err := hex.DecodeString(fixture["signature_hex"].(string))
	if err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(fixture["digest_hex"].(string))
	if err != nil {
		return nil, err
	}
	return keys.VerifyCompactSignature(publicKey, signature, digest)
}

func channelKeysOracleRunPEM(fixture map[string]any) (any, error) {
	operation, _ := fixture["operation"].(string)
	var privateKey *keys.PrivateKey
	var err error
	switch operation {
	case "encode", "":
		privateKey, err = channelKeysOraclePrivateKey(
			keys.MainNet, fixture["private_key_hex"].(string), make([]byte, 32),
		)
	case "decode", "round_trip":
		privateKey, err = keys.PrivateKeyFromPEM(keys.MainNet, fixture["pem"].(string))
	default:
		return nil, fmt.Errorf("unknown PEM operation %q", operation)
	}
	if err != nil {
		return nil, err
	}
	return channelKeysOracleKeyView(privateKey, true)
}

type channelKeysOracleUsage struct {
	values []any
	calls  []any
}

func (usage *channelKeysOracleUsage) lookup(_ *Account, publicKey *keys.PublicKey) (bool, error) {
	usage.calls = append(usage.calls, map[string]any{
		"index":          publicKey.ChildNumber(),
		"address":        publicKey.Address(),
		"public_key_hex": hex.EncodeToString(publicKey.CompressedBytes()),
	})
	if len(usage.values) == 0 {
		return false, errors.New("usage sequence exhausted")
	}
	value := usage.values[0]
	usage.values = usage.values[1:]
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case nil:
		return false, errors.New("injected channel-key usage error")
	case string:
		if typed == "error" {
			return false, errors.New("injected channel-key usage error")
		}
	case map[string]any:
		if message, exists := typed["error"]; exists {
			return false, errors.New(fmt.Sprint(message))
		}
	}
	return false, errors.New("usage entries must be booleans, null, 'error', or error objects")
}

type channelKeysOracleSaves struct {
	values []any
	calls  []any
}

func (saves *channelKeysOracleSaves) nextErrors() bool {
	if len(saves.values) == 0 {
		return false
	}
	if saves.values[0] == nil {
		return false
	}
	if value, ok := saves.values[0].(bool); ok && !value {
		return false
	}
	return true
}

func (saves *channelKeysOracleSaves) record(certificates *Object) {
	saves.calls = append(saves.calls, channelKeysOracleCertificatePairs(certificates))
	if len(saves.values) > 0 {
		saves.values = saves.values[1:]
	}
}

type channelKeysOracleRunner struct {
	t       *testing.T
	account *Account
	usage   *channelKeysOracleUsage
	saves   *channelKeysOracleSaves
	network keys.Network
}

func newChannelKeysOracleRunner(t *testing.T, fixture map[string]any) (*channelKeysOracleRunner, error) {
	network, err := channelKeysOracleNetwork(fixture)
	if err != nil {
		return nil, err
	}
	root, err := channelKeysOracleRootKey(network, fixture)
	if err != nil {
		return nil, err
	}
	certificates, err := channelKeysOracleCertificates(fixture["certificates"])
	if err != nil {
		return nil, err
	}
	data := NewObject(
		Member{Key: "modified_on", Value: 1},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
		Member{Key: "certificates", Value: certificates},
	)
	if root == nil {
		readOnly, keyErr := keys.NewPrivateKey(
			network, bytes.Repeat([]byte{3}, 32), make([]byte, 32), 0, 0, nil,
		)
		if keyErr != nil {
			return nil, keyErr
		}
		data.Set("public_key", readOnly.PublicKey().ExtendedKeyString())
	} else {
		data.Set("private_key", root.ExtendedKeyString())
	}
	account, err := NewAccount(network, data)
	if err != nil {
		return nil, err
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	account.wallet = wallet
	usage := &channelKeysOracleUsage{
		values: channelKeysOracleAnySlice(fixture["usage"]), calls: make([]any, 0),
	}
	saves := &channelKeysOracleSaves{
		values: channelKeysOracleAnySlice(fixture["save_errors"]), calls: make([]any, 0),
	}
	return &channelKeysOracleRunner{
		t: t, account: account, usage: usage, saves: saves, network: network,
	}, nil
}

func (runner *channelKeysOracleRunner) execute(action map[string]any) (any, error) {
	switch action["action"] {
	case "add", "add_channel_private_key":
		privateKey, err := channelKeysOraclePrivateKey(
			runner.network, action["private_key_hex"].(string), make([]byte, 32),
		)
		if err != nil {
			return nil, err
		}
		return nil, runner.account.AddChannelPrivateKey(privateKey)
	case "get", "get_channel_private_key":
		publicKey, err := hex.DecodeString(action["public_key_hex"].(string))
		if err != nil {
			return nil, err
		}
		privateKey, err := runner.account.GetChannelPrivateKey(publicKey, nil)
		if err != nil {
			return nil, err
		}
		return channelKeysOracleKeyView(privateKey, true)
	case "migrate", "maybe_migrate_certificates":
		if runner.saves.nextErrors() {
			runner.account.wallet.Storage = NewWalletStorage(
				filepath.Join(runner.t.TempDir(), "missing", "wallet"),
			)
		} else {
			runner.account.wallet.Storage = NewMemoryWalletStorage()
		}
		changed, err := runner.account.MigrateChannelKeys()
		if changed {
			runner.saves.record(runner.account.ChannelKeys)
		}
		return nil, err
	case "generate", "generate_next_key", "generate_channel_private_key":
		privateKey, err := runner.account.DeterministicChannelKeys.GenerateNextKey(runner.usage.lookup)
		if err != nil {
			return nil, err
		}
		return channelKeysOracleKeyView(privateKey, true)
	case "prime", "ensure_cache_primed":
		return nil, runner.account.DeterministicChannelKeys.EnsureCachePrimed(runner.usage.lookup)
	case "maybe", "maybe_generate_deterministic_key_for_channel":
		publicKey, err := hex.DecodeString(action["public_key_hex"].(string))
		if err != nil {
			return nil, err
		}
		_, err = runner.account.DeterministicChannelKeys.MaybeGenerateForChannel(publicKey)
		return nil, err
	case "get_cached", "get_private_key_from_pubkey_hash":
		return channelKeysOracleKeyView(
			runner.account.DeterministicChannelKeys.GetPrivateKey(action["address"].(string)), true,
		)
	case "set_usage":
		runner.usage.values = channelKeysOracleAnySlice(action["usage"])
		return nil, nil
	case "set_certificates":
		certificates, err := channelKeysOracleCertificates(action["certificates"])
		if err != nil {
			return nil, err
		}
		runner.account.ChannelKeys = certificates
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown channel-key action %v", action["action"])
	}
}

func channelKeysOracleAssertStateful(t *testing.T, fixtures []map[string]any, rawOracle any) {
	t.Helper()
	oracle, ok := rawOracle.([]any)
	if !ok || len(oracle) != len(fixtures) {
		t.Fatalf("channel-key stateful oracle shape = %T/%d, want %d", rawOracle, len(oracle), len(fixtures))
	}
	for caseIndex, fixture := range fixtures {
		name := fixture["name"].(string)
		t.Run(name, func(t *testing.T) {
			wantCase := oracle[caseIndex].(map[string]any)
			runner, err := newChannelKeysOracleRunner(t, fixture)
			channelKeysOracleAssertErrorParity(t, err, wantCase["error_type"])
			if err != nil {
				return
			}
			channelKeysOracleAssertJSON(t, runner.state(), wantCase["initial"])
			actions := fixture["actions"].([]any)
			wantActions := wantCase["actions"].([]any)
			if len(actions) != len(wantActions) {
				t.Fatalf("action count = %d, want %d", len(actions), len(wantActions))
			}
			for actionIndex, rawAction := range actions {
				action := rawAction.(map[string]any)
				want := wantActions[actionIndex].(map[string]any)
				got, actionErr := runner.execute(action)
				channelKeysOracleAssertErrorParity(t, actionErr, want["error_type"])
				if actionErr == nil {
					channelKeysOracleAssertJSON(t, got, want["result"])
				}
				channelKeysOracleAssertJSON(t, runner.state(), channelKeysOracleState(want))
			}
		})
	}
}

func (runner *channelKeysOracleRunner) state() map[string]any {
	manager := runner.account.DeterministicChannelKeys
	cacheKeys := make([]*keys.PrivateKey, 0, len(manager.Cache))
	for _, privateKey := range manager.Cache {
		cacheKeys = append(cacheKeys, privateKey)
	}
	sort.Slice(cacheKeys, func(left, right int) bool {
		if cacheKeys[left].ChildNumber() == cacheKeys[right].ChildNumber() {
			return cacheKeys[left].Address() < cacheKeys[right].Address()
		}
		return cacheKeys[left].ChildNumber() < cacheKeys[right].ChildNumber()
	})
	cache := make([]any, 0, len(cacheKeys))
	for _, privateKey := range cacheKeys {
		view, err := channelKeysOracleKeyView(privateKey, false)
		if err != nil {
			runner.t.Fatal(err)
		}
		cache = append(cache, []any{privateKey.Address(), view})
	}
	privateView, err := channelKeysOracleKeyView(manager.privateKey, false)
	if err != nil {
		runner.t.Fatal(err)
	}
	return map[string]any{
		"certificates":               channelKeysOracleCertificatePairs(runner.account.ChannelKeys),
		"cache":                      cache,
		"last_known":                 manager.LastKnown,
		"manager_private_key_loaded": manager.privateKey != nil,
		"manager_private_key":        privateView,
		"usage_calls":                runner.usage.calls,
		"usage_remaining":            runner.usage.values,
		"save_calls":                 runner.saves.calls,
		"save_remaining":             runner.saves.values,
	}
}

func channelKeysOracleState(value map[string]any) map[string]any {
	state := make(map[string]any, len(channelKeysOracleStateFields))
	for _, key := range channelKeysOracleStateFields {
		state[key] = value[key]
	}
	return state
}

func channelKeysOracleRootKey(network keys.Network, fixture map[string]any) (*keys.PrivateKey, error) {
	if value, exists := fixture["seed_hex"]; exists {
		seed, err := hex.DecodeString(value.(string))
		if err != nil {
			return nil, err
		}
		return keys.PrivateKeyFromSeed(network, seed)
	}
	privateValue, hasPrivate := fixture["root_private_key_hex"]
	chainValue, hasChain := fixture["root_chain_code_hex"]
	if !hasPrivate && !hasChain {
		return nil, nil
	}
	if !hasPrivate || !hasChain {
		return nil, errors.New("root private key and chain code must be supplied together")
	}
	chainCode, err := hex.DecodeString(chainValue.(string))
	if err != nil {
		return nil, err
	}
	return channelKeysOraclePrivateKey(network, privateValue.(string), chainCode)
}

func channelKeysOraclePrivateKey(network keys.Network, privateHex string, chainCode []byte) (*keys.PrivateKey, error) {
	privateKey, err := hex.DecodeString(privateHex)
	if err != nil {
		return nil, err
	}
	return keys.NewPrivateKey(network, privateKey, chainCode, 0, 0, nil)
}

func channelKeysOracleKeyView(privateKey *keys.PrivateKey, includePEM bool) (any, error) {
	if privateKey == nil {
		return nil, nil
	}
	chainCode := privateKey.ChainCode()
	result := map[string]any{
		"address":         privateKey.Address(),
		"private_key_hex": hex.EncodeToString(privateKey.PrivateKeyBytes()),
		"public_key_hex":  hex.EncodeToString(privateKey.PublicKey().CompressedBytes()),
		"chain_code_hex":  hex.EncodeToString(chainCode[:]),
		"n":               privateKey.ChildNumber(),
		"depth":           privateKey.Depth(),
	}
	if includePEM {
		encoded, err := privateKey.ToPEM()
		if err != nil {
			return nil, err
		}
		result["pem"] = encoded
		result["der_hex"] = hex.EncodeToString(channelKeysOraclePEMBodyValue(encoded))
	}
	return result, nil
}

func channelKeysOracleCertificates(value any) (*Object, error) {
	result := NewObject()
	if value == nil {
		return result, nil
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("certificates have type %T, want ordered pairs", value)
	}
	for _, rawEntry := range entries {
		entry, ok := rawEntry.([]any)
		if !ok || len(entry) != 2 {
			return nil, errors.New("certificate entries must be [key, value] pairs")
		}
		key, ok := entry[0].(string)
		if !ok {
			return nil, fmt.Errorf("certificate key has type %T", entry[0])
		}
		result.Set(key, entry[1])
	}
	return result, nil
}

func channelKeysOracleCertificatePairs(certificates *Object) []any {
	result := make([]any, 0, certificates.Len())
	for _, member := range certificates.Members() {
		result = append(result, []any{member.Key, member.Value})
	}
	return result
}

func channelKeysOracleAnySlice(value any) []any {
	if value == nil {
		return make([]any, 0)
	}
	values := value.([]any)
	return append(make([]any, 0, len(values)), values...)
}

func channelKeysOracleNetwork(fixture map[string]any) (keys.Network, error) {
	prefix, _ := fixture["address_prefix_hex"].(string)
	switch prefix {
	case "", "55":
		return keys.MainNet, nil
	case "6f":
		return keys.RegTest, nil
	default:
		return 0, fmt.Errorf("unsupported channel-key oracle address prefix %q", prefix)
	}
}

func channelKeysOracleAssertReference(t *testing.T, oracle map[string]any) {
	t.Helper()
	reference := oracle["reference"].(map[string]any)
	if reference["commit"] != channelKeysOraclePinnedCommit {
		t.Fatalf("channel-key oracle commit = %v", reference["commit"])
	}
	if reference["version"] != channelKeysOraclePinnedVersion {
		t.Fatalf("channel-key oracle version = %v", reference["version"])
	}
	coincurve := reference["coincurve"].(map[string]any)
	if coincurve["version"] != "15.0.0" || coincurve["requirement"] != "coincurve==15.0.0" {
		t.Fatalf("channel-key Coincurve reference = %v", coincurve)
	}
	asn1crypto := reference["asn1crypto"].(map[string]any)
	if asn1crypto["version"] != "1.5.1" {
		t.Fatalf("channel-key asn1crypto reference = %v", asn1crypto)
	}
	metadata := oracle["metadata"].(map[string]any)
	if metadata["python_assertions"] != true || metadata["compact_signature_bytes"] != json.Number("64") ||
		metadata["digest_bytes"] != json.Number("32") || metadata["deterministic_channel_path"] != "m/2/index" {
		t.Fatalf("channel-key oracle metadata = %v", metadata)
	}
	selfChecks, ok := metadata["adapter_self_checks"].(map[string]any)
	if !ok || len(selfChecks) < 6 {
		t.Fatalf("channel-key oracle self-checks = %v", metadata["adapter_self_checks"])
	}
	for name, passed := range selfChecks {
		if passed != true {
			t.Fatalf("channel-key oracle self-check %s = %v", name, passed)
		}
	}
}

func channelKeysOracleAssertErrorParity(t *testing.T, err error, rawErrorType any) {
	t.Helper()
	wantError := rawErrorType != nil
	if (err != nil) != wantError {
		t.Fatalf("error presence differs: Go=%v Python=%v", err, rawErrorType)
	}
}

func channelKeysOracleAssertJSON(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("channel-key value differs\nGo:     %s\nPython: %s", gotJSON, wantJSON)
	}
}

func runChannelKeysOracle(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	sdkRoot, script := channelKeysOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python channel-key oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python channel-key oracle: %v\n%s", err, output)
	}
	return result
}

func channelKeysOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate channel-key oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "channel_keys_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "setup.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "bip32.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "account.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK channel-key source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func channelKeysOracleMustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func channelKeysOraclePEMContents(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	return strings.Join(lines[1:len(lines)-1], "\n") + "\n"
}

func channelKeysOraclePEMBody(t *testing.T, value string) []byte {
	t.Helper()
	decoded := channelKeysOraclePEMBodyValue(value)
	if decoded == nil {
		t.Fatal("decode channel-key PEM body")
	}
	return decoded
}

func channelKeysOraclePEMBodyValue(value string) []byte {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) < 2 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil {
		return nil
	}
	return decoded
}

func channelKeysOracleWrapPEM(label string, der []byte) string {
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

func channelKeysOracleAppendSequenceField(t *testing.T, der, field []byte) []byte {
	t.Helper()
	var sequence asn1.RawValue
	rest, err := asn1.Unmarshal(der, &sequence)
	if err != nil || len(rest) != 0 || sequence.Tag != asn1.TagSequence {
		t.Fatalf("parse channel-key sequence = rest %x, %v", rest, err)
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

func channelKeysOracleIndefiniteSequence(t *testing.T, der []byte) []byte {
	t.Helper()
	var sequence asn1.RawValue
	rest, err := asn1.Unmarshal(der, &sequence)
	if err != nil || len(rest) != 0 || sequence.Tag != asn1.TagSequence {
		t.Fatalf("parse channel-key sequence = rest %x, %v", rest, err)
	}
	result := append([]byte{0x30, 0x80}, sequence.Bytes...)
	return append(result, 0, 0)
}
