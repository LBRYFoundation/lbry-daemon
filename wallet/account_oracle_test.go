package wallet

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
)

const (
	accountOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	accountOraclePinnedVersion = "0.113.0"
	accountOracleAlternateXPrv = "xprv9s21ZrQH143K3TsAz5efNV8K93g3Ms3FXcjaWB9fVUsMwAoE3ZT4vYymkp5BxKKfnpz8J6sHDFriX1SnpvjNkzcks8XBnxjGLS83BTyfpna"
)

type accountOracleCase struct {
	Name            string                     `json:"name"`
	Record          json.RawMessage            `json:"record"`
	Now             *float64                   `json:"now,omitempty"`
	Ledger          map[string]string          `json:"ledger,omitempty"`
	InitVectors     map[string]string          `json:"init_vectors,omitempty"`
	URandom         []string                   `json:"urandom,omitempty"`
	EncryptPassword *string                    `json:"encrypt_password,omitempty"`
	CertificateSets []accountCertificateSet    `json:"certificate_sets,omitempty"`
	Merges          []json.RawMessage          `json:"merges,omitempty"`
	Actions         []accountOracleCryptAction `json:"actions,omitempty"`
}

type accountCertificateSet struct {
	Name         string          `json:"name"`
	Certificates json.RawMessage `json:"certificates"`
}

type accountOracleCryptAction struct {
	Action             string `json:"action"`
	Password           string `json:"password,omitempty"`
	IncludeChannelKeys *bool  `json:"include_channel_keys,omitempty"`
}

type accountOracleDictView struct {
	JSON      *string `json:"json"`
	ErrorType *string `json:"error_type"`
}

type accountOracleHashView struct {
	InputJSON *string `json:"input_json"`
	Hash      *string `json:"hash"`
	ErrorType *string `json:"error_type"`
}

type accountOracleFullView struct {
	StateJSON                string                 `json:"state_json"`
	ToDict                   accountOracleDictView  `json:"to_dict"`
	ToDictWithoutChannelKeys accountOracleDictView  `json:"to_dict_without_channel_keys"`
	ToDictWithPassword       *accountOracleDictView `json:"to_dict_with_password"`
	Hash                     accountOracleHashView  `json:"hash"`
}

type accountOracleLoadResult struct {
	Name         string  `json:"name"`
	ErrorType    *string `json:"error_type"`
	URandomCalls []int   `json:"urandom_calls"`
	accountOracleFullView
}

type accountOracleHashVariant struct {
	Name      string                `json:"name"`
	ErrorType *string               `json:"error_type"`
	Hash      accountOracleHashView `json:"hash"`
}

type accountOracleHashResult struct {
	Name     string                     `json:"name"`
	Variants []accountOracleHashVariant `json:"variants"`
}

type accountOracleMergeStep struct {
	ErrorType *string `json:"error_type"`
	accountOracleFullView
}

type accountOracleMergeResult struct {
	Name      string                   `json:"name"`
	ErrorType *string                  `json:"error_type"`
	Initial   *accountOracleFullView   `json:"initial"`
	Merges    []accountOracleMergeStep `json:"merges"`
}

type accountOracleCryptStep struct {
	Action       string                 `json:"action"`
	Result       json.RawMessage        `json:"result"`
	ErrorType    *string                `json:"error_type"`
	ActionToDict *accountOracleDictView `json:"action_to_dict"`
	URandomCalls []int                  `json:"urandom_calls"`
	accountOracleFullView
}

type accountOracleCryptResult struct {
	Name      string                   `json:"name"`
	ErrorType *string                  `json:"error_type"`
	Initial   *accountOracleFullView   `json:"initial"`
	Actions   []accountOracleCryptStep `json:"actions"`
}

type accountOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion  string `json:"python_version"`
		UnicodeVersion string `json:"unicode_version"`
		PythonDebug    bool   `json:"python_debug"`
	} `json:"metadata"`
	LoadCases  []accountOracleLoadResult  `json:"load_cases"`
	HashCases  []accountOracleHashResult  `json:"hash_cases"`
	MergeCases []accountOracleMergeResult `json:"merge_cases"`
	CryptCases []accountOracleCryptResult `json:"crypt_cases"`
}

func TestAccountMatchesPinnedPythonOracle(t *testing.T) {
	password := "password"
	fixtureTime := 1_700_000_000.75
	lazyIVs := []string{
		"000102030405060708090a0b0c0d0e0f",
		"101112131415161718191a1b1c1d1e1f",
	}
	loadCases := []accountOracleCase{
		{
			Name: "seed wins and preserves original private string",
			Record: rawAccountJSON(`{
				"name":"","seed":"carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent",
				"private_key":"` + accountOracleAlternateXPrv + `","public_key":"` + mismatchedAccountXPub + `",
				"modified_on":123456789012345678901234567890,
				"address_generator":{"name":"deterministic-chain","receiving":{"gap":23,"maximum_uses_per_address":2},"change":{"gap":7,"maximum_uses_per_address":3}},
				"certificates":{"zeta":"first","alpha":"second","\u03b2":"snow \u2603"}
			}`),
			URandom: lazyIVs, EncryptPassword: &password,
		},
		{
			Name:   "private key wins over supplied public key",
			Record: rawAccountJSON(`{"name":"Private","private_key":"` + accountOracleAlternateXPrv + `","public_key":"invalid and ignored","modified_on":41,"address_generator":{"name":"single-address"}}`),
		},
		{
			Name:   "read only single address",
			Record: rawAccountJSON(`{"name":"Read only","public_key":"` + fixedAccountXPub + `","modified_on":42,"address_generator":{"name":"single-address"},"certificates":{"one":"value"}}`),
		},
		{
			Name:   "encrypted secrets are opaque",
			Record: rawAccountJSON(`{"name":"Locked","seed":"` + encryptedAccountSeed + `","private_key":"` + encryptedAccountXPrv + `","encrypted":true,"public_key":"` + mismatchedAccountXPub + `","modified_on":43}`),
		},
		{
			Name:   "testnet prefixes and default time",
			Record: rawAccountJSON(`{"seed":"carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent"}`),
			Now:    &fixtureTime, Ledger: map[string]string{"network": "testnet"},
		},
		{
			Name:   "partial deterministic record fails",
			Record: rawAccountJSON(`{"public_key":"` + fixedAccountXPub + `","address_generator":{"name":"deterministic-chain","receiving":{"gap":20}}}`),
		},
		{
			Name:   "unknown generator fails",
			Record: rawAccountJSON(`{"public_key":"` + fixedAccountXPub + `","address_generator":{"name":"future-generator"}}`),
		},
	}

	hashRecord := rawAccountJSON(`{"name":"Hash","seed":"carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent","modified_on":123,"address_generator":{"name":"single-address"}}`)
	hashCases := []accountOracleCase{{
		Name:   "certificate keys only",
		Record: hashRecord,
		CertificateSets: []accountCertificateSet{
			{Name: "zeta alpha", Certificates: rawAccountJSON(`{"zeta":"one","alpha":"two"}`)},
			{Name: "reordered changed values", Certificates: rawAccountJSON(`{"alpha":null,"zeta":{"changed":true}}`)},
			{Name: "new key", Certificates: rawAccountJSON(`{"alpha":null,"zeta":"other","beta":"new"}`)},
		},
	}}

	mergeCases := []accountOracleCase{{
		Name:   "timestamp comparison and partial mutation",
		Record: rawAccountJSON(`{"name":"Original","seed":"carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent","modified_on":10,"address_generator":{"name":"deterministic-chain","receiving":{"gap":20,"maximum_uses_per_address":1},"change":{"gap":6,"maximum_uses_per_address":1}},"certificates":{"zeta":"old","alpha":"stable"}}`),
		Now:    &fixtureTime,
		Merges: []json.RawMessage{
			rawAccountJSON(`{"name":"Ignored","modified_on":9,"certificates":{"alpha":"updated","new":"old record"}}`),
			rawAccountJSON(`{"name":"Fractional","modified_on":10.75,"address_generator":{"name":"deterministic-chain","receiving":{"maximum_uses_per_address":4},"change":{"gap":9}},"certificates":{"zeta":"new value"}}`),
			rawAccountJSON(`{"name":"Fractional again","modified_on":10.25,"address_generator":{"name":"deterministic-chain","receiving":{"gap":30},"change":{"maximum_uses_per_address":5}}}`),
			rawAccountJSON(`{"name":"Partially applied","modified_on":12,"address_generator":{"name":"single-address"},"certificates":{"must_not_apply":true}}`),
			rawAccountJSON(`{"modified_on":1,"certificates":{"after_error":"applied"}}`),
		},
	}}

	includeCertificates := true
	mixedPrivateKey, err := EncryptAccountSecret(
		"different-password", accountEncryptionXPrv, []byte("0000000000000000"),
	)
	if err != nil {
		t.Fatal(err)
	}
	seedWithTrailingPartialBlock := appendAccountCiphertextByte(t, encryptedAccountSeed, 0x42)
	cryptCases := []accountOracleCase{
		{
			Name:    "transient and persistent encryption",
			Record:  rawAccountJSON(`{"name":"Crypt","seed":"carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent","private_key":"` + accountOracleAlternateXPrv + `","public_key":"` + mismatchedAccountXPub + `","modified_on":123,"address_generator":{"name":"single-address"}}`),
			URandom: lazyIVs,
			Actions: []accountOracleCryptAction{
				{Action: "to_dict", Password: password, IncludeChannelKeys: &includeCertificates},
				{Action: "to_dict", Password: password, IncludeChannelKeys: &includeCertificates},
				{Action: "encrypt", Password: password},
				{Action: "decrypt", Password: "wrong"},
				{Action: "decrypt", Password: password},
				{Action: "encrypt", Password: password},
				{Action: "decrypt", Password: password},
			},
		},
		{
			Name:   "read only encryption accepts any password",
			Record: rawAccountJSON(`{"name":"Read only","public_key":"` + fixedAccountXPub + `","modified_on":2,"address_generator":{"name":"single-address"}}`),
			Actions: []accountOracleCryptAction{
				{Action: "encrypt", Password: "first"},
				{Action: "encrypt", Password: "again"},
				{Action: "decrypt", Password: "unrelated"},
				{Action: "decrypt", Password: "again"},
			},
		},
		{
			Name:   "decrypt recovers and reuses stored IVs",
			Record: rawAccountJSON(`{"name":"Recover","seed":"` + encryptedAccountSeed + `","private_key":"` + encryptedAccountXPrv + `","encrypted":true,"public_key":"` + mismatchedAccountXPub + `","modified_on":3,"address_generator":{"name":"single-address"}}`),
			Actions: []accountOracleCryptAction{
				{Action: "decrypt", Password: password},
				{Action: "to_dict", Password: password, IncludeChannelKeys: &includeCertificates},
				{Action: "encrypt", Password: password},
			},
		},
		{
			Name:   "private decrypt failure retains only recovered seed IV",
			Record: rawAccountJSON(`{"name":"Mixed","seed":"` + encryptedAccountSeed + `","private_key":"` + mixedPrivateKey + `","encrypted":true,"public_key":"` + fixedAccountXPub + `","modified_on":4,"address_generator":{"name":"single-address"}}`),
			Actions: []accountOracleCryptAction{
				{Action: "decrypt", Password: password},
			},
		},
		{
			Name:   "decrypt ignores trailing partial ciphertext block",
			Record: rawAccountJSON(`{"name":"Trailing","seed":"` + seedWithTrailingPartialBlock + `","private_key":"","encrypted":true,"public_key":"` + fixedAccountXPub + `","modified_on":5,"address_generator":{"name":"single-address"}}`),
			Actions: []accountOracleCryptAction{
				{Action: "decrypt", Password: password},
			},
		},
	}

	oracle := runAccountOracle(t, map[string]any{
		"load_cases": loadCases, "hash_cases": hashCases,
		"merge_cases": mergeCases, "crypt_cases": cryptCases,
	})
	if oracle.Reference.Commit != accountOraclePinnedCommit || oracle.Reference.Version != accountOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	if !oracle.Metadata.PythonDebug {
		t.Fatal("account oracle ran with Python assertions disabled")
	}
	for environment, got := range map[string]string{
		"LBRY_ORACLE_PYTHON_VERSION":  oracle.Metadata.PythonVersion,
		"LBRY_ORACLE_UNICODE_VERSION": oracle.Metadata.UnicodeVersion,
	} {
		if want := os.Getenv(environment); want != "" && got != want {
			t.Fatalf("oracle %s = %q, want %q", environment, got, want)
		}
	}
	assertAccountLoadCases(t, loadCases, oracle.LoadCases)
	assertAccountHashCases(t, hashCases, oracle.HashCases)
	assertAccountMergeCases(t, mergeCases, oracle.MergeCases)
	assertAccountCryptCases(t, cryptCases, oracle.CryptCases)
}

func assertAccountLoadCases(t *testing.T, fixtures []accountOracleCase, oracle []accountOracleLoadResult) {
	t.Helper()
	if len(fixtures) != len(oracle) {
		t.Fatalf("load result count = %d, want %d", len(oracle), len(fixtures))
	}
	for index, fixture := range fixtures {
		t.Run("load/"+fixture.Name, func(t *testing.T) {
			account, err := newOracleAccount(t, fixture, fixture.Record)
			assertAccountOracleErrorParity(t, err, oracle[index].ErrorType)
			if err != nil {
				return
			}
			for key, value := range fixture.InitVectors {
				decoded, decodeErr := hex.DecodeString(value)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if err := account.SetInitializationVector(key, decoded); err != nil {
					t.Fatal(err)
				}
			}
			assertAccountFullView(t, account, oracle[index].accountOracleFullView)
			if oracle[index].ToDictWithPassword != nil {
				object, err := account.ToDict(valueOrEmpty(fixture.EncryptPassword), true)
				assertAccountDictView(t, object, err, *oracle[index].ToDictWithPassword)
			}
			assertAccountEntropyCalls(t, account, oracle[index].URandomCalls)
		})
	}
}

func assertAccountHashCases(t *testing.T, fixtures []accountOracleCase, oracle []accountOracleHashResult) {
	t.Helper()
	for caseIndex, fixture := range fixtures {
		if caseIndex >= len(oracle) || len(fixture.CertificateSets) != len(oracle[caseIndex].Variants) {
			t.Fatalf("hash oracle shape mismatch for %q", fixture.Name)
		}
		var firstDigest string
		for variantIndex, certificates := range fixture.CertificateSets {
			t.Run("hash/"+certificates.Name, func(t *testing.T) {
				record := mustAccountObjectFromJSON(t, fixture.Record).ShallowCopy()
				record.Set("certificates", mustAccountObjectFromJSON(t, certificates.Certificates))
				account, err := NewAccount(accountOracleNetwork(fixture), record, WithAccountClock(accountOracleClock(fixture)))
				if err != nil {
					t.Fatal(err)
				}
				digest, err := account.Hash()
				want := oracle[caseIndex].Variants[variantIndex].Hash
				assertAccountHashView(t, account, digest, err, want)
				got := hex.EncodeToString(digest[:])
				if variantIndex == 0 {
					firstDigest = got
				} else if variantIndex == 1 && got != firstDigest {
					t.Fatalf("certificate values/order changed hash: %s != %s", got, firstDigest)
				} else if variantIndex == 2 && got == firstDigest {
					t.Fatalf("new certificate key did not change hash: %s", got)
				}
			})
		}
	}
}

func assertAccountMergeCases(t *testing.T, fixtures []accountOracleCase, oracle []accountOracleMergeResult) {
	t.Helper()
	for caseIndex, fixture := range fixtures {
		t.Run("merge/"+fixture.Name, func(t *testing.T) {
			account, err := newOracleAccount(t, fixture, fixture.Record)
			if err != nil || oracle[caseIndex].Initial == nil {
				t.Fatalf("initial account = %v, oracle=%#v", err, oracle[caseIndex].ErrorType)
			}
			assertAccountFullView(t, account, *oracle[caseIndex].Initial)
			for index, rawMerge := range fixture.Merges {
				err := account.Merge(mustAccountObjectFromJSON(t, rawMerge))
				assertAccountOracleErrorParity(t, err, oracle[caseIndex].Merges[index].ErrorType)
				assertAccountFullView(t, account, oracle[caseIndex].Merges[index].accountOracleFullView)
			}
		})
	}
}

func assertAccountCryptCases(t *testing.T, fixtures []accountOracleCase, oracle []accountOracleCryptResult) {
	t.Helper()
	for caseIndex, fixture := range fixtures {
		t.Run("crypt/"+fixture.Name, func(t *testing.T) {
			account, err := newOracleAccount(t, fixture, fixture.Record)
			if err != nil || oracle[caseIndex].Initial == nil {
				t.Fatalf("initial account = %v, oracle=%#v", err, oracle[caseIndex].ErrorType)
			}
			for key, value := range fixture.InitVectors {
				decoded, err := hex.DecodeString(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := account.SetInitializationVector(key, decoded); err != nil {
					t.Fatal(err)
				}
			}
			assertAccountFullView(t, account, *oracle[caseIndex].Initial)
			for index, action := range fixture.Actions {
				step := oracle[caseIndex].Actions[index]
				switch action.Action {
				case "to_dict":
					include := action.IncludeChannelKeys == nil || *action.IncludeChannelKeys
					object, actionErr := account.ToDict(action.Password, include)
					assertAccountOracleErrorParity(t, actionErr, step.ErrorType)
					if actionErr == nil {
						assertAccountOracleJSONValue(t, object, step.Result)
					}
					if step.ActionToDict == nil {
						t.Fatal("oracle omitted action_to_dict view")
					}
					assertAccountDictView(t, object, actionErr, *step.ActionToDict)
				case "encrypt":
					actionErr := account.Encrypt(action.Password)
					assertAccountOracleErrorParity(t, actionErr, step.ErrorType)
					assertAccountOracleBool(t, step.Result, actionErr == nil)
				case "decrypt":
					result, actionErr := account.Decrypt(action.Password)
					assertAccountOracleErrorParity(t, actionErr, step.ErrorType)
					assertAccountOracleBool(t, step.Result, result)
				default:
					t.Fatalf("unknown action %q", action.Action)
				}
				assertAccountFullView(t, account, step.accountOracleFullView)
				assertAccountEntropyCalls(t, account, step.URandomCalls)
			}
		})
	}
}

func assertAccountFullView(t *testing.T, account *Account, want accountOracleFullView) {
	t.Helper()
	stateJSON, err := accountOracleStateJSON(account)
	if err != nil {
		t.Fatal(err)
	}
	if stateJSON != want.StateJSON {
		t.Fatalf("state JSON differs\nGo:     %s\nPython: %s", stateJSON, want.StateJSON)
	}
	object, err := account.ToDict("", true)
	assertAccountDictView(t, object, err, want.ToDict)
	without, err := account.ToDict("", false)
	assertAccountDictView(t, without, err, want.ToDictWithoutChannelKeys)
	digest, hashErr := account.Hash()
	assertAccountHashView(t, account, digest, hashErr, want.Hash)
}

func assertAccountDictView(t *testing.T, object *Object, err error, want accountOracleDictView) {
	t.Helper()
	assertAccountOracleErrorParity(t, err, want.ErrorType)
	if err != nil {
		return
	}
	encoded, err := encodePreferenceJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if want.JSON == nil || string(encoded) != *want.JSON {
		t.Fatalf("dictionary JSON differs\nGo:     %s\nPython: %v", encoded, want.JSON)
	}
}

func assertAccountHashView(t *testing.T, account *Account, digest [32]byte, err error, want accountOracleHashView) {
	t.Helper()
	assertAccountOracleErrorParity(t, err, want.ErrorType)
	without, dictErr := account.ToDict("", false)
	if dictErr != nil {
		t.Fatal(dictErr)
	}
	encoded, encodeErr := encodePreferenceJSON(without)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if want.InputJSON == nil || string(encoded) != *want.InputJSON {
		t.Fatalf("hash input differs\nGo:     %s\nPython: %v", encoded, want.InputJSON)
	}
	if err == nil && (want.Hash == nil || hex.EncodeToString(digest[:]) != *want.Hash) {
		t.Fatalf("hash differs\nGo:     %x\nPython: %v", digest, want.Hash)
	}
}

func accountOracleStateJSON(account *Account) (string, error) {
	privateKey, privateKeyType := any(nil), any(nil)
	if account.PrivateKey != nil {
		privateKey = account.PrivateKey.ExtendedKeyString()
		privateKeyType = "PrivateKey"
	}
	generator, err := account.addressGeneratorObject()
	if err != nil {
		return "", err
	}
	state := NewObject(
		Member{Key: "id", Value: account.ID},
		Member{Key: "name", Value: account.Name},
		Member{Key: "seed", Value: account.Seed},
		Member{Key: "modified_on", Value: account.ModifiedOn},
		Member{Key: "private_key_string", Value: account.PrivateKeyString},
		Member{Key: "encrypted", Value: account.Encrypted},
		Member{Key: "private_key", Value: privateKey},
		Member{Key: "private_key_type", Value: privateKeyType},
		Member{Key: "public_key", Value: account.PublicKey.ExtendedKeyString()},
		Member{Key: "public_key_type", Value: "PublicKey"},
		Member{Key: "address_generator", Value: generator},
		Member{Key: "certificates", Value: account.ChannelKeys},
		Member{Key: "init_vectors", Value: accountOracleInitializationVectors(account)},
	)
	encoded, err := encodePreferenceJSON(state)
	return string(encoded), err
}

func newOracleAccount(t *testing.T, fixture accountOracleCase, record json.RawMessage) (*Account, error) {
	t.Helper()
	options := []AccountOption{WithAccountClock(accountOracleClock(fixture))}
	if len(fixture.URandom) > 0 {
		options = append(options, WithAccountEntropy(newAccountOracleEntropyReader(t, fixture.URandom)))
	}
	return NewAccount(accountOracleNetwork(fixture), mustAccountObjectFromJSON(t, record), options...)
}

func accountOracleInitializationVectors(account *Account) *Object {
	keys := make([]string, 0, len(account.initializationVectors))
	for key := range account.initializationVectors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := NewObject()
	for _, key := range keys {
		result.Set(key, hex.EncodeToString(account.initializationVectors[key]))
	}
	return result
}

type accountOracleEntropyReader struct {
	values [][]byte
	calls  []int
}

func newAccountOracleEntropyReader(t *testing.T, values []string) *accountOracleEntropyReader {
	t.Helper()
	reader := &accountOracleEntropyReader{}
	for _, value := range values {
		decoded, err := hex.DecodeString(value)
		if err != nil {
			t.Fatalf("decode account entropy fixture: %v", err)
		}
		reader.values = append(reader.values, decoded)
	}
	return reader
}

func (reader *accountOracleEntropyReader) Read(destination []byte) (int, error) {
	reader.calls = append(reader.calls, len(destination))
	if len(reader.values) == 0 {
		return 0, io.EOF
	}
	value := reader.values[0]
	reader.values = reader.values[1:]
	if len(value) != len(destination) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(destination, value), nil
}

func assertAccountEntropyCalls(t *testing.T, account *Account, want []int) {
	t.Helper()
	var got []int
	if reader, ok := account.entropy.(*accountOracleEntropyReader); ok {
		got = reader.calls
	}
	if len(got) != len(want) {
		t.Fatalf("entropy calls = %v, want %v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("entropy calls = %v, want %v", got, want)
		}
	}
}

func accountOracleNetwork(fixture accountOracleCase) keys.Network {
	switch fixture.Ledger["network"] {
	case "testnet":
		return keys.TestNet
	case "regtest":
		return keys.RegTest
	default:
		return keys.MainNet
	}
}

func accountOracleClock(fixture accountOracleCase) func() time.Time {
	value := 1_700_000_000.75
	if fixture.Now != nil {
		value = *fixture.Now
	}
	seconds, fraction := math.Modf(value)
	return func() time.Time { return time.Unix(int64(seconds), int64(fraction*1_000_000_000)) }
}

func mustAccountObjectFromJSON(t *testing.T, raw json.RawMessage) *Object {
	t.Helper()
	value, err := decodeOrderedJSON(raw)
	if err != nil {
		t.Fatalf("decode account fixture: %v\n%s", err, raw)
	}
	object, ok := value.(*Object)
	if !ok {
		t.Fatalf("account fixture has type %T, want object", value)
	}
	return object
}

func rawAccountJSON(value string) json.RawMessage { return json.RawMessage(value) }

func appendAccountCiphertextByte(t *testing.T, value string, trailing byte) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(append(decoded, trailing))
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertAccountOracleErrorParity(t *testing.T, err error, oracleErrorType *string) {
	t.Helper()
	if (err != nil) != (oracleErrorType != nil) {
		t.Fatalf("error parity differs: Go=%v, Python=%v", err, oracleErrorType)
	}
}

func assertAccountOracleBool(t *testing.T, raw json.RawMessage, want bool) {
	t.Helper()
	if bytes.Equal(raw, []byte("null")) {
		if want {
			t.Fatal("Python result is null, Go succeeded")
		}
		return
	}
	var got bool
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode oracle bool: %v (%s)", err, raw)
	}
	if got != want {
		t.Fatalf("bool result differs: Go=%v, Python=%v", want, got)
	}
}

func assertAccountOracleJSONValue(t *testing.T, value any, raw json.RawMessage) {
	t.Helper()
	encoded, err := encodePreferenceJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(data []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var result any
		if err := decoder.Decode(&result); err != nil {
			t.Fatalf("decode JSON value: %v (%s)", err, data)
		}
		return result
	}
	goValue, pythonValue := decode(encoded), decode(raw)
	if !reflect.DeepEqual(goValue, pythonValue) {
		t.Fatalf("JSON value differs\nGo:     %s\nPython: %s", encoded, raw)
	}
}

func runAccountOracle(t *testing.T, payload any) accountOracleOutput {
	t.Helper()
	sdkRoot, script := accountOraclePaths(t)
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
		t.Fatalf("Python account oracle failed: %v\n%s", err, stderr.String())
	}
	var result accountOracleOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Python account oracle: %v\n%s", err, output)
	}
	return result
}

func accountOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate account oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "account_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "account.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "bip32.py"),
		filepath.Join(sdkRoot, "lbry", "crypto", "crypt.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK account source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}
