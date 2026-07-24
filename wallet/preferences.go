package wallet

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type PreferenceOption func(*TimestampedPreferences)

func WithPreferenceClock(clock func() float64) PreferenceOption {
	return func(preferences *TimestampedPreferences) {
		if clock != nil {
			preferences.now = clock
		}
	}
}

type TimestampedPreferences struct {
	data *Object
	now  func() float64
}

func NewTimestampedPreferences(data *Object, options ...PreferenceOption) *TimestampedPreferences {
	preferences := &TimestampedPreferences{
		data: NewObject(),
		now: func() float64 {
			return float64(time.Now().UnixNano()) / float64(time.Second)
		},
	}
	if data != nil {
		preferences.data = data.ShallowCopy()
	}
	for _, option := range options {
		option(preferences)
	}
	return preferences
}

func (preferences *TimestampedPreferences) Data() *Object {
	return preferences.data
}

func (preferences *TimestampedPreferences) Get(key string) (any, bool, error) {
	entryValue, exists := preferences.data.Get(key)
	if !exists {
		return nil, false, nil
	}
	entry, ok := entryValue.(*Object)
	if !ok {
		return nil, true, fmt.Errorf("preference %q entry has type %T, want object", key, entryValue)
	}
	value, exists := entry.Get("value")
	if !exists {
		return nil, true, fmt.Errorf("preference %q has no value", key)
	}
	return value, true, nil
}

func (preferences *TimestampedPreferences) GetOr(key string, fallback any) (any, error) {
	value, exists, err := preferences.Get(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return fallback, nil
	}
	return value, nil
}

func (preferences *TimestampedPreferences) Set(key string, value any) {
	preferences.SetAt(key, value, int64(preferences.now()))
}

func (preferences *TimestampedPreferences) SetAt(key string, value any, timestamp int64) {
	preferences.data.Set(key, NewObject(
		Member{Key: "value", Value: value},
		Member{Key: "ts", Value: timestamp},
	))
}

func (preferences *TimestampedPreferences) Merge(other *Object) error {
	if other == nil {
		return errors.New("cannot merge nil preferences")
	}
	for _, member := range other.Members() {
		if localValue, exists := preferences.data.Get(member.Key); exists {
			incoming, ok := member.Value.(*Object)
			if !ok {
				return fmt.Errorf("preference %q entry has type %T, want object", member.Key, member.Value)
			}
			incomingTimestamp, exists := incoming.Get("ts")
			if !exists {
				return fmt.Errorf("preference %q has no timestamp", member.Key)
			}
			local, ok := localValue.(*Object)
			if !ok {
				return fmt.Errorf("preference %q local entry has type %T, want object", member.Key, localValue)
			}
			localTimestamp, exists := local.Get("ts")
			if !exists {
				return fmt.Errorf("preference %q local entry has no timestamp", member.Key)
			}
			older, err := pythonNumberLess(incomingTimestamp, localTimestamp)
			if err != nil {
				return fmt.Errorf("compare preference %q timestamps: %w", member.Key, err)
			}
			if older {
				continue
			}
		}
		preferences.data.Set(member.Key, member.Value)
	}
	return nil
}

func (preferences *TimestampedPreferences) WithoutTimestamps() (*Object, error) {
	result := NewObject()
	for _, member := range preferences.data.Members() {
		entry, ok := member.Value.(*Object)
		if !ok {
			return nil, fmt.Errorf("preference %q entry has type %T, want object", member.Key, member.Value)
		}
		value, exists := entry.Get("value")
		if !exists {
			return nil, fmt.Errorf("preference %q has no value", member.Key)
		}
		result.Set(member.Key, value)
	}
	return result, nil
}

func (preferences *TimestampedPreferences) HashJSON() ([]byte, error) {
	return encodePreferenceJSON(preferences.data)
}

func (preferences *TimestampedPreferences) Hash() ([sha256.Size]byte, error) {
	encoded, err := preferences.HashJSON()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func pythonNumberLess(left, right any) (bool, error) {
	leftNumber, err := pythonComparableNumber(left)
	if err != nil {
		return false, err
	}
	rightNumber, err := pythonComparableNumber(right)
	if err != nil {
		return false, err
	}
	if leftNumber.nan || rightNumber.nan {
		return false, nil
	}
	if leftNumber.infinity != 0 || rightNumber.infinity != 0 {
		return leftNumber.infinity < rightNumber.infinity, nil
	}
	return leftNumber.rational.Cmp(rightNumber.rational) < 0, nil
}

type comparableNumber struct {
	rational *big.Rat
	infinity int
	nan      bool
}

func pythonComparableNumber(value any) (comparableNumber, error) {
	if value == nil {
		return comparableNumber{}, errors.New("timestamp is null")
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return comparableNumber{rational: new(big.Rat).SetInt64(1)}, nil
		}
		return comparableNumber{rational: new(big.Rat)}, nil
	case json.Number:
		text := string(typed)
		if !strings.ContainsAny(text, ".eE") {
			integer, ok := new(big.Int).SetString(text, 10)
			if ok {
				return comparableNumber{rational: new(big.Rat).SetInt(integer)}, nil
			}
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return comparableNumber{}, err
		}
		return comparableFloat(number)
	case *big.Int:
		if typed == nil {
			return comparableNumber{}, errors.New("timestamp integer is nil")
		}
		return comparableNumber{rational: new(big.Rat).SetInt(typed)}, nil
	case big.Int:
		return comparableNumber{rational: new(big.Rat).SetInt(&typed)}, nil
	case float64:
		return comparableFloat(typed)
	case float32:
		return comparableFloat(float64(typed))
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return comparableNumber{rational: new(big.Rat).SetInt64(reflected.Int())}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := new(big.Int).SetUint64(reflected.Uint())
		return comparableNumber{rational: new(big.Rat).SetInt(integer)}, nil
	}
	return comparableNumber{}, fmt.Errorf("timestamp has non-numeric type %T", value)
}

func comparableFloat(value float64) (comparableNumber, error) {
	switch {
	case value != value:
		return comparableNumber{nan: true}, nil
	case value > 0 && value+value == value:
		return comparableNumber{infinity: 1}, nil
	case value < 0 && value+value == value:
		return comparableNumber{infinity: -1}, nil
	default:
		return comparableNumber{rational: new(big.Rat).SetFloat64(value)}, nil
	}
}
