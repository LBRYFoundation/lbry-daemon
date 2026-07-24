package legacycli

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestManifestCoversPinnedCommandSurface(t *testing.T) {
	manifest, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.commands) != 94 || len(ActiveMethods()) != 93 {
		t.Fatalf("commands = %d, active = %d", len(manifest.commands), len(ActiveMethods()))
	}
	if len(manifest.groups) != 19 || len(manifest.root) != 8 {
		t.Fatalf("groups = %d, root commands = %d", len(manifest.groups), len(manifest.root))
	}
	deprecated := manifest.commands["channel_new"]
	if deprecated.Replacement == nil || *deprecated.Replacement != "channel_create" {
		t.Fatalf("channel_new replacement = %#v", deprecated.Replacement)
	}
	for _, token := range []string{"stop", "publish", "account", "wallet"} {
		if !IsCommandToken(token) {
			t.Errorf("IsCommandToken(%q) = false", token)
		}
	}
	for _, token := range []string{"start", "channel_new", "wat"} {
		if IsCommandToken(token) {
			t.Errorf("IsCommandToken(%q) = true", token)
		}
	}
}

func TestEveryPinnedCommandDocHasGoDocoptGrammar(t *testing.T) {
	manifest, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for method, definition := range manifest.commands {
		if definition.Replacement != nil {
			continue
		}
		t.Run(method, func(t *testing.T) {
			_, usage, err := parseDocopt(definition.Doc, []string{})
			if err != nil && usage == "" {
				t.Fatalf("docopt language error: %v", err)
			}
		})
	}
}

func TestEveryPinnedCommandResolvesHelp(t *testing.T) {
	manifest, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	for method, definition := range manifest.commands {
		arguments := []string{definition.Name, "--help"}
		if definition.Group != nil {
			arguments = append([]string{*definition.Group}, arguments...)
		}
		result, err := Parse(arguments)
		if err != nil {
			t.Errorf("%s help: %v", method, err)
			continue
		}
		want := definition
		if definition.Replacement != nil {
			want = manifest.commands[*definition.Replacement]
		}
		if result.Invocation != nil || result.Help != want.Doc {
			t.Errorf("%s help did not resolve replacement documentation", method)
		}
	}
}

func TestParseRootAndGroupedCommands(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		method string
		check  func(*testing.T, map[string]any)
	}{
		{
			name:   "root protected strings and repeated values",
			args:   []string{"publish", "123", "--title=true", "--tags=1", "--tags=false"},
			method: "publish",
			check: func(t *testing.T, params map[string]any) {
				assertParam(t, params, "name", "123")
				assertParam(t, params, "title", true)
				assertParam(t, params, "tags", []string{"1", "false"})
				assertParam(t, params, "languages", []string{})
				assertParam(t, params, "preview", false)
				assertParam(t, params, "blocking", false)
			},
		},
		{
			name:   "group scalar normalization",
			args:   []string{"account", "add", "123", "--seed=001", "--single_key"},
			method: "account_add",
			check: func(t *testing.T, params map[string]any) {
				assertParam(t, params, "account_name", 123)
				assertParam(t, params, "seed", 1)
				assertParam(t, params, "single_key", true)
			},
		},
		{
			name:   "settings values normalize",
			args:   []string{"settings", "set", "udp_port", "5000"},
			method: "settings_set",
			check: func(t *testing.T, params map[string]any) {
				assertParam(t, params, "key", "udp_port")
				assertParam(t, params, "value", 5000)
			},
		},
		{
			name:   "variadic positionals stay strings",
			args:   []string{"resolve", "123", "true"},
			method: "resolve",
			check: func(t *testing.T, params map[string]any) {
				assertParam(t, params, "urls", []string{"123", "true"})
				assertParam(t, params, "include_purchase_receipt", false)
			},
		},
		{
			name:   "URI stays string",
			args:   []string{"get", "123"},
			method: "get",
			check: func(t *testing.T, params map[string]any) {
				assertParam(t, params, "uri", "123")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Parse(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if result.Invocation == nil || result.Invocation.Method != test.method {
				t.Fatalf("invocation = %#v, want method %s", result.Invocation, test.method)
			}
			test.check(t, result.Invocation.Params)
		})
	}
}

func TestParseDeprecatedCommand(t *testing.T) {
	result, err := Parse([]string{"channel", "new", "@name", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Notice != "channel_new is deprecated, using channel_create." {
		t.Fatalf("notice = %q", result.Notice)
	}
	if result.Invocation == nil || result.Invocation.RequestedMethod != "channel_new" || result.Invocation.Method != "channel_create" {
		t.Fatalf("invocation = %#v", result.Invocation)
	}
	assertParam(t, result.Invocation.Params, "name", "@name")
	assertParam(t, result.Invocation.Params, "bid", 1)

	help, err := Parse([]string{"channel", "new", "--help"})
	if err != nil {
		t.Fatal(err)
	}
	if help.Notice != result.Notice || !strings.Contains(help.Help, "channel_create") {
		t.Fatalf("deprecated help = %#v", help)
	}
}

func TestParseHelpAndUsageErrors(t *testing.T) {
	group, err := Parse([]string{"account"})
	if err != nil || !strings.Contains(group.Help, "balance") {
		t.Fatalf("account help = %#v, %v", group, err)
	}
	command, err := Parse([]string{"publish", "anything", "--help"})
	if err != nil || !strings.Contains(command.Help, "Create or replace a stream claim") {
		t.Fatalf("publish help = %#v, %v", command, err)
	}

	_, err = Parse([]string{"publish"})
	var usage *UsageError
	if !errors.As(err, &usage) || !strings.HasPrefix(usage.Message, "Usage:") {
		t.Fatalf("publish error = %T %v", err, err)
	}
	_, err = Parse([]string{"account", "wat"})
	if !errors.As(err, &usage) || !strings.Contains(usage.Help, "account") {
		t.Fatalf("unknown group command error = %T %v", err, err)
	}
}

func TestNormalizeLargeUnsignedInteger(t *testing.T) {
	value := normalizeCLIValue("000999999999999999999999999999999999", "amount")
	if value != json.Number("999999999999999999999999999999999") {
		t.Fatalf("large number = %#v", value)
	}
	if zero := normalizeCLIValue(strings.Repeat("0", 100), "amount"); zero != 0 {
		t.Fatalf("large zero = %#v", zero)
	}
}

func TestNormalizeUnicodeDecimalDigitsLikePythonInt(t *testing.T) {
	for _, test := range []struct {
		value string
		want  any
	}{
		{"\u0661\u0662\u0663", 123},
		{"\uff10\uff10\uff19", 9},
		{"\U0001d7d8\U0001d7d9", 1},
	} {
		if got := normalizeCLIValue(test.value, "page"); got != test.want {
			t.Errorf("normalizeCLIValue(%q) = %#v, want %#v", test.value, got, test.want)
		}
	}
	if got := normalizeCLIValue("\u00b2", "page"); got != "\u00b2" {
		t.Fatalf("non-decimal isdigit character = %#v", got)
	}
	if got := normalizeCLIValue("\u0661\u0662", "uri"); got != "\u0661\u0662" {
		t.Fatalf("protected Unicode numeric string = %#v", got)
	}
}

func TestPinnedDocoptEdgeCases(t *testing.T) {
	t.Run("unique long option prefix", func(t *testing.T) {
		result, err := Parse([]string{"wallet", "list", "--page_s=2"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "page_size", 2)
	})

	t.Run("end marker remains positional", func(t *testing.T) {
		result, err := Parse([]string{"get", "--", "-uri"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "uri", "--")
		assertParam(t, result.Invocation.Params, "file_name", "-uri")
		afterMarker, err := Parse([]string{"get", "uri", "--", "--help"})
		if err != nil || afterMarker.Help != "" {
			t.Fatalf("help after -- = %#v, %v", afterMarker, err)
		}
		assertParam(t, afterMarker.Invocation.Params, "file_name", "--")
		assertParam(t, afterMarker.Invocation.Params, "download_directory", "--help")
	})

	t.Run("greedy optional positional does not backtrack", func(t *testing.T) {
		_, err := Parse([]string{"account", "fund", "1"})
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("account fund error = %T %v", err, err)
		}
		result, err := Parse([]string{"account", "fund", "--amount=1"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "amount", 1)
	})

	t.Run("mixed positional and option alternative fails", func(t *testing.T) {
		_, err := Parse([]string{"get", "x", "z", "--file_name=y"})
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("get alternative error = %T %v", err, err)
		}
	})

	t.Run("malformed pinned trend options remain accepted", func(t *testing.T) {
		result, err := Parse([]string{"claim", "search", "--trending_global=3", "--trending_score=4"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "trending_global", 3)
		assertParam(t, result.Invocation.Params, "trending_score", 4)
	})

	t.Run("nested alternatives", func(t *testing.T) {
		result, err := Parse([]string{"txo", "list", "--is_my_output", "--is_not_my_input"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "is_my_output", true)
		assertParam(t, result.Invocation.Params, "is_not_my_input", true)
		assertParam(t, result.Invocation.Params, "is_my_input_or_output", false)
	})

	t.Run("usage-only aliases", func(t *testing.T) {
		result, err := Parse([]string{"peer", "ping", "--node_id=n", "--address=host", "--port=4444"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "node_id", "n")
		assertParam(t, result.Invocation.Params, "address", "host")
		assertParam(t, result.Invocation.Params, "port", 4444)
	})

	t.Run("options-section-only alias cannot satisfy usage", func(t *testing.T) {
		_, err := Parse([]string{"resolve", "--urls=x"})
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("resolve alias error = %T %v", err, err)
		}
	})

	t.Run("wallet change account typo still accepts value", func(t *testing.T) {
		result, err := Parse([]string{"wallet", "send", "1.0", "bAddress", "--change_account_id=abc"})
		if err != nil {
			t.Fatal(err)
		}
		assertParam(t, result.Invocation.Params, "change_account_id", "abc")
	})
}

func assertParam(t *testing.T, params map[string]any, name string, want any) {
	t.Helper()
	got, exists := params[name]
	if !exists || !reflect.DeepEqual(got, want) {
		t.Fatalf("param %s = %#v, exists %v, want %#v", name, got, exists, want)
	}
}
