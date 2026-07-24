package wallet

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestObjectPreservesInsertionOrderAndReplacementPosition(t *testing.T) {
	nested := NewObject(Member{Key: "value", Value: 1})
	object := NewObject(
		Member{Key: "second", Value: 2},
		Member{Key: "first", Value: nested},
	)
	object.Set("second", 20)
	object.Set("third", 3)
	if want := []string{"second", "first", "third"}; !reflect.DeepEqual(object.Keys(), want) {
		t.Fatalf("keys = %v, want %v", object.Keys(), want)
	}
	if !object.Delete("first") || object.Delete("missing") {
		t.Fatal("Delete returned the wrong result")
	}
	if want := []string{"second", "third"}; !reflect.DeepEqual(object.Keys(), want) {
		t.Fatalf("keys after delete = %v, want %v", object.Keys(), want)
	}

	copy := object.ShallowCopy()
	copy.Set("second", 200)
	if value, _ := object.Get("second"); value != 20 {
		t.Fatalf("top-level copy mutation changed source: %v", value)
	}
}

func TestOrderedJSONParserKeepsFirstDuplicatePositionAndLastValue(t *testing.T) {
	decoded, err := decodeOrderedJSON([]byte(`{"b": 1, "a": 2, "b": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	object := decoded.(*Object)
	if want := []string{"b", "a"}; !reflect.DeepEqual(object.Keys(), want) {
		t.Fatalf("keys = %v, want %v", object.Keys(), want)
	}
	value, _ := object.Get("b")
	if number, ok := value.(json.Number); !ok || number != "3" {
		t.Fatalf("duplicate value = %T(%v), want json.Number(3)", value, value)
	}
}
