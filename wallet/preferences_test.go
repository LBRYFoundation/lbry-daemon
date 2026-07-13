package wallet

import (
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

func TestTimestampedPreferencesEmptyAndFixedSetHashesMatchPython(t *testing.T) {
	preferences := NewTimestampedPreferences(nil)
	hash, err := preferences.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(hash[:]), "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"; got != want {
		t.Fatalf("empty hash = %s, want %s", got, want)
	}

	preferences.SetAt("one", 1, 12345)
	encoded, err := preferences.HashJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"one": {"value": 1, "ts": 12345}}`; got != want {
		t.Fatalf("hash JSON = %q, want %q", got, want)
	}
	hash, err = preferences.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(hash[:]), "c9e82bf4cb099dd0125f78fa381b21a8131af601917eb531e1f5f980f8f3da66"; got != want {
		t.Fatalf("fixed preference hash = %s, want %s", got, want)
	}
	entryValue, _ := preferences.Data().Get("one")
	if want := []string{"value", "ts"}; !reflect.DeepEqual(entryValue.(*Object).Keys(), want) {
		t.Fatalf("entry keys = %v, want %v", entryValue.(*Object).Keys(), want)
	}
}

func TestTimestampedPreferencesSetUsesTruncatedClockAndKeepsPosition(t *testing.T) {
	preferences := NewTimestampedPreferences(nil, WithPreferenceClock(func() float64 { return 12345.9 }))
	preferences.Set("second", 2)
	preferences.SetAt("first", 1, 10)
	preferences.Set("second", 20)
	if want := []string{"second", "first"}; !reflect.DeepEqual(preferences.Data().Keys(), want) {
		t.Fatalf("keys = %v, want %v", preferences.Data().Keys(), want)
	}
	entryValue, _ := preferences.Data().Get("second")
	timestamp, _ := entryValue.(*Object).Get("ts")
	if timestamp != int64(12345) {
		t.Fatalf("timestamp = %v, want 12345", timestamp)
	}
	value, exists, err := preferences.Get("second")
	if err != nil || !exists || value != 20 {
		t.Fatalf("Get = (%v, %v, %v)", value, exists, err)
	}
	fallback, err := preferences.GetOr("missing", "default")
	if err != nil || fallback != "default" {
		t.Fatalf("GetOr = (%v, %v)", fallback, err)
	}
}

func TestTimestampedPreferencesMergeMatchesStrictOlderRule(t *testing.T) {
	preferences := NewTimestampedPreferences(nil)
	preferences.SetAt("one", 1, 10)
	preferences.SetAt("conflict", 1, 10)

	remoteConflict := NewObject(
		Member{Key: "value", Value: 2}, Member{Key: "ts", Value: 20},
	)
	remote := NewObject(
		Member{Key: "two", Value: NewObject(
			Member{Key: "value", Value: 2}, Member{Key: "ts", Value: 20},
		)},
		Member{Key: "conflict", Value: remoteConflict},
	)
	if err := preferences.Merge(remote); err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "conflict", "two"}; !reflect.DeepEqual(preferences.Data().Keys(), want) {
		t.Fatalf("merged keys = %v, want %v", preferences.Data().Keys(), want)
	}
	value, _, _ := preferences.Get("conflict")
	if value != 2 {
		t.Fatalf("newer remote conflict = %v, want 2", value)
	}

	preferences.SetAt("conflict", 3, 21)
	if err := preferences.Merge(remote); err != nil {
		t.Fatal(err)
	}
	value, _, _ = preferences.Get("conflict")
	if value != 3 {
		t.Fatalf("older remote conflict replaced local: %v", value)
	}

	equal := NewObject(Member{Key: "conflict", Value: NewObject(
		Member{Key: "value", Value: 4}, Member{Key: "ts", Value: 21},
	)})
	if err := preferences.Merge(equal); err != nil {
		t.Fatal(err)
	}
	value, _, _ = preferences.Get("conflict")
	if value != 4 {
		t.Fatalf("equal timestamp did not replace local: %v", value)
	}
	stored, _ := preferences.Data().Get("conflict")
	if stored != equal.Members()[0].Value {
		t.Fatal("merge deep-copied the incoming preference entry")
	}
}

func TestTimestampedPreferencesMergeStoresMalformedNewEntryBeforeValidation(t *testing.T) {
	preferences := NewTimestampedPreferences(nil)
	if err := preferences.Merge(nil); err == nil {
		t.Fatal("nil preferences merge succeeded")
	}
	if err := preferences.Merge(NewObject(Member{Key: "new", Value: "raw"})); err != nil {
		t.Fatal(err)
	}
	stored, exists := preferences.Data().Get("new")
	if !exists || stored != "raw" {
		t.Fatalf("stored new preference = %v, %v", stored, exists)
	}
	if _, _, err := preferences.Get("new"); err == nil {
		t.Fatal("malformed stored preference unexpectedly decoded")
	}
	if err := preferences.Merge(NewObject(Member{Key: "new", Value: "replacement"})); err == nil {
		t.Fatal("malformed conflicting preference was not validated")
	}
}

func TestTimestampedPreferencesHashUsesInsertionOrder(t *testing.T) {
	first := NewTimestampedPreferences(nil)
	first.SetAt("a", 1, 1)
	first.SetAt("b", 2, 2)
	second := NewTimestampedPreferences(nil)
	second.SetAt("b", 2, 2)
	second.SetAt("a", 1, 1)
	firstHash, err := first.Hash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("opposite preference insertion orders produced the same hash")
	}
}

func TestTimestampedPreferencesWithoutTimestampsAndNestedOrderGuard(t *testing.T) {
	preferences := NewTimestampedPreferences(nil)
	preferences.SetAt("one", 1, 1)
	preferences.SetAt("two", "second", 2)
	values, err := preferences.WithoutTimestamps()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(values.Keys(), want) {
		t.Fatalf("value keys = %v, want %v", values.Keys(), want)
	}

	preferences.SetAt("object", map[string]any{"a": 1, "b": 2}, 3)
	if _, err := preferences.Hash(); !errors.Is(err, ErrUnorderedObject) {
		t.Fatalf("unordered object hash error = %v, want ErrUnorderedObject", err)
	}
}

func TestTimestampedPreferencesConstructorIsShallow(t *testing.T) {
	entry := NewObject(Member{Key: "value", Value: 1}, Member{Key: "ts", Value: 1})
	data := NewObject(Member{Key: "one", Value: entry})
	preferences := NewTimestampedPreferences(data)
	entry.Set("value", 2)
	value, _, err := preferences.Get("one")
	if err != nil || value != 2 {
		t.Fatalf("shallow constructor value = %v, %v", value, err)
	}
}

func TestTimestampedPreferencesMergeTreatsBooleanTimestampsAsIntegers(t *testing.T) {
	preferences := NewTimestampedPreferences(NewObject(Member{Key: "value", Value: NewObject(
		Member{Key: "value", Value: "local"}, Member{Key: "ts", Value: true},
	)}))
	older := NewObject(Member{Key: "value", Value: NewObject(
		Member{Key: "value", Value: "older"}, Member{Key: "ts", Value: false},
	)})
	if err := preferences.Merge(older); err != nil {
		t.Fatal(err)
	}
	value, _, _ := preferences.Get("value")
	if value != "local" {
		t.Fatalf("false timestamp replaced true timestamp: %v", value)
	}
	equal := NewObject(Member{Key: "value", Value: NewObject(
		Member{Key: "value", Value: "equal"}, Member{Key: "ts", Value: true},
	)})
	if err := preferences.Merge(equal); err != nil {
		t.Fatal(err)
	}
	value, _, _ = preferences.Get("value")
	if value != "equal" {
		t.Fatalf("equal Boolean timestamp did not replace: %v", value)
	}
}
