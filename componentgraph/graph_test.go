package componentgraph

import (
	"errors"
	"reflect"
	"testing"
)

var expectedLegacyStartStages = [][]string{
	{Database, ExchangeRateManager, Libtorrent, UPnP},
	{BlobManager, DHT, Wallet},
	{DiskSpace, FileManager, HashAnnouncer, PeerProtocolServer, WalletServerPayments},
	{BackgroundDownloader, TrackerAnnouncer},
}

func TestLegacyStartAndStopStages(t *testing.T) {
	start, err := LegacyStartStages(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(start, expectedLegacyStartStages) {
		t.Fatalf("start stages = %#v, want %#v", start, expectedLegacyStartStages)
	}
	stop, err := LegacyStopStages(nil)
	if err != nil {
		t.Fatal(err)
	}
	wantStop := [][]string{
		{BackgroundDownloader, TrackerAnnouncer},
		{DiskSpace, FileManager, HashAnnouncer, PeerProtocolServer, WalletServerPayments},
		{BlobManager, DHT, Wallet},
		{Database, ExchangeRateManager, Libtorrent, UPnP},
	}
	if !reflect.DeepEqual(stop, wantStop) {
		t.Fatalf("stop stages = %#v, want %#v", stop, wantStop)
	}
	if !reflect.DeepEqual(start, expectedLegacyStartStages) {
		t.Fatal("StopStages mutated a previous StartStages result")
	}
}

func TestLegacySkipBehaviorDoesNotCascade(t *testing.T) {
	_, err := LegacyStartStages([]string{DHT})
	var missing *MissingDependenciesError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want *MissingDependenciesError", err, err)
	}
	want := []Dependency{{Component: HashAnnouncer, Required: DHT}}
	if !reflect.DeepEqual(missing.Dependencies, want) {
		t.Fatalf("missing dependencies = %#v, want %#v", missing.Dependencies, want)
	}

	stages, err := LegacyStartStages([]string{DHT, HashAnnouncer, "unknown", DHT})
	if err != nil {
		t.Fatal(err)
	}
	wantStages := [][]string{
		{Database, ExchangeRateManager, Libtorrent, UPnP},
		{BlobManager, Wallet},
		{DiskSpace, FileManager, PeerProtocolServer, WalletServerPayments},
		{BackgroundDownloader, TrackerAnnouncer},
	}
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("skipped stages = %#v, want %#v", stages, wantStages)
	}
}

func TestLegacyAllComponentsCanBeSkipped(t *testing.T) {
	components := LegacyComponents()
	skipped := make([]string, len(components))
	for index, component := range components {
		skipped[index] = component.Name
	}
	stages, err := LegacyStartStages(skipped)
	if err != nil {
		t.Fatal(err)
	}
	if stages == nil || len(stages) != 0 {
		t.Fatalf("all-skipped stages = %#v, want non-nil empty stages", stages)
	}
}

func TestGraphReportsMissingDependenciesDeterministically(t *testing.T) {
	graph, err := New([]Component{
		{Name: "z", DependsOn: []string{"missing-b", "missing-a"}},
		{Name: "a", DependsOn: []string{"missing-z"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.StartStages(nil)
	var missing *MissingDependenciesError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %T %v, want *MissingDependenciesError", err, err)
	}
	want := []Dependency{
		{Component: "a", Required: "missing-z"},
		{Component: "z", Required: "missing-a"},
		{Component: "z", Required: "missing-b"},
	}
	if !reflect.DeepEqual(missing.Dependencies, want) {
		t.Fatalf("missing dependencies = %#v, want %#v", missing.Dependencies, want)
	}
}

func TestGraphReportsCyclesDeterministically(t *testing.T) {
	graph, err := New([]Component{
		{Name: "c", DependsOn: []string{"a"}},
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.StartStages(nil)
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("error = %T %v, want *CycleError", err, err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(cycle.Components, want) {
		t.Fatalf("cycle components = %#v, want %#v", cycle.Components, want)
	}
}

func TestGraphRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name       string
		components []Component
	}{
		{name: "empty name", components: []Component{{Name: ""}}},
		{name: "duplicate", components: []Component{{Name: "a"}, {Name: "a"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.components)
			var definition *DefinitionError
			if !errors.As(err, &definition) {
				t.Fatalf("error = %T %v, want *DefinitionError", err, err)
			}
		})
	}
}

func TestGraphCopiesDefinitionsAndResults(t *testing.T) {
	definitions := []Component{{Name: "a"}, {Name: "b", DependsOn: []string{"a"}}}
	graph, err := New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	definitions[1].DependsOn[0] = "changed"
	components := graph.Components()
	components[1].DependsOn[0] = "changed-again"
	stages, err := graph.StartStages(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a"}, {"b"}}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %#v, want %#v", stages, want)
	}
}
