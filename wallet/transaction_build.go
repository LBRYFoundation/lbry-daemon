package wallet

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

var (
	ErrUnattachedTransactionOutput = errors.New("transaction output is not attached to a transaction")
	ErrUnsupportedSpendOutput      = errors.New("attempting to spend unsupported transaction output")
	ErrInvalidTransactionClaimID   = errors.New("invalid transaction claim ID")
)

const (
	transactionNullSignatureSize = 72
	transactionNullPublicKeySize = 33
)

// NewTransaction constructs the default non-broadcast transaction used by the
// pinned SDK and materializes its derived byte and hash fields.
func NewTransaction() *Transaction {
	transaction := &Transaction{
		Version:   1,
		Witnesses: make([][]byte, 0),
		Inputs:    make([]TransactionInput, 0),
		Outputs:   make([]TransactionOutput, 0),
		Height:    -2,
		Position:  -1,
	}
	_ = transaction.RebuildDerived()
	return transaction
}

// AddInputs appends values in order, assigns their positions, and invalidates
// and rebuilds all serialized transaction state. The caller's slice is updated
// too, matching the SDK's mutation of the supplied input objects.
func (transaction *Transaction) AddInputs(inputs []TransactionInput) *Transaction {
	start := len(transaction.Inputs)
	for index := range inputs {
		inputs[index].Position = uint32(start + index)
	}
	transaction.Inputs = append(transaction.Inputs, inputs...)
	transaction.ResetDerived()
	_ = transaction.RebuildDerived()
	copy(inputs, transaction.Inputs[start:])
	return transaction
}

// AddOutputs appends values in order, assigns their positions and owner, and
// rebuilds output transaction IDs and hashes. The caller's slice is refreshed
// after the append for the same value-slice semantics as AddInputs.
func (transaction *Transaction) AddOutputs(outputs []TransactionOutput) *Transaction {
	start := len(transaction.Outputs)
	for index := range outputs {
		outputs[index].Position = uint32(start + index)
		outputs[index].owner = transaction
	}
	transaction.Outputs = append(transaction.Outputs, outputs...)
	transaction.ResetDerived()
	_ = transaction.RebuildDerived()
	copy(outputs, transaction.Outputs[start:])
	return transaction
}

// ResetDerived mirrors Transaction._reset: metadata, SegWit observations,
// witnesses, and trailing input are retained, while byte/hash caches and output
// link values are cleared.
func (transaction *Transaction) ResetDerived() {
	transaction.Raw = nil
	transaction.RawSansSegWit = nil
	transaction.Hash = [sha256.Size]byte{}
	transaction.ID = ""
	for index := range transaction.Outputs {
		transaction.Outputs[index].TransactionID = ""
		transaction.Outputs[index].TransactionHash = [sha256.Size]byte{}
		transaction.Outputs[index].owner = transaction
	}
}

// RebuildDerived serializes the current fields in the SDK's canonical
// non-witness form. A parsed SegWit flag and witnesses remain observable on the
// object, but they are deliberately not emitted after a reset.
func (transaction *Transaction) RebuildDerived() error {
	if transaction == nil {
		return fmt.Errorf("%w: nil transaction", ErrInvalidWalletTransaction)
	}
	if uint64(len(transaction.Inputs)) > uint64(math.MaxUint32)+1 {
		return fmt.Errorf("%w: too many inputs", ErrInvalidWalletTransaction)
	}
	if uint64(len(transaction.Outputs)) > uint64(math.MaxUint32)+1 {
		return fmt.Errorf("%w: too many outputs", ErrInvalidWalletTransaction)
	}
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		input.Position = uint32(index)
		input.owner = transaction
		if input.ResolvedOutput != nil {
			refreshInputPreviousOutput(transaction, input)
		}
	}
	for index := range transaction.Outputs {
		transaction.Outputs[index].Position = uint32(index)
		transaction.Outputs[index].owner = transaction
	}

	raw := transaction.serializeSansSegWit()
	transaction.Raw = append([]byte(nil), raw...)
	transaction.RawSansSegWit = append([]byte(nil), raw...)
	first := sha256.Sum256(transaction.RawSansSegWit)
	transaction.Hash = sha256.Sum256(first[:])
	transaction.ID = hex.EncodeToString(reverseTransactionBytes(transaction.Hash[:]))
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		output.TransactionID = transaction.ID
		output.TransactionHash = transaction.Hash
		output.owner = transaction
	}
	return nil
}

func refreshInputPreviousOutput(transaction *Transaction, input *TransactionInput) {
	output := currentTransactionOutput(input.ResolvedOutput)
	input.ResolvedOutput = output
	transactionHash := output.TransactionHash
	transactionID := output.TransactionID
	if output.owner != nil && output.owner != transaction {
		if output.owner.ID == "" {
			_ = output.owner.RebuildDerived()
		}
		transactionHash = output.owner.Hash
		transactionID = output.owner.ID
	}
	input.PreviousHash = transactionHash
	input.PreviousTxID = transactionID
	input.PreviousIndex = output.Position
}

// Size returns the serialized input size, including its CompactSize-prefixed
// script and sequence.
func (input TransactionInput) Size() int {
	buffer := bytes.NewBuffer(make([]byte, 0, 148))
	buffer.Write(input.PreviousHash[:])
	_ = binary.Write(buffer, binary.LittleEndian, input.PreviousIndex)
	source := input.Coinbase
	if !input.IsCoinbase() {
		source = input.Script.Source
	}
	writeTransactionVarBytes(buffer, source)
	_ = binary.Write(buffer, binary.LittleEndian, input.Sequence)
	return buffer.Len()
}

// Size returns the serialized output size, including its CompactSize-prefixed
// script.
func (output TransactionOutput) Size() int {
	buffer := bytes.NewBuffer(make([]byte, 0, len(output.Script.Source)+9))
	_ = binary.Write(buffer, binary.LittleEndian, output.Amount)
	writeTransactionVarBytes(buffer, output.Script.Source)
	return buffer.Len()
}

// Size returns the current serialized transaction size, rebuilding a reset
// transaction first.
func (transaction *Transaction) Size() int {
	if transaction == nil {
		return 0
	}
	if transaction.Raw == nil {
		_ = transaction.RebuildDerived()
	}
	return len(transaction.Raw)
}

// BaseSize returns the transaction size excluding all serialized inputs and
// outputs.
func (transaction *Transaction) BaseSize() int {
	size := transaction.Size()
	for _, input := range transaction.Inputs {
		size -= input.Size()
	}
	for _, output := range transaction.Outputs {
		size -= output.Size()
	}
	return size
}

// InputSum returns the value of inputs whose previous outputs are resolved.
func (transaction *Transaction) InputSum() uint64 {
	var sum uint64
	for _, input := range transaction.Inputs {
		if input.ResolvedOutput != nil {
			sum += currentTransactionOutput(input.ResolvedOutput).Amount
		}
	}
	return sum
}

// OutputSum returns the value of every transaction output.
func (transaction *Transaction) OutputSum() uint64 {
	var sum uint64
	for _, output := range transaction.Outputs {
		sum += output.Amount
	}
	return sum
}

// Fee matches the SDK's input_sum - output_sum contract for ordinary wallet
// amounts, including a negative result for an underfunded transaction.
func (transaction *Transaction) Fee() int64 {
	return int64(transaction.InputSum()) - int64(transaction.OutputSum())
}

func decodeTransactionClaimID(claimID string) ([]byte, error) {
	decoded, err := hex.DecodeString(claimID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTransactionClaimID, err)
	}
	return reverseTransactionBytes(decoded), nil
}

// NewPayPubKeyHashOutput constructs a P2PKH output without imposing Bitcoin's
// usual 20-byte hash length. The SDK's script factory accepts arbitrary bytes.
func NewPayPubKeyHashOutput(amount uint64, pubKeyHash []byte) TransactionOutput {
	script, err := NewPayPubKeyHashOutputScript(pubKeyHash)
	if err != nil {
		script.Err = err
	}
	return TransactionOutput{Amount: amount, Script: script}
}

// NewPayScriptHashOutput constructs a P2SH output.
func NewPayScriptHashOutput(amount uint64, scriptHash []byte) TransactionOutput {
	script, err := NewPayScriptHashOutputScript(scriptHash)
	if err != nil {
		script.Err = err
	}
	return TransactionOutput{Amount: amount, Script: script}
}

// NewReturnDataOutput constructs the zero-value OP_RETURN output used for
// transaction metadata.
func NewReturnDataOutput(data []byte) TransactionOutput {
	script, err := NewReturnDataOutputScript(data)
	if err != nil {
		script.Err = err
	}
	return TransactionOutput{Script: script}
}

// NewPurchaseDataOutput mirrors Output.add_purchase_data(Purchase(claimID)).
// Purchase claim hashes use internal byte order and proto3 omits an empty bytes
// field, leaving only the leading 'P' discriminator for an empty claim ID.
func NewPurchaseDataOutput(claimID string) (TransactionOutput, error) {
	internalClaimID, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return TransactionOutput{}, err
	}
	payload := []byte{'P'}
	if len(internalClaimID) > 0 {
		payload = protowire.AppendTag(payload, 1, protowire.BytesType)
		payload = protowire.AppendBytes(payload, internalClaimID)
	}
	return NewReturnDataOutput(payload), nil
}

// NewClaimNameOutput constructs a claim-name P2PKH output. Go strings are
// serialized as their UTF-8 byte representation, matching str.encode().
func NewClaimNameOutput(
	amount uint64, claimName string, claim, pubKeyHash []byte,
) TransactionOutput {
	script, err := NewClaimNamePubKeyHashOutputScript([]byte(claimName), claim, pubKeyHash)
	if err != nil {
		script.Err = err
	}
	return TransactionOutput{Amount: amount, Script: script}
}

// NewUpdateClaimOutput accepts the display-order hexadecimal claim ID and
// stores it in the reversed internal byte order used by output scripts.
func NewUpdateClaimOutput(
	amount uint64, claimName, claimID string, claim, pubKeyHash []byte,
) (TransactionOutput, error) {
	internalClaimID, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return TransactionOutput{}, err
	}
	script, err := NewUpdateClaimPubKeyHashOutputScript(
		[]byte(claimName), internalClaimID, claim, pubKeyHash,
	)
	if err != nil {
		return TransactionOutput{}, err
	}
	return TransactionOutput{Amount: amount, Script: script}, nil
}

// NewSupportOutput constructs a support-without-data P2PKH output.
func NewSupportOutput(
	amount uint64, claimName, claimID string, pubKeyHash []byte,
) (TransactionOutput, error) {
	internalClaimID, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return TransactionOutput{}, err
	}
	script, err := NewSupportPubKeyHashOutputScript([]byte(claimName), internalClaimID, pubKeyHash)
	if err != nil {
		return TransactionOutput{}, err
	}
	return TransactionOutput{Amount: amount, Script: script}, nil
}

// NewSupportDataOutput constructs a support-with-data P2PKH output.
func NewSupportDataOutput(
	amount uint64, claimName, claimID string, support, pubKeyHash []byte,
) (TransactionOutput, error) {
	internalClaimID, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return TransactionOutput{}, err
	}
	script, err := NewSupportDataPubKeyHashOutputScript(
		[]byte(claimName), internalClaimID, support, pubKeyHash,
	)
	if err != nil {
		return TransactionOutput{}, err
	}
	return TransactionOutput{Amount: amount, Script: script}, nil
}

// NewSpendInput constructs the SDK's unsigned P2PKH redemption placeholder.
// Claim, update, and support outputs qualify because their templates share the
// pay_pubkey_hash suffix.
func NewSpendInput(output *TransactionOutput) (TransactionInput, error) {
	if output == nil {
		return TransactionInput{}, ErrUnattachedTransactionOutput
	}
	if !currentTransactionOutput(output).Script.IsPayPubKeyHash() {
		return TransactionInput{}, ErrUnsupportedSpendOutput
	}
	var err error
	output, err = ensureSpendableOutputAttached(output)
	if err != nil {
		return TransactionInput{}, err
	}
	script, err := NewRedeemPubKeyHashInputScript(
		make([]byte, transactionNullSignatureSize),
		make([]byte, transactionNullPublicKeySize),
	)
	if err != nil {
		return TransactionInput{}, err
	}
	return TransactionInput{
		PreviousHash:   output.TransactionHash,
		PreviousTxID:   output.TransactionID,
		PreviousIndex:  output.Position,
		Sequence:       math.MaxUint32,
		Script:         script,
		ResolvedOutput: output,
	}, nil
}

// NewTimeLockSpendInput constructs the unsigned P2SH timelock redemption
// placeholder. As in the SDK, the referenced output template is not checked.
func NewTimeLockSpendInput(
	output *TransactionOutput, scriptSource []byte,
) (TransactionInput, error) {
	script, err := NewRedeemTimeLockScriptHashInputScript(
		make([]byte, transactionNullSignatureSize),
		make([]byte, transactionNullPublicKeySize),
		nil, nil, scriptSource,
	)
	if err != nil {
		return TransactionInput{}, err
	}
	output, err = ensureSpendableOutputAttached(output)
	if err != nil {
		return TransactionInput{}, err
	}
	return TransactionInput{
		PreviousHash:   output.TransactionHash,
		PreviousTxID:   output.TransactionID,
		PreviousIndex:  output.Position,
		Sequence:       math.MaxUint32,
		Script:         script,
		ResolvedOutput: output,
	}, nil
}

func currentTransactionOutput(output *TransactionOutput) *TransactionOutput {
	if output != nil && output.owner != nil &&
		uint64(output.Position) < uint64(len(output.owner.Outputs)) {
		return &output.owner.Outputs[output.Position]
	}
	return output
}

func ensureSpendableOutputAttached(output *TransactionOutput) (*TransactionOutput, error) {
	if output == nil {
		return nil, ErrUnattachedTransactionOutput
	}
	if output.owner != nil {
		owner := output.owner
		if uint64(output.Position) >= uint64(len(owner.Outputs)) {
			return nil, ErrUnattachedTransactionOutput
		}
		if output.owner.ID == "" {
			if err := output.owner.RebuildDerived(); err != nil {
				return nil, err
			}
		}
		output.TransactionID = output.owner.ID
		output.TransactionHash = output.owner.Hash
		output = &owner.Outputs[output.Position]
	}
	if output.TransactionID == "" {
		return nil, ErrUnattachedTransactionOutput
	}
	return output, nil
}
