package wallet

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestUnsignedTransactionConstructionSizesAndRoundTrip(t *testing.T) {
	pubKeyHash := make([]byte, 32)
	fundingOutput := NewPayPubKeyHashOutput(1_600_000, pubKeyHash)
	fundingOutputs := []TransactionOutput{fundingOutput}
	funding := NewTransaction().AddOutputs(fundingOutputs)

	input, err := NewSpendInput(&funding.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if input.Size() != 148 {
		t.Fatalf("input size = %d, want 148", input.Size())
	}
	if len(input.Script.Signature) != transactionNullSignatureSize ||
		len(input.Script.PublicKey) != transactionNullPublicKeySize ||
		input.Sequence != math.MaxUint32 {
		t.Fatalf("unsigned input = %#v", input)
	}

	output := NewPayPubKeyHashOutput(1_500_000, pubKeyHash)
	if output.Size() != 46 {
		t.Fatalf("output size = %d, want 46", output.Size())
	}
	inputs := []TransactionInput{input}
	outputs := []TransactionOutput{output}
	transaction := NewTransaction().AddInputs(inputs).AddOutputs(outputs)
	if transaction.Size() != 204 {
		t.Fatalf("transaction size = %d, want 204", transaction.Size())
	}
	if transaction.BaseSize() != 10 {
		t.Fatalf("base size = %d, want 10", transaction.BaseSize())
	}
	if transaction.InputSum() != 1_600_000 || transaction.OutputSum() != 1_500_000 ||
		transaction.Fee() != 100_000 {
		t.Fatalf("sums = %d - %d = %d", transaction.InputSum(), transaction.OutputSum(), transaction.Fee())
	}
	if inputs[0].Position != 0 || outputs[0].Position != 0 ||
		outputs[0].TransactionID != transaction.ID {
		t.Fatalf("caller slices were not linked: input=%#v output=%#v", inputs[0], outputs[0])
	}

	parsed, err := ParseTransaction(transaction.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID != transaction.ID || !bytes.Equal(parsed.Raw, transaction.Raw) ||
		parsed.Outputs[0].owner != parsed {
		t.Fatalf("round trip mismatch: parsed=%#v transaction=%#v", parsed, transaction)
	}
}

func TestTransactionAddAndRebuildRefreshDerivedOutputLinks(t *testing.T) {
	outputs := []TransactionOutput{
		NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x11}, 20)),
		NewClaimNameOutput(2, "name", []byte{0x00}, bytes.Repeat([]byte{0x22}, 20)),
	}
	transaction := NewTransaction().AddOutputs(outputs)
	firstID := transaction.ID
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		if output.owner != transaction || output.Position != uint32(index) ||
			output.TransactionID != transaction.ID || output.TransactionHash != transaction.Hash {
			t.Fatalf("output %d link = %#v", index, output)
		}
	}
	claimID, err := outputs[1].ClaimID()
	if err != nil || len(claimID) != 40 {
		t.Fatalf("claim ID = %q, %v", claimID, err)
	}

	transaction.LockTime = 7
	transaction.ResetDerived()
	if transaction.Raw != nil || transaction.ID != "" ||
		transaction.Outputs[1].TransactionID != "" {
		t.Fatalf("derived state was not reset: %#v", transaction)
	}
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	if transaction.ID == firstID || transaction.Outputs[1].TransactionID != transaction.ID ||
		transaction.Outputs[1].owner != transaction {
		t.Fatalf("derived state was not refreshed: %#v", transaction)
	}
	refreshedClaimID, err := outputs[1].ClaimID()
	if err != nil || refreshedClaimID == claimID || outputs[1].ID() != transaction.Outputs[1].ID() {
		t.Fatalf("caller output did not follow owner: id=%q claim=%q error=%v", outputs[1].ID(), refreshedClaimID, err)
	}

	canonical := append([]byte(nil), transaction.Raw...)
	transaction.AddInputs(nil)
	if !bytes.Equal(transaction.Raw, canonical) {
		t.Fatalf("empty append changed canonical raw: %x != %x", transaction.Raw, canonical)
	}
}

func TestTransactionDirectMutationRetainsCacheUntilReset(t *testing.T) {
	transaction := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x23}, 20)),
	})
	originalRaw := append([]byte(nil), transaction.Raw...)
	originalID := transaction.ID
	transaction.Outputs[0].Amount = 2
	if !bytes.Equal(transaction.Raw, originalRaw) || transaction.ID != originalID {
		t.Fatalf("direct mutation unexpectedly invalidated derived state")
	}
	transaction.ResetDerived()
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(transaction.Raw, originalRaw) || transaction.ID == originalID {
		t.Fatalf("explicit reset did not rebuild direct mutation")
	}

	inputResetID := transaction.ID
	transaction.LockTime = 8
	transaction.AddInputs(nil)
	if transaction.ID == inputResetID || binary.LittleEndian.Uint32(transaction.Raw[len(transaction.Raw)-4:]) != 8 {
		t.Fatalf("empty AddInputs did not reset: %s", transaction.ID)
	}
	outputResetID := transaction.ID
	transaction.LockTime = 9
	transaction.AddOutputs(nil)
	if transaction.ID == outputResetID || binary.LittleEndian.Uint32(transaction.Raw[len(transaction.Raw)-4:]) != 9 {
		t.Fatalf("empty AddOutputs did not reset: %s", transaction.ID)
	}
}

func TestTransactionRebuildCanonicalizesNonCanonicalCountsAndDropsTrailing(t *testing.T) {
	pubKeyHash := bytes.Repeat([]byte{0x31}, 20)
	funding := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(10, pubKeyHash),
	})
	input, err := NewSpendInput(&funding.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	canonical := NewTransaction().AddInputs([]TransactionInput{input}).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(9, pubKeyHash),
	})
	nonCanonical := makeNonCanonicalTransaction(canonical)
	parsed, err := ParseTransaction(nonCanonical)
	if err != nil {
		t.Fatal(err)
	}
	initialID := parsed.ID
	if !bytes.Equal(parsed.Trailing, []byte{0xcc}) {
		t.Fatalf("trailing = %x", parsed.Trailing)
	}
	parsed.AddInputs(nil)
	if !bytes.Equal(parsed.Raw, canonical.Raw) || parsed.ID == initialID {
		t.Fatalf("canonical rebuild = %x / %s, want %x", parsed.Raw, parsed.ID, canonical.Raw)
	}
	if !bytes.Equal(parsed.Trailing, []byte{0xcc}) {
		t.Fatalf("trailing observation was discarded: %x", parsed.Trailing)
	}
}

func TestTransactionRebuildDropsWitnessAndTrailingSerialization(t *testing.T) {
	outputScript, err := NewPayPubKeyHashOutputScript(bytes.Repeat([]byte{0x33}, 20))
	if err != nil {
		t.Fatal(err)
	}
	raw := makeSimpleSegWitTransaction(outputScript.Source)
	parsed, err := ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical := append([]byte(nil), parsed.RawSansSegWit...)
	wantID := parsed.ID
	if parsed.SegWitFlag != 1 || len(parsed.Witnesses) != 1 ||
		!bytes.Equal(parsed.Trailing, []byte{0xbb}) {
		t.Fatalf("parsed SegWit observations = %#v", parsed)
	}

	parsed.AddOutputs(nil)
	if !bytes.Equal(parsed.Raw, wantCanonical) || !bytes.Equal(parsed.RawSansSegWit, wantCanonical) {
		t.Fatalf("rebuilt raw = %x / %x, want %x", parsed.Raw, parsed.RawSansSegWit, wantCanonical)
	}
	if parsed.ID != wantID {
		t.Fatalf("SegWit ID changed after canonical rebuild: %s != %s", parsed.ID, wantID)
	}
	if parsed.SegWitFlag != 1 || len(parsed.Witnesses) != 1 ||
		!bytes.Equal(parsed.Witnesses[0], []byte{0xaa}) ||
		!bytes.Equal(parsed.Trailing, []byte{0xbb}) {
		t.Fatalf("rebuild discarded parsed observations: %#v", parsed)
	}
	if parsed.Outputs[0].owner != parsed || parsed.Outputs[0].TransactionID != parsed.ID {
		t.Fatalf("parsed output was not refreshed: %#v", parsed.Outputs[0])
	}
}

func TestTransactionOutputFactoriesUseDisplayClaimIDs(t *testing.T) {
	hash := bytes.Repeat([]byte{0x44}, 20)
	claimID := "001122334455"
	wantInternal, _ := hex.DecodeString("554433221100")

	update, err := NewUpdateClaimOutput(1, "name", claimID, []byte{0x00}, hash)
	if err != nil {
		t.Fatal(err)
	}
	support, err := NewSupportOutput(2, "name", claimID, hash)
	if err != nil {
		t.Fatal(err)
	}
	supportData, err := NewSupportDataOutput(3, "name", claimID, []byte{0x00}, hash)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []TransactionOutput{update, support, supportData} {
		if !bytes.Equal(output.Script.ClaimID, wantInternal) {
			t.Fatalf("internal claim ID = %x, want %x", output.Script.ClaimID, wantInternal)
		}
	}
	if _, err := NewSupportOutput(1, "name", "abc", hash); !errors.Is(err, ErrInvalidTransactionClaimID) {
		t.Fatalf("odd claim ID error = %v", err)
	}
	if empty, err := NewSupportOutput(1, "name", "", hash); err != nil || len(empty.Script.ClaimID) != 0 {
		t.Fatalf("empty claim ID = %#v, %v", empty, err)
	}
}

func TestPurchaseDataOutputUsesPinnedProto3WireFormat(t *testing.T) {
	output, err := NewPurchaseDataOutput("001122334455")
	if err != nil {
		t.Fatal(err)
	}
	wantData, _ := hex.DecodeString("500a06554433221100")
	if output.Amount != 0 || output.Script.Template != TransactionScriptReturnData ||
		!bytes.Equal(output.Script.Data, wantData) ||
		!bytes.Equal(output.Script.Source, append([]byte{transactionOpReturn, byte(len(wantData))}, wantData...)) {
		t.Fatalf("purchase output = %#v, want payload %x", output, wantData)
	}

	empty, err := NewPurchaseDataOutput("")
	if err != nil || !bytes.Equal(empty.Script.Data, []byte{'P'}) ||
		!bytes.Equal(empty.Script.Source, []byte{transactionOpReturn, 1, 'P'}) {
		t.Fatalf("empty purchase output = %#v, %v", empty, err)
	}
	if _, err := NewPurchaseDataOutput("abc"); !errors.Is(err, ErrInvalidTransactionClaimID) {
		t.Fatalf("invalid purchase claim ID error = %v", err)
	}
	large, err := NewPurchaseDataOutput(strings.Repeat("ab", 130))
	if err != nil || !bytes.HasPrefix(
		large.Script.Source, []byte{transactionOpReturn, transactionOpPushData1, 0x86, 'P', 0x0a, 0x82, 0x01},
	) {
		t.Fatalf("arbitrary-length purchase output = %x, %v", large.Script.Source, err)
	}
}

func TestNewSpendInputAttachmentAndTemplateRules(t *testing.T) {
	hash := bytes.Repeat([]byte{0x55}, 20)
	if _, err := NewSpendInput(nil); !errors.Is(err, ErrUnattachedTransactionOutput) {
		t.Fatalf("nil output error = %v", err)
	}
	unattached := NewPayPubKeyHashOutput(1, hash)
	if _, err := NewSpendInput(&unattached); !errors.Is(err, ErrUnattachedTransactionOutput) {
		t.Fatalf("unattached output error = %v", err)
	}
	unattachedScriptHash := NewPayScriptHashOutput(1, hash)
	if _, err := NewSpendInput(&unattachedScriptHash); !errors.Is(err, ErrUnsupportedSpendOutput) {
		t.Fatalf("unattached P2SH precedence error = %v", err)
	}

	claim := NewClaimNameOutput(1, "name", []byte{0x00}, hash)
	transaction := NewTransaction().AddOutputs([]TransactionOutput{claim})
	if _, err := NewSpendInput(&transaction.Outputs[0]); err != nil {
		t.Fatalf("claim P2PKH spend: %v", err)
	}

	scriptHash := NewPayScriptHashOutput(1, hash)
	transaction = NewTransaction().AddOutputs([]TransactionOutput{scriptHash})
	if _, err := NewSpendInput(&transaction.Outputs[0]); !errors.Is(err, ErrUnsupportedSpendOutput) {
		t.Fatalf("P2SH spend error = %v", err)
	}

	timeLockSource, _ := hex.DecodeString("02d200b17576a914555555555555555555555555555555555555555588ac")
	timeLockInput, err := NewTimeLockSpendInput(&transaction.Outputs[0], timeLockSource)
	if err != nil {
		t.Fatal(err)
	}
	if timeLockInput.Script.Template != TransactionInputScriptHashTime ||
		timeLockInput.Script.Script == nil ||
		timeLockInput.Script.Script.Height == nil ||
		timeLockInput.Script.Script.Height.Int64() != 210 {
		t.Fatalf("timelock input = %#v", timeLockInput)
	}
}

func TestSpendInputFollowsParentAfterCallerCopyAndSliceGrowth(t *testing.T) {
	hash := bytes.Repeat([]byte{0x66}, 20)
	callerOutputs := []TransactionOutput{NewPayPubKeyHashOutput(10, hash)}
	parent := NewTransaction().AddOutputs(callerOutputs)
	parent.AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(20, hash)})
	parent.Outputs[0].Amount = 30
	parent.LockTime = 12
	parent.ResetDerived()
	if err := parent.RebuildDerived(); err != nil {
		t.Fatal(err)
	}

	input, err := NewSpendInput(&callerOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if input.ResolvedOutput != &parent.Outputs[0] || input.PreviousTxID != parent.ID ||
		input.ResolvedOutput.Amount != 30 {
		t.Fatalf("spend did not resolve current parent output: %#v", input)
	}
	transaction := NewTransaction().AddInputs([]TransactionInput{input})
	if transaction.Inputs[0].TransactionID() != transaction.ID || transaction.InputSum() != 30 {
		t.Fatalf("input parent link/sum = %q/%d", transaction.Inputs[0].TransactionID(), transaction.InputSum())
	}
	parent.Outputs[0].Amount = 40
	if transaction.InputSum() != 40 {
		t.Fatalf("input sum retained stale parent amount: %d", transaction.InputSum())
	}
}

func makeSimpleSegWitTransaction(outputScript []byte) []byte {
	raw := make([]byte, 0, 100)
	raw = append(raw, 1, 0, 0, 0) // version
	raw = append(raw, 0, 1, 1)    // marker, flag, input count
	raw = append(raw, bytes.Repeat([]byte{0x11}, 32)...)
	raw = append(raw, 0, 0, 0, 0)             // previous position
	raw = append(raw, 0)                      // empty input script
	raw = append(raw, 0xff, 0xff, 0xff, 0xff) // sequence
	raw = append(raw, 1)                      // output count
	raw = append(raw, 1, 0, 0, 0, 0, 0, 0, 0) // amount
	raw = append(raw, byte(len(outputScript)))
	raw = append(raw, outputScript...)
	raw = append(raw, 1, 1, 0xaa) // one witness item
	raw = append(raw, 0, 0, 0, 0) // locktime
	return append(raw, 0xbb)      // ignored trailing byte
}

func makeNonCanonicalTransaction(transaction *Transaction) []byte {
	buffer := bytes.NewBuffer(make([]byte, 0, len(transaction.Raw)+7))
	_ = binary.Write(buffer, binary.LittleEndian, transaction.Version)
	buffer.Write([]byte{0xfd, byte(len(transaction.Inputs)), 0})
	for _, input := range transaction.Inputs {
		buffer.Write(input.PreviousHash[:])
		_ = binary.Write(buffer, binary.LittleEndian, input.PreviousIndex)
		writeTransactionVarBytes(buffer, input.Script.Source)
		_ = binary.Write(buffer, binary.LittleEndian, input.Sequence)
	}
	buffer.Write([]byte{0xfd, byte(len(transaction.Outputs)), 0})
	for _, output := range transaction.Outputs {
		_ = binary.Write(buffer, binary.LittleEndian, output.Amount)
		writeTransactionVarBytes(buffer, output.Script.Source)
	}
	_ = binary.Write(buffer, binary.LittleEndian, transaction.LockTime)
	buffer.WriteByte(0xcc)
	return buffer.Bytes()
}
