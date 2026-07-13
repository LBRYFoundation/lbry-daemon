package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotHubOutputsEncodesOutputErrorMissingAndBlockedStates(t *testing.T) {
	fixture := newHubOutputsSnapshotFixture(t)
	snapshot, err := fixture.ledger.SnapshotHubOutputs(
		context.Background(), fixture.outputs,
		ResolvedTransactionOutputAnnotationOptions{},
		LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Offset != 7 || snapshot.Total != 11 || len(snapshot.Items) != 4 {
		t.Fatalf("snapshot page = %#v", snapshot)
	}

	output, ok := snapshot.Items[0].(map[string]any)
	if !ok || output["txid"] != fixture.transaction.ID || output["type"] != "claim" ||
		output["name"] != "snapshot" || output["short_url"] != "lbry://snapshot#s" ||
		output["canonical_url"] != "lbry://snapshot#canonical" ||
		output["protobuf"] != hex.EncodeToString(fixture.payload) {
		t.Fatalf("snapshot output = %#v", snapshot.Items[0])
	}
	meta, ok := output["meta"].(map[string]any)
	if !ok || meta["effective_amount"] != "0.00000006" ||
		meta["support_amount"] != "0.00000007" {
		t.Fatalf("snapshot output meta = %#v", output["meta"])
	}

	wantInvalid := map[string]any{
		"error": map[string]any{"name": "INVALID", "text": "invalid result"},
	}
	if !reflect.DeepEqual(snapshot.Items[1], wantInvalid) {
		t.Fatalf("invalid result = %#v, want %#v", snapshot.Items[1], wantInvalid)
	}
	blockedResult, ok := snapshot.Items[2].(map[string]any)
	if !ok {
		t.Fatalf("blocked result = %#v", snapshot.Items[2])
	}
	blockedError := blockedResult["error"].(map[string]any)
	blockedCensor, ok := blockedError["censor"].(map[string]any)
	if blockedError["name"] != "BLOCKED" || blockedError["text"] != "blocked result" ||
		!ok || blockedCensor["txid"] != fixture.transaction.ID {
		t.Fatalf("recursive blocked result = %#v", blockedResult)
	}
	if snapshot.Items[3] != nil {
		t.Fatalf("missing result = %#v, want nil", snapshot.Items[3])
	}

	if snapshot.Blocked["total"] != uint32(9) {
		t.Fatalf("blocked total = %#v", snapshot.Blocked)
	}
	channels, ok := snapshot.Blocked["channels"].([]any)
	if !ok || len(channels) != 3 {
		t.Fatalf("blocked channels = %#v", snapshot.Blocked["channels"])
	}
	first := channels[0].(map[string]any)
	firstChannel, ok := first["channel"].(map[string]any)
	if !ok || first["blocked"] != uint32(4) ||
		firstChannel["txid"] != fixture.transaction.ID {
		t.Fatalf("output blocked channel = %#v", first)
	}
	second := channels[1].(map[string]any)
	wantBlockedError := map[string]any{
		"error": map[string]any{"name": "NOT_FOUND", "text": "missing channel"},
	}
	if second["blocked"] != uint32(3) ||
		!reflect.DeepEqual(second["channel"], wantBlockedError) {
		t.Fatalf("error blocked channel = %#v", second)
	}
	third := channels[2].(map[string]any)
	if third["blocked"] != uint32(2) || third["channel"] != nil {
		t.Fatalf("missing blocked channel = %#v", third)
	}
}

func TestSnapshotHubOutputsIsDetachedAndJSONSerializable(t *testing.T) {
	fixture := newHubOutputsSnapshotFixture(t)
	snapshot, err := fixture.ledger.SnapshotHubOutputs(
		context.Background(), fixture.outputs,
		ResolvedTransactionOutputAnnotationOptions{},
		LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var serialized map[string]any
	if err := json.Unmarshal(before, &serialized); err != nil {
		t.Fatal(err)
	}
	if serialized["Offset"] != float64(7) || serialized["Total"] != float64(11) {
		t.Fatalf("serialized snapshot = %#v", serialized)
	}

	fixture.transaction.ID = strings.Repeat("f", 64)
	fixture.transaction.Outputs[0].Amount = 999
	fixture.transaction.Outputs[0].Meta["short_url"] = "mutated"
	fixture.transaction.Outputs[0].Meta["effective_amount"] = uint64(999)
	fixture.outputs.TXOs[1].Error.Text = "mutated invalid result"
	fixture.outputs.TXOs[2].Error.Text = "mutated blocked result"
	fixture.outputs.Blocked[0].Count = 99
	fixture.outputs.BlockedTotal = 100
	fixture.outputs.Offset = 101
	fixture.outputs.Total = 102

	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("snapshot changed after source mutation:\nbefore %s\nafter  %s", before, after)
	}
}

func TestSnapshotHubOutputsRunsValidationBeforeTypedEncodingFailure(t *testing.T) {
	fixture := newHubOutputsSnapshotFixture(t)
	fixture.transaction.Outputs[0].RepostedClaim = &fixture.transaction.Outputs[0]
	validationError := errors.New("response validation failed")
	_, err := fixture.ledger.SnapshotHubOutputsBeforeEncoding(
		context.Background(), fixture.outputs,
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
		func(HubOutputsPage) error { return validationError },
	)
	if !errors.Is(err, validationError) || errors.Is(err, ErrHubOutputsSnapshotEncoding) {
		t.Fatalf("pre-encoding validation error = %T %v", err, err)
	}

	_, err = fixture.ledger.SnapshotHubOutputs(
		context.Background(), fixture.outputs,
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
	)
	var encodingError *HubOutputsSnapshotEncodingError
	if !errors.Is(err, ErrHubOutputsSnapshotEncoding) ||
		!errors.As(err, &encodingError) || encodingError.Err == nil {
		t.Fatalf("snapshot encoding error = %T %v", err, err)
	}
}

type hubOutputsSnapshotFixture struct {
	ledger      *Ledger
	outputs     *HubOutputs
	transaction *Transaction
	payload     []byte
}

func newHubOutputsSnapshotFixture(t *testing.T) hubOutputsSnapshotFixture {
	t.Helper()
	ledger := transactionResolvedWireLedger(t)
	payload := claimWireOracleMustHex(t, transactionResolvedWireStream)
	transaction := transactionResolvedWireClaimTransaction(
		t, 0x71, "snapshot", payload,
	)
	transaction.IsVerified = true
	missingHash := hubOutputsFetchAdapterHash(0xb0)
	dummy := &Transaction{Hash: hubOutputsFetchAdapterHash(0xd0), IsVerified: true}
	outputReference := &HubOutput{
		TransactionHash: transaction.Hash[:], Position: 0, Height: uint32(transaction.Height),
	}
	missingReference := &HubOutput{
		TransactionHash: missingHash[:], Height: 8,
	}
	outputs := &HubOutputs{
		TXOs: []*HubOutput{
			{
				TransactionHash: transaction.Hash[:], Position: 0,
				Height: uint32(transaction.Height),
				Claim: &HubClaimMeta{
					ShortURL: "snapshot#s", CanonicalURL: "snapshot#canonical",
					EffectiveAmount: 6, SupportAmount: 7,
				},
			},
			{Error: &HubError{Code: HubErrorInvalid, Text: "invalid result"}},
			{Error: &HubError{
				Code: HubErrorBlocked, Text: "blocked result",
				Blocked: &HubBlocked{Channel: outputReference},
			}},
			missingReference,
		},
		Blocked: []*HubBlocked{
			{Count: 4, Channel: outputReference},
			{Count: 3, Channel: &HubOutput{Error: &HubError{
				Code: HubErrorNotFound, Text: "missing channel",
			}}},
			{Count: 2, Channel: missingReference},
		},
		BlockedTotal: 9,
		Offset:       7,
		Total:        11,
	}

	cache := ledger.ledgerTransactionCache()
	for _, request := range outputs.TransactionRequests() {
		cached := dummy
		if request.TxID == transaction.ID {
			cached = transaction
		}
		if err := cache.insertPlaceholder(request.TxID); err != nil {
			t.Fatal(err)
		}
		if err := cache.setExisting(request.TxID, cached); err != nil {
			t.Fatal(err)
		}
	}
	return hubOutputsSnapshotFixture{
		ledger: ledger, outputs: outputs, transaction: transaction, payload: payload,
	}
}
