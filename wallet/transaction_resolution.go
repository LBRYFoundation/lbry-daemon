package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	ErrTransactionOutputOutOfRange = errors.New("transaction output position is out of range")
	ErrInvalidStoredTransaction    = errors.New("invalid stored wallet transaction")
)

type unresolvedTransactionInput struct {
	input *TransactionInput
	txid  string
	txoid string
	index uint32
}

// ResolveTransactionInputs follows the pinned ledger order: outputs in the
// same pending batch, stored txo rows, then stored raw transactions. Previous
// transactions absent from the remote address history are not consulted.
func (ledger *Ledger) ResolveTransactionInputs(
	ctx context.Context,
	transactions []*Transaction,
	remoteHistory map[string]struct{},
) error {
	pending := make(map[string]*Transaction, len(transactions))
	for _, transaction := range transactions {
		if transaction == nil {
			return fmt.Errorf("%w: transaction is nil", ErrInvalidWalletTransaction)
		}
		pending[transaction.ID] = transaction
	}

	unresolved := make([]unresolvedTransactionInput, 0)
	txoids := make([]string, 0)
	for _, transaction := range transactions {
		for index := range transaction.Inputs {
			input := &transaction.Inputs[index]
			if input.ResolvedOutput != nil {
				continue
			}
			if _, exists := remoteHistory[input.PreviousTxID]; !exists {
				continue
			}
			if previous := pending[input.PreviousTxID]; previous != nil {
				if uint64(input.PreviousIndex) >= uint64(len(previous.Outputs)) {
					return transactionOutputRangeError(input.PreviousTxID, input.PreviousIndex)
				}
				input.ResolvedOutput = &previous.Outputs[input.PreviousIndex]
				continue
			}
			unresolved = append(unresolved, unresolvedTransactionInput{
				input: input, txid: input.PreviousTxID,
				txoid: input.PreviousOutputID(), index: input.PreviousIndex,
			})
			txoids = append(txoids, input.PreviousOutputID())
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	if ledger == nil || ledger.Database == nil {
		return ErrTransactionPersistenceUnavailable
	}
	storedOutputs, err := ledger.Database.GetOutputsByID(ctx, txoids)
	if err != nil {
		return err
	}
	missingByTXID := make(map[string][]unresolvedTransactionInput)
	missingOrder := make([]string, 0)
	for _, wanted := range unresolved {
		if stored, exists := storedOutputs[wanted.txoid]; exists {
			output, err := transactionOutputFromStored(
				stored.TXID, stored.Position, stored.Amount, stored.Script,
			)
			if err != nil {
				return fmt.Errorf("%w: output %s: %v", ErrInvalidStoredTransaction, wanted.txoid, err)
			}
			wanted.input.ResolvedOutput = output
			continue
		}
		if _, exists := missingByTXID[wanted.txid]; !exists {
			missingOrder = append(missingOrder, wanted.txid)
		}
		missingByTXID[wanted.txid] = append(missingByTXID[wanted.txid], wanted)
	}

	for _, txid := range missingOrder {
		wantedInputs := missingByTXID[txid]
		stored, err := ledger.Database.GetTransaction(ctx, txid)
		if err != nil {
			return err
		}
		if stored == nil {
			continue
		}
		previous, err := ParseTransaction(stored.Raw)
		if err != nil {
			return fmt.Errorf("%w %s: %v", ErrInvalidStoredTransaction, txid, err)
		}
		previous.Height = stored.Height
		previous.Position = stored.Position
		previous.IsVerified = stored.IsVerified
		previous.JulianDay = cloneTransactionFloat(stored.Day)
		for _, wanted := range wantedInputs {
			if uint64(wanted.index) >= uint64(len(previous.Outputs)) {
				return transactionOutputRangeError(txid, wanted.index)
			}
			wanted.input.ResolvedOutput = &previous.Outputs[wanted.index]
		}
	}
	return nil
}

func transactionOutputFromStored(
	txid string, position, amount int64, script []byte,
) (*TransactionOutput, error) {
	if position < 0 || uint64(position) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("%w: %s:%d", ErrTransactionOutputOutOfRange, txid, position)
	}
	if amount < 0 {
		return nil, errors.New("stored output amount is negative")
	}
	transactionHash, err := transactionHashFromID(txid)
	if err != nil {
		return nil, err
	}
	return &TransactionOutput{
		TransactionID:   txid,
		TransactionHash: transactionHash,
		Position:        uint32(position),
		Amount:          uint64(amount),
		Script:          ParseTransactionOutputScript(script),
	}, nil
}

func transactionHashFromID(txid string) ([32]byte, error) {
	var hash [32]byte
	display, err := hex.DecodeString(txid)
	if err != nil || len(display) != len(hash) {
		return hash, fmt.Errorf("invalid stored transaction ID %q", txid)
	}
	copy(hash[:], reverseTransactionBytes(display))
	return hash, nil
}

func transactionOutputRangeError(txid string, position uint32) error {
	return fmt.Errorf("%w: %s:%d", ErrTransactionOutputOutOfRange, txid, position)
}

func cloneTransactionFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
