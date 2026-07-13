package wallet

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

const SPVResolveMethod = "blockchain.claimtrie.resolve"

var (
	ErrResolveUnavailable = errors.New("resolve is unavailable")
	ErrResolveResult      = errors.New("invalid resolve result")
)

// LedgerSPVRetriableValueSource is the positional retriable-call boundary used
// by resolve. Retrying a timed-out or disconnected request remains the SPV
// network's responsibility.
type LedgerSPVRetriableValueSource interface {
	RetriableValue(context.Context, string, []any, bool) (any, error)
}

// ResolveResultError preserves the Python TypeError raised when a truthy hub
// result cannot be passed to base64.b64decode.
type ResolveResultError struct {
	Name    string
	Message string
}

func (err *ResolveResultError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *ResolveResultError) PythonErrorName() string {
	if err == nil || err.Name == "" {
		return "TypeError"
	}
	return err.Name
}

func (err *ResolveResultError) Unwrap() error { return ErrResolveResult }

// QueryResolveBatch performs one configured Network.resolve call. Callers own
// the legacy Ledger.resolve orchestration that splits URL lists into batches of
// 100 before invoking this boundary.
func (ledger *Ledger) QueryResolveBatch(
	ctx context.Context, urls []string,
) (*HubOutputs, error) {
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger is nil", ErrResolveUnavailable)
	}
	ledger.spvSync.mu.Lock()
	network := ledger.SPVNetwork
	ledger.spvSync.mu.Unlock()
	source, ok := network.(LedgerSPVRetriableValueSource)
	if !ok || isNilLedgerSPVRetriableValueSource(source) {
		return nil, fmt.Errorf(
			"%w: ledger SPV network does not support retriable values",
			ErrResolveUnavailable,
		)
	}

	params := make([]any, len(urls))
	for index, url := range urls {
		params[index] = url
	}
	value, err := source.RetriableValue(ctx, SPVResolveMethod, params, false)
	if err != nil {
		return nil, err
	}
	encoded := ""
	if pythonJSONTruthy(value) {
		var valid bool
		encoded, valid = value.(string)
		if !valid {
			return nil, &ResolveResultError{
				Name: "TypeError",
				Message: fmt.Sprintf(
					"argument should be a bytes-like object or ASCII string, not '%s'",
					transactionInfoPythonType(value),
				),
			}
		}
	}
	return DecodeHubOutputsBase64(encoded)
}

func isNilLedgerSPVRetriableValueSource(source LedgerSPVRetriableValueSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
