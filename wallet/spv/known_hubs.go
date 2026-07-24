package spv

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

const KnownHubsFilename = "known_hubs.yml"

var (
	ErrInvalidKnownHubs = errors.New("invalid known hubs file")
	ErrInvalidHub       = errors.New("invalid known hub")
)

type HubDetails map[string]any

type Hub struct {
	Server  Server
	Details HubDetails
}

type KnownHubs struct {
	mu      sync.RWMutex
	saveMu  sync.Mutex
	path    string
	hubs    []Hub
	indexes map[Server]int
}

func OpenKnownHubs(path string) (*KnownHubs, error) {
	known := &KnownHubs{path: path, indexes: make(map[Server]int)}
	if path == "" {
		return known, nil
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return known, nil
	}
	if err != nil {
		return nil, err
	}
	if err := known.load(contents); err != nil {
		return nil, err
	}
	return known, nil
}

func NewMemoryKnownHubs() *KnownHubs {
	known, _ := OpenKnownHubs("")
	return known
}

func (known *KnownHubs) Path() string {
	if known == nil {
		return ""
	}
	known.mu.RLock()
	defer known.mu.RUnlock()
	return known.path
}

func (known *KnownHubs) Exists() bool {
	path := known.Path()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (known *KnownHubs) Len() int {
	if known == nil {
		return 0
	}
	known.mu.RLock()
	defer known.mu.RUnlock()
	return len(known.hubs)
}

func (known *KnownHubs) Servers() []Server {
	if known == nil {
		return []Server{}
	}
	known.mu.RLock()
	defer known.mu.RUnlock()
	servers := make([]Server, len(known.hubs))
	for index, hub := range known.hubs {
		servers[index] = hub.Server
	}
	return servers
}

func (known *KnownHubs) Snapshot() []Hub {
	if known == nil {
		return []Hub{}
	}
	known.mu.RLock()
	defer known.mu.RUnlock()
	snapshot := make([]Hub, len(known.hubs))
	for index, hub := range known.hubs {
		snapshot[index] = Hub{Server: hub.Server, Details: cloneHubDetails(hub.Details)}
	}
	return snapshot
}

// SetString retains the pinned first-insertion-wins behavior. Existing hub
// details are never replaced by peer discovery or a later load entry.
func (known *KnownHubs) SetString(value string, details HubDetails) (bool, error) {
	server, accepted, err := parseHubString(value)
	if err != nil || !accepted {
		return false, err
	}
	return known.Set(server, details), nil
}

func (known *KnownHubs) Set(server Server, details HubDetails) bool {
	if known == nil {
		return false
	}
	known.mu.Lock()
	defer known.mu.Unlock()
	return known.setLocked(server, details)
}

func (known *KnownHubs) setLocked(server Server, details HubDetails) bool {
	if known.indexes == nil {
		known.indexes = make(map[Server]int)
	}
	if _, exists := known.indexes[server]; exists {
		return false
	}
	known.indexes[server] = len(known.hubs)
	known.hubs = append(known.hubs, Hub{Server: server, Details: cloneHubDetails(details)})
	return true
}

func (known *KnownHubs) AddStrings(values []string) (bool, error) {
	added := false
	for _, value := range values {
		inserted, err := known.SetString(value, HubDetails{})
		if err != nil {
			return added, err
		}
		added = added || inserted
	}
	return added, nil
}

// UpdateCountry mirrors setdefault(..., {}).update({"country": value}) and
// intentionally does not save the file. Network peer discovery owns saving.
func (known *KnownHubs) UpdateCountry(server Server, country string) {
	if known == nil {
		return
	}
	known.mu.Lock()
	defer known.mu.Unlock()
	index, exists := known.indexes[server]
	if !exists {
		known.setLocked(server, HubDetails{})
		index = known.indexes[server]
	}
	if known.hubs[index].Details == nil {
		known.hubs[index].Details = HubDetails{}
	}
	known.hubs[index].Details["country"] = country
}

// Filter matches Python's OR across constraints, including missing keys when
// matchNone is true. An empty constraint set returns every known hub.
func (known *KnownHubs) Filter(matchNone bool, constraints HubDetails) []Hub {
	if known == nil {
		return []Hub{}
	}
	known.mu.RLock()
	defer known.mu.RUnlock()
	result := make([]Hub, 0, len(known.hubs))
	for _, hub := range known.hubs {
		matched := len(constraints) == 0
		for key, constraint := range constraints {
			value, exists := hub.Details[key]
			if hubValuesEqual(value, constraint) || matchNone && (!exists || value == nil) {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, Hub{Server: hub.Server, Details: cloneHubDetails(hub.Details)})
		}
	}
	return result
}

func (known *KnownHubs) Save() error {
	if known == nil {
		return nil
	}
	known.saveMu.Lock()
	defer known.saveMu.Unlock()
	known.mu.RLock()
	path := known.path
	snapshot := make([]Hub, len(known.hubs))
	for index, hub := range known.hubs {
		snapshot[index] = Hub{Server: hub.Server, Details: cloneHubDetails(hub.Details)}
	}
	known.mu.RUnlock()
	if path == "" {
		return nil
	}
	root, err := encodeKnownHubsNode(snapshot)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)
	encodeErr := encoder.Encode(root)
	closeEncoderErr := encoder.Close()
	closeFileErr := file.Close()
	return errors.Join(encodeErr, closeEncoderErr, closeFileErr)
}

func (known *KnownHubs) load(contents []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidKnownHubs, err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%w: top-level value must be a mapping", ErrInvalidKnownHubs)
	}
	root := document.Content[0]
	type yamlHub struct {
		value       string
		detailsNode *yaml.Node
	}
	entries := make([]yamlHub, 0, len(root.Content)/2)
	entryIndexes := make(map[string]int, len(root.Content)/2)
	for index := 0; index+1 < len(root.Content); index += 2 {
		var value string
		if err := root.Content[index].Decode(&value); err != nil {
			return fmt.Errorf("%w: hub key: %v", ErrInvalidKnownHubs, err)
		}
		if existing, duplicate := entryIndexes[value]; duplicate {
			entries[existing].detailsNode = root.Content[index+1]
			continue
		}
		entryIndexes[value] = len(entries)
		entries = append(entries, yamlHub{value: value, detailsNode: root.Content[index+1]})
	}
	for _, entry := range entries {
		detailsNode := entry.detailsNode
		if detailsNode.Kind != yaml.MappingNode {
			return fmt.Errorf("%w: details for %q must be a mapping", ErrInvalidKnownHubs, entry.value)
		}
		var details HubDetails
		if err := detailsNode.Decode(&details); err != nil {
			return fmt.Errorf("%w: details for %q: %v", ErrInvalidKnownHubs, entry.value, err)
		}
		if _, err := known.SetString(entry.value, details); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidKnownHubs, err)
		}
	}
	return nil
}

func encodeKnownHubsNode(hubs []Hub) (*yaml.Node, error) {
	sorted := append([]Hub(nil), hubs...)
	sort.SliceStable(sorted, func(left, right int) bool {
		return sorted[left].Server.String() < sorted[right].Server.String()
	})
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, hub := range sorted {
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: hub.Server.String()}
		var details yaml.Node
		if err := details.Encode(map[string]any(hub.Details)); err != nil {
			return nil, err
		}
		root.Content = append(root.Content, key, &details)
	}
	return root, nil
}

func parseHubString(value string) (Server, bool, error) {
	if value == "" || strings.Count(value, ":") != 1 {
		return Server{}, false, nil
	}
	host, portText, _ := strings.Cut(value, ":")
	cleanedPort, ok := cleanPythonIntegerString(portText)
	if !ok {
		return Server{}, false, fmt.Errorf("%w %q: invalid integer", ErrInvalidHub, value)
	}
	port64, err := strconv.ParseInt(cleanedPort, 10, 0)
	if err != nil {
		return Server{}, false, fmt.Errorf("%w %q: %v", ErrInvalidHub, value, err)
	}
	return Server{Host: host, Port: int(port64)}, true, nil
}

func cloneHubDetails(details HubDetails) HubDetails {
	if details == nil {
		return HubDetails{}
	}
	cloned := make(HubDetails, len(details))
	for key, value := range details {
		cloned[key] = cloneHubValue(value)
	}
	return cloned
}

func cloneHubValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneHubReflect(reflect.ValueOf(value)).Interface()
}

func cloneHubReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneHubReflect(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned.SetMapIndex(iterator.Key(), cloneHubReflect(iterator.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneHubReflect(value.Index(index)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			cloned.Index(index).Set(cloneHubReflect(value.Index(index)))
		}
		return cloned
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type().Elem())
		cloned.Elem().Set(cloneHubReflect(value.Elem()))
		return cloned
	default:
		return value
	}
}

func hubValuesEqual(left, right any) bool {
	leftNumber, leftNumeric := hubNumericValue(left)
	rightNumber, rightNumeric := hubNumericValue(right)
	if leftNumeric || rightNumeric {
		if !leftNumeric || !rightNumeric || leftNumber.nan || rightNumber.nan {
			return false
		}
		if leftNumber.infinity != 0 || rightNumber.infinity != 0 {
			return leftNumber.infinity == rightNumber.infinity
		}
		return leftNumber.rational.Cmp(rightNumber.rational) == 0
	}
	return reflect.DeepEqual(left, right)
}

type hubNumber struct {
	rational *big.Rat
	infinity int
	nan      bool
}

func hubNumericValue(value any) (hubNumber, bool) {
	if value == nil {
		return hubNumber{}, false
	}
	if number, ok := value.(json.Number); ok {
		result := hubNumber{rational: new(big.Rat)}
		text := number.String()
		if !strings.ContainsAny(text, ".eE") {
			if integer, accepted := new(big.Int).SetString(text, 10); accepted {
				result.rational.SetInt(integer)
				return result, true
			}
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return hubNumber{}, false
		}
		if math.IsNaN(parsed) {
			result.nan = true
		} else if math.IsInf(parsed, 1) {
			result.infinity = 1
		} else if math.IsInf(parsed, -1) {
			result.infinity = -1
		} else {
			result.rational.SetFloat64(parsed)
		}
		return result, true
	}
	reflected := reflect.ValueOf(value)
	result := hubNumber{rational: new(big.Rat)}
	switch reflected.Kind() {
	case reflect.Bool:
		if reflected.Bool() {
			result.rational.SetInt64(1)
		}
		return result, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		result.rational.SetInt64(reflected.Int())
		return result, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		integer := new(big.Int).SetUint64(reflected.Uint())
		result.rational.SetInt(integer)
		return result, true
	case reflect.Float32, reflect.Float64:
		number := reflected.Float()
		if math.IsNaN(number) {
			result.nan = true
			return result, true
		}
		if math.IsInf(number, 1) {
			result.infinity = 1
			return result, true
		}
		if math.IsInf(number, -1) {
			result.infinity = -1
			return result, true
		}
		result.rational.SetFloat64(number)
		return result, true
	default:
		return hubNumber{}, false
	}
}

func cleanPythonIntegerString(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	prefix := ""
	if trimmed[0] == '+' || trimmed[0] == '-' {
		prefix = trimmed[:1]
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return "", false
	}
	var cleaned strings.Builder
	cleaned.WriteString(prefix)
	previousDigit := false
	for index := 0; index < len(trimmed); index++ {
		value := trimmed[index]
		switch {
		case value >= '0' && value <= '9':
			cleaned.WriteByte(value)
			previousDigit = true
		case value == '_' && previousDigit && index+1 < len(trimmed) &&
			trimmed[index+1] >= '0' && trimmed[index+1] <= '9':
			previousDigit = false
		default:
			return "", false
		}
	}
	return cleaned.String(), previousDigit
}
