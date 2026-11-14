package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

// testEnv is a Linux environment whose data directory is a fresh temp dir.
func testEnv(t *testing.T) (Environment, string) {
	t.Helper()
	dataDir := t.TempDir()
	home := t.TempDir()
	vars := map[string]string{"LBRY_DATA_DIR": dataDir}
	return Environment{
		goos:     "linux",
		home:     home,
		getenv:   func(k string) string { return vars[k] },
		exists:   func(string) bool { return false },
		readFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
	}, dataDir
}

func openOK(t *testing.T, env Environment) Configuration {
	t.Helper()
	c, err := Open(env)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

func readFile(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, settingsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("settings file is not JSON: %v\n%s", err, data)
	}
	return m
}

func TestDefaults(t *testing.T) {
	env, dataDir := testEnv(t)
	all := openOK(t, env).All()

	if len(all) != len(definitions) {
		t.Fatalf("All() has %d settings, want %d", len(all), len(definitions))
	}
	checks := map[string]any{
		"save_files":              false,
		"save_blobs":              true,
		"blob_storage_limit":      0,
		"blob_lru_cache_size":     32,
		"download_timeout":        30.0,
		"streaming_server":        "localhost:5280",
		"coin_selection_strategy": "prefer_confirmed",
		"data_dir":                dataDir,
	}
	for key, want := range checks {
		if got := all[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if got := all["max_key_fee"]; !reflect.DeepEqual(got, map[string]any{"currency": "USD", "amount": 50.0}) {
		t.Errorf("max_key_fee = %#v", got)
	}
	// Servers are [host, port] pairs, as lbrynet's settings_get returns them.
	hubs, _ := all["lbryum_servers"].([]any)
	if len(hubs) != 12 || !reflect.DeepEqual(hubs[0], []any{"spv11.lbry.com", 50001}) {
		t.Errorf("lbryum_servers = %#v", all["lbryum_servers"])
	}
	if _, err := os.Stat(filepath.Join(dataDir, settingsFileName)); !os.IsNotExist(err) {
		t.Error("reading the defaults must not create a settings file")
	}
}

func TestEveryDefaultSurvivesItsOwnValidation(t *testing.T) {
	env, _ := testEnv(t)
	for i := range definitions {
		d := &definitions[i]
		if d.kind == kindPath {
			continue
		}
		if _, err := coerce(d, persistedForm(d.def)); err != nil {
			t.Errorf("default of %s is rejected: %v", d.name, err)
		}
	}
	_ = env
}

// persistedForm is what the default would look like after a round trip through the settings file.
func persistedForm(v any) any {
	data, _ := json.Marshal(persist(v))
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

func TestSetAndGetEachKind(t *testing.T) {
	env, _ := testEnv(t)
	c := openOK(t, env)

	cases := []struct {
		name  string
		key   string
		value any
		want  any
	}{
		{"int from a JSON number", "blob_storage_limit", float64(1024), 1024},
		{"int from a string", "blob_storage_limit", " 2048 ", 2048},
		{"float from an int", "download_timeout", 45, 45.0},
		{"float from a string", "download_timeout", "12.5", 12.5},
		{"bool", "save_files", true, true},
		{"bool from a string", "save_files", "false", false},
		{"string", "jurisdiction", "us", "us"},
		{"path", "download_dir", "/data/dl", "/data/dl"},
		{"strings", "components_to_skip", []any{"a", "b"}, []string{"a", "b"}},
		{"choice", "coin_selection_strategy", "standard", "standard"},
		{"servers from host:port", "lbryum_servers", []any{"hub.example:50001", "other.example:1"},
			[]any{[]any{"hub.example", 50001}, []any{"other.example", 1}}},
		{"servers from a JSON string", "lbryum_servers", `["hub.example:50001"]`, []any{[]any{"hub.example", 50001}}},
		{"servers from pairs", "fixed_peers", []any{[]any{"p.example", float64(5567)}}, []any{[]any{"p.example", 5567}}},
		{"no servers", "reflector_servers", []any{}, []any{}},
		{"max key fee as a dict", "max_key_fee", map[string]any{"currency": "lbc", "amount": 5.0},
			map[string]any{"currency": "LBC", "amount": 5.0}},
		{"max key fee as text", "max_key_fee", "7 usd", map[string]any{"currency": "USD", "amount": 7.0}},
		{"max key fee as a JSON dict", "max_key_fee", `{"currency": "BTC", "amount": 1}`,
			map[string]any{"currency": "BTC", "amount": 1.0}},
		{"no max key fee", "max_key_fee", nil, nil},
		{"no max key fee as text", "max_key_fee", "null", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Set(tc.key, tc.value)
			if err != nil {
				t.Fatalf("Set(%q, %#v): %v", tc.key, tc.value, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Set returned %#v, want %#v", got, tc.want)
			}
			if again := c.Get(tc.key); !reflect.DeepEqual(again, tc.want) {
				t.Errorf("Get returned %#v, want %#v", again, tc.want)
			}
		})
	}
}

func TestSetRejectsBadInput(t *testing.T) {
	env, _ := testEnv(t)
	c := openOK(t, env)

	cases := []struct {
		name  string
		key   string
		value any
		is    error
	}{
		{"unknown key", "no_such_setting", 1, ErrUnknownSetting},
		{"read-only data_dir", "data_dir", "/elsewhere", ErrReadOnly},
		{"read-only wallet_dir", "wallet_dir", "/elsewhere", ErrReadOnly},
		{"fractional int", "blob_storage_limit", 1.5, nil},
		{"text for an int", "blob_storage_limit", "lots", nil},
		{"null for an int", "blob_storage_limit", nil, nil},
		{"maybe for a bool", "save_files", "maybe", nil},
		{"number for a string", "jurisdiction", 5, nil},
		{"choice not on the list", "coin_selection_strategy", "random", nil},
		{"unknown currency", "max_key_fee", map[string]any{"currency": "XYZ", "amount": 1.0}, nil},
		{"negative fee", "max_key_fee", map[string]any{"currency": "USD", "amount": -1.0}, nil},
		{"fee with extra keys", "max_key_fee", map[string]any{"currency": "USD", "amount": 1.0, "x": 1}, nil},
		{"server without a port", "lbryum_servers", []any{"hub.example"}, nil},
		{"server with a bad port", "lbryum_servers", []any{"hub.example:99999"}, nil},
		{"servers that aren't a list", "lbryum_servers", "hub.example:50001", nil},
		{"strings with a number", "wallets", []any{"ok", 3}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := c.Get(tc.key)
			_, err := c.Set(tc.key, tc.value)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !IsUserError(err) {
				t.Errorf("a bad request must be a user error, got %v", err)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error = %v, want %v", err, tc.is)
			}
			if after := c.Get(tc.key); !reflect.DeepEqual(after, before) {
				t.Errorf("a rejected Set changed the value: %#v -> %#v", before, after)
			}
		})
	}
}

func TestPersistsAcrossRestarts(t *testing.T) {
	env, dataDir := testEnv(t)
	c := openOK(t, env)
	if _, err := c.Set("blob_storage_limit", 4096); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Set("lbryum_servers", []any{"hub.example:50001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Set("max_key_fee", nil); err != nil {
		t.Fatal(err)
	}

	// Only what differs from the defaults is written, and servers are "host:port" strings.
	file := readFile(t, dataDir)
	if len(file) != 3 {
		t.Errorf("file has %d keys, want 3: %v", len(file), file)
	}
	if got := file["lbryum_servers"]; !reflect.DeepEqual(got, []any{"hub.example:50001"}) {
		t.Errorf("lbryum_servers in the file = %#v", got)
	}
	if v, present := file["max_key_fee"]; !present || v != nil {
		t.Errorf("a cleared max_key_fee must be stored as null, got %v (present=%v)", v, present)
	}

	again := openOK(t, env)
	if got := again.Get("blob_storage_limit"); got != 4096 {
		t.Errorf("after restart blob_storage_limit = %#v", got)
	}
	if got := again.Get("lbryum_servers"); !reflect.DeepEqual(got, []any{[]any{"hub.example", 50001}}) {
		t.Errorf("after restart lbryum_servers = %#v", got)
	}
	if got := again.Get("max_key_fee"); got != nil {
		t.Errorf("after restart max_key_fee = %#v, want nil", got)
	}
}

func TestClearGoesBackToTheDefault(t *testing.T) {
	env, dataDir := testEnv(t)
	c := openOK(t, env)
	_, _ = c.Set("save_files", true)
	_, _ = c.Set("jurisdiction", "us")

	got, err := c.Clear("save_files")
	if err != nil || got != false {
		t.Fatalf("Clear = %#v, %v", got, err)
	}
	file := readFile(t, dataDir)
	if _, present := file["save_files"]; present {
		t.Error("a cleared setting must be removed from the file")
	}
	if file["jurisdiction"] != "us" {
		t.Error("clearing one setting must not touch the others")
	}
	if _, err := c.Clear("nope"); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("Clear of an unknown key: %v", err)
	}
	if _, err := c.Clear("data_dir"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Clear of data_dir: %v", err)
	}
}

func TestUnknownAndInvalidEntriesInTheFileAreKept(t *testing.T) {
	env, dataDir := testEnv(t)
	body := `{"from_a_newer_daemon": {"x": 1}, "blob_storage_limit": "lots", "save_files": true, "data_dir": "/ignored"}`
	if err := os.WriteFile(filepath.Join(dataDir, settingsFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(env)
	if err == nil {
		t.Error("an invalid value in the file should be reported")
	}
	if c.Get("save_files") != true {
		t.Error("valid values must still load")
	}
	if c.Get("blob_storage_limit") != 0 {
		t.Error("an invalid value must fall back to the default")
	}
	if c.Get("data_dir") != dataDir {
		t.Error("data_dir in the file must not override the real one")
	}

	if _, err := c.Set("jurisdiction", "us"); err != nil {
		t.Fatal(err)
	}
	file := readFile(t, dataDir)
	for _, key := range []string{"from_a_newer_daemon", "blob_storage_limit", "data_dir"} {
		if _, present := file[key]; !present {
			t.Errorf("%s was dropped from the file", key)
		}
	}
	if file["blob_storage_limit"] != "lots" {
		t.Errorf("an invalid value must be written back untouched, got %v", file["blob_storage_limit"])
	}
}

func TestCorruptFileIsMovedAside(t *testing.T) {
	env, dataDir := testEnv(t)
	path := filepath.Join(dataDir, settingsFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(env)
	if err == nil {
		t.Error("a corrupt file should be reported")
	}
	if c.Get("save_blobs") != true {
		t.Error("the defaults must still be usable")
	}
	if saved, readErr := os.ReadFile(path + ".bad"); readErr != nil || string(saved) != "{not json" {
		t.Errorf("the corrupt file was not kept: %v %q", readErr, saved)
	}
	if _, err := c.Set("save_files", true); err != nil {
		t.Fatalf("settings must be writable after a corrupt file: %v", err)
	}
	if readFile(t, dataDir)["save_files"] != true {
		t.Error("the new file was not written")
	}
}

func TestFailedWriteIsUndone(t *testing.T) {
	env, _ := testEnv(t)
	// A data directory that can't be created: its parent is a regular file.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"LBRY_DATA_DIR": filepath.Join(blocker, "data")}
	env.getenv = func(k string) string { return vars[k] }
	// Reading the settings file fails too (its parent isn't a directory); the daemon still gets a
	// usable Configuration with the defaults.
	c, openErr := Open(env)
	if openErr == nil {
		t.Error("expected Open to report that the file can't be read")
	}

	if _, err := c.Set("save_files", true); err == nil {
		t.Fatal("expected the write to fail")
	} else if IsUserError(err) {
		t.Errorf("a write failure is the daemon's problem, not the caller's: %v", err)
	}
	if c.Get("save_files") != false {
		t.Error("a failed Set must not leave the new value in memory")
	}

	if _, err := c.Clear("save_files"); err == nil {
		t.Error("expected Clear to fail to write too")
	}
}

func TestCopiesShareState(t *testing.T) {
	env, _ := testEnv(t)
	a := openOK(t, env)
	b := a
	if _, err := a.Set("save_files", true); err != nil {
		t.Fatal(err)
	}
	if b.Get("save_files") != true {
		t.Error("a copy of Configuration must see changes made through the original")
	}
}

func TestConcurrentUse(t *testing.T) {
	env, dataDir := testEnv(t)
	c := openOK(t, env)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := c.Set("blob_storage_limit", i); err != nil {
				t.Error(err)
			}
			_ = c.All()
			_ = c.Get("blob_storage_limit")
		}(i)
	}
	wg.Wait()

	final := c.Get("blob_storage_limit").(int)
	if got := readFile(t, dataDir)["blob_storage_limit"]; got != float64(final) {
		t.Errorf("file has %v but memory has %d", got, final)
	}
	if entries, _ := filepath.Glob(filepath.Join(dataDir, "*.tmp")); len(entries) != 0 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestGetUnknown(t *testing.T) {
	env, _ := testEnv(t)
	c := openOK(t, env)
	if c.Get("nope") != nil {
		t.Error("Get of an unknown key must be nil")
	}
	if _, ok := c.Lookup("nope"); ok {
		t.Error("Lookup of an unknown key must say so")
	}
	if v, ok := c.Lookup("save_files"); !ok || v != false {
		t.Errorf("Lookup(save_files) = %v, %v", v, ok)
	}
}

func TestDefaultDirectories(t *testing.T) {
	fake := func(goos, home string, vars map[string]string, existing ...string) Environment {
		has := map[string]bool{}
		for _, p := range existing {
			has[p] = true
		}
		return Environment{
			goos:     goos,
			home:     home,
			getenv:   func(k string) string { return vars[k] },
			exists:   func(p string) bool { return has[p] },
			readFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		}
	}
	join := filepath.Join

	t.Run("linux, new install", func(t *testing.T) {
		data, wallet, dl := defaultDirs(fake("linux", "/home/u", nil))
		if data != join("/home/u", ".local", "share", "lbry", "lbrynet") ||
			wallet != join("/home/u", ".local", "share", "lbry", "lbryum") ||
			dl != join("/home/u", "Downloads") {
			t.Errorf("got %s, %s, %s", data, wallet, dl)
		}
	})
	t.Run("linux honours XDG_DATA_HOME", func(t *testing.T) {
		data, _, _ := defaultDirs(fake("linux", "/home/u", map[string]string{"XDG_DATA_HOME": "/xdg"}))
		if data != join("/xdg", "lbry", "lbrynet") {
			t.Errorf("got %s", data)
		}
	})
	t.Run("linux, existing ~/.lbrynet keeps being used", func(t *testing.T) {
		data, wallet, _ := defaultDirs(fake("linux", "/home/u", nil, join("/home/u", ".lbrynet")))
		if data != join("/home/u", ".lbrynet") || wallet != join("/home/u", ".lbryum") {
			t.Errorf("got %s, %s", data, wallet)
		}
	})
	t.Run("linux reads the XDG download dir", func(t *testing.T) {
		env := fake("linux", "/home/u", nil)
		env.readFile = func(string) ([]byte, error) {
			return []byte("XDG_DESKTOP_DIR=\"$HOME/Desktop\"\nXDG_DOWNLOAD_DIR=\"$HOME/Got\"\n"), nil
		}
		if _, _, dl := defaultDirs(env); dl != join("/home/u", "Got") {
			t.Errorf("got %s", dl)
		}
	})
	t.Run("windows, new install", func(t *testing.T) {
		data, wallet, _ := defaultDirs(fake("windows", `C:\Users\u`, map[string]string{"APPDATA": `C:\R`, "LOCALAPPDATA": `C:\L`}))
		if data != join(`C:\L`, "lbry", "lbrynet") || wallet != join(`C:\L`, "lbry", "lbryum") {
			t.Errorf("got %s, %s", data, wallet)
		}
	})
	t.Run("windows, existing roaming folder", func(t *testing.T) {
		data, _, _ := defaultDirs(fake("windows", `C:\Users\u`, map[string]string{"APPDATA": `C:\R`}, join(`C:\R`, "lbrynet")))
		if data != join(`C:\R`, "lbrynet") {
			t.Errorf("got %s", data)
		}
	})
	t.Run("macOS", func(t *testing.T) {
		data, wallet, dl := defaultDirs(fake("darwin", "/Users/u", nil))
		if data != join("/Users/u", "Library", "Application Support", "LBRY") ||
			wallet != join("/Users/u", ".lbryum") || dl != join("/Users/u", "Downloads") {
			t.Errorf("got %s, %s, %s", data, wallet, dl)
		}
	})
	t.Run("environment variables override", func(t *testing.T) {
		data, wallet, dl := defaultDirs(fake("linux", "/home/u", map[string]string{
			"LBRY_DATA_DIR": "/d", "LBRY_WALLET_DIR": "/w", "LBRY_DOWNLOAD_DIR": "/dl",
		}))
		if data != "/d" || wallet != "/w" || dl != "/dl" {
			t.Errorf("got %s, %s, %s", data, wallet, dl)
		}
	})
}

func TestPathSettingsExpandHomeAndVariables(t *testing.T) {
	env, _ := testEnv(t)
	vars := map[string]string{"LBRY_DATA_DIR": env.getenv("LBRY_DATA_DIR"), "DRIVE": "/mnt/big"}
	env.getenv = func(k string) string { return vars[k] }
	c := openOK(t, env)

	if _, err := c.Set("download_dir", "~/dl"); err != nil {
		t.Fatal(err)
	}
	if got := c.Get("download_dir"); got != filepath.Join(env.home, "dl") {
		t.Errorf("~ not expanded: %v", got)
	}
	if _, err := c.Set("download_dir", "$DRIVE/dl"); err != nil {
		t.Fatal(err)
	}
	if got := c.Get("download_dir"); got != "/mnt/big/dl" {
		t.Errorf("$VAR not expanded: %v", got)
	}
	// What is stored is what the user typed, so it can be edited and moved between machines.
	if got := readFile(t, vars["LBRY_DATA_DIR"])["download_dir"]; got != "$DRIVE/dl" {
		t.Errorf("file has %v", got)
	}
}

func TestSettingNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range definitions {
		if seen[d.name] {
			t.Errorf("duplicate setting %s", d.name)
		}
		seen[d.name] = true
		if d.kind == kindChoice && len(d.choices) == 0 {
			t.Errorf("%s is a choice with no choices", d.name)
		}
	}
}
