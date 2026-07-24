package wallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"lbry/daemon/wallet/ledgerdb"
)

const SPVTransactionInfoMethod = "blockchain.transaction.info"

var (
	ErrTransactionLookupUnavailable = errors.New("transaction lookup is unavailable")
	ErrTransactionInfoResult        = errors.New("invalid transaction info result")
)

type TransactionLookupFailure struct {
	Success bool   `json:"success"`
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type TransactionLookupResult struct {
	Transaction *Transaction
	Failure     *TransactionLookupFailure
	Ledger      *Ledger
}

type TransactionLookupArgumentError struct {
	Name    string
	Message string
}

func (err TransactionLookupArgumentError) Error() string           { return err.Message }
func (err TransactionLookupArgumentError) PythonErrorName() string { return err.Name }

type TransactionInfoCompatibilityError struct {
	Name    string
	Message string
}

func (err TransactionInfoCompatibilityError) Error() string           { return err.Message }
func (err TransactionInfoCompatibilityError) PythonErrorName() string { return err.Name }
func (err TransactionInfoCompatibilityError) Unwrap() error           { return ErrTransactionInfoResult }

type transactionLookupCodeMessageError interface {
	error
	RPCCode() int64
	RPCMessage() string
}

type transactionLookupOneShotSource interface {
	OneShotValue(context.Context, string, []any, bool) (any, error)
}

// GetTransaction mirrors WalletManager.get_transaction: the default ledger's
// hydrated SQLite row wins, while a miss performs one transaction.info lookup
// without caching or persisting the remote transaction.
func (manager *WalletManager) GetTransaction(
	ctx context.Context, txid any,
) (TransactionLookupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ledger := manager.DefaultLedger()
	if ledger == nil || ledger.Database == nil {
		return TransactionLookupResult{}, fmt.Errorf(
			"%w: default ledger database is unavailable", ErrTransactionLookupUnavailable,
		)
	}
	queryTXID, err := transactionLookupSQLiteValue(txid)
	if err != nil {
		return TransactionLookupResult{}, err
	}
	limit := 1
	transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
		Query: ledgerdb.TransactionQuery{
			TXIDValue: queryTXID, HasTXIDValue: true, Limit: &limit,
		},
	})
	if err != nil {
		return TransactionLookupResult{}, err
	}
	if len(transactions) > 0 {
		return TransactionLookupResult{Transaction: transactions[0], Ledger: ledger}, nil
	}

	ledger.spvSync.mu.Lock()
	network := ledger.SPVNetwork
	ledger.spvSync.mu.Unlock()
	source, ok := network.(LedgerSPVAddressSource)
	if !ok || source == nil {
		return TransactionLookupResult{}, fmt.Errorf(
			"%w: ledger SPV network does not support transaction info", ErrTransactionLookupUnavailable,
		)
	}
	oneShot, ok := network.(transactionLookupOneShotSource)
	if !ok || oneShot == nil {
		return TransactionLookupResult{}, fmt.Errorf(
			"%w: ledger SPV network does not support one-shot transaction info", ErrTransactionLookupUnavailable,
		)
	}
	value, err := oneShot.OneShotValue(
		ctx, SPVTransactionInfoMethod, []any{txid}, true,
	)
	if err != nil {
		var codeMessage transactionLookupCodeMessageError
		if errors.As(err, &codeMessage) {
			message := codeMessage.RPCMessage()
			code := codeMessage.RPCCode()
			if strings.Contains(message, "No such mempool or blockchain transaction.") {
				code = 404
				message = "transaction not found"
			}
			return TransactionLookupResult{
				Failure: &TransactionLookupFailure{Success: false, Code: code, Message: message},
				Ledger:  ledger,
			}, nil
		}
		return TransactionLookupResult{}, err
	}

	rawValue, merkle, err := parseTransactionInfoResult(value)
	if err != nil {
		return TransactionLookupResult{}, err
	}
	heightValue, heightExists := merkle["block_height"]
	rawHex, err := transactionInfoRawHex(rawValue)
	if err != nil {
		return TransactionLookupResult{}, err
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return TransactionLookupResult{}, transactionInfoCompatibilityError(
			"Error", "Non-hexadecimal digit found",
		)
	}
	if len(raw) == 0 {
		return TransactionLookupResult{}, transactionInfoCompatibilityError(
			"TypeError", "'<' not supported between instances of 'NoneType' and 'int'",
		)
	}
	if len(raw) < 4 {
		return TransactionLookupResult{}, transactionInfoCompatibilityError(
			"error", "unpack requires a buffer of 4 bytes",
		)
	}
	transaction, err := ParseTransaction(raw)
	if err != nil {
		return TransactionLookupResult{}, fmt.Errorf("%w: raw: %v", ErrTransactionInfoResult, err)
	}
	height, heightPresent, err := transactionInfoHeight(heightValue, heightExists)
	if err != nil {
		return TransactionLookupResult{}, err
	}
	transaction.Height = height
	transaction.HeightMissing = !heightPresent
	if height > 0 {
		result := TransactionFetchResult{
			Request:      TransactionFetchRequest{TxID: transaction.ID, Height: height},
			RemoteHeight: height, Transaction: transaction, Merkle: merkle,
		}
		if _, err := ledger.verifyFetchedTransaction(ctx, source, &result); err != nil {
			return TransactionLookupResult{}, err
		}
	}
	return TransactionLookupResult{Transaction: transaction, Ledger: ledger}, nil
}

func parseTransactionInfoResult(value any) (any, map[string]any, error) {
	items, err := transactionInfoUnpack(value)
	if err != nil {
		return nil, nil, err
	}
	merkle, ok := items[1].(map[string]any)
	if !ok {
		return nil, nil, transactionInfoCompatibilityError(
			"AttributeError",
			fmt.Sprintf("'%s' object has no attribute 'get'", transactionInfoPythonType(items[1])),
		)
	}
	return items[0], merkle, nil
}

func transactionInfoUnpack(value any) ([]any, error) {
	switch typed := value.(type) {
	case []any:
		if err := transactionInfoUnpackLength(len(typed)); err != nil {
			return nil, err
		}
		return typed, nil
	case map[string]any:
		if err := transactionInfoUnpackLength(len(typed)); err != nil {
			return nil, err
		}
		// JSON object order is not retained by Go, but both unpacked values are
		// strings and the second one deterministically fails merkle.get.
		return []any{"", ""}, nil
	case string:
		length := len([]rune(typed))
		if err := transactionInfoUnpackLength(length); err != nil {
			return nil, err
		}
		values := []rune(typed)
		return []any{string(values[0]), string(values[1])}, nil
	default:
		return nil, transactionInfoCompatibilityError(
			"TypeError",
			fmt.Sprintf("cannot unpack non-iterable %s object", transactionInfoPythonType(value)),
		)
	}
}

func transactionInfoUnpackLength(length int) error {
	switch {
	case length < 2:
		return transactionInfoCompatibilityError(
			"ValueError",
			fmt.Sprintf("not enough values to unpack (expected 2, got %d)", length),
		)
	case length > 2:
		return transactionInfoCompatibilityError(
			"ValueError", "too many values to unpack (expected 2)",
		)
	default:
		return nil
	}
}

func transactionInfoRawHex(value any) (string, error) {
	if rawHex, ok := value.(string); ok {
		if len(rawHex)%2 != 0 {
			return "", transactionInfoCompatibilityError("Error", "Odd-length string")
		}
		return rawHex, nil
	}
	return "", transactionInfoCompatibilityError(
		"TypeError",
		fmt.Sprintf(
			"argument should be bytes, buffer or ASCII string, not '%s'",
			transactionInfoPythonType(value),
		),
	)
}

func transactionInfoPythonType(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			return "float"
		}
		return "int"
	case float32, float64:
		return "float"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func transactionInfoCompatibilityError(name, message string) error {
	return TransactionInfoCompatibilityError{Name: name, Message: message}
}

func transactionInfoHeight(value any, exists bool) (int64, bool, error) {
	if !exists || value == nil {
		return 0, false, nil
	}
	height, err := transactionMerkleInteger(value)
	if err != nil {
		return 0, false, fmt.Errorf("%w: block_height: %v", ErrTransactionInfoResult, err)
	}
	return height, true, nil
}

func transactionLookupSQLiteValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool, int64, float64:
		return typed, nil
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			floating, err := strconv.ParseFloat(typed.String(), 64)
			if err != nil {
				return nil, TransactionLookupArgumentError{
					Name: "ValueError", Message: "invalid transaction id number",
				}
			}
			return floating, nil
		}
		integer, ok := new(big.Int).SetString(typed.String(), 10)
		if !ok {
			return nil, TransactionLookupArgumentError{
				Name: "ValueError", Message: "invalid transaction id number",
			}
		}
		if !integer.IsInt64() {
			return nil, TransactionLookupArgumentError{
				Name: "OverflowError", Message: "Python int too large to convert to SQLite INTEGER",
			}
		}
		return integer.Int64(), nil
	case []any:
		return nil, transactionLookupBindingError("list")
	case map[string]any:
		return nil, transactionLookupBindingError("dict")
	default:
		return value, nil
	}
}

func transactionLookupBindingError(pythonType string) error {
	return TransactionLookupArgumentError{
		Name: "ProgrammingError",
		Message: fmt.Sprintf(
			"Error binding parameter 1: type '%s' is not supported", pythonType,
		),
	}
}
