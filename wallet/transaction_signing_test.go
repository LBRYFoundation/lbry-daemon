package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	transactionSigningVectorPrivate  = "aa2d0f7f58abf07145ea83735a57d5170aff682bf47e61ff8b2a0f9a0cb61d3f"
	transactionSigningVectorPublic   = "02c68e2d1cf85404c86244ffa279f4c5cd00331e996d30a86d6e46480e3a9220f4"
	transactionSigningVectorPreimage = "0100000001a9f894c5a7c8493625f883cbd4e28b9f757b6fc2b5e3eb09c49725c66f7cc7dd" +
		"000000001976a91401244bd9f88fab49355f927b105d5650a8db344888acffffffff01802b530b00000000" +
		"1976a91415a5ba33e2057819330e043b6b0b27b6f292c50c88ac0000000001000000"
	transactionSigningVectorDigest = "680389ba7b86796509bfed4a8a0f2a33c7168bfa4524f34351de06ab929ba33f"
	transactionSigningVectorDER    = "304402200dafa26ad7cf38c5a971c8a25ce7d85a076235f146126762296b1223c42ae21e" +
		"022020ef9eeb8398327891008c5c0be4357683f12cb22346691ff23914f457bf6796"
	transactionSigningVectorID = "70243c617cffb4ea1575999ce65131b187b5d0bd8410eb4b0762704de26ebb75"
)

func TestTransactionSignaturePreimageAndSignMatchPinnedVector(t *testing.T) {
	privateKey := transactionSigningKey(t, transactionSigningVectorPrivate)
	previousHash := transactionSigningHex(t,
		"a9f894c5a7c8493625f883cbd4e28b9f757b6fc2b5e3eb09c49725c66f7cc7dd",
	)
	previousScript, err := NewPayPubKeyHashOutputScript(transactionSigningHex(
		t, "01244bd9f88fab49355f927b105d5650a8db3448",
	))
	if err != nil {
		t.Fatal(err)
	}
	var previousHashArray [sha256.Size]byte
	copy(previousHashArray[:], previousHash)
	resolvedOutput := &TransactionOutput{
		TransactionID:   hex.EncodeToString(reverseTransactionBytes(previousHash)),
		TransactionHash: previousHashArray,
		Amount:          200_000_000,
		Script:          previousScript,
	}
	inputScript, err := NewRedeemPubKeyHashInputScript(
		make([]byte, transactionNullSignatureSize), make([]byte, transactionNullPublicKeySize),
	)
	if err != nil {
		t.Fatal(err)
	}
	outputScript, err := NewPayPubKeyHashOutputScript(transactionSigningHex(
		t, "15a5ba33e2057819330e043b6b0b27b6f292c50c",
	))
	if err != nil {
		t.Fatal(err)
	}
	transaction := &Transaction{
		Version: 1,
		Inputs: []TransactionInput{{
			PreviousHash:   previousHashArray,
			PreviousIndex:  0,
			Sequence:       math.MaxUint32,
			Script:         inputScript,
			ResolvedOutput: resolvedOutput,
		}},
		Outputs:  []TransactionOutput{{Amount: 190_000_000, Script: outputScript}},
		Height:   -2,
		Position: -1,
	}

	preimage, err := transaction.SignaturePreimage(0)
	if err != nil || hex.EncodeToString(preimage) != transactionSigningVectorPreimage {
		t.Fatalf("signature preimage = %x, %v", preimage, err)
	}
	digest, err := transaction.SignatureDigest(0)
	if err != nil || hex.EncodeToString(digest[:]) != transactionSigningVectorDigest {
		t.Fatalf("signature digest = %x, %v", digest, err)
	}
	providerCalls := 0
	err = transaction.Sign(context.Background(), func(
		ctx context.Context, index int, input *TransactionInput, output *TransactionOutput,
	) (*keys.PrivateKey, error) {
		providerCalls++
		if ctx == nil || index != 0 || input != &transaction.Inputs[0] || output != resolvedOutput {
			t.Fatalf("provider args = %v, %d, %p, %p", ctx, index, input, output)
		}
		return privateKey, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSignature := transactionSigningHex(t, transactionSigningVectorDER+"01")
	if providerCalls != 1 ||
		!bytes.Equal(transaction.Inputs[0].Script.Signature, wantSignature) ||
		hex.EncodeToString(transaction.Inputs[0].Script.PublicKey) != transactionSigningVectorPublic ||
		transaction.ID != transactionSigningVectorID || len(transaction.Raw) == 0 {
		t.Fatalf("signed transaction = calls %d, input %#v, id %q, raw %x",
			providerCalls, transaction.Inputs[0], transaction.ID, transaction.Raw)
	}
	parsed := ParseTransactionInputScript(transaction.Inputs[0].Script.Source)
	if parsed.Err != nil || !bytes.Equal(parsed.Signature, wantSignature) ||
		!bytes.Equal(parsed.PublicKey, transaction.Inputs[0].Script.PublicKey) {
		t.Fatalf("generated signed input = %#v", parsed)
	}
	firstRaw := append([]byte(nil), transaction.Raw...)
	if err := transaction.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return privateKey, nil
	}); err != nil || !bytes.Equal(transaction.Raw, firstRaw) {
		t.Fatalf("repeat signing = raw %x, %v", transaction.Raw, err)
	}
}

func TestTransactionSignaturePreimageSubstitutesOnlyTargetInput(t *testing.T) {
	firstKey := transactionSigningKey(t, "01")
	secondKey := transactionSigningKey(t, "02")
	firstInput, firstOutput := transactionSigningSpend(t, firstKey, 700_000)
	secondInput, secondOutput := transactionSigningSpend(t, secondKey, 900_000)
	firstInput.Sequence = 10
	secondInput.Sequence = 20
	transaction := NewTransaction().
		AddInputs([]TransactionInput{firstInput, secondInput}).
		AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(
			500_000, bytes.Repeat([]byte{0x61}, 20),
		)})
	transaction.Version = 2
	transaction.LockTime = 33
	transaction.ResetDerived()
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}

	for target, wantScript := range map[int][]byte{
		0: firstOutput.Script.Source,
		1: secondOutput.Script.Source,
	} {
		preimage, err := transaction.SignaturePreimage(target)
		if err != nil {
			t.Fatal(err)
		}
		parts := parseTransactionSigningPreimage(t, preimage)
		if parts.version != 2 || parts.lockTime != 33 || parts.hashType != TransactionSigHashAll ||
			len(parts.inputScripts) != 2 || !bytes.Equal(parts.inputScripts[target], wantScript) ||
			len(parts.inputScripts[1-target]) != 0 || len(parts.outputScripts) != 1 ||
			!bytes.Equal(parts.outputScripts[0], transaction.Outputs[0].Script.Source) {
			t.Fatalf("target %d preimage parts = %#v", target, parts)
		}
	}
	for _, target := range []int{-1, 2, 99} {
		preimage, err := transaction.SignaturePreimage(target)
		if err != nil {
			t.Fatalf("out-of-range target %d: %v", target, err)
		}
		for index, script := range parseTransactionSigningPreimage(t, preimage).inputScripts {
			if len(script) != 0 {
				t.Fatalf("target %d input %d script = %x, want empty", target, index, script)
			}
		}
	}
}

func TestTransactionSigningUsesRawTimeLockRedeemScript(t *testing.T) {
	privateKey := transactionSigningKey(t, "03")
	identifier := privateKey.Identifier()
	subscript, err := NewTimeLockInputSubscript(big.NewInt(210), identifier[:])
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := keys.Hash160(subscript.Source)
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayScriptHashOutput(800_000, scriptHash[:]),
	})
	input, err := NewTimeLockSpendInput(&parent.Outputs[0], subscript.Source)
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction().
		AddInputs([]TransactionInput{input}).
		AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(700_000, identifier[:])})
	preimage, err := transaction.SignaturePreimage(0)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseTransactionSigningPreimage(t, preimage)
	if !bytes.Equal(parts.inputScripts[0], subscript.Source) ||
		bytes.Equal(parts.inputScripts[0], parent.Outputs[0].Script.Source) {
		t.Fatalf("timelock preimage script = %x", parts.inputScripts[0])
	}
	wantDER, err := privateKey.Sign(preimage)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return privateKey, nil
	}); err != nil {
		t.Fatal(err)
	}
	if transaction.Inputs[0].Script.Script == nil ||
		!bytes.Equal(transaction.Inputs[0].Script.Script.Source, subscript.Source) ||
		!bytes.Equal(transaction.Inputs[0].Script.Signature, append(wantDER, byte(TransactionSigHashAll))) {
		t.Fatalf("signed timelock input = %#v", transaction.Inputs[0])
	}
}

func TestTransactionSigningPreservesPartialMutationAndTypedErrors(t *testing.T) {
	privateKey := transactionSigningKey(t, "04")
	identifier := privateKey.Identifier()
	firstInput, _ := transactionSigningSpend(t, privateKey, 900_000)
	secondInput, _ := transactionSigningSpend(t, privateKey, 800_000)
	transaction := NewTransaction().
		AddInputs([]TransactionInput{firstInput, secondInput}).
		AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(
			1_600_000, identifier[:],
		)})
	providerErr := errors.New("provider stopped")
	err := transaction.Sign(context.Background(), func(
		_ context.Context, index int, _ *TransactionInput, _ *TransactionOutput,
	) (*keys.PrivateKey, error) {
		if index == 1 {
			return nil, providerErr
		}
		return privateKey, nil
	})
	if !errors.Is(err, ErrTransactionSigning) || !errors.Is(err, providerErr) {
		t.Fatalf("partial signing error = %v", err)
	}
	if bytes.Equal(transaction.Inputs[0].Script.Signature, make([]byte, transactionNullSignatureSize)) ||
		!bytes.Equal(transaction.Inputs[1].Script.Signature, make([]byte, transactionNullSignatureSize)) ||
		transaction.Raw != nil || transaction.ID != "" {
		t.Fatalf("partial signing state = %#v", transaction)
	}

	missingResolved := NewTransaction().AddInputs([]TransactionInput{{
		Script:   firstInput.Script,
		Sequence: math.MaxUint32,
	}})
	if err := missingResolved.Sign(context.Background(), nil); !errors.Is(err, ErrTransactionSigning) ||
		!errors.Is(err, ErrUnattachedTransactionOutput) {
		t.Fatalf("missing resolved output error = %v", err)
	}
	missingDirectScript := NewTransaction().AddInputs([]TransactionInput{{
		ResolvedOutput: firstInput.ResolvedOutput,
		Sequence:       math.MaxUint32,
	}})
	if _, err := missingDirectScript.SignaturePreimage(0); !errors.Is(err, ErrTransactionSigning) {
		t.Fatalf("missing direct preimage script error = %v", err)
	}

	input, output := transactionSigningSpend(t, privateKey, 700_000)
	nilProvider := NewTransaction().AddInputs([]TransactionInput{input})
	if err := nilProvider.Sign(context.Background(), nil); !errors.Is(err, ErrTransactionSigningKeyUnavailable) {
		t.Fatalf("nil provider error = %v", err)
	}
	input.ResolvedOutput = output
	nilKey := NewTransaction().AddInputs([]TransactionInput{input})
	if err := nilKey.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return nil, nil
	}); !errors.Is(err, ErrTransactionSigningKeyUnavailable) {
		t.Fatalf("nil key error = %v", err)
	}

	returnScript, err := NewReturnDataOutputScript([]byte("unsupported"))
	if err != nil {
		t.Fatal(err)
	}
	unsupportedParent := NewTransaction().AddOutputs([]TransactionOutput{{Amount: 1, Script: returnScript}})
	unsupportedInput := transactionSigningManualInput(t, &unsupportedParent.Outputs[0])
	unsupported := NewTransaction().AddInputs([]TransactionInput{unsupportedInput})
	if err := unsupported.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		return privateKey, nil
	}); !errors.Is(err, ErrUnsupportedSpendOutput) || !errors.Is(err, ErrTransactionSigning) {
		t.Fatalf("unsupported output error = %v", err)
	}

	validInput, _ := transactionSigningSpend(t, privateKey, 600_000)
	validInput.Script = TransactionInputScript{}
	missingScript := NewTransaction().AddInputs([]TransactionInput{validInput})
	providerCalled := false
	if err := missingScript.Sign(context.Background(), func(
		context.Context, int, *TransactionInput, *TransactionOutput,
	) (*keys.PrivateKey, error) {
		providerCalled = true
		return privateKey, nil
	}); !errors.Is(err, ErrTransactionSigning) || providerCalled {
		t.Fatalf("missing input script = %v, provider called %v", err, providerCalled)
	}
}

func TestTransactionSignaturePreimageUsesLiveParentReference(t *testing.T) {
	privateKey := transactionSigningKey(t, "05")
	identifier := privateKey.Identifier()
	callerOutputs := []TransactionOutput{NewPayPubKeyHashOutput(1_000, identifier[:])}
	parent := NewTransaction().AddOutputs(callerOutputs)
	input, err := NewSpendInput(&callerOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	staleHash := input.PreviousHash
	parent.AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(2_000, identifier[:])})
	parent.Outputs[0].Amount = 1_001
	parent.ResetDerived()
	transaction := NewTransaction().AddInputs([]TransactionInput{input})
	preimage, err := transaction.SignaturePreimage(-1)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseTransactionSigningPreimage(t, preimage)
	if parent.ID == "" || parts.previousHashes[0] != parent.Hash || parts.previousHashes[0] == staleHash ||
		parts.previousIndices[0] != parent.Outputs[0].Position {
		t.Fatalf("live previous ref = %x:%d, parent %x:%d, stale %x",
			parts.previousHashes[0], parts.previousIndices[0], parent.Hash,
			parent.Outputs[0].Position, staleHash)
	}
}

type transactionSigningPreimageParts struct {
	version         uint32
	previousHashes  [][sha256.Size]byte
	previousIndices []uint32
	inputScripts    [][]byte
	outputScripts   [][]byte
	lockTime        uint32
	hashType        uint32
}

func parseTransactionSigningPreimage(t *testing.T, preimage []byte) transactionSigningPreimageParts {
	t.Helper()
	reader := transactionReader{raw: preimage}
	version, err := reader.uint32()
	if err != nil {
		t.Fatal(err)
	}
	inputCount, err := reader.compactSize()
	if err != nil {
		t.Fatal(err)
	}
	parts := transactionSigningPreimageParts{
		version:         version,
		previousHashes:  make([][sha256.Size]byte, 0, inputCount),
		previousIndices: make([]uint32, 0, inputCount),
		inputScripts:    make([][]byte, 0, inputCount),
	}
	for index := uint64(0); index < inputCount; index++ {
		encodedHash, readErr := reader.read(sha256.Size)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var hash [sha256.Size]byte
		copy(hash[:], encodedHash)
		previousIndex, readErr := reader.uint32()
		if readErr != nil {
			t.Fatal(readErr)
		}
		script, readErr := reader.varBytes()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, readErr = reader.uint32(); readErr != nil {
			t.Fatal(readErr)
		}
		parts.previousHashes = append(parts.previousHashes, hash)
		parts.previousIndices = append(parts.previousIndices, previousIndex)
		parts.inputScripts = append(parts.inputScripts, script)
	}
	outputCount, err := reader.compactSize()
	if err != nil {
		t.Fatal(err)
	}
	parts.outputScripts = make([][]byte, 0, outputCount)
	for index := uint64(0); index < outputCount; index++ {
		if _, err = reader.uint64(); err != nil {
			t.Fatal(err)
		}
		script, readErr := reader.varBytes()
		if readErr != nil {
			t.Fatal(readErr)
		}
		parts.outputScripts = append(parts.outputScripts, script)
	}
	if parts.lockTime, err = reader.uint32(); err != nil {
		t.Fatal(err)
	}
	if parts.hashType, err = reader.uint32(); err != nil {
		t.Fatal(err)
	}
	if reader.offset != len(preimage) {
		t.Fatalf("preimage has %d trailing bytes", len(preimage)-reader.offset)
	}
	return parts
}

func transactionSigningSpend(
	t *testing.T, privateKey *keys.PrivateKey, amount uint64,
) (TransactionInput, *TransactionOutput) {
	t.Helper()
	identifier := privateKey.Identifier()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(amount, identifier[:]),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	return input, &parent.Outputs[0]
}

func transactionSigningManualInput(t *testing.T, output *TransactionOutput) TransactionInput {
	t.Helper()
	script, err := NewRedeemPubKeyHashInputScript(
		make([]byte, transactionNullSignatureSize), make([]byte, transactionNullPublicKeySize),
	)
	if err != nil {
		t.Fatal(err)
	}
	return TransactionInput{
		PreviousHash:   output.TransactionHash,
		PreviousTxID:   output.TransactionID,
		PreviousIndex:  output.Position,
		Sequence:       math.MaxUint32,
		Script:         script,
		ResolvedOutput: output,
	}
}

func transactionSigningKey(t *testing.T, encoded string) *keys.PrivateKey {
	t.Helper()
	secret := transactionSigningHex(t, encoded)
	if len(secret) < 32 {
		padded := make([]byte, 32)
		copy(padded[32-len(secret):], secret)
		secret = padded
	}
	privateKey, err := keys.NewPrivateKey(keys.MainNet, secret, make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func transactionSigningHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
