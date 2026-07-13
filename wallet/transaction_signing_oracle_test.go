package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"lbry/daemon/wallet/keys"
)

var transactionSigningOraclePinnedSources = map[string]string{
	"lbry/wallet/bip32.py": "bbc027ae706338bd7a232290c110dcefc308b2b635179e01f51487cf8b05825a",
}

type transactionSigningOracleResponse struct {
	Reference struct {
		Commit              string            `json:"commit"`
		Version             string            `json:"version"`
		SourceSHA256        map[string]string `json:"source_sha256"`
		SigningSourceSHA256 map[string]string `json:"signing_source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		TransactionSignExecuted  bool   `json:"transaction_sign_executed"`
		DeterministicSECPAdapter bool   `json:"deterministic_secp256k1_adapter"`
	} `json:"metadata"`
	Signing transactionSigningOracleCases `json:"signing"`
}

type transactionSigningOracleCases struct {
	UnitFixtureSignatureHex string                          `json:"unit_fixture_signature_hex"`
	UnitFixture             transactionSigningUnitFixture   `json:"unit_fixture"`
	P2PKH                   transactionSigningOracleCase    `json:"p2pkh"`
	MultiInput              transactionSigningOracleCase    `json:"multi_input"`
	TimeLock                transactionSigningOracleCase    `json:"timelock"`
	Errors                  []transactionSigningOracleError `json:"errors"`
}

type transactionSigningUnitFixture struct {
	PrivateKeyHex string `json:"private_key_hex"`
	PublicKeyHex  string `json:"public_key_hex"`
	PreimageHex   string `json:"preimage_hex"`
	DigestHex     string `json:"digest_hex"`
	SignatureHex  string `json:"signature_hex"`
	Matches       bool   `json:"matches"`
}

type transactionSigningOracleCase struct {
	Name                    string     `json:"name"`
	UnsignedRawHex          string     `json:"unsigned_raw_hex"`
	UnsignedID              string     `json:"unsigned_id"`
	PreimagesHex            []string   `json:"preimages_hex"`
	DigestsHex              []string   `json:"digests_hex"`
	KeyPayloadsHex          [][]string `json:"key_payloads_hex"`
	LookupAddresses         []string   `json:"lookup_addresses"`
	SignaturesHex           []string   `json:"signatures_hex"`
	DERSignaturesHex        []string   `json:"der_signatures_hex"`
	PublicKeysHex           []string   `json:"public_keys_hex"`
	InputScriptsHex         []string   `json:"input_scripts_hex"`
	PreimagesAfterHex       []string   `json:"preimages_after_hex"`
	PostSignRawCacheIsNone  bool       `json:"post_sign_raw_cache_is_none"`
	PostSignIDCacheIsNone   bool       `json:"post_sign_id_cache_is_none"`
	FinalRawHex             string     `json:"final_raw_hex"`
	FinalHashHex            string     `json:"final_hash_hex"`
	FinalID                 string     `json:"final_id"`
	RawChanged              bool       `json:"raw_changed"`
	IDChanged               bool       `json:"id_changed"`
	PreviousOutputScriptHex string     `json:"previous_output_script_hex"`
	SelectedScriptsHex      []string   `json:"selected_scripts_hex"`
	RedeemScriptHex         string     `json:"redeem_script_hex"`
}

type transactionSigningOracleError struct {
	Name                string   `json:"name"`
	ErrorType           string   `json:"error_type"`
	ErrorMessage        string   `json:"error_message"`
	RawCacheIsNone      bool     `json:"raw_cache_is_none"`
	IDCacheIsNone       bool     `json:"id_cache_is_none"`
	BeforeScriptsHex    []string `json:"before_scripts_hex"`
	AfterScriptsHex     []string `json:"after_scripts_hex"`
	SignaturesHex       []string `json:"signatures_hex"`
	FirstKeyPayloadsHex []string `json:"first_key_payloads_hex"`
}

func TestTransactionSigningMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionSigningOracle(t)
	if oracle.Reference.Commit != transactionOraclePinnedCommit ||
		oracle.Reference.Version != transactionOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.SigningSourceSHA256, transactionSigningOraclePinnedSources) {
		t.Fatalf("transaction signing oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.TransactionSignExecuted || !oracle.Metadata.DeterministicSECPAdapter {
		t.Fatalf("transaction signing oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction signing oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
	const unitFixtureSignature = "304402200dafa26ad7cf38c5a971c8a25ce7d85a076235f146126762296b1223c42ae21e022020ef9eeb8398327891008c5c0be4357683f12cb22346691ff23914f457bf679601"
	if oracle.Signing.UnitFixtureSignatureHex != unitFixtureSignature {
		t.Fatalf("pinned unit signature = %q, want %q", oracle.Signing.UnitFixtureSignatureHex, unitFixtureSignature)
	}
	assertGoSigningUnitFixture(t, oracle.Signing.UnitFixture)

	assertGoP2PKHSigningMatchesOracle(t, oracle.Signing.P2PKH)
	assertGoMultiInputSigningMatchesOracle(t, oracle.Signing.MultiInput)
	assertGoTimeLockSigningMatchesOracle(t, oracle.Signing.TimeLock)
	assertGoSigningErrorsMatchOracle(t, oracle.Signing.Errors)
}

func assertGoSigningUnitFixture(t *testing.T, expected transactionSigningUnitFixture) {
	t.Helper()
	if !expected.Matches || expected.SignatureHex == "" ||
		expected.SignatureHex != "304402200dafa26ad7cf38c5a971c8a25ce7d85a076235f146126762296b1223c42ae21e022020ef9eeb8398327891008c5c0be4357683f12cb22346691ff23914f457bf679601" {
		t.Fatalf("Python signing adapter unit fixture = %+v", expected)
	}
	privateKeyBytes := transactionOracleDecodeHex(t, expected.PrivateKeyHex)
	privateKey, err := keys.NewPrivateKey(
		keys.MainNet, privateKeyBytes, make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	preimage := transactionOracleDecodeHex(t, expected.PreimageHex)
	digest := transactionOracleDecodeHex(t, expected.DigestHex)
	firstDigest := sha256.Sum256(preimage)
	secondDigest := sha256.Sum256(firstDigest[:])
	signature, err := privateKey.Sign(preimage)
	if err != nil {
		t.Fatal(err)
	}
	signature = append(signature, byte(TransactionSigHashAll))
	if hex.EncodeToString(privateKey.PublicKey().CompressedBytes()) != expected.PublicKeyHex ||
		!bytes.Equal(secondDigest[:], digest) || hex.EncodeToString(signature) != expected.SignatureHex {
		t.Fatalf("Go signing unit fixture = pubkey %x digest %x signature %x, want %+v",
			privateKey.PublicKey().CompressedBytes(), secondDigest, signature, expected)
	}
}

func assertGoP2PKHSigningMatchesOracle(t *testing.T, expected transactionSigningOracleCase) {
	t.Helper()
	privateKey := transactionSigningOraclePrivateKey(t, 1)
	pubKeyHash := keys.Hash160(privateKey.PublicKey().CompressedBytes())
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_600_000, pubKeyHash[:]),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.Version = 2
	transaction.LockTime = 9
	transaction.AddInputs([]TransactionInput{input}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_500_000, bytes.Repeat([]byte{0x91}, 20)),
	})
	if hex.EncodeToString(parent.Outputs[0].Script.Source) != expected.PreviousOutputScriptHex {
		t.Fatalf("P2PKH selected script = %x, want %s", parent.Outputs[0].Script.Source, expected.PreviousOutputScriptHex)
	}
	assertGoSignedTransactionMatchesOracle(
		t, expected, transaction, []*keys.PrivateKey{privateKey},
		func(context.Context, int, *TransactionInput, *TransactionOutput) (*keys.PrivateKey, error) {
			return privateKey, nil
		},
	)
}

func assertGoMultiInputSigningMatchesOracle(t *testing.T, expected transactionSigningOracleCase) {
	t.Helper()
	privateKeys := []*keys.PrivateKey{
		transactionSigningOraclePrivateKey(t, 2),
		transactionSigningOraclePrivateKey(t, 0x12345),
	}
	inputs := make([]TransactionInput, 0, len(privateKeys))
	selectedScripts := make([]string, 0, len(privateKeys))
	for index, privateKey := range privateKeys {
		pubKeyHash := keys.Hash160(privateKey.PublicKey().CompressedBytes())
		parent := NewTransaction().AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(1_000_000+uint64(index)*500_000, pubKeyHash[:]),
		})
		input, err := NewSpendInput(&parent.Outputs[0])
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
		selectedScripts = append(selectedScripts, hex.EncodeToString(parent.Outputs[0].Script.Source))
	}
	if !reflect.DeepEqual(selectedScripts, expected.SelectedScriptsHex) {
		t.Fatalf("multi-input selected scripts = %v, want %v", selectedScripts, expected.SelectedScriptsHex)
	}
	transaction := NewTransaction()
	transaction.Version = 3
	transaction.LockTime = 11
	transaction.AddInputs(inputs).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(2_300_000, bytes.Repeat([]byte{0x92}, 20)),
	})
	assertGoSignedTransactionMatchesOracle(
		t, expected, transaction, privateKeys,
		func(_ context.Context, index int, _ *TransactionInput, _ *TransactionOutput) (*keys.PrivateKey, error) {
			return privateKeys[index], nil
		},
	)
}

func assertGoTimeLockSigningMatchesOracle(t *testing.T, expected transactionSigningOracleCase) {
	t.Helper()
	privateKey := transactionSigningOraclePrivateKey(t, 3)
	pubKeyHash := keys.Hash160(privateKey.PublicKey().CompressedBytes())
	redeemScript, err := NewTimeLockInputSubscript(big.NewInt(500), pubKeyHash[:])
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := keys.Hash160(redeemScript.Source)
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayScriptHashOutput(2_000_000, scriptHash[:]),
	})
	input, err := NewTimeLockSpendInput(&parent.Outputs[0], redeemScript.Source)
	if err != nil {
		t.Fatal(err)
	}
	input.Sequence = 0xfffffffe
	transaction := NewTransaction()
	transaction.Version = 2
	transaction.LockTime = 500
	transaction.AddInputs([]TransactionInput{input}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_900_000, bytes.Repeat([]byte{0x93}, 20)),
	})
	if hex.EncodeToString(redeemScript.Source) != expected.RedeemScriptHex ||
		hex.EncodeToString(parent.Outputs[0].Script.Source) != expected.PreviousOutputScriptHex {
		t.Fatalf("timelock scripts = %x / %x, want %s / %s", redeemScript.Source,
			parent.Outputs[0].Script.Source, expected.RedeemScriptHex, expected.PreviousOutputScriptHex)
	}
	assertGoSignedTransactionMatchesOracle(
		t, expected, transaction, []*keys.PrivateKey{privateKey},
		func(context.Context, int, *TransactionInput, *TransactionOutput) (*keys.PrivateKey, error) {
			return privateKey, nil
		},
	)
}

func assertGoSignedTransactionMatchesOracle(
	t *testing.T, expected transactionSigningOracleCase, transaction *Transaction,
	privateKeys []*keys.PrivateKey, provider TransactionSigningKeyProvider,
) {
	t.Helper()
	preimages := make([]string, len(transaction.Inputs))
	digests := make([]string, len(transaction.Inputs))
	for index := range transaction.Inputs {
		preimage, err := transaction.SignaturePreimage(index)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := transaction.SignatureDigest(index)
		if err != nil {
			t.Fatal(err)
		}
		preimages[index] = hex.EncodeToString(preimage)
		digests[index] = hex.EncodeToString(digest[:])
	}
	if len(expected.KeyPayloadsHex) != len(preimages) {
		t.Fatalf("Python signing key payload groups = %v, want one per input", expected.KeyPayloadsHex)
	}
	for index := range preimages {
		if !reflect.DeepEqual(expected.KeyPayloadsHex[index], []string{preimages[index]}) {
			t.Fatalf("Python key %d received %v, expected preimage %s",
				index, expected.KeyPayloadsHex[index], preimages[index])
		}
	}
	if hex.EncodeToString(transaction.Raw) != expected.UnsignedRawHex ||
		transaction.ID != expected.UnsignedID || !reflect.DeepEqual(preimages, expected.PreimagesHex) ||
		!reflect.DeepEqual(digests, expected.DigestsHex) {
		t.Fatalf("unsigned signing state = raw %x id %s preimages %v digests %v, want %+v",
			transaction.Raw, transaction.ID, preimages, digests, expected)
	}
	if err := transaction.Sign(context.Background(), provider); err != nil {
		t.Fatal(err)
	}

	signatures := make([]string, len(transaction.Inputs))
	derSignatures := make([]string, len(transaction.Inputs))
	publicKeys := make([]string, len(transaction.Inputs))
	inputScripts := make([]string, len(transaction.Inputs))
	preimagesAfter := make([]string, len(transaction.Inputs))
	for index := range transaction.Inputs {
		signature := transaction.Inputs[index].Script.Signature
		if len(signature) == 0 || signature[len(signature)-1] != byte(TransactionSigHashAll) {
			t.Fatalf("input %d signature hash type = %x", index, signature)
		}
		signatures[index] = hex.EncodeToString(signature)
		derSignatures[index] = hex.EncodeToString(signature[:len(signature)-1])
		publicKeys[index] = hex.EncodeToString(transaction.Inputs[index].Script.PublicKey)
		inputScripts[index] = hex.EncodeToString(transaction.Inputs[index].Script.Source)
		preimage, err := transaction.SignaturePreimage(index)
		if err != nil {
			t.Fatal(err)
		}
		preimagesAfter[index] = hex.EncodeToString(preimage)
		if publicKeys[index] != hex.EncodeToString(privateKeys[index].PublicKey().CompressedBytes()) {
			t.Fatalf("input %d public key = %s", index, publicKeys[index])
		}
	}
	if !reflect.DeepEqual(signatures, expected.SignaturesHex) ||
		!reflect.DeepEqual(derSignatures, expected.DERSignaturesHex) ||
		!reflect.DeepEqual(publicKeys, expected.PublicKeysHex) ||
		!reflect.DeepEqual(inputScripts, expected.InputScriptsHex) ||
		!reflect.DeepEqual(preimagesAfter, expected.PreimagesAfterHex) ||
		hex.EncodeToString(transaction.Raw) != expected.FinalRawHex ||
		hex.EncodeToString(transaction.Hash[:]) != expected.FinalHashHex ||
		transaction.ID != expected.FinalID ||
		!expected.PostSignRawCacheIsNone || !expected.PostSignIDCacheIsNone ||
		transaction.Raw == nil || transaction.ID == "" ||
		(hex.EncodeToString(transaction.Raw) != expected.UnsignedRawHex) != expected.RawChanged ||
		(transaction.ID != expected.UnsignedID) != expected.IDChanged {
		t.Fatalf("signed transaction = raw %x hash %x id %s signatures %v scripts %v preimages %v, want %+v",
			transaction.Raw, transaction.Hash, transaction.ID, signatures, inputScripts, preimagesAfter, expected)
	}
}

func assertGoSigningErrorsMatchOracle(t *testing.T, expected []transactionSigningOracleError) {
	t.Helper()
	if len(expected) != 4 {
		t.Fatalf("signing error cases = %d, want 4", len(expected))
	}
	byName := make(map[string]transactionSigningOracleError, len(expected))
	wantPythonErrors := map[string][2]string{
		"missing second key after partial sign": {"AssertionError", "Cannot find private key for signing output."},
		"unsupported previous output":           {"NotImplementedError", "Don't know how to spend this output."},
		"unresolved previous output":            {"AssertionError", ""},
		"p2sh missing extra keys":               {"AttributeError", "'NoneType' object has no attribute 'values'"},
	}
	for _, value := range expected {
		want, exists := wantPythonErrors[value.Name]
		if !exists || value.ErrorType != want[0] || value.ErrorMessage != want[1] {
			t.Fatalf("signing oracle Python error = %+v, want %v", value, want)
		}
		byName[value.Name] = value
	}

	firstKey := transactionSigningOraclePrivateKey(t, 4)
	secondKey := transactionSigningOraclePrivateKey(t, 5)
	privateKeys := []*keys.PrivateKey{firstKey, secondKey}
	inputs := make([]TransactionInput, 0, 2)
	for index, privateKey := range privateKeys {
		pubKeyHash := keys.Hash160(privateKey.PublicKey().CompressedBytes())
		parent := NewTransaction().AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(1_000_000+uint64(index), pubKeyHash[:]),
		})
		input, err := NewSpendInput(&parent.Outputs[0])
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
	}
	partial := NewTransaction().AddInputs(inputs).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_500_000, bytes.Repeat([]byte{0x94}, 20)),
	})
	partialExpected := byName["missing second key after partial sign"]
	assertGoSigningBeforeError(t, partialExpected, partial)
	partialPreimage, err := partial.SignaturePreimage(0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]string{hex.EncodeToString(partialPreimage)}, partialExpected.FirstKeyPayloadsHex) {
		t.Fatalf("partial-sign first payload = %x, want %v", partialPreimage, partialExpected.FirstKeyPayloadsHex)
	}
	missingSecondKey := errors.New("missing second signing key")
	err = partial.Sign(context.Background(), func(
		_ context.Context, index int, _ *TransactionInput, _ *TransactionOutput,
	) (*keys.PrivateKey, error) {
		if index == 0 {
			return firstKey, nil
		}
		return nil, missingSecondKey
	})
	if !errors.Is(err, ErrTransactionSigning) || !errors.Is(err, missingSecondKey) {
		t.Fatalf("partial signing error = %v", err)
	}
	assertGoSigningErrorState(t, partialExpected, partial)

	unsupportedKey := transactionSigningOraclePrivateKey(t, 6)
	fullScript, err := NewPayPubKeyFullOutputScript(unsupportedKey.PublicKey().CompressedBytes())
	if err != nil {
		t.Fatal(err)
	}
	unsupportedParent := NewTransaction().AddOutputs([]TransactionOutput{{Amount: 1_000_000, Script: fullScript}})
	unsupportedInput := transactionSigningOracleInput(t, &unsupportedParent.Outputs[0])
	unsupported := NewTransaction().AddInputs([]TransactionInput{unsupportedInput}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(900_000, bytes.Repeat([]byte{0x95}, 20)),
	})
	assertGoSigningBeforeError(t, byName["unsupported previous output"], unsupported)
	err = unsupported.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return unsupportedKey, nil
	})
	if !errors.Is(err, ErrTransactionSigning) || !errors.Is(err, ErrUnsupportedSpendOutput) {
		t.Fatalf("unsupported signing error = %v", err)
	}
	assertGoSigningErrorState(t, byName["unsupported previous output"], unsupported)

	pubKeyHash := keys.Hash160(firstKey.PublicKey().CompressedBytes())
	unresolvedParent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_000_000, pubKeyHash[:]),
	})
	unresolvedInput, err := NewSpendInput(&unresolvedParent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	unresolvedInput.ResolvedOutput = nil
	unresolved := NewTransaction().AddInputs([]TransactionInput{unresolvedInput}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(900_000, bytes.Repeat([]byte{0x96}, 20)),
	})
	assertGoSigningBeforeError(t, byName["unresolved previous output"], unresolved)
	err = unresolved.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return firstKey, nil
	})
	if !errors.Is(err, ErrTransactionSigning) || !errors.Is(err, ErrUnattachedTransactionOutput) {
		t.Fatalf("unresolved signing error = %v", err)
	}
	assertGoSigningErrorState(t, byName["unresolved previous output"], unresolved)

	timeLockKey := transactionSigningOraclePrivateKey(t, 7)
	timeLockHash := keys.Hash160(timeLockKey.PublicKey().CompressedBytes())
	redeemScript, err := NewTimeLockInputSubscript(big.NewInt(600), timeLockHash[:])
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := keys.Hash160(redeemScript.Source)
	timeLockParent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayScriptHashOutput(1_000_000, scriptHash[:]),
	})
	timeLockInput, err := NewTimeLockSpendInput(&timeLockParent.Outputs[0], redeemScript.Source)
	if err != nil {
		t.Fatal(err)
	}
	missingExtra := NewTransaction().AddInputs([]TransactionInput{timeLockInput}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(900_000, bytes.Repeat([]byte{0x97}, 20)),
	})
	assertGoSigningBeforeError(t, byName["p2sh missing extra keys"], missingExtra)
	err = missingExtra.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrTransactionSigning) || !errors.Is(err, ErrTransactionSigningKeyUnavailable) {
		t.Fatalf("missing P2SH key error = %v", err)
	}
	assertGoSigningErrorState(t, byName["p2sh missing extra keys"], missingExtra)
}

func assertGoSigningBeforeError(
	t *testing.T, expected transactionSigningOracleError, transaction *Transaction,
) {
	t.Helper()
	scripts := make([]string, len(transaction.Inputs))
	for index := range transaction.Inputs {
		scripts[index] = hex.EncodeToString(transaction.Inputs[index].Script.Source)
	}
	if !reflect.DeepEqual(scripts, expected.BeforeScriptsHex) {
		t.Fatalf("unsigned scripts = %v, want %v", scripts, expected.BeforeScriptsHex)
	}
}

func assertGoSigningErrorState(
	t *testing.T, expected transactionSigningOracleError, transaction *Transaction,
) {
	t.Helper()
	afterScripts := make([]string, len(transaction.Inputs))
	signatures := make([]string, len(transaction.Inputs))
	for index := range transaction.Inputs {
		afterScripts[index] = hex.EncodeToString(transaction.Inputs[index].Script.Source)
		signatures[index] = hex.EncodeToString(transaction.Inputs[index].Script.Signature)
	}
	if !expected.RawCacheIsNone || !expected.IDCacheIsNone ||
		transaction.Raw != nil || transaction.ID != "" ||
		!reflect.DeepEqual(afterScripts, expected.AfterScriptsHex) ||
		!reflect.DeepEqual(signatures, expected.SignaturesHex) {
		t.Fatalf("signing error state = raw %x id %q scripts %v signatures %v, want %+v",
			transaction.Raw, transaction.ID, afterScripts, signatures, expected)
	}
}

func transactionSigningOracleInput(t *testing.T, output *TransactionOutput) TransactionInput {
	t.Helper()
	script, err := NewRedeemPubKeyHashInputScript(make([]byte, 72), make([]byte, 33))
	if err != nil {
		t.Fatal(err)
	}
	return TransactionInput{
		PreviousHash: output.TransactionHash, PreviousTxID: output.TransactionID,
		PreviousIndex: output.Position, Sequence: math.MaxUint32,
		Script: script, ResolvedOutput: output,
	}
}

func transactionSigningOraclePrivateKey(t *testing.T, value uint64) *keys.PrivateKey {
	t.Helper()
	secret := make([]byte, 32)
	binary.BigEndian.PutUint64(secret[24:], value)
	privateKey, err := keys.NewPrivateKey(keys.MainNet, secret, make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func runTransactionSigningOracle(t *testing.T) transactionSigningOracleResponse {
	t.Helper()
	sdkRoot, script := transactionOraclePaths(t)
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction signing oracle failed: %v\n%s", err, output)
	}
	var oracle transactionSigningOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction signing oracle: %v\n%s", err, output)
	}
	return oracle
}
