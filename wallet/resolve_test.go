package wallet

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

type resolveWorkflowCall struct {
	ctx        context.Context
	method     string
	params     []any
	restricted bool
}

type resolveWorkflowSource struct {
	mu           sync.Mutex
	results      []any
	resultIndex  int
	calls        []resolveWorkflowCall
	events       []string
	callEvents   chan resolveWorkflowCall
	transaction  any
	transactionE error
}

func (*resolveWorkflowSource) Start(context.Context) error { return nil }
func (*resolveWorkflowSource) Stop(context.Context) error  { return nil }
func (*resolveWorkflowSource) RemoteHeight() int           { return 0 }
func (*resolveWorkflowSource) IsConnected() bool           { return true }
func (*resolveWorkflowSource) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*resolveWorkflowSource) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*resolveWorkflowSource) SetConnectedHandler(func(context.Context)) {}
func (*resolveWorkflowSource) SubscribeAddresses(context.Context, []string) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}
func (*resolveWorkflowSource) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected mapping call")
}

func (source *resolveWorkflowSource) RetriableValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	call := resolveWorkflowCall{
		ctx: ctx, method: method, params: append([]any(nil), params...), restricted: restricted,
	}
	source.mu.Lock()
	source.calls = append(source.calls, call)
	source.events = append(source.events, method)
	var result any
	var err error
	switch method {
	case SPVResolveMethod:
		if source.resultIndex >= len(source.results) {
			err = fmt.Errorf("unexpected resolve call %d", source.resultIndex+1)
		} else {
			result = source.results[source.resultIndex]
			source.resultIndex++
		}
	case SPVTransactionBatchMethod:
		result, err = source.transaction, source.transactionE
		if result == nil && err == nil {
			result = map[string]any{}
		}
	default:
		err = fmt.Errorf("unexpected retriable method %q", method)
	}
	callEvents := source.callEvents
	source.mu.Unlock()
	if callEvents != nil {
		callEvents <- call
	}
	return result, err
}

func (source *resolveWorkflowSource) snapshotCalls() []resolveWorkflowCall {
	source.mu.Lock()
	defer source.mu.Unlock()
	calls := make([]resolveWorkflowCall, len(source.calls))
	copy(calls, source.calls)
	return calls
}

func (source *resolveWorkflowSource) appendEvent(event string) {
	source.mu.Lock()
	source.events = append(source.events, event)
	source.mu.Unlock()
}

func (source *resolveWorkflowSource) snapshotEvents() []string {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]string(nil), source.events...)
}

func TestResolveAndSnapshotBatches205RequestsInOrder(t *testing.T) {
	requests := resolveWorkflowRequests(205)
	source := &resolveWorkflowSource{results: []any{
		resolveWorkflowErrorPage(100, HubErrorInvalid, "batch error"),
		resolveWorkflowErrorPage(100, HubErrorInvalid, "batch error"),
		resolveWorkflowErrorPage(5, HubErrorInvalid, "batch error"),
	}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "resolve")

	result, err := ledger.ResolveAndSnapshot(
		ctx, requests, ResolvedTransactionOutputAnnotationOptions{},
		LegacyTransactionJSONOptions{}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(requests) {
		t.Fatalf("resolve result count = %d, want %d", len(result), len(requests))
	}
	resolveWorkflowAssertError(t, result[0], "INVALID", "batch error")
	resolveWorkflowAssertError(t, result[len(result)-1], "INVALID", "batch error")

	calls := source.snapshotCalls()
	wantSizes := []int{100, 100, 5}
	if len(calls) != len(wantSizes) {
		t.Fatalf("resolve calls = %#v, want sizes %v", calls, wantSizes)
	}
	requestIndex := 0
	for callIndex, call := range calls {
		if call.ctx != ctx || call.method != SPVResolveMethod || call.restricted ||
			len(call.params) != wantSizes[callIndex] {
			t.Fatalf("resolve call %d = %#v", callIndex, call)
		}
		for _, parameter := range call.params {
			if parameter != requests[requestIndex].URL {
				t.Fatalf(
					"resolve parameter %d = %#v, want %q",
					requestIndex, parameter, requests[requestIndex].URL,
				)
			}
			requestIndex++
		}
	}
}

func TestResolveAndSnapshotMapsMissingAndHubErrors(t *testing.T) {
	missingURL := "lbry://missing"
	errorURL := "lbry://invalid"
	source := &resolveWorkflowSource{results: []any{resolveWorkflowPage(
		resolveWorkflowMissingOutput(0x44),
		resolveWorkflowErrorOutput(HubErrorInvalid, "invalid from hub"),
	)}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}

	result, err := ledger.ResolveAndSnapshot(
		context.Background(),
		[]ResolveRequest{{URL: missingURL}, {URL: errorURL}},
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"error": map[string]any{
			"name": "NOT_FOUND", "text": missingURL + " did not resolve to a claim",
		}},
		map[string]any{"error": map[string]any{
			"name": "INVALID", "text": "invalid from hub",
		}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("mapped resolve result = %#v, want %#v", result, want)
	}
	calls := source.snapshotCalls()
	if len(calls) != 2 || calls[0].method != SPVResolveMethod ||
		calls[1].method != SPVTransactionBatchMethod || len(calls[1].params) != 1 {
		t.Fatalf("missing/error calls = %#v", calls)
	}
}

func TestResolveChannelSignatureValidationUsesHydratedChannel(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	stream := &fixture.stream.Outputs[0]
	stream.Channel = &fixture.channel.Outputs[0]

	valid, err := fixture.ledger.resolveChannelSignatureValid(stream)
	if err != nil || !valid {
		t.Fatalf("valid resolved channel signature = %t, %v", valid, err)
	}

	mutated := *stream
	mutated.Script = stream.Script
	mutated.Script.Claim = append([]byte(nil), stream.Script.Claim...)
	mutated.Script.Claim[21] ^= 1
	valid, err = fixture.ledger.resolveChannelSignatureValid(&mutated)
	if err != nil || valid {
		t.Fatalf("mutated resolved channel signature = %t, %v", valid, err)
	}

	overflow := *stream
	overflow.Script = stream.Script
	overflow.Script.Claim = append([]byte(nil), stream.Script.Claim...)
	for index := 21; index < 53; index++ {
		overflow.Script.Claim[index] = 0xff
	}
	valid, err = fixture.ledger.resolveChannelSignatureValid(&overflow)
	var signatureErr *ResolveSignatureError
	if valid || !errors.As(err, &signatureErr) ||
		signatureErr.PythonErrorName() != "AssertionError" || err.Error() != "" {
		t.Fatalf("overflow resolved channel signature = %t, %T %v", valid, err, err)
	}
}

func TestResolveAndSnapshotChecksGlobalCountAfterAllBatches(t *testing.T) {
	source := &resolveWorkflowSource{results: []any{
		resolveWorkflowErrorPage(99, HubErrorNotFound, "missing"),
		resolveWorkflowErrorPage(100, HubErrorNotFound, "missing"),
		resolveWorkflowErrorPage(5, HubErrorNotFound, "missing"),
	}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	callbackCalled := false
	result, err := ledger.ResolveAndSnapshot(
		context.Background(), resolveWorkflowRequests(205),
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
		func([]*TransactionOutput) error {
			callbackCalled = true
			return nil
		},
	)
	var countErr *ResolveResultCountError
	if result != nil || !errors.Is(err, ErrResolveResultCount) || !errors.As(err, &countErr) ||
		countErr.PythonErrorName() != "AssertionError" ||
		err.Error() != "Mismatch between urls requested for resolve and responses received." {
		t.Fatalf("count mismatch result = %#v, %T %v", result, err, err)
	}
	if callbackCalled {
		t.Fatal("count mismatch invoked before-encoding callback")
	}
	calls := source.snapshotCalls()
	if len(calls) != 3 || len(calls[0].params) != 100 ||
		len(calls[1].params) != 100 || len(calls[2].params) != 5 {
		t.Fatalf("count mismatch calls = %#v", calls)
	}
}

func TestResolveAndSnapshotCallsCallbackForErrorOnlyResults(t *testing.T) {
	source := &resolveWorkflowSource{results: []any{
		resolveWorkflowErrorPage(2, HubErrorBlocked, "blocked"),
	}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	callbackCalls := 0
	result, err := ledger.ResolveAndSnapshot(
		context.Background(),
		[]ResolveRequest{{URL: "lbry://one"}, {URL: "lbry://two"}},
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
		func(outputs []*TransactionOutput) error {
			callbackCalls++
			if outputs == nil || len(outputs) != 0 {
				t.Fatalf("error-only callback outputs = %#v, want non-nil empty", outputs)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 1 || len(result) != 2 {
		t.Fatalf("callback calls = %d, result = %#v", callbackCalls, result)
	}
}

func TestResolveAndSnapshotCallbackFailureFollowsAllBatchesAndUnlocks(t *testing.T) {
	source := &resolveWorkflowSource{results: []any{
		resolveWorkflowErrorPage(100, HubErrorInvalid, "invalid"),
		resolveWorkflowErrorPage(100, HubErrorInvalid, "invalid"),
		resolveWorkflowErrorPage(5, HubErrorInvalid, "invalid"),
	}}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	callbackErr := errors.New("save resolved outputs failed")
	callbackCalls := 0
	result, err := ledger.ResolveAndSnapshot(
		context.Background(), resolveWorkflowRequests(205),
		ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
		func(outputs []*TransactionOutput) error {
			callbackCalls++
			source.appendEvent("callback")
			if len(outputs) != 0 {
				t.Fatalf("callback outputs = %#v, want none", outputs)
			}
			if ledger.hubOutputsInflateMu.TryLock() {
				ledger.hubOutputsInflateMu.Unlock()
				t.Fatal("callback ran without the inflation lock")
			}
			return callbackErr
		},
	)
	if result != nil || !errors.Is(err, callbackErr) || callbackCalls != 1 {
		t.Fatalf("callback failure = result %#v, calls %d, error %v", result, callbackCalls, err)
	}
	wantEvents := []string{SPVResolveMethod, SPVResolveMethod, SPVResolveMethod, "callback"}
	if events := source.snapshotEvents(); !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("callback event order = %v, want %v", events, wantEvents)
	}
	if !ledger.hubOutputsInflateMu.TryLock() {
		t.Fatal("callback failure did not release the inflation lock")
	}
	ledger.hubOutputsInflateMu.Unlock()
}

func TestResolveAndSnapshotSerializesConcurrentCallbacks(t *testing.T) {
	callEvents := make(chan resolveWorkflowCall, 2)
	source := &resolveWorkflowSource{
		results: []any{
			resolveWorkflowErrorPage(1, HubErrorInvalid, "first"),
			resolveWorkflowErrorPage(1, HubErrorInvalid, "second"),
		},
		callEvents: callEvents,
	}
	ledger := &Ledger{Headers: &Headers{}, SPVNetwork: source}
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		_, err := ledger.ResolveAndSnapshot(
			context.Background(), []ResolveRequest{{URL: "lbry://first"}},
			ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
			func([]*TransactionOutput) error {
				close(firstEntered)
				<-releaseFirst
				return nil
			},
		)
		firstDone <- err
	}()
	resolveWorkflowWaitSignal(t, firstEntered, "first callback")
	resolveWorkflowWaitCall(t, callEvents, "first resolve call")

	go func() {
		_, err := ledger.ResolveAndSnapshot(
			context.Background(), []ResolveRequest{{URL: "lbry://second"}},
			ResolvedTransactionOutputAnnotationOptions{}, LegacyTransactionJSONOptions{},
			func([]*TransactionOutput) error {
				secondEntered <- struct{}{}
				return nil
			},
		)
		secondDone <- err
	}()
	resolveWorkflowWaitCall(t, callEvents, "second resolve call")
	select {
	case <-secondEntered:
		release()
		t.Fatal("concurrent callback entered while the first callback held the inflation lock")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	resolveWorkflowWaitSignal(t, secondEntered, "second callback after unlock")
	if err := resolveWorkflowWaitError(t, firstDone, "first resolve"); err != nil {
		t.Fatal(err)
	}
	if err := resolveWorkflowWaitError(t, secondDone, "second resolve"); err != nil {
		t.Fatal(err)
	}
}

func resolveWorkflowRequests(count int) []ResolveRequest {
	requests := make([]ResolveRequest, count)
	for index := range requests {
		requests[index] = ResolveRequest{URL: fmt.Sprintf("lbry://claim-%d", index)}
	}
	return requests
}

func resolveWorkflowErrorPage(count int, code HubErrorCode, text string) string {
	outputs := make([][]byte, count)
	for index := range outputs {
		outputs[index] = resolveWorkflowErrorOutput(code, text)
	}
	return resolveWorkflowPage(outputs...)
}

func resolveWorkflowPage(outputs ...[]byte) string {
	var page []byte
	for _, output := range outputs {
		page = resolveWorkflowBytesField(page, 1, output)
	}
	return base64.StdEncoding.EncodeToString(page)
}

func resolveWorkflowErrorOutput(code HubErrorCode, text string) []byte {
	errorMessage := resolveWorkflowVarintField(nil, 1, uint64(code))
	errorMessage = resolveWorkflowBytesField(errorMessage, 2, []byte(text))
	return resolveWorkflowBytesField(nil, 15, errorMessage)
}

func resolveWorkflowMissingOutput(marker byte) []byte {
	return resolveWorkflowBytesField(nil, 1, bytes.Repeat([]byte{marker}, 32))
}

func resolveWorkflowBytesField(
	message []byte, field protowire.Number, value []byte,
) []byte {
	message = protowire.AppendTag(message, field, protowire.BytesType)
	return protowire.AppendBytes(message, value)
}

func resolveWorkflowVarintField(
	message []byte, field protowire.Number, value uint64,
) []byte {
	message = protowire.AppendTag(message, field, protowire.VarintType)
	return protowire.AppendVarint(message, value)
}

func resolveWorkflowAssertError(t *testing.T, value any, name, text string) {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("resolve error value = %#v", value)
	}
	errorValue, ok := object["error"].(map[string]any)
	if !ok || errorValue["name"] != name || errorValue["text"] != text {
		t.Fatalf("resolve error = %#v, want %s %q", object["error"], name, text)
	}
}

func resolveWorkflowWaitCall(
	t *testing.T, calls <-chan resolveWorkflowCall, description string,
) resolveWorkflowCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return resolveWorkflowCall{}
	}
}

func resolveWorkflowWaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func resolveWorkflowWaitError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

var _ LedgerSPVNetwork = (*resolveWorkflowSource)(nil)
var _ LedgerSPVAddressSource = (*resolveWorkflowSource)(nil)
var _ LedgerSPVRetriableValueSource = (*resolveWorkflowSource)(nil)
