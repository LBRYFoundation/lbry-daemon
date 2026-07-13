package spv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestKnownHubsPersistenceOrderingAndFirstInsertionWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), KnownHubsFilename)
	known, err := OpenKnownHubs(path)
	if err != nil {
		t.Fatal(err)
	}
	if known.Exists() || known.Len() != 0 || len(known.Servers()) != 0 {
		t.Fatalf("new known hubs = exists %t, snapshot %#v", known.Exists(), known.Snapshot())
	}
	if added, err := known.SetString("new.hub.io:99", HubDetails{"jurisdiction": "us"}); err != nil || !added {
		t.Fatalf("first set = %t, %v", added, err)
	}
	if added, err := known.SetString("new.hub.io:99", HubDetails{"jurisdiction": "changed"}); err != nil || added {
		t.Fatalf("duplicate set = %t, %v", added, err)
	}
	if added, err := known.SetString("not-a-hub", HubDetails{}); err != nil || added {
		t.Fatalf("malformed ignored set = %t, %v", added, err)
	}
	if _, err := known.SetString("bad:port", HubDetails{}); !errors.Is(err, ErrInvalidHub) {
		t.Fatalf("bad port error = %v", err)
	}
	_, _ = known.SetString("any.hub.io:99", HubDetails{})
	_, _ = known.SetString("oth.hub.io:99", HubDetails{"jurisdiction": "other"})
	if err := known.Save(); err != nil {
		t.Fatal(err)
	}
	wantYAML := "any.hub.io:99: {}\nnew.hub.io:99:\n  jurisdiction: us\noth.hub.io:99:\n  jurisdiction: other\n"
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != wantYAML {
		t.Fatalf("known hubs YAML = %q, %v; want %q", contents, err, wantYAML)
	}

	reloaded, err := OpenKnownHubs(path)
	if err != nil {
		t.Fatal(err)
	}
	wantServers := []Server{
		{Host: "any.hub.io", Port: 99},
		{Host: "new.hub.io", Port: 99},
		{Host: "oth.hub.io", Port: 99},
	}
	if !reflect.DeepEqual(reloaded.Servers(), wantServers) {
		t.Fatalf("reloaded servers = %#v, want %#v", reloaded.Servers(), wantServers)
	}
	if details := reloaded.Snapshot()[1].Details; details["jurisdiction"] != "us" {
		t.Fatalf("first details were replaced: %#v", details)
	}
}

func TestKnownHubsFilterUsesPythonORAndMatchNone(t *testing.T) {
	known := NewMemoryKnownHubs()
	_, _ = known.SetString("new.hub.io:99", HubDetails{"jurisdiction": "us", "tier": "paid"})
	_, _ = known.SetString("any.hub.io:99", HubDetails{})
	_, _ = known.SetString("oth.hub.io:99", HubDetails{"jurisdiction": "other"})
	if got := known.Filter(false, nil); len(got) != 3 {
		t.Fatalf("unfiltered hubs = %#v", got)
	}
	wantUS := []Hub{{
		Server:  Server{Host: "new.hub.io", Port: 99},
		Details: HubDetails{"jurisdiction": "us", "tier": "paid"},
	}}
	if got := known.Filter(false, HubDetails{"jurisdiction": "us"}); !reflect.DeepEqual(got, wantUS) {
		t.Fatalf("jurisdiction filter = %#v, want %#v", got, wantUS)
	}
	if got := known.Filter(false, HubDetails{"jurisdiction": "missing", "tier": "paid"}); !reflect.DeepEqual(got, wantUS) {
		t.Fatalf("OR filter = %#v, want %#v", got, wantUS)
	}
	if got := known.Filter(true, HubDetails{"jurisdiction": "us"}); len(got) != 2 || got[1].Server.Host != "any.hub.io" {
		t.Fatalf("match-none filter = %#v", got)
	}
	numeric := NewMemoryKnownHubs()
	numeric.Set(Server{Host: "number", Port: 1}, HubDetails{"value": 1})
	for _, constraint := range []any{1.0, true} {
		if got := numeric.Filter(false, HubDetails{"value": constraint}); len(got) != 1 {
			t.Fatalf("Python numeric filter for %#v = %#v", constraint, got)
		}
	}
}

func TestKnownHubsCountryUpdatesRemainMemoryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), KnownHubsFilename)
	known, err := OpenKnownHubs(path)
	if err != nil {
		t.Fatal(err)
	}
	server := Server{Host: "hub.example", Port: 50001}
	known.Set(server, HubDetails{})
	if err := known.Save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	known.UpdateCountry(server, "US")
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("country update unexpectedly saved file: before %q after %q", before, after)
	}
	if country := known.Snapshot()[0].Details["country"]; country != "US" {
		t.Fatalf("in-memory country = %#v", country)
	}
}

func TestKnownHubsAddStringsAndMalformedFiles(t *testing.T) {
	known := NewMemoryKnownHubs()
	added, err := known.AddStrings([]string{"one.example:1", "", "one.example:1", "two.example:2"})
	if err != nil || !added || known.Len() != 2 {
		t.Fatalf("add hubs = %t, %v, snapshot %#v", added, err, known.Snapshot())
	}
	for name, contents := range map[string]string{
		"empty":          "",
		"sequence":       "- hub.example:1\n",
		"scalar details": "hub.example:1: invalid\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), KnownHubsFilename)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenKnownHubs(path); !errors.Is(err, ErrInvalidKnownHubs) {
				t.Fatalf("malformed load error = %v", err)
			}
		})
	}
}

func TestKnownHubsConcurrentFirstInsertionIsStable(t *testing.T) {
	known := NewMemoryKnownHubs()
	var workers sync.WaitGroup
	for index := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			known.Set(Server{Host: "same.example", Port: 1}, HubDetails{"writer": index})
		}()
	}
	workers.Wait()
	if known.Len() != 1 {
		t.Fatalf("concurrent known hubs = %#v", known.Snapshot())
	}
	writer, ok := known.Snapshot()[0].Details["writer"].(int)
	if !ok || writer < 0 || writer >= 32 {
		t.Fatalf("retained writer = %#v", known.Snapshot()[0].Details["writer"])
	}
}

func TestKnownHubsDeepClonesDetailsAndLoadsLastExactYAMLKey(t *testing.T) {
	details := HubDetails{"nested": map[string]any{"values": []any{"first"}}}
	known := NewMemoryKnownHubs()
	known.Set(Server{Host: "hub", Port: 1}, details)
	details["nested"].(map[string]any)["values"].([]any)[0] = "caller mutation"
	snapshot := known.Snapshot()
	values := snapshot[0].Details["nested"].(map[string]any)["values"].([]any)
	if values[0] != "first" {
		t.Fatalf("stored nested details = %#v", snapshot)
	}
	values[0] = "snapshot mutation"
	if got := known.Snapshot()[0].Details["nested"].(map[string]any)["values"].([]any)[0]; got != "first" {
		t.Fatalf("snapshot mutated stored details: %#v", got)
	}

	path := filepath.Join(t.TempDir(), KnownHubsFilename)
	contents := "hub:1:\n  country: first\nhub:1:\n  country: last\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenKnownHubs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Snapshot()[0].Details["country"]; got != "last" {
		t.Fatalf("duplicate YAML key country = %#v, want last", got)
	}
}
