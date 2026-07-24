package componentgraph

import (
	"fmt"
	"sort"
	"strings"
)

const (
	Database             = "database"
	BlobManager          = "blob_manager"
	Wallet               = "wallet"
	WalletServerPayments = "wallet_server_payments"
	DHT                  = "dht"
	HashAnnouncer        = "hash_announcer"
	FileManager          = "file_manager"
	DiskSpace            = "disk_space"
	BackgroundDownloader = "background_downloader"
	PeerProtocolServer   = "peer_protocol_server"
	UPnP                 = "upnp"
	ExchangeRateManager  = "exchange_rate_manager"
	TrackerAnnouncer     = "tracker_announcer_component"
	Libtorrent           = "libtorrent_component"
)

type Component struct {
	Name      string
	DependsOn []string
}

type Dependency struct {
	Component string
	Required  string
}

type MissingDependenciesError struct {
	Dependencies []Dependency
}

func (err *MissingDependenciesError) Error() string {
	parts := make([]string, len(err.Dependencies))
	for index, dependency := range err.Dependencies {
		parts[index] = fmt.Sprintf("%s requires %s", dependency.Component, dependency.Required)
	}
	return "component graph has missing dependencies: " + strings.Join(parts, ", ")
}

type CycleError struct {
	Components []string
}

func (err *CycleError) Error() string {
	return "component graph contains a dependency cycle among: " + strings.Join(err.Components, ", ")
}

type DefinitionError struct {
	Message string
}

func (err *DefinitionError) Error() string {
	return "invalid component graph: " + err.Message
}

type Graph struct {
	components map[string]Component
}

func New(components []Component) (*Graph, error) {
	graph := &Graph{components: make(map[string]Component, len(components))}
	for _, component := range components {
		if component.Name == "" {
			return nil, &DefinitionError{Message: "component name is empty"}
		}
		if _, exists := graph.components[component.Name]; exists {
			return nil, &DefinitionError{Message: fmt.Sprintf("component %q is defined more than once", component.Name)}
		}
		component.DependsOn = append([]string{}, component.DependsOn...)
		graph.components[component.Name] = component
	}
	return graph, nil
}

func (graph *Graph) Components() []Component {
	if graph == nil {
		return nil
	}
	components := make([]Component, 0, len(graph.components))
	for _, component := range graph.components {
		component.DependsOn = append([]string{}, component.DependsOn...)
		components = append(components, component)
	}
	sort.Slice(components, func(left, right int) bool {
		return components[left].Name < components[right].Name
	})
	return components
}

func (graph *Graph) StartStages(skipped []string) ([][]string, error) {
	if graph == nil {
		return nil, &DefinitionError{Message: "graph is nil"}
	}
	skipSet := make(map[string]struct{}, len(skipped))
	for _, name := range skipped {
		skipSet[name] = struct{}{}
	}
	active := make(map[string]Component, len(graph.components))
	for name, component := range graph.components {
		if _, skip := skipSet[name]; !skip {
			active[name] = component
		}
	}

	missing := make([]Dependency, 0)
	for name, component := range active {
		for _, required := range component.DependsOn {
			if _, exists := active[required]; !exists {
				missing = append(missing, Dependency{Component: name, Required: required})
			}
		}
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(left, right int) bool {
			if missing[left].Component == missing[right].Component {
				return missing[left].Required < missing[right].Required
			}
			return missing[left].Component < missing[right].Component
		})
		return nil, &MissingDependenciesError{Dependencies: missing}
	}

	remaining := make(map[string]Component, len(active))
	for name, component := range active {
		remaining[name] = component
	}
	staged := make(map[string]struct{}, len(active))
	stages := make([][]string, 0)
	for len(remaining) > 0 {
		stage := make([]string, 0)
		for name, component := range remaining {
			ready := true
			for _, required := range component.DependsOn {
				if _, exists := staged[required]; !exists {
					ready = false
					break
				}
			}
			if ready {
				stage = append(stage, name)
			}
		}
		if len(stage) == 0 {
			cycle := cycleMembers(remaining)
			sort.Strings(cycle)
			return nil, &CycleError{Components: cycle}
		}
		sort.Strings(stage)
		stages = append(stages, stage)
		for _, name := range stage {
			delete(remaining, name)
			staged[name] = struct{}{}
		}
	}
	return stages, nil
}

func cycleMembers(remaining map[string]Component) []string {
	cycle := make([]string, 0, len(remaining))
	for name := range remaining {
		if dependencyReaches(name, name, remaining, make(map[string]struct{})) {
			cycle = append(cycle, name)
		}
	}
	return cycle
}

func dependencyReaches(
	target, current string,
	remaining map[string]Component,
	visited map[string]struct{},
) bool {
	if _, seen := visited[current]; seen {
		return false
	}
	visited[current] = struct{}{}
	for _, dependency := range remaining[current].DependsOn {
		if dependency == target {
			return true
		}
		if _, exists := remaining[dependency]; exists && dependencyReaches(target, dependency, remaining, visited) {
			return true
		}
	}
	return false
}

func (graph *Graph) StopStages(skipped []string) ([][]string, error) {
	stages, err := graph.StartStages(skipped)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(stages)-1; left < right; left, right = left+1, right-1 {
		stages[left], stages[right] = stages[right], stages[left]
	}
	return stages, nil
}
