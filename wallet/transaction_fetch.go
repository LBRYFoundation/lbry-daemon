package wallet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const (
	SPVTransactionBatchMethod = "blockchain.transaction.get_batch"
	TransactionFetchBatchSize = 100
)

var (
	ErrTransactionFetchResult          = errors.New("invalid transaction fetch result")
	ErrTransactionFetchResultExtra     = errors.New("transaction fetch result contains an unrequested transaction")
	ErrTransactionFetchResultMalformed = errors.New("transaction fetch result is malformed")
)

type TransactionFetchResultErrorKind string

const (
	TransactionFetchResultExtra     TransactionFetchResultErrorKind = "extra"
	TransactionFetchResultMalformed TransactionFetchResultErrorKind = "malformed"
)

// TransactionFetchResultError keeps extra and malformed server responses
// distinguishable without coupling the pure decoder to an RPC type.
type TransactionFetchResultError struct {
	Kind  TransactionFetchResultErrorKind
	TxID  string
	Field string
	Cause error
}

func (fetchErr *TransactionFetchResultError) Error() string {
	if fetchErr == nil {
		return ErrTransactionFetchResult.Error()
	}
	detail := string(fetchErr.Kind)
	if fetchErr.TxID != "" {
		detail += " transaction " + fetchErr.TxID
	}
	if fetchErr.Field != "" {
		detail += " field " + fetchErr.Field
	}
	if fetchErr.Cause != nil {
		detail += ": " + fetchErr.Cause.Error()
	}
	return ErrTransactionFetchResult.Error() + ": " + detail
}

func (fetchErr *TransactionFetchResultError) Unwrap() error {
	if fetchErr == nil {
		return nil
	}
	return fetchErr.Cause
}

func (fetchErr *TransactionFetchResultError) Is(target error) bool {
	if fetchErr == nil {
		return false
	}
	if target == ErrTransactionFetchResult {
		return true
	}
	switch fetchErr.Kind {
	case TransactionFetchResultExtra:
		return target == ErrTransactionFetchResultExtra
	case TransactionFetchResultMalformed:
		return target == ErrTransactionFetchResultMalformed
	default:
		return false
	}
}

type TransactionFetchRequest struct {
	TxID   string
	Height int64
}

// TransactionFetchBatch contains the exact flat parameter list accepted by
// Network.get_transaction_batch in SDK 0.113.0.
type TransactionFetchBatch struct {
	Requests      []TransactionFetchRequest
	Params        []any
	RemoteHeights map[string]int64
	Restricted    bool
}

type TransactionFetchResult struct {
	Request      TransactionFetchRequest
	RemoteHeight int64
	Transaction  *Transaction
	Merkle       map[string]any
}

// PlanTransactionFetchBatches mirrors Ledger.request_transactions and
// Ledger._single_batch: a stable height sort, batches of 100, and unrestricted
// selection only for batches wholly below the highest checkpoint with at least
// two distinct positive heights.
func PlanTransactionFetchBatches(
	requests []TransactionFetchRequest, highestCheckpointHeight int64,
) []TransactionFetchBatch {
	ordered := append([]TransactionFetchRequest(nil), requests...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].Height < ordered[right].Height
	})
	remoteHeights := make(map[string]int64, len(ordered))
	for _, request := range ordered {
		remoteHeights[request.TxID] = request.Height
	}

	batches := make([]TransactionFetchBatch, 0, (len(ordered)+TransactionFetchBatchSize-1)/TransactionFetchBatchSize)
	for start := 0; start < len(ordered); start += TransactionFetchBatchSize {
		end := min(start+TransactionFetchBatchSize, len(ordered))
		batchRequests := append([]TransactionFetchRequest(nil), ordered[start:end]...)
		params := make([]any, len(batchRequests))
		batchHeights := make(map[string]int64, len(batchRequests))
		minimumHeight := remoteHeights[batchRequests[0].TxID]
		maximumHeight := minimumHeight
		for index, request := range batchRequests {
			params[index] = request.TxID
			height := remoteHeights[request.TxID]
			batchHeights[request.TxID] = height
			minimumHeight = min(minimumHeight, height)
			maximumHeight = max(maximumHeight, height)
		}
		unrestricted := minimumHeight > 0 && minimumHeight < maximumHeight &&
			maximumHeight < highestCheckpointHeight
		batches = append(batches, TransactionFetchBatch{
			Requests: batchRequests, Params: params,
			RemoteHeights: batchHeights, Restricted: !unrestricted,
		})
	}
	return batches
}

// ParseTransactionFetchBatchResult rejects unrequested keys and preserves an
// ordered response's JSON member order. Missing requested keys are skipped:
// the pinned _single_batch returns the partial mapping and address history sync
// detects the missing transaction only after saving the returned batch. The raw
// transaction ID is deliberately not compared with the response key, matching
// _single_batch, which indexes the parsed transaction by tx.id.
func ParseTransactionFetchBatchResult(
	batch TransactionFetchBatch, value any,
) ([]TransactionFetchResult, error) {
	response, keys, ordered, ok := transactionFetchMapping(value)
	if !ok {
		return nil, malformedTransactionFetchResult("", "result", fmt.Errorf("got %T, want object", value))
	}

	requested := make(map[string]struct{}, len(batch.Requests))
	for _, request := range batch.Requests {
		requested[request.TxID] = struct{}{}
	}
	if !ordered {
		keys = unorderedTransactionFetchKeys(batch, response)
	}
	extra := make([]string, 0)
	for _, txid := range keys {
		if _, exists := requested[txid]; !exists {
			extra = append(extra, txid)
		}
	}
	if len(extra) > 0 {
		if !ordered {
			sort.Strings(extra)
		}
		return nil, &TransactionFetchResultError{
			Kind: TransactionFetchResultExtra, TxID: extra[0],
		}
	}

	results := make([]TransactionFetchResult, 0, len(keys))
	for _, txid := range keys {
		responseEntry := response[txid]
		entry, ok := responseEntry.([]any)
		if !ok || len(entry) != 2 {
			return nil, malformedTransactionFetchResult(
				txid, "entry", fmt.Errorf("got %T with length %d, want two-item array", responseEntry, transactionFetchLength(responseEntry)),
			)
		}
		rawHex, ok := entry[0].(string)
		if !ok {
			return nil, malformedTransactionFetchResult(
				txid, "raw", fmt.Errorf("got %T, want hexadecimal string", entry[0]),
			)
		}
		raw, err := hex.DecodeString(rawHex)
		if err != nil {
			return nil, malformedTransactionFetchResult(txid, "raw", err)
		}
		transaction, err := ParseTransaction(raw)
		if err != nil {
			return nil, malformedTransactionFetchResult(txid, "raw", err)
		}
		remoteHeight := int64(0)
		if batch.RemoteHeights != nil {
			if height, exists := batch.RemoteHeights[txid]; exists {
				remoteHeight = height
			}
		} else {
			for _, request := range batch.Requests {
				if request.TxID == txid {
					remoteHeight = request.Height
					break
				}
			}
		}
		transaction.Height = remoteHeight
		merkle, ok := transactionFetchMerkleObject(entry[1])
		if !ok {
			return nil, malformedTransactionFetchResult(
				txid, "merkle", fmt.Errorf("got %T, want object or null", entry[1]),
			)
		}
		results = append(results, TransactionFetchResult{
			Request: TransactionFetchRequest{TxID: txid, Height: remoteHeight}, RemoteHeight: remoteHeight,
			Transaction: transaction, Merkle: merkle,
		})
	}
	return results, nil
}

func malformedTransactionFetchResult(txid, field string, cause error) error {
	return &TransactionFetchResultError{
		Kind: TransactionFetchResultMalformed, TxID: txid, Field: field, Cause: cause,
	}
}

type transactionFetchOrderedObject interface {
	Keys() []string
	Get(string) (any, bool)
}

func transactionFetchMapping(value any) (map[string]any, []string, bool, bool) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		return typed, keys, false, true
	case transactionFetchOrderedObject:
		mapping := make(map[string]any)
		keys := typed.Keys()
		for _, key := range keys {
			mapping[key], _ = typed.Get(key)
		}
		return mapping, keys, true, true
	default:
		return nil, nil, false, false
	}
}

func unorderedTransactionFetchKeys(
	batch TransactionFetchBatch, response map[string]any,
) []string {
	keys := make([]string, 0, len(response))
	seen := make(map[string]struct{}, len(response))
	for _, request := range batch.Requests {
		if _, exists := response[request.TxID]; !exists {
			continue
		}
		if _, exists := seen[request.TxID]; exists {
			continue
		}
		seen[request.TxID] = struct{}{}
		keys = append(keys, request.TxID)
	}
	extra := make([]string, 0, len(response)-len(keys))
	for key := range response {
		if _, exists := seen[key]; !exists {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

func transactionFetchMerkleObject(value any) (map[string]any, bool) {
	if value == nil {
		return nil, true
	}
	mapping, _, _, ok := transactionFetchMapping(value)
	if !ok {
		return nil, false
	}
	copyOfMapping := make(map[string]any, len(mapping))
	for key, member := range mapping {
		copyOfMapping[key] = member
	}
	return copyOfMapping, true
}

func transactionFetchLength(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	default:
		return -1
	}
}
