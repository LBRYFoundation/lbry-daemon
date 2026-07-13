package spv

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
	"testing"
	"time"
)

var selectionOraclePinnedSources = map[string]string{
	"lbry/__init__.py":                  "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/conf.py":                      "ddedb9961723e67387fde0e02f7308fc6725f682802e1c3ec9030f6ccceac3e5",
	"lbry/schema/attrs.py":              "e2c01abf8a152ca224f557d38a4932b40ce0ceb880c27b2dbe0bca15c4a51624",
	"lbry/schema/types/v2/claim_pb2.py": "3edb36895d7d2f294e27019438332ca8a7ed4cb3c0f30ee33c9aa406bf000c98",
	"lbry/utils.py":                     "831e7d0062a9beb952a25be28f2b4ff58a721ff9c0b62ddc1d5a5e4d3a1b52d1",
	"lbry/wallet/network.py":            "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/udp.py":                "0520ffc127ddcc1285e4964ae995a6b5d36c42ad824d0ceb454e930e2750c094",
}

type selectionOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Wire struct {
		Magic           uint32 `json:"magic"`
		ProtocolVersion byte   `json:"protocol_version"`
		PingSize        int    `json:"ping_size"`
		PingHex         string `json:"ping_hex"`
		NonstandardPing struct {
			Version    byte   `json:"version"`
			PaddingHex string `json:"padding_hex"`
		} `json:"nonstandard_ping"`
		PongSize       int    `json:"pong_size"`
		AvailableUSHex string `json:"available_us_hex"`
		AvailableUS    struct {
			Version       byte   `json:"version"`
			Flags         byte   `json:"flags"`
			Height        uint32 `json:"height"`
			TipHex        string `json:"tip_hex"`
			SourceAddress string `json:"source_address"`
			Country       uint16 `json:"country"`
			CountryName   string `json:"country_name"`
			Available     bool   `json:"available"`
		} `json:"available_us"`
		Flags []struct {
			Flags     byte `json:"flags"`
			Available bool `json:"available"`
		} `json:"flags"`
	} `json:"wire"`
	Countries       []string          `json:"countries"`
	CountryExamples map[string]uint16 `json:"country_examples"`
	Selection       struct {
		SourcePrecedence               []string `json:"source_precedence"`
		DNSCacheSeconds                int      `json:"dns_cache_seconds"`
		ProbeTimeoutSeconds            int      `json:"probe_timeout_seconds"`
		AvailableOrder                 string   `json:"available_order"`
		UnavailableCompletesProbe      bool     `json:"unavailable_completes_probe"`
		NoPongFallback                 string   `json:"no_pong_fallback"`
		FallbackBypassesJurisdiction   bool     `json:"fallback_bypasses_jurisdiction"`
		JurisdictionCaseSensitive      bool     `json:"jurisdiction_case_sensitive"`
		CountryUpdatesSavedImmediately bool     `json:"country_updates_saved_immediately"`
	} `json:"selection"`
	KnownHubs struct {
		FirstResult       string      `json:"first_result"`
		DuplicateResult   *string     `json:"duplicate_result"`
		IgnoredResult     *string     `json:"ignored_result"`
		UnderscoreResult  string      `json:"underscore_result"`
		Ordered           []oracleHub `json:"ordered"`
		FilterOR          []oracleHub `json:"filter_or"`
		FilterMatchNone   []oracleHub `json:"filter_match_none"`
		PartialErrorType  string      `json:"partial_error_type"`
		PartialAfterError []oracleHub `json:"partial_after_error"`
		NumericTrue       []oracleHub `json:"numeric_true"`
		NumericFloat      []oracleHub `json:"numeric_float"`
		FalsyPeerEntries  []oracleHub `json:"falsy_peer_entries"`
	} `json:"known_hubs"`
	Metadata struct {
		PythonVersion string `json:"python_version"`
	} `json:"metadata"`
}

type oracleHub struct {
	Host    string         `json:"host"`
	Port    int            `json:"port"`
	Details map[string]any `json:"details"`
}

func TestSPVSelectionMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runSPVSelectionOracle(t)
	if oracle.Reference.Commit != spvOraclePinnedCommit ||
		oracle.Reference.Version != spvOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, selectionOraclePinnedSources) {
		t.Fatalf("selection oracle reference = %+v", oracle.Reference)
	}
	wire := oracle.Wire
	if wire.Magic != SPVStatusMagic || wire.ProtocolVersion != SPVStatusProtocolVersion ||
		wire.PingSize != SPVPingSize || wire.PongSize != SPVPongSize ||
		hex.EncodeToString(MakePing()) != wire.PingHex {
		t.Fatalf("selection wire metadata = %+v", wire)
	}
	nonstandard, err := hex.DecodeString("56311933ff" + wire.NonstandardPing.PaddingHex + "737566666978")
	if err != nil {
		t.Fatal(err)
	}
	decodedPing, err := DecodePing(nonstandard)
	if err != nil || decodedPing.Version != wire.NonstandardPing.Version ||
		hex.EncodeToString(decodedPing.Padding[:]) != wire.NonstandardPing.PaddingHex {
		t.Fatalf("nonstandard ping = %#v, %v", decodedPing, err)
	}
	pongBytes, err := hex.DecodeString(wire.AvailableUSHex)
	if err != nil {
		t.Fatal(err)
	}
	pong, err := DecodePong(append(pongBytes, []byte("suffix")...))
	if err != nil || pong.ProtocolVersion != wire.AvailableUS.Version ||
		pong.Flags != wire.AvailableUS.Flags || pong.Height != wire.AvailableUS.Height ||
		hex.EncodeToString(pong.Tip[:]) != wire.AvailableUS.TipHex ||
		pong.SourceIP() != wire.AvailableUS.SourceAddress || pong.Country != wire.AvailableUS.Country ||
		pong.Available() != wire.AvailableUS.Available {
		t.Fatalf("available US pong = %#v, %v", pong, err)
	}
	if name, err := pong.CountryName(); err != nil || name != wire.AvailableUS.CountryName {
		t.Fatalf("pong country = %q, %v", name, err)
	}
	for _, fixture := range wire.Flags {
		if got := (Pong{Flags: fixture.Flags}).Available(); got != fixture.Available {
			t.Fatalf("flags %d availability = %t, want %t", fixture.Flags, got, fixture.Available)
		}
	}

	if len(oracle.Countries) != len(countryNames()) {
		t.Fatalf("country count = %d, want %d", len(countryNames()), len(oracle.Countries))
	}
	for code, want := range oracle.Countries {
		got, err := CountryName(uint16(code))
		if err != nil || got != want {
			t.Fatalf("country %d = %q, %v; Python %q", code, got, err, want)
		}
	}
	for name, code := range oracle.CountryExamples {
		if got, err := CountryCode(name); err != nil || got != code {
			t.Fatalf("country code %q = %d, %v; Python %d", name, got, err, code)
		}
	}
	selection := oracle.Selection
	if !reflect.DeepEqual(selection.SourcePrecedence, []string{"explicit_servers", "known_hubs", "default_servers"}) ||
		selection.DNSCacheSeconds != int(DefaultDNSCacheTTL/time.Second) ||
		selection.ProbeTimeoutSeconds != int(DefaultProbeTimeout/time.Second) ||
		selection.AvailableOrder != "response_arrival" || selection.UnavailableCompletesProbe ||
		selection.NoPongFallback != "random_numeric_ip" || !selection.FallbackBypassesJurisdiction ||
		!selection.JurisdictionCaseSensitive || selection.CountryUpdatesSavedImmediately {
		t.Fatalf("selection metadata = %+v", selection)
	}
	assertKnownHubsOracle(t, oracle.KnownHubs)
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && oracle.Metadata.PythonVersion != want {
		t.Fatalf("selection oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
}

func assertKnownHubsOracle(t *testing.T, fixture struct {
	FirstResult       string      `json:"first_result"`
	DuplicateResult   *string     `json:"duplicate_result"`
	IgnoredResult     *string     `json:"ignored_result"`
	UnderscoreResult  string      `json:"underscore_result"`
	Ordered           []oracleHub `json:"ordered"`
	FilterOR          []oracleHub `json:"filter_or"`
	FilterMatchNone   []oracleHub `json:"filter_match_none"`
	PartialErrorType  string      `json:"partial_error_type"`
	PartialAfterError []oracleHub `json:"partial_after_error"`
	NumericTrue       []oracleHub `json:"numeric_true"`
	NumericFloat      []oracleHub `json:"numeric_float"`
	FalsyPeerEntries  []oracleHub `json:"falsy_peer_entries"`
}) {
	t.Helper()
	known := NewMemoryKnownHubs()
	added, err := known.SetString("first:99", HubDetails{"country": "US", "tier": "paid"})
	if err != nil || !added || fixture.FirstResult != "first:99" {
		t.Fatalf("first known hub = %t, %v", added, err)
	}
	duplicate, err := known.SetString("first:99", HubDetails{"country": "KP"})
	ignored, ignoredErr := known.SetString("too:many:colons", HubDetails{})
	underscore, underscoreErr := known.SetString("underscore:1_2", HubDetails{})
	known.SetString("missing:13", HubDetails{})
	if err != nil || duplicate || fixture.DuplicateResult != nil || ignoredErr != nil || ignored ||
		fixture.IgnoredResult != nil || underscoreErr != nil || !underscore || fixture.UnderscoreResult != "underscore:1_2" {
		t.Fatalf("known-hub insertion results differ from Python")
	}
	if got := oracleHubs(known.Snapshot()); !reflect.DeepEqual(got, fixture.Ordered) {
		t.Fatalf("known hubs = %#v, Python %#v", got, fixture.Ordered)
	}
	if got := oracleHubs(known.Filter(false, HubDetails{"country": "US", "tier": "absent"})); !reflect.DeepEqual(got, fixture.FilterOR) {
		t.Fatalf("known-hub OR filter = %#v, Python %#v", got, fixture.FilterOR)
	}
	if got := oracleHubs(known.Filter(true, HubDetails{"country": "US"})); !reflect.DeepEqual(got, fixture.FilterMatchNone) {
		t.Fatalf("known-hub match-none filter = %#v, Python %#v", got, fixture.FilterMatchNone)
	}
	partial := NewMemoryKnownHubs()
	added, err = partial.AddStrings([]string{"partial:1", "broken:not-a-port", "later:2"})
	if !added || err == nil || fixture.PartialErrorType != "ValueError" ||
		!reflect.DeepEqual(oracleHubs(partial.Snapshot()), fixture.PartialAfterError) {
		t.Fatalf("partial known-hub batch = %t, %v, %#v", added, err, partial.Snapshot())
	}
	numeric := NewMemoryKnownHubs()
	numeric.Set(Server{Host: "number", Port: 14}, HubDetails{"value": 1})
	if got := oracleHubs(numeric.Filter(false, HubDetails{"value": true})); !oracleHubSlicesEqual(got, fixture.NumericTrue) {
		t.Fatalf("numeric true filter = %#v, Python %#v", got, fixture.NumericTrue)
	}
	if got := oracleHubs(numeric.Filter(false, HubDetails{"value": 1.0})); !oracleHubSlicesEqual(got, fixture.NumericFloat) {
		t.Fatalf("numeric float filter = %#v, Python %#v", got, fixture.NumericFloat)
	}
	falsy := NewMemoryKnownHubs()
	falsyNetwork, err := NewNetwork(NetworkConfig{KnownHubs: falsy})
	if err != nil {
		t.Fatal(err)
	}
	falsyNetwork.updateKnownHubs([]any{nil, false, 0, []any{}, map[string]any{}, "good:15"})
	if got := oracleHubs(falsy.Snapshot()); !reflect.DeepEqual(got, fixture.FalsyPeerEntries) {
		t.Fatalf("falsy peer entries = %#v, Python %#v", got, fixture.FalsyPeerEntries)
	}
}

func oracleHubs(hubs []Hub) []oracleHub {
	result := make([]oracleHub, len(hubs))
	for index, hub := range hubs {
		result[index] = oracleHub{Host: hub.Server.Host, Port: hub.Server.Port, Details: map[string]any(hub.Details)}
	}
	return result
}

func oracleHubSlicesEqual(left, right []oracleHub) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Host != right[index].Host || left[index].Port != right[index].Port ||
			len(left[index].Details) != len(right[index].Details) {
			return false
		}
		for key, leftValue := range left[index].Details {
			rightValue, exists := right[index].Details[key]
			if !exists || !hubValuesEqual(leftValue, rightValue) {
				return false
			}
		}
	}
	return true
}

func runSPVSelectionOracle(t *testing.T) selectionOracleResponse {
	t.Helper()
	sdkRoot, script := spvSelectionOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python SPV selection oracle failed: %v\n%s", err, output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var oracle selectionOracleResponse
	if err := decoder.Decode(&oracle); err != nil {
		t.Fatalf("decode SPV selection oracle: %v\n%s", err, output)
	}
	return oracle
}

func spvSelectionOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate SPV selection oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "spv_selection_oracle.py")
	for relative := range selectionOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local SPV selection source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("SPV selection oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}
