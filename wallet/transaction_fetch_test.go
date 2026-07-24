package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const transactionFetchFixtureHex = "01000000010000000000000000000000000000000000000000000000000000000000000000" +
	"ffffffff1f04ffff001d010417696e736572742074696d657374616d7020737472696e67ffffffff" +
	"01000004bfc91b8e001976a914345991dbf57bfb014b87006acdfafbfc5fe8292f88ac00000000"

func TestPlanTransactionFetchBatchesStableOrderSizeAndFlatParams(t *testing.T) {
	requests := make([]TransactionFetchRequest, 205)
	for index := range requests {
		// Reverse the height groups while keeping three equal-height entries in
		// their original order so stability is independently observable.
		requests[index] = TransactionFetchRequest{
			TxID: fmt.Sprintf("tx-%03d", index), Height: int64((204 - index) / 3),
		}
	}
	original := append([]TransactionFetchRequest(nil), requests...)
	batches := PlanTransactionFetchBatches(requests, 1_000)
	if !reflect.DeepEqual(requests, original) {
		t.Fatal("planner mutated its input")
	}
	if len(batches) != 3 || len(batches[0].Requests) != 100 ||
		len(batches[1].Requests) != 100 || len(batches[2].Requests) != 5 {
		t.Fatalf("batch dimensions = %d / %d,%d,%d", len(batches), len(batches[0].Requests), len(batches[1].Requests), len(batches[2].Requests))
	}

	flattened := make([]TransactionFetchRequest, 0, len(requests))
	for batchIndex, batch := range batches {
		if len(batch.Params) != len(batch.Requests) {
			t.Fatalf("batch %d params = %d, requests = %d", batchIndex, len(batch.Params), len(batch.Requests))
		}
		for index, request := range batch.Requests {
			if batch.Params[index] != request.TxID {
				t.Fatalf("batch %d param %d = %#v, want flat txid %q", batchIndex, index, batch.Params[index], request.TxID)
			}
			flattened = append(flattened, request)
		}
	}
	for index := 1; index < len(flattened); index++ {
		previous, current := flattened[index-1], flattened[index]
		if previous.Height > current.Height {
			t.Fatalf("height order at %d = %d before %d", index, previous.Height, current.Height)
		}
		if previous.Height == current.Height && previous.TxID > current.TxID {
			// IDs increase with the original index; an unstable sort could invert
			// equal-height members.
			t.Fatalf("equal-height order at %d = %q before %q", index, previous.TxID, current.TxID)
		}
	}
	if !batches[0].Restricted {
		t.Fatal("batch containing height zero was unrestricted")
	}
	if batches[1].Restricted || batches[2].Restricted {
		t.Fatalf("positive checkpointed batches restricted = %t, %t", batches[1].Restricted, batches[2].Restricted)
	}
}

func TestPlanTransactionFetchRestrictionBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		heights    []int64
		checkpoint int64
		restricted bool
	}{
		{"strictly inside", []int64{2, 1}, 3, false},
		{"minimum zero", []int64{0, 1}, 3, true},
		{"minimum negative", []int64{-1, 1}, 3, true},
		{"one distinct height", []int64{1, 1}, 3, true},
		{"maximum equals checkpoint", []int64{1, 3}, 3, true},
		{"maximum above checkpoint", []int64{1, 4}, 3, true},
	}
	for _, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			requests := make([]TransactionFetchRequest, len(fixture.heights))
			for index, height := range fixture.heights {
				requests[index] = TransactionFetchRequest{TxID: fmt.Sprintf("tx%d", index), Height: height}
			}
			batches := PlanTransactionFetchBatches(requests, fixture.checkpoint)
			if len(batches) != 1 || batches[0].Restricted != fixture.restricted {
				t.Fatalf("batches = %#v, want restricted %t", batches, fixture.restricted)
			}
		})
	}
	if batches := PlanTransactionFetchBatches(nil, 3); len(batches) != 0 {
		t.Fatalf("empty plan = %#v", batches)
	}
}

func TestParseTransactionFetchBatchResultUsesOrderedResponseOrderAndRawID(t *testing.T) {
	batch := TransactionFetchBatch{Requests: []TransactionFetchRequest{
		{TxID: "requested-a", Height: 9},
		{TxID: "requested-b", Height: 4},
	}}
	response := map[string]any{
		"requested-b": []any{transactionFetchFixtureHex, map[string]any{
			"block_height": json.Number("4"),
		}},
		"requested-a": []any{transactionFetchFixtureHex + "00", nil},
	}
	results, err := ParseTransactionFetchBatchResult(batch, response)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Request != batch.Requests[0] ||
		results[1].Request != batch.Requests[1] {
		t.Fatalf("ordered results = %#v", results)
	}
	if results[0].Transaction.ID == "requested-a" || results[1].Transaction.ID == "requested-b" {
		t.Fatal("decoder compared or replaced the raw transaction ID")
	}
	if results[0].Transaction.Height != 9 || results[0].RemoteHeight != 9 ||
		results[1].Transaction.Height != 4 || results[1].RemoteHeight != 4 {
		t.Fatalf("decoded heights = %#v", results)
	}
	if len(results[0].Transaction.Trailing) != 1 || results[0].Merkle != nil ||
		results[1].Merkle["block_height"] != json.Number("4") {
		t.Fatalf("decoded results = %#v", results)
	}

	orderedResponse := NewObject(
		Member{Key: "requested-b", Value: response["requested-b"]},
		Member{Key: "requested-a", Value: response["requested-a"]},
	)
	orderedResults, err := ParseTransactionFetchBatchResult(batch, orderedResponse)
	if err != nil || len(orderedResults) != 2 ||
		orderedResults[0].Request.TxID != "requested-b" ||
		orderedResults[1].Request.TxID != "requested-a" {
		t.Fatalf("Object response = %#v, %v", orderedResults, err)
	}
}

func TestParseTransactionFetchBatchResultOrderedDuplicateReplacementPosition(t *testing.T) {
	batch := TransactionFetchBatch{Requests: []TransactionFetchRequest{
		{TxID: "first", Height: 1},
		{TxID: "second", Height: 2},
	}}
	ordered := NewObject(
		Member{Key: "second", Value: []any{transactionFetchFixtureHex, nil}},
		Member{Key: "first", Value: []any{transactionFetchFixtureHex, nil}},
		Member{Key: "second", Value: []any{transactionFetchFixtureHex + "00", nil}},
	)
	results, err := ParseTransactionFetchBatchResult(batch, ordered)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Request.TxID != "second" ||
		results[1].Request.TxID != "first" || len(results[0].Transaction.Trailing) != 1 {
		t.Fatalf("duplicate ordered results = %#v", results)
	}
}

func TestTransactionFetchDuplicateIDsUseFinalHeightAndOneResponseItem(t *testing.T) {
	requests := []TransactionFetchRequest{
		{TxID: "same", Height: 1},
		{TxID: "other", Height: 2},
		{TxID: "same", Height: 9},
	}
	batches := PlanTransactionFetchBatches(requests, 10)
	if len(batches) != 1 || batches[0].RemoteHeights["same"] != 9 ||
		batches[0].Restricted {
		t.Fatalf("duplicate plan = %#v", batches)
	}
	entry := []any{transactionFetchFixtureHex, nil}
	results, err := ParseTransactionFetchBatchResult(batches[0], map[string]any{
		"same": entry, "other": entry,
	})
	if err != nil || len(results) != 2 || results[0].Request.TxID != "same" ||
		results[0].RemoteHeight != 9 || results[0].Transaction.Height != 9 ||
		results[1].Request.TxID != "other" || results[1].RemoteHeight != 2 {
		t.Fatalf("duplicate results = %#v, %v", results, err)
	}
}

func TestParseTransactionFetchBatchResultKeyBehavior(t *testing.T) {
	batch := TransactionFetchBatch{Requests: []TransactionFetchRequest{{TxID: "wanted", Height: 1}}}
	results, err := ParseTransactionFetchBatchResult(batch, map[string]any{})
	if err != nil || len(results) != 0 {
		t.Fatalf("missing response entry = %#v, %v", results, err)
	}

	entry := []any{transactionFetchFixtureHex, nil}
	_, err = ParseTransactionFetchBatchResult(batch, map[string]any{
		"wanted": entry, "z-extra": entry, "a-extra": entry,
	})
	assertTransactionFetchResultError(t, err, TransactionFetchResultExtra, "a-extra", "")

	results, err = ParseTransactionFetchBatchResult(TransactionFetchBatch{}, map[string]any{})
	if err != nil || len(results) != 0 {
		t.Fatalf("empty result = %#v, %v", results, err)
	}
	_, err = ParseTransactionFetchBatchResult(TransactionFetchBatch{}, map[string]any{"extra": entry})
	assertTransactionFetchResultError(t, err, TransactionFetchResultExtra, "extra", "")
}

func TestParseTransactionFetchBatchResultTypedMalformedFailures(t *testing.T) {
	batch := TransactionFetchBatch{Requests: []TransactionFetchRequest{{TxID: "wanted", Height: 1}}}
	tests := []struct {
		name  string
		value any
		field string
	}{
		{"top level", []any{}, "result"},
		{"entry type", map[string]any{"wanted": "bad"}, "entry"},
		{"entry short", map[string]any{"wanted": []any{transactionFetchFixtureHex}}, "entry"},
		{"entry long", map[string]any{"wanted": []any{transactionFetchFixtureHex, nil, nil}}, "entry"},
		{"raw type", map[string]any{"wanted": []any{1, nil}}, "raw"},
		{"raw hexadecimal", map[string]any{"wanted": []any{"zz", nil}}, "raw"},
		{"raw transaction", map[string]any{"wanted": []any{"00", nil}}, "raw"},
		{"merkle type", map[string]any{"wanted": []any{transactionFetchFixtureHex, false}}, "merkle"},
	}
	for _, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			_, err := ParseTransactionFetchBatchResult(batch, fixture.value)
			assertTransactionFetchResultError(t, err, TransactionFetchResultMalformed, "", fixture.field)
		})
	}
}

func assertTransactionFetchResultError(
	t *testing.T, err error, kind TransactionFetchResultErrorKind, txid, field string,
) {
	t.Helper()
	if !errors.Is(err, ErrTransactionFetchResult) {
		t.Fatalf("error = %v, want ErrTransactionFetchResult", err)
	}
	var typed *TransactionFetchResultError
	if !errors.As(err, &typed) || typed.Kind != kind || typed.Field != field ||
		(txid != "" && typed.TxID != txid) {
		t.Fatalf("typed error = %#v, want kind %q txid %q field %q", typed, kind, txid, field)
	}
	wantSentinel := map[TransactionFetchResultErrorKind]error{
		TransactionFetchResultExtra:     ErrTransactionFetchResultExtra,
		TransactionFetchResultMalformed: ErrTransactionFetchResultMalformed,
	}[kind]
	if !errors.Is(err, wantSentinel) {
		t.Fatalf("error = %v, want %v", err, wantSentinel)
	}
}
