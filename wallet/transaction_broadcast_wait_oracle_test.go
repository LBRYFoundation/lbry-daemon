package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

const (
	transactionBroadcastWaitOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionBroadcastWaitOraclePinnedVersion = "0.113.0"
)

var transactionBroadcastWaitOraclePinnedSources = map[string]string{
	"lbry/__init__.py":      "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/ledger.py": "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
}

var transactionBroadcastWaitOraclePinnedMethods = map[string]string{
	"broadcast_or_release": "19708de115f2ff002e1b15bd8dca9902889969c2ea79f19689cdecb02f522d60",
	"broadcast":            "55b913aba9fae62a8a0bc88fbd5e0c108e17483c06c3a8ed5cf4c3a825faa1c7",
	"wait":                 "1806a9a177584e7938b4099cd6c89462f10a94bc1b28c8ad48bc13dd45d3c11c",
	"_wait_round":          "3fedda13520ba582ed62255c678be20162ae9e60dd39d71af9d166ec0389ca45",
}

type transactionBroadcastWaitOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
	} `json:"metadata"`
	Broadcast          []transactionBroadcastWaitOracleCase `json:"broadcast"`
	BroadcastOrRelease []transactionBroadcastWaitOracleCase `json:"broadcast_or_release"`
	WaitRound          []transactionBroadcastWaitOracleCase `json:"wait_round"`
	Wait               []transactionBroadcastWaitOracleCase `json:"wait"`
}

type transactionBroadcastWaitOracleCase struct {
	Name         string  `json:"name"`
	Blocking     bool    `json:"blocking"`
	OK           bool    `json:"ok"`
	Result       any     `json:"result"`
	ErrorType    *string `json:"error_type"`
	ErrorMessage *string `json:"error_message"`

	NetworkCalls []string                              `json:"network_calls"`
	ReleaseCalls int                                   `json:"release_calls"`
	WaitCalls    []transactionBroadcastWaitOracleWait  `json:"wait_calls"`
	DBCalls      [][]string                            `json:"db_calls"`
	EventMatches []bool                                `json:"event_matches"`
	EventWaits   []transactionBroadcastWaitOracleEvent `json:"event_waits"`
	HistoryCalls []transactionBroadcastWaitHistoryCall `json:"history_calls"`
	AddressCalls []string                              `json:"address_calls"`
	ClockCalls   []float64                             `json:"clock_calls"`
}

type transactionBroadcastWaitOracleWait struct {
	Height  int64    `json:"height"`
	Timeout *float64 `json:"timeout"`
}

type transactionBroadcastWaitOracleEvent struct {
	Count   int `json:"count"`
	Matched int `json:"matched"`
	Pending int `json:"pending"`
	Timeout int `json:"timeout"`
}

type transactionBroadcastWaitHistoryCall struct {
	Address string `json:"address"`
	History string `json:"history"`
}

func TestTransactionBroadcastAndWaitMatchPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionBroadcastWaitOracle(t)
	if oracle.Reference.Commit != transactionBroadcastWaitOraclePinnedCommit ||
		oracle.Reference.Version != transactionBroadcastWaitOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionBroadcastWaitOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionBroadcastWaitOraclePinnedMethods) {
		t.Fatalf("transaction broadcast/wait oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || oracle.Metadata.ExternalNetworkUsed {
		t.Fatalf("transaction broadcast/wait oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction broadcast/wait Python version = %q, want %q",
			oracle.Metadata.PythonVersion, want)
	}

	assertTransactionBroadcastOracleContract(t, oracle)
	assertGoTransactionBroadcastOracle(t, oracle)
	assertGoTransactionWaitOracle(t, oracle)
}

func assertTransactionBroadcastOracleContract(
	t *testing.T, oracle transactionBroadcastWaitOracleResponse,
) {
	t.Helper()
	if len(oracle.Broadcast) != 2 || len(oracle.BroadcastOrRelease) != 6 ||
		len(oracle.WaitRound) != 5 || len(oracle.Wait) != 4 {
		t.Fatalf("oracle case counts = broadcast %d orchestration %d round %d wait %d",
			len(oracle.Broadcast), len(oracle.BroadcastOrRelease),
			len(oracle.WaitRound), len(oracle.Wait))
	}
	success := transactionBroadcastWaitOracleCaseNamed(t, oracle.Broadcast, "success")
	failure := transactionBroadcastWaitOracleCaseNamed(t, oracle.Broadcast, "rpc failure")
	if !success.OK || success.Result != "accepted" ||
		!reflect.DeepEqual(success.NetworkCalls, []string{"00ff"}) ||
		failure.OK || transactionBroadcastWaitOracleError(failure) != "RuntimeError: rejected" {
		t.Fatalf("broadcast cases = success %+v, failure %+v", success, failure)
	}

	type orchestrationWant struct {
		name         string
		ok           bool
		errorType    string
		releaseCalls int
		waitCalls    int
	}
	for _, want := range []orchestrationWant{
		{"success nonblocking", true, "", 0, 0},
		{"success blocking", true, "", 0, 1},
		{"broadcast failure releases", false, "RuntimeError", 1, 0},
		{"broadcast cancellation releases", false, "CancelledError", 1, 0},
		{"release failure masks broadcast", false, "ValueError", 1, 0},
		{"blocking wait failure does not release", false, "TimeoutError", 0, 1},
	} {
		got := transactionBroadcastWaitOracleCaseNamed(t, oracle.BroadcastOrRelease, want.name)
		gotErrorType := ""
		if got.ErrorType != nil {
			gotErrorType = *got.ErrorType
		}
		if got.OK != want.ok || gotErrorType != want.errorType ||
			got.ReleaseCalls != want.releaseCalls || len(got.WaitCalls) != want.waitCalls ||
			!reflect.DeepEqual(got.NetworkCalls, []string{"00ff"}) {
			t.Fatalf("broadcast_or_release %q = %+v, want %+v", want.name, got, want)
		}
		if len(got.WaitCalls) == 1 &&
			(got.WaitCalls[0].Height != -1 || got.WaitCalls[0].Timeout != nil) {
			t.Fatalf("broadcast_or_release %q wait call = %+v", want.name, got.WaitCalls[0])
		}
	}

	allEvents := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "all addresses observed by events",
	)
	history := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "partial events fall back to history",
	)
	mempool := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "mempool history satisfies positive height",
	)
	low := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "low match stops later record scan",
	)
	empty := transactionBroadcastWaitOracleCaseNamed(t, oracle.WaitRound, "no owned records")
	if !allEvents.OK || allEvents.Result != true || len(allEvents.DBCalls) != 1 ||
		!reflect.DeepEqual(allEvents.EventMatches, []bool{true, true}) ||
		!history.OK || history.Result != true || len(history.DBCalls) != 2 ||
		len(history.HistoryCalls) != 1 || history.HistoryCalls[0].Address != "pub:02" ||
		!mempool.OK || mempool.Result != true ||
		!low.OK || low.Result != false || len(low.HistoryCalls) != 1 ||
		low.HistoryCalls[0].Address != "pub:01" || empty.OK ||
		transactionBroadcastWaitOracleError(empty) != "ValueError: Set of Tasks/Futures is empty." {
		t.Fatalf("wait-round cases = all %+v history %+v mempool %+v low %+v empty %+v",
			allEvents, history, mempool, low, empty)
	}

	addresses := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.Wait, "collects and deduplicates affected addresses",
	)
	zero := transactionBroadcastWaitOracleCaseNamed(t, oracle.Wait, "zero timeout expands to 600")
	negative := transactionBroadcastWaitOracleCaseNamed(t, oracle.Wait, "negative timeout skips rounds")
	scriptInput := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.Wait, "resolved script-hash input lacks pubkey hash",
	)
	if !addresses.OK || len(addresses.DBCalls) != 1 || len(addresses.DBCalls[0]) != 2 ||
		len(addresses.AddressCalls) != 3 || zero.OK ||
		transactionBroadcastWaitOracleError(zero) !=
			"TimeoutError: Timed out waiting for transaction. target" ||
		!reflect.DeepEqual(zero.ClockCalls, []float64{0, 0, 601}) || len(zero.DBCalls) != 2 ||
		negative.OK || len(negative.DBCalls) != 0 ||
		transactionBroadcastWaitOracleError(negative) !=
			"TimeoutError: Timed out waiting for transaction. target" ||
		scriptInput.OK || scriptInput.ErrorType == nil || *scriptInput.ErrorType != "AttributeError" ||
		len(scriptInput.DBCalls) != 0 {
		t.Fatalf("wait cases = addresses %+v zero %+v negative %+v script %+v",
			addresses, zero, negative, scriptInput)
	}
}

func assertGoTransactionBroadcastOracle(
	t *testing.T, oracle transactionBroadcastWaitOracleResponse,
) {
	t.Helper()
	pythonSuccess := transactionBroadcastWaitOracleCaseNamed(t, oracle.Broadcast, "success")
	network := &transactionBroadcastWaitOracleNetwork{result: pythonSuccess.Result}
	ledger := &Ledger{SPVNetwork: network}
	transaction := &Transaction{Raw: []byte{0x00, 0xff}, ID: "target"}
	result, err := ledger.BroadcastTransaction(context.Background(), transaction)
	if err != nil || result != pythonSuccess.Result ||
		!reflect.DeepEqual(network.calls, pythonSuccess.NetworkCalls) {
		t.Fatalf("Go broadcast = result %#v calls %#v error %v, Python %+v",
			result, network.calls, err, pythonSuccess)
	}

	network.calls = nil
	if err := ledger.BroadcastOrRelease(context.Background(), transaction, false); err != nil {
		t.Fatal(err)
	}
	pythonNonblocking := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.BroadcastOrRelease, "success nonblocking",
	)
	if !reflect.DeepEqual(network.calls, pythonNonblocking.NetworkCalls) {
		t.Fatalf("Go broadcast_or_release calls = %#v, Python %#v",
			network.calls, pythonNonblocking.NetworkCalls)
	}

	database := transactionBroadcastWaitOracleDatabase(t)
	network.err = errors.New("rejected")
	ledger.Database = database
	if err := ledger.BroadcastOrRelease(context.Background(), transaction, false); err == nil ||
		err.Error() != "rejected" {
		t.Fatalf("Go failed broadcast release error = %v", err)
	}

	if err := database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BroadcastOrRelease(context.Background(), transaction, false); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("Go release failure did not mask broadcast: %v", err)
	}
}

func assertGoTransactionWaitOracle(
	t *testing.T, oracle transactionBroadcastWaitOracleResponse,
) {
	t.Helper()
	ctx := context.Background()
	ledger, transaction, addresses := transactionBroadcastWaitOracleLedger(t, 2)
	pythonAddresses := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.Wait, "collects and deduplicates affected addresses",
	)
	collected, err := ledger.transactionWaitAddresses(transaction)
	if err != nil || len(collected) != len(pythonAddresses.DBCalls[0]) ||
		!reflect.DeepEqual(collected, addresses) {
		t.Fatalf("Go affected addresses = %#v, %v, Python DB call %#v",
			collected, err, pythonAddresses.DBCalls[0])
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- ledger.waitTransaction(ctx, transaction, -1, 1, transactionWaitOptions{
			roundDuration: time.Second,
			now:           func() time.Duration { return 0 },
		})
	}()
	waitForTransactionBroadcastWaitListener(t, ledger)
	for _, address := range addresses {
		if err := ledger.publishTransactionBatch(address, []*Transaction{{ID: "target", Height: 0}}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-waitResult:
		if err != nil || !pythonAddresses.OK {
			t.Fatalf("Go event wait = %v, Python %+v", err, pythonAddresses)
		}
	case <-time.After(time.Second):
		t.Fatal("Go event wait did not complete")
	}

	mempool := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "mempool history satisfies positive height",
	)
	if err := ledger.Database.SetAddressHistory(ctx, addresses[0], "target:0:"); err != nil {
		t.Fatal(err)
	}
	matched, err := ledger.waitTransactionRound(ctx, "target", 5, addresses[:1], time.Millisecond)
	if err != nil || matched != mempool.Result.(bool) {
		t.Fatalf("Go mempool history match = %t, %v, Python %+v", matched, err, mempool)
	}

	low := transactionBroadcastWaitOracleCaseNamed(
		t, oracle.WaitRound, "low match stops later record scan",
	)
	if err := ledger.Database.SetAddressHistory(ctx, addresses[0], "target:2:"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.SetAddressHistory(ctx, addresses[1], "target:7:"); err != nil {
		t.Fatal(err)
	}
	matched, err = ledger.waitTransactionRound(ctx, "target", 5, addresses, time.Millisecond)
	if err != nil || matched != low.Result.(bool) {
		t.Fatalf("Go low history scan = %t, %v, Python %+v", matched, err, low)
	}

	zero := transactionBroadcastWaitOracleCaseNamed(t, oracle.Wait, "zero timeout expands to 600")
	if err := ledger.Database.SetAddressHistory(ctx, addresses[0], ""); err != nil {
		t.Fatal(err)
	}
	clock := transactionBroadcastWaitOracleClock(0, 0, 601*time.Second)
	err = ledger.waitTransaction(ctx, &Transaction{
		ID: "target", Outputs: []TransactionOutput{transaction.Outputs[0]},
	}, -1, 0, transactionWaitOptions{roundDuration: time.Millisecond, now: clock})
	if err == nil || err.Error() != *zero.ErrorMessage || !errors.Is(err, ErrTransactionWaitTimeout) {
		t.Fatalf("Go zero timeout = %v, Python %+v", err, zero)
	}

	negative := transactionBroadcastWaitOracleCaseNamed(t, oracle.Wait, "negative timeout skips rounds")
	err = ledger.waitTransaction(ctx, &Transaction{
		ID: "target", Outputs: []TransactionOutput{transaction.Outputs[0]},
	}, -1, -1, transactionWaitOptions{now: transactionBroadcastWaitOracleClock(0, 0)})
	if err == nil || err.Error() != *negative.ErrorMessage || !errors.Is(err, ErrTransactionWaitTimeout) {
		t.Fatalf("Go negative timeout = %v, Python %+v", err, negative)
	}

	scriptOutput := NewPayScriptHashOutput(1, bytes.Repeat([]byte{0x33}, 20))
	err = ledger.WaitTransaction(ctx, &Transaction{
		ID: "target", Inputs: []TransactionInput{{ResolvedOutput: &scriptOutput}},
	}, -1, 1)
	if !errors.Is(err, ErrTransactionWaitInputScript) {
		t.Fatalf("Go script-hash input error = %v", err)
	}

	empty := transactionBroadcastWaitOracleCaseNamed(t, oracle.WaitRound, "no owned records")
	unknown := NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x44}, 20))
	unknownAddress, err := unknown.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	matched, err = ledger.waitTransactionRound(ctx, "target", -1, []string{unknownAddress}, time.Millisecond)
	if matched || !errors.Is(err, ErrTransactionWaitNoRecords) || empty.ErrorType == nil {
		t.Fatalf("Go no-record wait = %t, %v, Python %+v", matched, err, empty)
	}
}

type transactionBroadcastWaitOracleNetwork struct {
	mu     sync.Mutex
	result any
	err    error
	calls  []string
}

func (*transactionBroadcastWaitOracleNetwork) Start(context.Context) error { return nil }
func (*transactionBroadcastWaitOracleNetwork) Stop(context.Context) error  { return nil }
func (*transactionBroadcastWaitOracleNetwork) RemoteHeight() int           { return 0 }
func (*transactionBroadcastWaitOracleNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unused header RPC")
}
func (network *transactionBroadcastWaitOracleNetwork) BroadcastTransaction(
	_ context.Context, raw string,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.calls = append(network.calls, raw)
	return network.result, network.err
}

func transactionBroadcastWaitOracleLedger(
	t *testing.T, count int,
) (*Ledger, *Transaction, []string) {
	t.Helper()
	database := transactionBroadcastWaitOracleDatabase(t)
	ledger := &Ledger{Network: keys.MainNet, Database: database}
	pubKeyHash := bytes.Repeat([]byte{0x11}, 20)
	scriptHash := bytes.Repeat([]byte{0x22}, 20)
	pubOutput := NewPayPubKeyHashOutput(1, pubKeyHash)
	scriptOutput := NewPayScriptHashOutput(1, scriptHash)
	pubAddress, err := pubOutput.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	scriptAddress, err := scriptOutput.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	addresses := []string{pubAddress, scriptAddress}
	addressKeys := make([]ledgerdb.AddressKey, count)
	for index := range count {
		addressKeys[index] = ledgerdb.AddressKey{
			Address: addresses[index], Chain: 0, N: int64(index),
			PublicKey: []byte{byte(index + 1)}, ChainCode: []byte{0},
		}
	}
	if err := database.AddKeys(context.Background(), "oracle", addressKeys); err != nil {
		t.Fatal(err)
	}
	transaction := &Transaction{
		ID: "target",
		Inputs: []TransactionInput{
			{},
			{ResolvedOutput: &pubOutput},
		},
		Outputs: []TransactionOutput{pubOutput, scriptOutput},
	}
	return ledger, transaction, addresses
}

func transactionBroadcastWaitOracleDatabase(t *testing.T) *ledgerdb.DB {
	t.Helper()
	database, err := ledgerdb.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if database.IsOpen() {
			if err := database.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	return database
}

func waitForTransactionBroadcastWaitListener(t *testing.T, ledger *Ledger) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		ledger.transactionEvents.mu.Lock()
		count := len(ledger.transactionEvents.listeners)
		ledger.transactionEvents.mu.Unlock()
		if count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("transaction wait listener was not registered")
		}
		time.Sleep(time.Millisecond)
	}
}

func transactionBroadcastWaitOracleClock(values ...time.Duration) func() time.Duration {
	index := 0
	return func() time.Duration {
		value := values[len(values)-1]
		if index < len(values) {
			value = values[index]
			index++
		}
		return value
	}
}

func transactionBroadcastWaitOracleCaseNamed(
	t *testing.T, cases []transactionBroadcastWaitOracleCase, name string,
) transactionBroadcastWaitOracleCase {
	t.Helper()
	for _, fixture := range cases {
		if fixture.Name == name {
			return fixture
		}
	}
	t.Fatalf("transaction broadcast/wait oracle case %q is missing", name)
	return transactionBroadcastWaitOracleCase{}
}

func transactionBroadcastWaitOracleError(fixture transactionBroadcastWaitOracleCase) string {
	if fixture.ErrorType == nil || fixture.ErrorMessage == nil {
		return ""
	}
	return *fixture.ErrorType + ": " + *fixture.ErrorMessage
}

func runTransactionBroadcastWaitOracle(t *testing.T) transactionBroadcastWaitOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction broadcast/wait oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionBroadcastWaitOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction broadcast/wait source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_broadcast_wait_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction broadcast/wait oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction broadcast/wait oracle failed: %v\n%s", err, output)
	}
	var oracle transactionBroadcastWaitOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction broadcast/wait oracle: %v\n%s", err, output)
	}
	return oracle
}
