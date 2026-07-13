package wallet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
)

const (
	SPVLiveHeaderBatchSize = 2001
	SPVLiveHeaderMaxRewind = 100
)

var (
	ErrSPVLiveHeaderHexMissing = errors.New("SPV live-header response is missing hex")
	ErrSPVLiveHeaderHexType    = errors.New("SPV live-header hex is not a string")
	ErrSPVLiveHeaderHeight     = errors.New("invalid SPV live-header height")
	ErrSPVLiveHeaderRewind     = errors.New("SPV live-header reorganization failed")
)

const spvGenesisRewindMessage = "Blockchain reorganization rewound all the way back to genesis hash. " +
	"Something is very wrong. Maybe you are on the wrong blockchain?"

type SPVLiveHeaderUpdate struct {
	Height int
	Hex    string
}

type SPVLiveHeaderRewindError struct {
	Message string
}

func (err *SPVLiveHeaderRewindError) Error() string {
	if err == nil {
		return ErrSPVLiveHeaderRewind.Error()
	}
	return err.Message
}

func (*SPVLiveHeaderRewindError) Unwrap() error { return ErrSPVLiveHeaderRewind }

type spvLiveHeaderStore interface {
	Len() int
	Height() int
	ConnectContext(context.Context, int, []byte) (int, error)
}

type spvLiveHeaderHooks struct {
	onAdded    func(height, change int)
	onRewind   func(context.Context, int) error
	onRejected func()
}

func updateSPVLiveHeaders(
	ctx context.Context,
	store spvLiveHeaderStore,
	network LedgerSPVNetwork,
	ledgerID string,
	update *SPVLiveHeaderUpdate,
	hooks spvLiveHeaderHooks,
) error {
	if ctx == nil {
		return errors.New("SPV live-header context is nil")
	}
	if store == nil {
		return errors.New("SPV live-header store is unavailable")
	}
	if network == nil {
		return ErrLedgerSPVUnavailable
	}
	height := 0
	encoded := ""
	subscriptionUpdate := update != nil
	if update != nil {
		height = update.Height
		encoded = update.Hex
	}
	rewound := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if update == nil || height > store.Len() {
			height = store.Len()
			encoded = ""
			subscriptionUpdate = false
			update = &SPVLiveHeaderUpdate{}
		}
		if encoded == "" {
			result, err := network.RetriableCall(ctx, SPVHeaderRPCMethod, []any{
				height, SPVLiveHeaderBatchSize, 0, false,
			}, height >= network.RemoteHeight()-SPVHeaderRPCRestrictionDistance)
			if err != nil {
				return err
			}
			encoded, err = spvLiveHeaderHex(result)
			if err != nil {
				return err
			}
		}
		if encoded == "" {
			return nil
		}
		raw, err := hex.DecodeString(encoded)
		if err != nil {
			return err
		}
		added, err := store.ConnectContext(ctx, height, raw)
		if err != nil {
			return err
		}
		switch {
		case added > 0:
			height += added
			if hooks.onAdded != nil {
				hooks.onAdded(store.Height(), added)
			}
			if rewound > 0 {
				rewound = 0
				if hooks.onRewind != nil {
					if err := hooks.onRewind(ctx, height); err != nil {
						return err
					}
				}
			}
			if subscriptionUpdate {
				return nil
			}
		case added == 0:
			height--
			rewound++
			if hooks.onRejected != nil {
				hooks.onRejected()
			}
		default:
			return fmt.Errorf("headers.connect() returned negative number (%d)", added)
		}
		if height < 0 {
			return &SPVLiveHeaderRewindError{Message: spvGenesisRewindMessage}
		}
		if rewound >= SPVLiveHeaderMaxRewind {
			return &SPVLiveHeaderRewindError{Message: fmt.Sprintf(
				"Blockchain reorganization dropped %d headers. This is highly unusual. "+
					"Will not continue to attempt reorganizing. Please, delete the ledger "+
					"synchronization directory inside your wallet directory (folder: '%s') and "+
					"restart the program to synchronize from scratch.",
				rewound, ledgerID,
			)}
		}
		encoded = ""
		subscriptionUpdate = false
	}
}

func spvLiveHeaderHex(result map[string]any) (string, error) {
	value, exists := result["hex"]
	if !exists {
		return "", ErrSPVLiveHeaderHexMissing
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	if pythonFalseySPVValue(value) {
		return "", nil
	}
	return "", fmt.Errorf("%w: got %T", ErrSPVLiveHeaderHexType, value)
}

func parseSPVLiveHeaderUpdate(params any) (SPVLiveHeaderUpdate, error) {
	values, ok := params.([]any)
	if !ok || len(values) == 0 {
		return SPVLiveHeaderUpdate{}, fmt.Errorf("%w: notification has type %T", ErrSPVLiveHeaderHeight, params)
	}
	header, ok := values[0].(map[string]any)
	if !ok {
		return SPVLiveHeaderUpdate{}, fmt.Errorf("%w: header has type %T", ErrSPVLiveHeaderHeight, values[0])
	}
	height, err := spvLiveHeaderInteger(header["height"])
	if err != nil {
		return SPVLiveHeaderUpdate{}, err
	}
	encodedValue, exists := header["hex"]
	if !exists {
		return SPVLiveHeaderUpdate{}, ErrSPVLiveHeaderHexMissing
	}
	encoded, ok := encodedValue.(string)
	if !ok {
		if pythonFalseySPVValue(encodedValue) {
			return SPVLiveHeaderUpdate{Height: height}, nil
		}
		return SPVLiveHeaderUpdate{}, fmt.Errorf("%w: got %T", ErrSPVLiveHeaderHexType, encodedValue)
	}
	return SPVLiveHeaderUpdate{Height: height, Hex: encoded}, nil
}

func spvLiveHeaderInteger(value any) (int, error) {
	var integer int64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrSPVLiveHeaderHeight, typed)
		}
		integer = parsed
	case int:
		return typed, nil
	case int8:
		integer = int64(typed)
	case int16:
		integer = int64(typed)
	case int32:
		integer = int64(typed)
	case int64:
		integer = typed
	default:
		return 0, fmt.Errorf("%w: got %T", ErrSPVLiveHeaderHeight, value)
	}
	converted := int(integer)
	if int64(converted) != integer {
		return 0, fmt.Errorf("%w: %d is outside the Go integer range", ErrSPVLiveHeaderHeight, integer)
	}
	return converted, nil
}

func pythonFalseySPVValue(value any) bool {
	if value == nil {
		return true
	}
	if number, ok := value.(json.Number); ok {
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return err == nil && parsed == 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return !reflected.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflected.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() == 0 && !math.IsNaN(reflected.Float())
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return reflected.Len() == 0
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		return reflected.IsNil()
	default:
		return false
	}
}
