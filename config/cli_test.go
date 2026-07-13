package config

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestCommandLineOptionsCoverEverySetting(t *testing.T) {
	options := commandLineOptions(true)
	bySetting := make(map[string][]cliOption)
	for _, option := range options {
		if option.setting != "" {
			bySetting[option.setting] = append(bySetting[option.setting], option)
		}
	}
	for _, spec := range defaultSpecs(DefaultPaths()) {
		want := 1
		if spec.Kind == KindToggle || spec.Kind == KindMaxKeyFee {
			want = 2
		}
		if got := len(bySetting[spec.Name]); got != want {
			t.Errorf("CLI options for %s = %d, want %d", spec.Name, got, want)
		}
	}
	if got := len(bySetting); got != 55 {
		t.Fatalf("settings covered = %d, want 55", got)
	}
}

func TestParseCommandLineLeavesUnsetSettingsAbsent(t *testing.T) {
	parsed, err := ParseCommandLine([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Command != "start" || len(parsed.Settings) != 0 {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestParseCommandLineSettingActions(t *testing.T) {
	parsed, err := ParseCommandLine([]string{
		"start",
		"--tcp-port", "4", "--tcp-port=1_000",
		"--use-upnp", "--no-use-upnp", "--use-upnp",
		"--components-to-skip", "wallet", "--components-to-skip=dht",
		"--known-dht-nodes=a.example:1", "--known-dht-nodes", "bad",
		"--coin-selection-strategy", "not-a-strategy",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"tcp_port":                "1_000",
		"use_upnp":                true,
		"components_to_skip":      []string{"wallet", "dht"},
		"known_dht_nodes":         []string{"a.example:1", "bad"},
		"coin_selection_strategy": "not-a-strategy",
	}
	if !reflect.DeepEqual(parsed.Settings, want) {
		t.Fatalf("settings = %#v, want %#v", parsed.Settings, want)
	}
}

func TestParseCommandLineMaxKeyFee(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want any
	}{
		{name: "amount and currency", args: []string{"start", "--max-key-fee", "-1", "usd"}, want: []string{"-1", "usd"}},
		{name: "null", args: []string{"start", "--max-key-fee=null"}, want: []string{"null"}},
		{name: "disabled", args: []string{"start", "--no-max-key-fee"}, want: nil},
		{name: "last wins", args: []string{"start", "--max-key-fee", "1", "USD", "--max-key-fee=2", "BTC"}, want: []string{"2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParseCommandLine(test.args)
			if err != nil {
				t.Fatal(err)
			}
			value, exists := parsed.Settings["max_key_fee"]
			if !exists || !reflect.DeepEqual(value, test.want) {
				t.Fatalf("max_key_fee = %#v, exists %v, want %#v", value, exists, test.want)
			}
		})
	}
	parsed, err := ParseCommandLine([]string{"start", "--max-key-fee=null", "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.Settings["max_key_fee"], []string{"null"}) || !reflect.DeepEqual(parsed.Unknown, []string{"ignored"}) {
		t.Fatalf("attached max fee parse = %#v", parsed)
	}
}

func TestParseCommandLineSpecialStartOptions(t *testing.T) {
	parsed, err := ParseCommandLine([]string{
		"start", "--quiet", "--no-logging", "--verbose", "lbry", "aiohttp",
		"--initial-headers", "headers.bin", "--help",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Quiet || !parsed.NoLogging || !parsed.VerboseSet || !parsed.Help {
		t.Fatalf("boolean options = %#v", parsed)
	}
	if !reflect.DeepEqual(parsed.Verbose, []string{"lbry", "aiohttp"}) {
		t.Fatalf("verbose = %#v", parsed.Verbose)
	}
	if parsed.InitialHeaders != "headers.bin" {
		t.Fatalf("initial headers = %q", parsed.InitialHeaders)
	}

	emptyVerbose, err := ParseCommandLine([]string{"start", "--verbose", "--quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if !emptyVerbose.VerboseSet || emptyVerbose.Verbose == nil || len(emptyVerbose.Verbose) != 0 {
		t.Fatalf("empty verbose = %#v", emptyVerbose.Verbose)
	}
}

func TestParseCommandLineRootAndStartOrdering(t *testing.T) {
	root, err := ParseCommandLine([]string{"--api", "root:1", "--version", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	if root.Command != "" || !root.Version || !root.Help || root.Settings["api"] != "root:1" {
		t.Fatalf("root parse = %#v", root)
	}

	start, err := ParseCommandLine([]string{
		"--api", "root:1", "--help", "--version", "start", "--tcp-port", "4445", "--version",
	})
	if err != nil {
		t.Fatal(err)
	}
	if start.Help || !start.Version {
		t.Fatalf("root defaults were not overwritten correctly: %#v", start)
	}
	if _, exists := start.Settings["api"]; exists {
		t.Fatalf("root api survived start defaults: %#v", start.Settings)
	}
	if start.Settings["tcp_port"] != "4445" || !reflect.DeepEqual(start.Unknown, []string{"--version"}) {
		t.Fatalf("start parse = %#v", start)
	}
}

func TestParseCommandLineRootEndMarkerAndVersionClusters(t *testing.T) {
	for _, args := range [][]string{{"--", "--version"}, {"--", "--help"}, {"--", "--api"}} {
		_, err := ParseCommandLine(args)
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("ParseCommandLine(%#v) error = %T %v, want *UsageError", args, err, err)
		}
	}

	tests := []struct {
		argument string
		unknown  []string
	}{
		{argument: "-vv"},
		{argument: "-vfoo", unknown: []string{"-foo"}},
		{argument: "-vvfoo", unknown: []string{"-foo"}},
	}
	for _, test := range tests {
		parsed, err := ParseCommandLine([]string{test.argument})
		if err != nil {
			t.Fatalf("ParseCommandLine(%q): %v", test.argument, err)
		}
		if !parsed.Version || !reflect.DeepEqual(parsed.Unknown, test.unknown) {
			t.Fatalf("ParseCommandLine(%q) = %#v", test.argument, parsed)
		}
	}
	_, err := ParseCommandLine([]string{"-v=foo"})
	if err == nil || err.Error() != "argument -v/--version: ignored explicit argument 'foo'" {
		t.Fatalf("-v=foo error = %v", err)
	}
}

func TestParseCommandLineReturnsRecognizedClientRemainder(t *testing.T) {
	isCommand := func(token string) bool {
		return token == "status" || token == "account"
	}
	parsed, err := ParseCommandLineWithCommands([]string{
		"--api", "daemon.example:5279", "--help", "--root-extra", "status", "--help",
	}, isCommand)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Command != "status" || parsed.Help || parsed.Settings["api"] != "daemon.example:5279" {
		t.Fatalf("client prefix = %#v", parsed)
	}
	want := []string{"status", "--help", "--root-extra"}
	if !reflect.DeepEqual(parsed.CommandArguments, want) {
		t.Fatalf("client remainder = %#v, want %#v", parsed.CommandArguments, want)
	}

	afterMarker, err := ParseCommandLineWithCommands([]string{"--", "account", "list"}, isCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterMarker.CommandArguments, []string{"account", "list"}) {
		t.Fatalf("client remainder after -- = %#v", afterMarker.CommandArguments)
	}

	_, err = ParseCommandLineWithCommands([]string{"wat"}, isCommand)
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("unknown client command error = %T %v", err, err)
	}
}

func TestParseCommandLineKnownAndUnknownArguments(t *testing.T) {
	parsed, err := ParseCommandLine([]string{
		"--root-unknown", "start", "--start-unknown=value", "positional", "--tcp-p", "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Settings["tcp_port"] != "4" {
		t.Fatalf("abbreviated tcp_port = %#v", parsed.Settings["tcp_port"])
	}
	wantUnknown := []string{"--root-unknown", "--start-unknown=value", "positional"}
	if !reflect.DeepEqual(parsed.Unknown, wantUnknown) {
		t.Fatalf("unknown = %#v, want %#v", parsed.Unknown, wantUnknown)
	}

	afterMarker, err := ParseCommandLine([]string{"start", "--", "--tcp-port", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(afterMarker.Settings) != 0 || !reflect.DeepEqual(afterMarker.Unknown, []string{"--", "--tcp-port", "4"}) {
		t.Fatalf("after -- = %#v", afterMarker)
	}
}

func TestParseCommandLineUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "missing scalar", args: []string{"start", "--tcp-port"}, message: "argument --tcp-port: expected one argument"},
		{name: "missing max fee", args: []string{"start", "--max-key-fee", "--quiet"}, message: "argument --max-key-fee: expected at least one argument"},
		{name: "ambiguous abbreviation", args: []string{"start", "--stream"}, message: "ambiguous option: --stream could match"},
		{name: "toggle explicit value", args: []string{"start", "--use-upnp=false"}, message: "ignored explicit argument 'false'"},
		{name: "invalid command", args: []string{"publish"}, message: "argument COMMAND: invalid choice: 'publish'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseCommandLine(test.args)
			var usage *UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("error = %T %v, want *UsageError", err, err)
			}
			if !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %q, want substring %q", err, test.message)
			}
		})
	}
}

func TestParsedCommandLineFeedsArgumentLayer(t *testing.T) {
	parsed, err := ParseCommandLine([]string{
		"start",
		"--tcp-port", "1_000",
		"--download-timeout", "nan",
		"--known-dht-nodes", "valid.example:4444",
		"--known-dht-nodes", "invalid",
		"--max-key-fee", "1.5", "btc",
		"--coin-selection-strategy", "invalid-but-accepted",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(Options{Arguments: parsed.Settings, Environment: map[string]string{}, InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := store.Get("tcp_port"); value != 1000 {
		t.Fatalf("tcp_port = %#v", value)
	}
	if value, _ := store.Get("download_timeout"); !math.IsNaN(value.(float64)) {
		t.Fatalf("download_timeout = %#v", value)
	}
	if value, _ := store.Get("known_dht_nodes"); !reflect.DeepEqual(value, []Server{{Host: "valid.example", Port: 4444}}) {
		t.Fatalf("known_dht_nodes = %#v", value)
	}
	if value, _ := store.Get("max_key_fee"); !reflect.DeepEqual(value, map[string]any{"amount": 1.5, "currency": "BTC"}) {
		t.Fatalf("max_key_fee = %#v", value)
	}
	if value, _ := store.Get("coin_selection_strategy"); value != "invalid-but-accepted" {
		t.Fatalf("coin_selection_strategy = %#v", value)
	}
}
