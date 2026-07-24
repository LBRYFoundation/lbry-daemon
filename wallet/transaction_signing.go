package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"lbry/daemon/wallet/keys"
)

const TransactionSigHashAll uint32 = 1

var (
	ErrTransactionSigning               = errors.New("wallet transaction signing failed")
	ErrTransactionSigningKeyUnavailable = errors.New("transaction signing key is unavailable")
)

// TransactionSigningKeyProvider keeps transaction signing independent from
// account storage. The callback may choose a key using the input, its resolved
// output, or external wallet state.
type TransactionSigningKeyProvider func(
	context.Context, int, *TransactionInput, *TransactionOutput,
) (*keys.PrivateKey, error)

// SignaturePreimage reproduces Transaction._serialize_for_signature with
// SIGHASH_ALL. Out-of-range indices are intentionally accepted and result in
// every input receiving an empty script.
func (transaction *Transaction) SignaturePreimage(inputIndex int) ([]byte, error) {
	if transaction == nil {
		return nil, fmt.Errorf("%w: nil transaction", ErrTransactionSigning)
	}
	return transaction.signaturePreimage(inputIndex, transaction.serializeSigningOutputs())
}

// SignatureDigest returns the double-SHA256 digest signed for an input.
func (transaction *Transaction) SignatureDigest(inputIndex int) ([sha256.Size]byte, error) {
	preimage, err := transaction.SignaturePreimage(inputIndex)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	first := sha256.Sum256(preimage)
	return sha256.Sum256(first[:]), nil
}

func (transaction *Transaction) signaturePreimage(
	inputIndex int, serializedOutputs []byte,
) ([]byte, error) {
	buffer := bytes.NewBuffer(make([]byte, 0, len(transaction.Raw)+4))
	_ = binary.Write(buffer, binary.LittleEndian, transaction.Version)
	writeTransactionCompactSize(buffer, uint64(len(transaction.Inputs)))
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		previousHash, previousIndex, err := transaction.signingPreviousReference(index, input)
		if err != nil {
			return nil, err
		}
		buffer.Write(previousHash[:])
		_ = binary.Write(buffer, binary.LittleEndian, previousIndex)
		scriptSource := []byte(nil)
		if index == inputIndex {
			scriptSource, err = transaction.signatureScriptSource(index, input)
			if err != nil {
				return nil, err
			}
		}
		writeTransactionVarBytes(buffer, scriptSource)
		_ = binary.Write(buffer, binary.LittleEndian, input.Sequence)
	}
	buffer.Write(serializedOutputs)
	_ = binary.Write(buffer, binary.LittleEndian, transaction.LockTime)
	_ = binary.Write(buffer, binary.LittleEndian, TransactionSigHashAll)
	return buffer.Bytes(), nil
}

func (transaction *Transaction) signingPreviousReference(
	inputIndex int, input *TransactionInput,
) ([sha256.Size]byte, uint32, error) {
	previousHash := input.PreviousHash
	previousIndex := input.PreviousIndex
	if input.ResolvedOutput == nil {
		return previousHash, previousIndex, nil
	}
	resolvedOutput := currentTransactionOutput(input.ResolvedOutput)
	previousIndex = resolvedOutput.Position
	previousHash = resolvedOutput.TransactionHash
	if resolvedOutput.owner != nil && resolvedOutput.owner != transaction {
		parent := resolvedOutput.owner
		if parent.ID == "" {
			if err := parent.RebuildDerived(); err != nil {
				return [sha256.Size]byte{}, 0, fmt.Errorf(
					"%w: input %d parent transaction: %w",
					ErrTransactionSigning, inputIndex, err,
				)
			}
		}
		previousHash = parent.Hash
	}
	return previousHash, previousIndex, nil
}

func (transaction *Transaction) signatureScriptSource(
	inputIndex int, input *TransactionInput,
) ([]byte, error) {
	if input.Script.Template == "" && input.Script.Source == nil && input.Script.Err == nil {
		return nil, fmt.Errorf(
			"%w: input %d script is unavailable", ErrTransactionSigning, inputIndex,
		)
	}
	if input.Script.Err != nil {
		return nil, fmt.Errorf(
			"%w: input %d script: %w", ErrTransactionSigning, inputIndex, input.Script.Err,
		)
	}
	if strings.HasPrefix(input.Script.Template, "script_hash+") {
		if input.Script.Script == nil {
			return nil, fmt.Errorf(
				"%w: input %d redeem script is unavailable", ErrTransactionSigning, inputIndex,
			)
		}
		return input.Script.Script.Source, nil
	}
	if input.ResolvedOutput == nil {
		return nil, fmt.Errorf(
			"%w: input %d: %w", ErrTransactionSigning, inputIndex, ErrUnattachedTransactionOutput,
		)
	}
	return currentTransactionOutput(input.ResolvedOutput).Script.Source, nil
}

func (transaction *Transaction) serializeSigningOutputs() []byte {
	buffer := bytes.NewBuffer(nil)
	writeTransactionCompactSize(buffer, uint64(len(transaction.Outputs)))
	for index := range transaction.Outputs {
		output := &transaction.Outputs[index]
		_ = binary.Write(buffer, binary.LittleEndian, output.Amount)
		writeTransactionVarBytes(buffer, output.Script.Source)
	}
	return buffer.Bytes()
}

// Sign signs inputs in order and preserves the SDK's partial-mutation behavior:
// if a later input fails, earlier input scripts remain signed while transaction
// byte and hash caches remain reset.
func (transaction *Transaction) Sign(
	ctx context.Context, keyProvider TransactionSigningKeyProvider,
) error {
	if transaction == nil {
		return fmt.Errorf("%w: nil transaction", ErrTransactionSigning)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	transaction.ResetDerived()
	var serializedOutputs []byte
	for index := range transaction.Inputs {
		input := &transaction.Inputs[index]
		if input.Script.Template == "" && input.Script.Source == nil && input.Script.Err == nil {
			return fmt.Errorf(
				"%w: input %d script is unavailable", ErrTransactionSigning, index,
			)
		}
		if input.ResolvedOutput == nil {
			return fmt.Errorf(
				"%w: input %d: %w", ErrTransactionSigning, index, ErrUnattachedTransactionOutput,
			)
		}
		resolvedOutput := currentTransactionOutput(input.ResolvedOutput)
		if resolvedOutput.Script.Err != nil {
			return fmt.Errorf(
				"%w: input %d resolved output script: %w",
				ErrTransactionSigning, index, resolvedOutput.Script.Err,
			)
		}
		if !resolvedOutput.Script.IsPayPubKeyHash() && !resolvedOutput.Script.IsPayScriptHash() {
			return fmt.Errorf(
				"%w: input %d: %w", ErrTransactionSigning, index, ErrUnsupportedSpendOutput,
			)
		}
		if keyProvider == nil {
			return fmt.Errorf(
				"%w: input %d: %w",
				ErrTransactionSigning, index, ErrTransactionSigningKeyUnavailable,
			)
		}
		privateKey, err := keyProvider(ctx, index, input, resolvedOutput)
		if err != nil {
			return fmt.Errorf("%w: input %d key provider: %w", ErrTransactionSigning, index, err)
		}
		if privateKey == nil {
			return fmt.Errorf(
				"%w: input %d: %w",
				ErrTransactionSigning, index, ErrTransactionSigningKeyUnavailable,
			)
		}
		if serializedOutputs == nil {
			serializedOutputs = transaction.serializeSigningOutputs()
		}
		preimage, err := transaction.signaturePreimage(index, serializedOutputs)
		if err != nil {
			return err
		}
		signature, err := privateKey.Sign(preimage)
		if err != nil {
			return fmt.Errorf("%w: input %d private key: %w", ErrTransactionSigning, index, err)
		}
		input.Script.Signature = append(signature, byte(TransactionSigHashAll))
		input.Script.PublicKey = privateKey.PublicKey().CompressedBytes()
		if err := input.Script.Generate(); err != nil {
			return fmt.Errorf("%w: input %d script: %w", ErrTransactionSigning, index, err)
		}
	}
	transaction.ResetDerived()
	if err := transaction.RebuildDerived(); err != nil {
		return fmt.Errorf("%w: rebuild transaction: %w", ErrTransactionSigning, err)
	}
	return nil
}
