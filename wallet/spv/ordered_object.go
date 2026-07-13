package spv

import (
	"bytes"
	"encoding/json"
)

// OrderedObject is a JSON object that retains the insertion order of its
// keys. Replacing an existing key preserves its original position, matching
// CPython dict behavior when JSON contains duplicate object members.
type OrderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *OrderedObject {
	return &OrderedObject{values: make(map[string]any)}
}

func (object *OrderedObject) set(key string, value any) {
	if _, exists := object.values[key]; !exists {
		object.keys = append(object.keys, key)
	}
	object.values[key] = value
}

// Keys returns a copy of the object keys in JSON insertion order.
func (object *OrderedObject) Keys() []string {
	if object == nil {
		return []string{}
	}
	return append([]string(nil), object.keys...)
}

// Get returns the value associated with key.
func (object *OrderedObject) Get(key string) (any, bool) {
	if object == nil {
		return nil, false
	}
	value, exists := object.values[key]
	return value, exists
}

// MarshalJSON preserves key order when an ordered object is used by protocol
// fixtures or forwarded through another JSON boundary.
func (object *OrderedObject) MarshalJSON() ([]byte, error) {
	if object == nil {
		return []byte("null"), nil
	}
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, key := range object.keys {
		if index > 0 {
			encoded.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		encodedValue, err := json.Marshal(object.values[key])
		if err != nil {
			return nil, err
		}
		encoded.Write(encodedKey)
		encoded.WriteByte(':')
		encoded.Write(encodedValue)
	}
	encoded.WriteByte('}')
	return encoded.Bytes(), nil
}
