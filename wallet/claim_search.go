package wallet

import (
	"context"
	"errors"
	"fmt"
	"reflect"
)

const SPVClaimSearchMethod = "blockchain.claimtrie.search"

var (
	ErrClaimSearchUnavailable = errors.New("claim search is unavailable")
	ErrClaimSearchResult      = errors.New("invalid claim search result")
)

// LedgerSPVNamedValueSource is the one-shot named-parameter boundary used by
// claim_search. Initial claim queries are not replayed on a replacement SPV
// session.
type LedgerSPVNamedValueSource interface {
	OneShotNamedValue(context.Context, string, map[string]any, bool) (any, error)
}

// ClaimSearchResultError preserves the Python TypeError raised when the hub
// returns a truthy value that base64.b64decode cannot accept.
type ClaimSearchResultError struct {
	Name    string
	Message string
}

func (err *ClaimSearchResultError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *ClaimSearchResultError) PythonErrorName() string {
	if err == nil || err.Name == "" {
		return "TypeError"
	}
	return err.Name
}

func (err *ClaimSearchResultError) Unwrap() error { return ErrClaimSearchResult }

// QueryClaimSearch performs the configured one-shot hub query and decodes its
// result.proto page. Python uses an empty string when encoded_outputs is falsey
// before requiring a string result.
func (ledger *Ledger) QueryClaimSearch(
	ctx context.Context, params map[string]any,
) (*HubOutputs, error) {
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger is nil", ErrClaimSearchUnavailable)
	}
	ledger.spvSync.mu.Lock()
	network := ledger.SPVNetwork
	ledger.spvSync.mu.Unlock()
	source, ok := network.(LedgerSPVNamedValueSource)
	if !ok || isNilLedgerSPVNamedValueSource(source) {
		return nil, fmt.Errorf(
			"%w: ledger SPV network does not support one-shot named values",
			ErrClaimSearchUnavailable,
		)
	}

	value, err := source.OneShotNamedValue(ctx, SPVClaimSearchMethod, params, false)
	if err != nil {
		return nil, err
	}
	encoded := ""
	if pythonJSONTruthy(value) {
		var valid bool
		encoded, valid = value.(string)
		if !valid {
			return nil, &ClaimSearchResultError{
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

func isNilLedgerSPVNamedValueSource(source LedgerSPVNamedValueSource) bool {
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
