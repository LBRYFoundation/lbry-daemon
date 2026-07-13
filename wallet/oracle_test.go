package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

const (
	walletOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	walletOraclePinnedVersion = "0.113.0"
)

type walletStorageOracleCase struct {
	Name        string  `json:"name"`
	WithPath    *bool   `json:"with_path,omitempty"`
	Initial     *string `json:"initial,omitempty"`
	InitialMode int     `json:"initial_mode,omitempty"`
	Default     any     `json:"default,omitempty"`
	Action      string  `json:"action,omitempty"`
	Value       any     `json:"value,omitempty"`
}

type walletStorageOracleOutcome struct {
	Name          string  `json:"name"`
	Result        any     `json:"result"`
	ErrorType     *string `json:"error_type"`
	FinalExists   bool    `json:"final_exists"`
	FinalContents *string `json:"final_contents"`
	FinalMode     *int    `json:"final_mode"`
	TempExists    bool    `json:"temp_exists"`
}

type walletPreferenceOperation struct {
	Action string  `json:"action"`
	Key    string  `json:"key,omitempty"`
	Value  any     `json:"value,omitempty"`
	Time   float64 `json:"time,omitempty"`
}

type walletPreferenceOracleCase struct {
	Name       string                      `json:"name"`
	Initial    any                         `json:"initial,omitempty"`
	Operations []walletPreferenceOperation `json:"operations,omitempty"`
}

type walletPreferenceGet struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type walletPreferenceOracleOutcome struct {
	Name              string                `json:"name"`
	Data              any                   `json:"data"`
	KeyOrder          []string              `json:"key_order"`
	EntryKeyOrder     map[string][]string   `json:"entry_key_order"`
	WithoutTimestamps any                   `json:"without_timestamps"`
	Hash              string                `json:"hash"`
	Gets              []walletPreferenceGet `json:"gets"`
	ErrorType         *string               `json:"error_type"`
}

type walletOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Metadata struct {
		LatestVersion int `json:"latest_version"`
	} `json:"metadata"`
	StorageCases    []walletStorageOracleOutcome    `json:"storage_cases"`
	PreferenceCases []walletPreferenceOracleOutcome `json:"preference_cases"`
}

func TestWalletStorageAndPreferencesMatchPinnedPythonOracle(t *testing.T) {
	sdkRoot, script := walletOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	withoutPath := false
	currentTrue := `{"accounts":[],"preferences":{},"name":"Boolean","version":true}`
	currentFloat := `{"version":1.0,"name":"Float","preferences":{},"accounts":[]}`
	upgrade := `{"version":2,"name":"Old","extra":true}`
	malformed := `{`
	nonObject := `[]`
	existing := `old`
	storageCases := []walletStorageOracleCase{
		{Name: "missing"},
		{Name: "falsy custom default", Default: map[string]any{}},
		{Name: "truthy custom default", Default: map[string]any{
			"version": LatestVersion, "custom": "value",
		}},
		{Name: "current bool", Initial: &currentTrue},
		{Name: "current float", Initial: &currentFloat},
		{Name: "upgrade bug", Initial: &upgrade},
		{Name: "malformed", Initial: &malformed},
		{Name: "non-object", Initial: &nonObject},
		{Name: "memory write", WithPath: &withoutPath, Action: "write", Value: map[string]any{
			"z": "é😀<>&", "a": []any{
				1, json.Number("2.0"), json.Number("1e-5"), json.Number("1e-4"),
				json.Number("1e15"), json.Number("1e16"), json.Number("1e20"),
			},
		}},
		{Name: "float edge write", WithPath: &withoutPath, Action: "write", Value: []any{
			json.Number("-0.0"), json.Number("1.2345678901234567"),
			json.Number("0.84551240822557006"), json.Number("1e-6"), json.Number("1e-7"),
			json.Number("1e14"), json.Number("1e15"), json.Number("1e16"),
			json.Number("5e-324"), json.Number("2.2250738585072014e-308"),
			json.Number("1.7976931348623157e308"),
		}},
		{Name: "new file write", Action: "write", Value: oracleWalletDocument("New")},
		{Name: "existing file write", Initial: &existing, InitialMode: 0o640,
			Action: "write", Value: oracleWalletDocument("Existing")},
	}
	preferenceCases := []walletPreferenceOracleCase{
		{Name: "empty"},
		{Name: "loaded sorted entry", Initial: map[string]any{
			"one": map[string]any{"value": 1, "ts": 12345},
		}},
		{Name: "fixed set", Operations: []walletPreferenceOperation{
			{Action: "set", Key: "one", Value: 1, Time: 12345.9},
			{Action: "get", Key: "one"},
		}},
		{Name: "merge ordering", Operations: []walletPreferenceOperation{
			{Action: "set", Key: "one", Value: 1, Time: 10},
			{Action: "set", Key: "conflict", Value: 1, Time: 10},
			{Action: "merge", Value: map[string]any{
				"conflict": map[string]any{"value": 2, "ts": 20},
				"two":      map[string]any{"value": 2, "ts": 20},
			}},
			{Action: "set", Key: "conflict", Value: 3, Time: 21},
			{Action: "merge", Value: map[string]any{
				"conflict": map[string]any{"value": 4, "ts": 21},
			}},
			{Action: "get", Key: "conflict"},
		}},
		{Name: "reverse insertion", Operations: []walletPreferenceOperation{
			{Action: "set", Key: "b", Value: 2, Time: 2},
			{Action: "set", Key: "a", Value: 1, Time: 1},
		}},
		{Name: "ordered nested value", Operations: []walletPreferenceOperation{
			{Action: "set", Key: "object", Time: 9, Value: map[string]any{
				"z": "é<>&", "a": []any{1, json.Number("2.0")},
			}},
		}},
	}

	oracle := runWalletOracle(t, python, script, sdkRoot, storageCases, preferenceCases)
	if oracle.Reference.Commit != walletOraclePinnedCommit || oracle.Reference.Version != walletOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	if oracle.Metadata.LatestVersion != LatestVersion {
		t.Fatalf("oracle latest version = %d, want %d", oracle.Metadata.LatestVersion, LatestVersion)
	}

	goStorage := make([]walletStorageOracleOutcome, len(storageCases))
	for index, fixture := range storageCases {
		goStorage[index] = executeGoStorageCase(t, fixture)
	}
	goPreferences := make([]walletPreferenceOracleOutcome, len(preferenceCases))
	for index, fixture := range preferenceCases {
		goPreferences[index] = executeGoPreferenceCase(t, fixture)
	}
	assertOracleJSONEqual(t, "wallet storage", goStorage, oracle.StorageCases)
	assertOracleJSONEqual(t, "wallet preferences", goPreferences, oracle.PreferenceCases)
}

func executeGoStorageCase(t *testing.T, fixture walletStorageOracleCase) walletStorageOracleOutcome {
	t.Helper()
	directory := t.TempDir()
	withPath := fixture.WithPath == nil || *fixture.WithPath
	path := filepath.Join(directory, "wallet.json")
	if fixture.Initial != nil {
		mode := os.FileMode(fixture.InitialMode)
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(path, []byte(*fixture.Initial), mode); err != nil {
			t.Fatal(err)
		}
	}
	var storage *WalletStorage
	var defaults []*Object
	if fixture.Default != nil {
		defaults = []*Object{oracleOrderedValue(fixture.Default).(*Object)}
	}
	if withPath {
		storage = NewWalletStorage(path, defaults...)
	} else {
		storage = NewMemoryWalletStorage(defaults...)
	}
	outcome := walletStorageOracleOutcome{Name: fixture.Name}
	var err error
	if fixture.Action == "write" {
		var result []byte
		result, err = storage.Write(oracleOrderedValue(fixture.Value))
		if result != nil {
			outcome.Result = string(result)
		}
	} else {
		outcome.Result, err = storage.Read()
	}
	if err != nil {
		errorType := "error"
		var decodeError *WalletJSONDecodeError
		var objectError *WalletJSONObjectError
		switch {
		case errors.As(err, &decodeError):
			errorType = "JSONDecodeError"
		case errors.As(err, &objectError):
			errorType = "AttributeError"
		}
		outcome.ErrorType = &errorType
	}
	if withPath {
		contents, readErr := os.ReadFile(path)
		if readErr == nil {
			outcome.FinalExists = true
			text := string(contents)
			outcome.FinalContents = &text
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			mode := int(info.Mode().Perm())
			outcome.FinalMode = &mode
		} else if !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		temporary, globErr := filepath.Glob(filepath.Join(directory, "wallet.json.tmp.*"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		outcome.TempExists = len(temporary) > 0
	}
	return outcome
}

func executeGoPreferenceCase(t *testing.T, fixture walletPreferenceOracleCase) walletPreferenceOracleOutcome {
	t.Helper()
	var initial *Object
	if fixture.Initial != nil {
		initial = oracleOrderedValue(fixture.Initial).(*Object)
	}
	preferences := NewTimestampedPreferences(initial)
	gets := make([]walletPreferenceGet, 0)
	var caseErr error
	for _, operation := range fixture.Operations {
		switch operation.Action {
		case "set":
			preferences.SetAt(operation.Key, oracleOrderedValue(operation.Value), int64(operation.Time))
		case "merge":
			caseErr = preferences.Merge(oracleOrderedValue(operation.Value).(*Object))
		case "get":
			value, _, err := preferences.Get(operation.Key)
			caseErr = err
			if err == nil {
				gets = append(gets, walletPreferenceGet{Key: operation.Key, Value: value})
			}
		default:
			caseErr = errors.New("unknown preference operation")
		}
		if caseErr != nil {
			break
		}
	}
	withoutTimestamps, err := preferences.WithoutTimestamps()
	if caseErr == nil {
		caseErr = err
	}
	hash, err := preferences.Hash()
	if caseErr == nil {
		caseErr = err
	}
	entryOrder := make(map[string][]string, preferences.Data().Len())
	for _, member := range preferences.Data().Members() {
		if entry, ok := member.Value.(*Object); ok {
			entryOrder[member.Key] = entry.Keys()
		}
	}
	outcome := walletPreferenceOracleOutcome{
		Name:              fixture.Name,
		Data:              preferences.Data(),
		KeyOrder:          preferences.Data().Keys(),
		EntryKeyOrder:     entryOrder,
		WithoutTimestamps: withoutTimestamps,
		Hash:              hex.EncodeToString(hash[:]),
		Gets:              gets,
	}
	if caseErr != nil {
		errorType := "error"
		outcome.ErrorType = &errorType
	}
	return outcome
}

func oracleWalletDocument(name string) map[string]any {
	return map[string]any{
		"version": LatestVersion, "name": name,
		"preferences": map[string]any{}, "accounts": []any{},
	}
}

func oracleOrderedValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		object := NewObject()
		for _, key := range keys {
			object.Set(key, oracleOrderedValue(typed[key]))
		}
		return object
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = oracleOrderedValue(item)
		}
		return result
	default:
		return value
	}
}

func walletOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate wallet oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "wallet_storage_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "wallet.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK wallet source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func runWalletOracle(
	t *testing.T,
	python, script, sdkRoot string,
	storageCases []walletStorageOracleCase,
	preferenceCases []walletPreferenceOracleCase,
) walletOracleOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"storage_cases": storageCases, "preference_cases": preferenceCases,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python wallet storage oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result walletOracleOutput
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python wallet oracle: %v\n%s", err, output)
	}
	return result
}

func assertOracleJSONEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	gotDecoder := json.NewDecoder(bytes.NewReader(gotJSON))
	gotDecoder.UseNumber()
	wantDecoder := json.NewDecoder(bytes.NewReader(wantJSON))
	wantDecoder.UseNumber()
	if err := gotDecoder.Decode(&gotValue); err != nil {
		t.Fatal(err)
	}
	if err := wantDecoder.Decode(&wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		prettyGot, _ := json.MarshalIndent(gotValue, "", "  ")
		prettyWant, _ := json.MarshalIndent(wantValue, "", "  ")
		t.Fatalf("%s differs\nGo:     %s\nPython: %s", name, prettyGot, prettyWant)
	}
}
