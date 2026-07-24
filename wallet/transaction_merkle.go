package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
)

var (
	ErrTransactionMerkle          = errors.New("transaction merkle verification failed")
	ErrMalformedTransactionMerkle = errors.New("transaction merkle proof is malformed")
)

type TransactionMerkleError struct {
	Field string
	Cause error
}

func (merkleErr *TransactionMerkleError) Error() string {
	if merkleErr == nil {
		return ErrTransactionMerkle.Error()
	}
	message := ErrTransactionMerkle.Error()
	if merkleErr.Field != "" {
		message += ": " + merkleErr.Field
	}
	if merkleErr.Cause != nil {
		message += ": " + merkleErr.Cause.Error()
	}
	return message
}

func (merkleErr *TransactionMerkleError) Unwrap() error {
	if merkleErr == nil {
		return nil
	}
	return merkleErr.Cause
}

func (merkleErr *TransactionMerkleError) Is(target error) bool {
	return merkleErr != nil && (target == ErrTransactionMerkle || target == ErrMalformedTransactionMerkle)
}

type TransactionMerkleVerificationStatus string

const (
	TransactionMerkleHeightGated   TransactionMerkleVerificationStatus = "height_gated"
	TransactionMerkleProofRequired TransactionMerkleVerificationStatus = "proof_required"
	TransactionMerkleProofMissing  TransactionMerkleVerificationStatus = "proof_missing"
	TransactionMerkleMatched       TransactionMerkleVerificationStatus = "matched"
	TransactionMerkleMismatched    TransactionMerkleVerificationStatus = "mismatched"
)

// TransactionMerkleRoot mirrors Ledger.get_root_of_merkle_tree. Branches are
// display-order hexadecimal hashes; the working branch and intermediate
// hashes remain in internal byte order until the final display conversion.
func TransactionMerkleRoot(
	branches []string, branchPosition int64, workingBranch [sha256.Size]byte,
) (string, error) {
	working := append([]byte(nil), workingBranch[:]...)
	for index, branch := range branches {
		other, err := hex.DecodeString(branch)
		if err != nil {
			return "", malformedTransactionMerkle(fmt.Sprintf("merkle[%d]", index), err)
		}
		other = reverseTransactionBytes(other)
		combined := make([]byte, 0, len(working)+len(other))
		if ((branchPosition >> index) & 1) != 0 {
			combined = append(combined, other...)
			combined = append(combined, working...)
		} else {
			combined = append(combined, working...)
			combined = append(combined, other...)
		}
		first := sha256.Sum256(combined)
		second := sha256.Sum256(first[:])
		working = append(working[:0], second[:]...)
	}
	return hex.EncodeToString(reverseTransactionBytes(working)), nil
}

// ApplyTransactionMerkleVerification applies the state transitions from
// Ledger.maybe_verify_transaction without performing its fallback RPC call.
// ProofRequired tells the caller to obtain that proof. A truthy object without
// a "merkle" member is a no-op, as in the pinned Python implementation.
func ApplyTransactionMerkleVerification(
	transaction *Transaction,
	remoteHeight int64,
	headerCount int,
	headerMerkleRoot []byte,
	merkle map[string]any,
) (TransactionMerkleVerificationStatus, error) {
	if transaction == nil {
		return "", malformedTransactionMerkle("transaction", errors.New("transaction is nil"))
	}
	transaction.Height = remoteHeight
	if remoteHeight <= 0 || remoteHeight >= int64(headerCount) {
		return TransactionMerkleHeightGated, nil
	}
	if len(merkle) == 0 {
		return TransactionMerkleProofRequired, nil
	}
	branchesValue, exists := merkle["merkle"]
	if !exists {
		return TransactionMerkleProofMissing, nil
	}
	branches, err := transactionMerkleBranches(branchesValue)
	if err != nil {
		return "", err
	}
	positionValue, exists := merkle["pos"]
	if !exists {
		return "", malformedTransactionMerkle("pos", errors.New("member is missing"))
	}
	position, err := transactionMerkleInteger(positionValue)
	if err != nil {
		return "", malformedTransactionMerkle("pos", err)
	}
	root, err := TransactionMerkleRoot(branches, position, transaction.Hash)
	if err != nil {
		return "", err
	}
	transaction.Position = position
	transaction.IsVerified = root == string(headerMerkleRoot)
	if transaction.IsVerified {
		return TransactionMerkleMatched, nil
	}
	return TransactionMerkleMismatched, nil
}

func transactionMerkleBranches(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		branches := make([]string, len(typed))
		for index, value := range typed {
			branch, ok := value.(string)
			if !ok {
				return nil, malformedTransactionMerkle(
					fmt.Sprintf("merkle[%d]", index), fmt.Errorf("got %T, want hexadecimal string", value),
				)
			}
			branches[index] = branch
		}
		return branches, nil
	default:
		return nil, malformedTransactionMerkle("merkle", fmt.Errorf("got %T, want array", value))
	}
}

func transactionMerkleInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%q is not an int64", typed)
		}
		return integer, nil
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, fmt.Errorf("%d exceeds int64", typed)
		}
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, fmt.Errorf("%d exceeds int64", typed)
		}
		return int64(typed), nil
	default:
		if value == nil {
			return 0, errors.New("value is nil")
		}
		return 0, fmt.Errorf("got %s, want integer", reflect.TypeOf(value))
	}
}

func malformedTransactionMerkle(field string, cause error) error {
	return &TransactionMerkleError{Field: field, Cause: cause}
}
