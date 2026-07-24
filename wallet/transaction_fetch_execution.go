package wallet

import (
	"context"
	"errors"
	"fmt"
)

const SPVTransactionMerkleMethod = "blockchain.transaction.get_merkle"

var ErrTransactionFetchUnavailable = errors.New("transaction fetch RPC is unavailable")

// ExecutedTransactionFetchResult keeps the batch response ordering from
// ParseTransactionFetchBatchResult together with the final merkle state.
type ExecutedTransactionFetchResult struct {
	Result       TransactionFetchResult
	Verification TransactionMerkleVerificationStatus
}

// ExecutedTransactionFetchBatch is one yielded request_transactions batch.
// Transactions uses the parsed transaction ID, not the server response key,
// matching the dictionary assembled by Ledger._single_batch.
type ExecutedTransactionFetchBatch struct {
	Batch        TransactionFetchBatch
	Results      []ExecutedTransactionFetchResult
	Transactions map[string]*Transaction
}

// RequestTransactions executes the pure transaction fetch plan against the
// ledger's retriable SPV RPC boundary. Batches retain planner order, while
// results retain the hub response order; the compatibility map is keyed by
// each raw transaction's computed ID.
func (ledger *Ledger) RequestTransactions(
	ctx context.Context, requests []TransactionFetchRequest,
) ([]ExecutedTransactionFetchBatch, error) {
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger is nil", ErrTransactionFetchUnavailable)
	}
	if ledger.Headers == nil {
		return nil, fmt.Errorf("%w: ledger headers are nil", ErrTransactionFetchUnavailable)
	}
	source, ok := ledger.SPVNetwork.(LedgerSPVAddressSource)
	if !ok || source == nil {
		return nil, fmt.Errorf("%w: ledger SPV network does not support retriable values", ErrTransactionFetchUnavailable)
	}
	return ledger.requestTransactions(ctx, source, requests)
}

func (ledger *Ledger) requestTransactions(
	ctx context.Context,
	source LedgerSPVAddressSource,
	requests []TransactionFetchRequest,
) ([]ExecutedTransactionFetchBatch, error) {
	if err := validateTransactionFetchExecution(ledger, source); err != nil {
		return nil, err
	}

	batches := PlanTransactionFetchBatches(
		requests, transactionFetchHighestCheckpointHeight(ledger.Headers),
	)
	executed := make([]ExecutedTransactionFetchBatch, 0, len(batches))
	for _, batch := range batches {
		batchResult, err := ledger.requestTransactionBatch(ctx, source, batch)
		if err != nil {
			return nil, err
		}
		executed = append(executed, batchResult)
	}
	return executed, nil
}

// requestTransactionBatch executes an existing global plan without rebuilding
// its RemoteHeights map. This matters when duplicate IDs cross batch boundaries:
// Python applies the final height observed across the complete request stream.
func (ledger *Ledger) requestTransactionBatch(
	ctx context.Context,
	source LedgerSPVAddressSource,
	batch TransactionFetchBatch,
) (ExecutedTransactionFetchBatch, error) {
	if err := validateTransactionFetchExecution(ledger, source); err != nil {
		return ExecutedTransactionFetchBatch{}, err
	}
	value, err := source.RetriableValue(
		ctx, SPVTransactionBatchMethod, batch.Params, batch.Restricted,
	)
	if err != nil {
		return ExecutedTransactionFetchBatch{}, err
	}
	results, err := ParseTransactionFetchBatchResult(batch, value)
	if err != nil {
		return ExecutedTransactionFetchBatch{}, err
	}

	batchResult := ExecutedTransactionFetchBatch{
		Batch:        batch,
		Results:      make([]ExecutedTransactionFetchResult, 0, len(results)),
		Transactions: make(map[string]*Transaction, len(results)),
	}
	for _, result := range results {
		status, err := ledger.verifyFetchedTransaction(ctx, source, &result)
		if err != nil {
			return ExecutedTransactionFetchBatch{}, err
		}
		batchResult.Results = append(batchResult.Results, ExecutedTransactionFetchResult{
			Result: result, Verification: status,
		})
		batchResult.Transactions[result.Transaction.ID] = result.Transaction
	}
	return batchResult, nil
}

func validateTransactionFetchExecution(
	ledger *Ledger, source LedgerSPVAddressSource,
) error {
	if ledger == nil {
		return fmt.Errorf("%w: ledger is nil", ErrTransactionFetchUnavailable)
	}
	if ledger.Headers == nil {
		return fmt.Errorf("%w: ledger headers are nil", ErrTransactionFetchUnavailable)
	}
	if source == nil {
		return fmt.Errorf("%w: retriable value source is nil", ErrTransactionFetchUnavailable)
	}
	return nil
}

func transactionFetchHighestCheckpointHeight(headers *Headers) int64 {
	if headers == nil {
		return 0
	}
	headers.mu.RLock()
	height := headers.checkpoints.lastHeight()
	headers.mu.RUnlock()
	return int64(max(0, height))
}

func (ledger *Ledger) verifyFetchedTransaction(
	ctx context.Context,
	source LedgerSPVAddressSource,
	result *TransactionFetchResult,
) (TransactionMerkleVerificationStatus, error) {
	if result == nil || result.Transaction == nil {
		return "", malformedTransactionMerkle("transaction", errors.New("transaction is nil"))
	}
	transaction := result.Transaction
	transaction.Height = result.RemoteHeight
	headerCount := ledger.Headers.Len()
	merkle := result.Merkle

	status, err := stageTransactionMerkleVerification(
		transaction, result.RemoteHeight, headerCount, merkle,
	)
	if err != nil {
		return "", err
	}
	if status == TransactionMerkleHeightGated || status == TransactionMerkleProofMissing {
		return status, nil
	}
	if status == TransactionMerkleProofRequired {
		value, err := source.RetriableValue(ctx, SPVTransactionMerkleMethod, []any{
			transaction.ID, result.RemoteHeight,
		}, false)
		if err != nil {
			return "", err
		}
		if value == nil {
			return "", malformedTransactionFetchResult(
				result.Request.TxID, "merkle",
				errors.New("fallback proof is null"),
			)
		}
		fallbackMerkle, ok := transactionFetchMerkleObject(value)
		if !ok {
			return "", malformedTransactionFetchResult(
				result.Request.TxID, "merkle",
				fmt.Errorf("got %T, want object or null", value),
			)
		}
		merkle = fallbackMerkle
		result.Merkle = merkle
		// The fallback RPC has already run. An empty object now follows the
		// Python membership check and is a missing-proof no-op.
		if len(merkle) == 0 {
			return TransactionMerkleProofMissing, nil
		}
		status, err = stageTransactionMerkleVerification(
			transaction, result.RemoteHeight, headerCount, merkle,
		)
		if err != nil {
			return "", err
		}
		if status == TransactionMerkleProofMissing {
			return status, nil
		}
	}

	// A staged application proves that the object has a well-formed merkle
	// branch and position. Only this path needs the corresponding header.
	header, err := ledger.Headers.Get(int(result.RemoteHeight))
	if err != nil {
		return "", err
	}
	return ApplyTransactionMerkleVerification(
		transaction, result.RemoteHeight, headerCount, header.MerkleRoot, merkle,
	)
}

func stageTransactionMerkleVerification(
	transaction *Transaction,
	remoteHeight int64,
	headerCount int,
	merkle map[string]any,
) (TransactionMerkleVerificationStatus, error) {
	staged := *transaction
	return ApplyTransactionMerkleVerification(
		&staged, remoteHeight, headerCount, nil, merkle,
	)
}
