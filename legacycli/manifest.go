package legacycli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed manifest.json
var manifestJSON []byte

type commandDefinition struct {
	Method      string  `json:"method"`
	Name        string  `json:"name"`
	Group       *string `json:"group"`
	Doc         string  `json:"doc"`
	Replacement *string `json:"replacement"`
}

type commandManifest struct {
	Groups   map[string]string   `json:"groups"`
	Commands []commandDefinition `json:"commands"`
}

type loadedManifest struct {
	groups   map[string]string
	commands map[string]commandDefinition
	root     map[string]commandDefinition
	grouped  map[string]map[string]commandDefinition
}

var (
	manifestOnce sync.Once
	manifestData loadedManifest
	manifestErr  error
)

func loadManifest() (loadedManifest, error) {
	manifestOnce.Do(func() {
		var decoded commandManifest
		if err := json.Unmarshal(manifestJSON, &decoded); err != nil {
			manifestErr = fmt.Errorf("decode embedded CLI manifest: %w", err)
			return
		}
		manifestData = loadedManifest{
			groups:   decoded.Groups,
			commands: make(map[string]commandDefinition, len(decoded.Commands)),
			root:     make(map[string]commandDefinition),
			grouped:  make(map[string]map[string]commandDefinition, len(decoded.Groups)),
		}
		for name := range decoded.Groups {
			manifestData.grouped[name] = make(map[string]commandDefinition)
		}
		for _, command := range decoded.Commands {
			if command.Method == "" || command.Name == "" || command.Doc == "" {
				manifestErr = fmt.Errorf("embedded CLI manifest contains an incomplete command: %#v", command)
				return
			}
			if _, exists := manifestData.commands[command.Method]; exists {
				manifestErr = fmt.Errorf("duplicate CLI method %q", command.Method)
				return
			}
			manifestData.commands[command.Method] = command
			if command.Group == nil {
				if _, exists := manifestData.root[command.Name]; exists {
					manifestErr = fmt.Errorf("duplicate root CLI command %q", command.Name)
					return
				}
				manifestData.root[command.Name] = command
				continue
			}
			commands, exists := manifestData.grouped[*command.Group]
			if !exists {
				manifestErr = fmt.Errorf("CLI method %q has unknown group %q", command.Method, *command.Group)
				return
			}
			if _, exists := commands[command.Name]; exists {
				manifestErr = fmt.Errorf("duplicate %s CLI command %q", *command.Group, command.Name)
				return
			}
			commands[command.Name] = command
		}
		if len(manifestData.commands) != 94 {
			manifestErr = fmt.Errorf("embedded CLI manifest has %d commands, want 94", len(manifestData.commands))
			return
		}
		for method, command := range manifestData.commands {
			if command.Replacement == nil {
				continue
			}
			replacement, exists := manifestData.commands[*command.Replacement]
			if !exists || replacement.Replacement != nil || *command.Replacement == method {
				manifestErr = fmt.Errorf("invalid replacement %q for deprecated CLI method %q", *command.Replacement, method)
				return
			}
		}
	})
	return manifestData, manifestErr
}

// RootHelp returns a concise help page for the embedded legacy command set.
func RootHelp() string {
	manifest, err := loadManifest()
	if err != nil {
		return err.Error()
	}
	var help strings.Builder
	help.WriteString("Usage:  lbrynet COMMAND\n\n")
	help.WriteString("Grouped Commands:\n")
	groups := sortedKeys(manifest.groups)
	for _, group := range groups {
		fmt.Fprintf(&help, "  %-14s %s\n", group, firstLine(manifest.groups[group]))
	}
	help.WriteString("\nCommands:\n")
	commands := orderedRootCommands(manifest.root)
	for _, name := range commands {
		fmt.Fprintf(&help, "  %-20s %s\n", name, firstLine(manifest.root[name].Doc))
	}
	return help.String()
}

func orderedRootCommands(commands map[string]commandDefinition) []string {
	preferred := []string{"stop", "get", "publish", "resolve"}
	ordered := make([]string, 0, len(commands))
	seen := make(map[string]struct{}, len(preferred))
	for _, name := range preferred {
		if _, exists := commands[name]; exists {
			ordered = append(ordered, name)
			seen[name] = struct{}{}
		}
	}
	for _, name := range sortedKeys(commands) {
		if _, exists := seen[name]; !exists {
			ordered = append(ordered, name)
		}
	}
	return ordered
}

// GroupHelp returns a concise command listing for one legacy API group.
func GroupHelp(group string) (string, bool) {
	manifest, err := loadManifest()
	if err != nil {
		return err.Error(), false
	}
	description, exists := manifest.groups[group]
	if !exists {
		return "", false
	}
	var help strings.Builder
	fmt.Fprintf(&help, "Usage:  lbrynet %s COMMAND\n\n%s\n\nCommands:\n", group, description)
	commands := manifest.grouped[group]
	for _, name := range sortedKeys(commands) {
		fmt.Fprintf(&help, "  %-20s %s\n", name, firstLine(commands[name].Doc))
	}
	return help.String(), true
}

// ActiveMethods returns all non-deprecated RPC method names exposed by the
// pinned client command manifest.
func ActiveMethods() []string {
	manifest, err := loadManifest()
	if err != nil {
		return nil
	}
	methods := make([]string, 0, len(manifest.commands))
	for method, command := range manifest.commands {
		if command.Replacement == nil {
			methods = append(methods, method)
		}
	}
	sort.Strings(methods)
	return methods
}

// DeprecatedMethods returns deprecated CLI method names and their active
// replacement. The returned map is independent of the embedded manifest.
func DeprecatedMethods() map[string]string {
	manifest, err := loadManifest()
	if err != nil {
		return nil
	}
	deprecated := make(map[string]string)
	for method, command := range manifest.commands {
		if command.Replacement != nil {
			deprecated[method] = *command.Replacement
		}
	}
	return deprecated
}

// IsCommandToken reports whether a root-level positional token begins a legacy
// API command. It includes both ungrouped commands and group names.
func IsCommandToken(token string) bool {
	manifest, err := loadManifest()
	if err != nil {
		return false
	}
	if _, exists := manifest.root[token]; exists {
		return true
	}
	_, exists := manifest.grouped[token]
	return exists
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if line, _, found := strings.Cut(value, "\n"); found {
		return strings.TrimSpace(line)
	}
	return value
}
