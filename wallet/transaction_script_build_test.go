package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
)

func TestGeneratedTransactionScriptsMatchPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionOracle(t)
	pubKey := append([]byte{0x02}, bytes.Repeat([]byte{0x03}, 32)...)
	pubKeyHash := bytes.Repeat([]byte{0x11}, 20)
	scriptHash := bytes.Repeat([]byte{0x22}, 20)
	claimID := bytes.Repeat([]byte{0x33}, 20)
	claimName := []byte("name")
	claim := []byte{0x01, 0x02, 0x03}
	support := []byte{0x04, 0x05}

	outputBuilders := map[string]func() (TransactionOutputScript, error){
		"pay pubkey full": func() (TransactionOutputScript, error) {
			return NewPayPubKeyFullOutputScript(pubKey)
		},
		"pay pubkey hash": func() (TransactionOutputScript, error) {
			return NewPayPubKeyHashOutputScript(pubKeyHash)
		},
		"pay script hash": func() (TransactionOutputScript, error) {
			return NewPayScriptHashOutputScript(scriptHash)
		},
		"pay segwit": func() (TransactionOutputScript, error) {
			return NewSegWitOutputScript(scriptHash)
		},
		"return data": func() (TransactionOutputScript, error) {
			return NewReturnDataOutputScript([]byte{0xaa, 0xbb})
		},
		"claim pubkey hash": func() (TransactionOutputScript, error) {
			return NewClaimNamePubKeyHashOutputScript(claimName, claim, pubKeyHash)
		},
		"claim script hash": func() (TransactionOutputScript, error) {
			return NewClaimNameScriptHashOutputScript(claimName, claim, scriptHash)
		},
		"support pubkey hash": func() (TransactionOutputScript, error) {
			return NewSupportPubKeyHashOutputScript(claimName, claimID, pubKeyHash)
		},
		"support script hash": func() (TransactionOutputScript, error) {
			return NewSupportScriptHashOutputScript(claimName, claimID, scriptHash)
		},
		"support data pubkey hash": func() (TransactionOutputScript, error) {
			return NewSupportDataPubKeyHashOutputScript(claimName, claimID, support, pubKeyHash)
		},
		"support empty data script hash": func() (TransactionOutputScript, error) {
			return NewSupportDataScriptHashOutputScript(claimName, claimID, []byte{}, scriptHash)
		},
		"update pubkey hash": func() (TransactionOutputScript, error) {
			return NewUpdateClaimPubKeyHashOutputScript(claimName, claimID, claim, pubKeyHash)
		},
		"update script hash": func() (TransactionOutputScript, error) {
			return NewUpdateClaimScriptHashOutputScript(claimName, claimID, claim, scriptHash)
		},
	}
	generatedOutputs := 0
	for _, expected := range oracle.Scripts {
		builder, ok := outputBuilders[expected.Name]
		if !ok {
			continue
		}
		generatedOutputs++
		t.Run("output/"+expected.Name, func(t *testing.T) {
			actual, err := builder()
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(actual.Source); got != expected.SourceHex {
				t.Fatalf("generated source = %s, want pinned Python %s", got, expected.SourceHex)
			}
			assertTransactionOracleOutputScript(t, expected, ParseTransactionOutputScript(actual.Source))
		})
	}
	if generatedOutputs != len(outputBuilders) {
		t.Fatalf("matched %d/%d generated output fixtures", generatedOutputs, len(outputBuilders))
	}

	inputExpected := make(map[string]transactionOracleScript)
	for _, expected := range oracle.InputScripts {
		inputExpected[expected.Name] = expected
	}
	inputBuilders := map[string]func() (TransactionInputScript, error){
		"redeem pubkey": func() (TransactionInputScript, error) {
			return NewRedeemPubKeyInputScript([]byte{0x30, 0x01, 0x02})
		},
		"redeem pubkey hash": func() (TransactionInputScript, error) {
			return NewRedeemPubKeyHashInputScript([]byte{0x30, 0x01, 0x02}, pubKey)
		},
		"redeem timelock": func() (TransactionInputScript, error) {
			expected := inputExpected["redeem timelock"]
			parsed := ParseTransactionInputScript(transactionOracleDecodeHex(t, expected.SourceHex))
			return NewRedeemTimeLockScriptHashInputScript(
				parsed.Signature, parsed.PublicKey, nil, nil, parsed.Script.Source,
			)
		},
		"redeem multisig": func() (TransactionInputScript, error) {
			expected := inputExpected["redeem multisig"]
			parsed := ParseTransactionInputScript(transactionOracleDecodeHex(t, expected.SourceHex))
			return NewRedeemMultiSigScriptHashInputScript(parsed.Signatures, parsed.Script.PublicKeys)
		},
	}
	for name, builder := range inputBuilders {
		name, builder := name, builder
		t.Run("input/"+name, func(t *testing.T) {
			expected, ok := inputExpected[name]
			if !ok {
				t.Fatalf("pinned Python input fixture %q is missing", name)
			}
			actual, err := builder()
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(actual.Source); got != expected.SourceHex {
				t.Fatalf("generated source = %s, want pinned Python %s", got, expected.SourceHex)
			}
			assertTransactionOracleInputScript(t, expected, ParseTransactionInputScript(actual.Source))
		})
	}
}

func TestTransactionScriptCanonicalPushBoundaries(t *testing.T) {
	tests := []struct {
		size   int
		prefix string
	}{
		{0, "00"},
		{1, "01"},
		{75, "4b"},
		{76, "4c4c"},
		{255, "4cff"},
		{256, "4d0001"},
		{65535, "4dffff"},
		{65536, "4e00000100"},
	}
	for _, test := range tests {
		data := bytes.Repeat([]byte{0xa5}, test.size)
		encoded, err := encodeTransactionPushData(data)
		if err != nil {
			t.Fatalf("size %d: %v", test.size, err)
		}
		prefix := transactionOracleDecodeHex(t, test.prefix)
		if !bytes.HasPrefix(encoded, prefix) || !bytes.Equal(encoded[len(prefix):], data) {
			t.Fatalf("size %d encoded prefix/payload = %x", test.size, encoded[:min(len(encoded), 8)])
		}
	}
}

func TestTransactionScriptSignedIntegerEncoding(t *testing.T) {
	tests := []struct {
		value int64
		want  string
	}{
		{0, "0100"},
		{1, "0101"},
		{127, "017f"},
		{128, "028000"},
		{255, "02ff00"},
		{256, "020001"},
		{-1, "01ff"},
		{-128, "0280ff"},
		{-129, "027fff"},
	}
	for _, test := range tests {
		got, err := encodeTransactionScriptInteger(big.NewInt(test.value))
		if err != nil || hex.EncodeToString(got) != test.want {
			t.Fatalf("integer %d = %x, %v; want %s", test.value, got, err, test.want)
		}
	}
	if _, err := encodeTransactionScriptInteger(nil); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("nil integer error = %v", err)
	}
}

func TestTransactionScriptSmallIntegerAndMultisigQuirks(t *testing.T) {
	for value, want := range map[int]byte{1: transactionOp1, 16: transactionOp16} {
		got, err := encodeTransactionSmallInteger(value)
		if err != nil || got != want {
			t.Fatalf("small integer %d = %x, %v; want %x", value, got, err, want)
		}
	}
	for _, value := range []int{0, 17} {
		if _, err := encodeTransactionSmallInteger(value); !errors.Is(err, ErrTransactionScriptGeneration) {
			t.Fatalf("small integer %d error = %v", value, err)
		}
	}
	if _, err := NewRedeemMultiSigScriptHashInputScript(nil, [][]byte{{1}}); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("empty signatures error = %v", err)
	}
	if _, err := NewRedeemMultiSigScriptHashInputScript(
		[][]byte{{1}}, make([][]byte, 17),
	); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("17 public keys error = %v", err)
	}

	mismatched, err := NewRedeemMultiSigScriptHashInputScript(
		[][]byte{{1}, {2}}, [][]byte{{3}},
	)
	if err != nil || mismatched.Script.SignaturesCount != 2 || mismatched.Script.PublicKeysCount != 1 {
		t.Fatalf("mismatched multisig = %#v, %v", mismatched, err)
	}

	subscript, err := NewMultiSigInputSubscript(1, [][]byte{{3}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	emptyMany := TransactionInputScript{
		Template: TransactionInputScriptHashMulti,
		Signatures: [][]byte{
			{},
		},
		Script: &subscript,
	}
	if err := emptyMany.Generate(); err != nil {
		t.Fatal(err)
	}
	if len(emptyMany.Source) < 2 || emptyMany.Source[0] != transactionOp0 || emptyMany.Source[1] != transactionOp0 {
		t.Fatalf("empty PUSH_MANY source = %x", emptyMany.Source)
	}
	if parsed := ParseTransactionInputScript(emptyMany.Source); !errors.Is(parsed.Err, ErrInvalidTransactionScript) {
		t.Fatalf("empty PUSH_MANY unexpectedly round-tripped: %#v", parsed)
	}
}

func TestTransactionTimeLockGenerationBranchesAndRawSource(t *testing.T) {
	direct, err := NewTimeLockInputSubscript(big.NewInt(0), []byte{})
	if err != nil || hex.EncodeToString(direct.Source) != "0100b17576a90088ac" {
		t.Fatalf("direct zero timelock = %x, %v", direct.Source, err)
	}
	if _, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), big.NewInt(0), []byte("h"), nil,
	); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("factory zero height error = %v", err)
	}
	if _, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), big.NewInt(1), nil, nil,
	); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("factory empty pubkey hash error = %v", err)
	}

	noncanonical := transactionOracleDecodeHex(t, "4c0101b17576a9016888ac")
	fromSource, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), nil, nil, noncanonical,
	)
	if err != nil || !bytes.Equal(fromSource.Script.Source, noncanonical) ||
		!bytes.HasSuffix(fromSource.Source, append([]byte{byte(len(noncanonical))}, noncanonical...)) {
		t.Fatalf("raw timelock source = %#v, %v", fromSource, err)
	}
	previousOuter := append([]byte(nil), fromSource.Source...)
	if err := fromSource.Generate(); err != nil || !bytes.Equal(fromSource.Source, previousOuter) {
		t.Fatalf("outer regeneration changed raw subscript = %x, %v", fromSource.Source, err)
	}
	if err := fromSource.Script.Generate(); err != nil || bytes.Equal(fromSource.Script.Source, noncanonical) {
		t.Fatalf("nested regeneration did not canonicalize = %x, %v", fromSource.Script.Source, err)
	}

	priority, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), big.NewInt(1), []byte("h"), []byte("invalid"),
	)
	if err != nil || priority.Script.Height.Cmp(big.NewInt(1)) != 0 ||
		!bytes.Equal(priority.Script.PubKeyHash, []byte("h")) {
		t.Fatalf("height branch priority = %#v, %v", priority, err)
	}
	negative, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), big.NewInt(-1), []byte("h"), nil,
	)
	if err != nil || !bytes.HasPrefix(negative.Script.Source, []byte{1, 0xff}) {
		t.Fatalf("negative timelock = %#v, %v", negative, err)
	}
	if _, err := NewRedeemTimeLockScriptHashInputScript(
		[]byte("s"), []byte("k"), nil, nil, []byte("invalid"),
	); !errors.Is(err, ErrTransactionScriptGeneration) {
		t.Fatalf("invalid raw timelock error = %v", err)
	}
}

func TestTransactionScriptGenerateMutationErrorsAndNoLengthValidation(t *testing.T) {
	noOutput := ParseTransactionOutputScript(nil)
	if err := noOutput.Generate(); !errors.Is(err, ErrTransactionScriptGeneration) ||
		len(noOutput.Source) != 0 {
		t.Fatalf("no_script output generation = %#v, %v", noOutput, err)
	}
	noInput := ParseTransactionInputScript(nil)
	if err := noInput.Generate(); !errors.Is(err, ErrTransactionScriptGeneration) ||
		len(noInput.Source) != 0 {
		t.Fatalf("no_script input generation = %#v, %v", noInput, err)
	}

	output, err := NewPayPubKeyHashOutputScript(nil)
	if err != nil || !bytes.Equal(output.Source, []byte{
		transactionOpDup, transactionOpHash160, transactionOp0,
		transactionOpEqualVerify, transactionOpCheckSig,
	}) {
		t.Fatalf("empty pubkey hash output = %#v, %v", output, err)
	}
	oldSource := append([]byte(nil), output.Source...)
	output.Template = "unknown"
	if err := output.Generate(); !errors.Is(err, ErrTransactionScriptGeneration) ||
		!bytes.Equal(output.Source, oldSource) || !output.HasPubKeyHash {
		t.Fatalf("atomic output failure = %#v, %v", output, err)
	}

	input, err := NewRedeemPubKeyHashInputScript([]byte{1}, []byte{2})
	if err != nil {
		t.Fatal(err)
	}
	oldSource = append(oldSource[:0], input.Source...)
	input.Template = TransactionInputScriptHashTime
	input.Script = nil
	if err := input.Generate(); !errors.Is(err, ErrTransactionScriptGeneration) ||
		!bytes.Equal(input.Source, oldSource) {
		t.Fatalf("atomic input failure = %#v, %v", input, err)
	}

	subscript, err := NewMultiSigInputSubscript(1, [][]byte{{1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldSource = append(oldSource[:0], subscript.Source...)
	subscript.SignaturesCount = 0
	if err := subscript.Generate(); !errors.Is(err, ErrTransactionScriptGeneration) ||
		!bytes.Equal(subscript.Source, oldSource) {
		t.Fatalf("atomic subscript failure = %#v, %v", subscript, err)
	}

	claimName := []byte("n")
	claim := []byte("c")
	hash := []byte("h")
	generated, err := NewClaimNamePubKeyHashOutputScript(claimName, claim, hash)
	if err != nil {
		t.Fatal(err)
	}
	claimName[0], claim[0], hash[0] = 'x', 'x', 'x'
	if bytes.Contains(generated.Source, []byte("x")) {
		t.Fatalf("constructor retained caller aliases: %x", generated.Source)
	}
	generated.ClaimName = []byte("changed")
	if err := generated.Generate(); err != nil || !bytes.Contains(generated.Source, []byte("changed")) {
		t.Fatalf("regenerated mutation = %x, %v", generated.Source, err)
	}
}
