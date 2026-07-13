package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

type Options struct {
	Paths       *Paths
	ConfigPath  string
	Runtime     map[string]any
	Arguments   map[string]any
	Environment map[string]string
	InMemory    bool
}

type Store struct {
	mu          sync.RWMutex
	specs       []Spec
	byName      map[string]Spec
	runtime     map[string]any
	arguments   map[string]any
	environment map[string]any
	persisted   map[string]any
	persistPath string
	inMemory    bool
}

type PythonError struct {
	Name    string
	Message string
}

func (err *PythonError) Error() string {
	return err.Message
}

func (err *PythonError) PythonErrorName() string {
	return err.Name
}

func New(options Options) (*Store, error) {
	paths := DefaultPaths()
	if options.Paths != nil {
		paths = *options.Paths
	}
	if paths.Config == "" && paths.DataDir != "" {
		paths.Config = filepath.Join(paths.DataDir, "daemon_settings.yml")
	}
	if dataDir, ok := options.Runtime["data_dir"].(string); ok && dataDir != "" && options.ConfigPath == "" {
		paths.Config = filepath.Join(ExpandPath(dataDir), "daemon_settings.yml")
	}

	store := &Store{
		specs:       defaultSpecs(paths),
		byName:      make(map[string]Spec),
		runtime:     make(map[string]any),
		arguments:   make(map[string]any),
		environment: make(map[string]any),
		persisted:   make(map[string]any),
		inMemory:    options.InMemory,
	}
	for _, spec := range store.specs {
		store.byName[spec.Name] = spec
	}

	for name, value := range options.Runtime {
		spec, exists := store.byName[name]
		if !exists {
			continue
		}
		if err := validate(spec, value); err != nil {
			return nil, err
		}
		store.runtime[name] = cloneValue(value)
	}
	for name, value := range options.Arguments {
		if err := store.loadUnvalidated(store.arguments, name, value); err != nil {
			return nil, err
		}
	}
	if options.ConfigPath != "" {
		store.arguments["config"] = options.ConfigPath
	}

	environment := options.Environment
	if environment == nil {
		environment = currentEnvironment()
	}
	for _, spec := range store.specs {
		value, exists := environment["LBRY_"+strings.ToUpper(spec.Name)]
		if !exists {
			continue
		}
		cleaned, err := deserialize(spec, value)
		if err != nil {
			return nil, err
		}
		store.environment[spec.Name] = cleaned
	}

	if configValue, exists := store.effectiveLocked("config"); exists {
		store.persistPath, _ = configValue.(string)
		store.persistPath = ExpandPath(store.persistPath)
	}
	if !store.inMemory && store.persistPath != "" {
		extension := filepath.Ext(store.persistPath)
		if extension != ".yml" && extension != ".yaml" {
			return nil, &PythonError{
				Name: "AssertionError",
				Message: fmt.Sprintf(
					"File extension '%s' is not supported, configuration file must be in YAML (.yaml).",
					extension,
				),
			}
		}
		upgraded, err := store.loadPersisted()
		if err != nil {
			return nil, err
		}
		if upgraded {
			if err := store.saveLocked(); err != nil {
				return nil, err
			}
		}
	}
	return store, nil
}

func NewMemory() *Store {
	store, err := New(Options{Environment: map[string]string{}, InMemory: true})
	if err != nil {
		panic(err)
	}
	return store
}

func currentEnvironment() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

func (store *Store) loadUnvalidated(destination map[string]any, name string, value any) error {
	spec, exists := store.byName[name]
	if !exists {
		return nil
	}
	if value == nil && (spec.Kind == KindServers || spec.Kind == KindStrings) {
		return nil
	}
	cleaned, err := deserialize(spec, value)
	if err != nil {
		return err
	}
	destination[name] = cleaned
	return nil
}

func (store *Store) loadPersisted() (bool, error) {
	data, err := os.ReadFile(store.persistPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	entries, err := decodeYAMLMapping(data)
	if err != nil {
		return false, err
	}

	aliases := make(map[string]string)
	for _, spec := range store.specs {
		for _, previous := range spec.PreviousNames {
			aliases[previous] = spec.Name
		}
	}
	upgraded := false
	for _, entry := range entries {
		if _, isAlias := aliases[entry.name]; isAlias {
			continue
		}
		spec, exists := store.byName[entry.name]
		if !exists {
			continue
		}
		cleaned, err := deserialize(spec, entry.value)
		if err != nil {
			return false, err
		}
		store.persisted[entry.name] = cleaned
	}
	for _, entry := range entries {
		canonical, isAlias := aliases[entry.name]
		if !isAlias {
			continue
		}
		upgraded = true
		spec := store.byName[canonical]
		cleaned, err := deserialize(spec, entry.value)
		if err != nil {
			return false, err
		}
		store.persisted[canonical] = cleaned
	}
	return upgraded, nil
}

type yamlEntry struct {
	name  string
	value any
}

func decodeYAMLMapping(data []byte) ([]yamlEntry, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if len(document.Content) == 0 {
		return nil, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		var decodedRoot any
		if err := decodePythonYAMLValue(root, &decodedRoot); err != nil {
			return nil, err
		}
		if pythonFalsey(decodedRoot) {
			return nil, nil
		}
		return nil, &PythonError{
			Name:    "AttributeError",
			Message: fmt.Sprintf("'%s' object has no attribute 'items'", pythonTypeName(decodedRoot)),
		}
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	return decodePythonYAMLMapping(root)
}

func pythonFalsey(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case bool:
		return !typed
	case string:
		return typed == ""
	case int:
		return typed == 0
	case int64:
		return typed == 0
	case uint64:
		return typed == 0
	case float64:
		return typed == 0
	}
	reflected := reflect.ValueOf(value)
	return (reflected.Kind() == reflect.Map || reflected.Kind() == reflect.Slice) && reflected.Len() == 0
}

func decodePythonYAMLMapping(node *yaml.Node) ([]yamlEntry, error) {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	if node.Kind != yaml.MappingNode {
		return nil, &PythonError{Name: "ConstructorError", Message: "expected a mapping for merging"}
	}
	entries := make([]yamlEntry, 0, len(node.Content)/2)
	positions := make(map[string]int)
	add := func(entry yamlEntry, overwrite bool) {
		if position, exists := positions[entry.name]; exists {
			if overwrite {
				entries[position].value = entry.value
			}
			return
		}
		positions[entry.name] = len(entries)
		entries = append(entries, entry)
	}

	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value != "<<" {
			continue
		}
		mergeNode := node.Content[index+1]
		mergeMappings := []*yaml.Node{mergeNode}
		if mergeNode.Kind == yaml.SequenceNode {
			mergeMappings = mergeNode.Content
		}
		for _, mapping := range mergeMappings {
			merged, err := decodePythonYAMLMapping(mapping)
			if err != nil {
				return nil, err
			}
			for _, entry := range merged {
				add(entry, false)
			}
		}
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		name := node.Content[index].Value
		if name == "<<" {
			continue
		}
		var value any
		if err := decodePythonYAMLValue(node.Content[index+1], &value); err != nil {
			return nil, err
		}
		add(yamlEntry{name: name, value: value}, true)
	}
	return entries, nil
}

func decodePythonYAMLValue(node *yaml.Node, destination *any) error {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return decodePythonYAMLValue(node.Alias, destination)
	}
	if node.Kind == yaml.SequenceNode {
		items := make([]any, len(node.Content))
		for index, child := range node.Content {
			if err := decodePythonYAMLValue(child, &items[index]); err != nil {
				return err
			}
		}
		*destination = items
		return nil
	}
	if node.Kind == yaml.MappingNode {
		entries, err := decodePythonYAMLMapping(node)
		if err != nil {
			return err
		}
		mapping := make(map[string]any, len(entries))
		for _, entry := range entries {
			mapping[entry.name] = entry.value
		}
		*destination = mapping
		return nil
	}
	if node.Kind == yaml.ScalarNode && node.Style == 0 {
		if value, resolved := decodePythonYAMLScalar(node.Value); resolved {
			*destination = value
			return nil
		}
		*destination = node.Value
		return nil
	}
	return node.Decode(destination)
}

func decodePythonYAMLScalar(value string) (any, bool) {
	switch value {
	case "", "~", "null", "Null", "NULL":
		return nil, true
	case "yes", "Yes", "YES", "true", "True", "TRUE", "on", "On", "ON":
		return true, true
	case "no", "No", "NO", "false", "False", "FALSE", "off", "Off", "OFF":
		return false, true
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return math.Inf(1), true
	case "-.inf", "-.Inf", "-.INF":
		return math.Inf(-1), true
	case ".nan", ".NaN", ".NAN":
		return math.NaN(), true
	}
	if integer, ok := parsePythonYAMLInteger(value); ok {
		return nativeOrBigInteger(integer), true
	}
	if number, ok := parsePythonYAMLFloat(value); ok {
		return number, true
	}
	return nil, false
}

func parsePythonYAMLInteger(value string) (*big.Int, bool) {
	cleaned := strings.ReplaceAll(value, "_", "")
	negative := false
	if cleaned != "" && (cleaned[0] == '+' || cleaned[0] == '-') {
		negative = cleaned[0] == '-'
		cleaned = cleaned[1:]
	}
	if cleaned == "" {
		return nil, false
	}
	if strings.Contains(cleaned, ":") {
		parts := strings.Split(cleaned, ":")
		if len(parts) < 2 || !validDecimalDigits(parts[0], false) || parts[0][0] == '0' {
			return nil, false
		}
		total := new(big.Int)
		base := big.NewInt(60)
		for index, part := range parts {
			if !validDecimalDigits(part, false) || index > 0 && (len(part) > 2 || len(part) == 2 && part[0] > '5') {
				return nil, false
			}
			piece, _ := new(big.Int).SetString(part, 10)
			total.Mul(total, base)
			total.Add(total, piece)
		}
		if negative {
			total.Neg(total)
		}
		return total, true
	}

	base := 10
	digits := cleaned
	switch {
	case strings.HasPrefix(cleaned, "0b"):
		base, digits = 2, cleaned[2:]
	case strings.HasPrefix(cleaned, "0x"):
		base, digits = 16, cleaned[2:]
	case len(cleaned) > 1 && cleaned[0] == '0':
		base, digits = 8, cleaned[1:]
	case cleaned == "0":
		return new(big.Int), true
	case cleaned[0] < '1' || cleaned[0] > '9':
		return nil, false
	}
	if digits == "" || !validBaseDigits(digits, base) {
		return nil, false
	}
	integer, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return nil, false
	}
	if negative {
		integer.Neg(integer)
	}
	return integer, true
}

func parsePythonYAMLFloat(value string) (float64, bool) {
	cleaned := strings.ReplaceAll(value, "_", "")
	if strings.Contains(cleaned, ":") {
		parts := strings.Split(cleaned, ":")
		if len(parts) < 2 || !strings.Contains(parts[len(parts)-1], ".") {
			return 0, false
		}
		negative := strings.HasPrefix(parts[0], "-")
		parts[0] = strings.TrimPrefix(strings.TrimPrefix(parts[0], "+"), "-")
		result := 0.0
		for index, part := range parts {
			if index < len(parts)-1 && !validDecimalDigits(part, false) {
				return 0, false
			}
			piece, err := strconv.ParseFloat(part, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return 0, false
			}
			result = result*60 + piece
		}
		if negative {
			result = -result
		}
		return result, true
	}
	dot := strings.IndexByte(cleaned, '.')
	if dot < 0 {
		return 0, false
	}
	if dot == 0 {
		if len(cleaned) == 1 || cleaned[1] < '0' || cleaned[1] > '9' {
			return 0, false
		}
	} else {
		unsigned := strings.TrimPrefix(strings.TrimPrefix(cleaned, "+"), "-")
		if unsigned == "" || unsigned[0] < '0' || unsigned[0] > '9' {
			return 0, false
		}
	}
	if exponent := strings.IndexAny(cleaned, "eE"); exponent >= 0 {
		if exponent+2 >= len(cleaned) || (cleaned[exponent+1] != '+' && cleaned[exponent+1] != '-') ||
			!validDecimalDigits(cleaned[exponent+2:], false) {
			return 0, false
		}
	}
	number, err := strconv.ParseFloat(cleaned, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return number, true
}

func validDecimalDigits(value string, allowUnderscore bool) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || allowUnderscore && character == '_' {
			continue
		}
		return false
	}
	return true
}

func validBaseDigits(value string, base int) bool {
	for _, character := range value {
		digit := -1
		switch {
		case character >= '0' && character <= '9':
			digit = int(character - '0')
		case character >= 'a' && character <= 'f':
			digit = int(character-'a') + 10
		case character >= 'A' && character <= 'F':
			digit = int(character-'A') + 10
		}
		if digit < 0 || digit >= base {
			return false
		}
	}
	return value != ""
}

func (store *Store) Snapshot() map[string]any {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make(map[string]any, len(store.specs))
	for _, spec := range store.specs {
		value, _ := store.effectiveLocked(spec.Name)
		result[spec.Name] = exposedValue(spec, value)
	}
	return result
}

func (store *Store) Get(name string) (any, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	spec, exists := store.byName[name]
	if !exists {
		return nil, false
	}
	value, _ := store.effectiveLocked(name)
	return exposedValue(spec, value), true
}

// IsSet reports whether a setting exists in any non-default configuration
// layer, matching Setting.is_set in the pinned SDK.
func (store *Store) IsSet(name string) bool {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, layer := range []map[string]any{
		store.runtime, store.arguments, store.environment, store.persisted,
	} {
		if _, exists := layer[name]; exists {
			return true
		}
	}
	return false
}

// IsSetToDefault matches Setting.is_set_to_default: it considers only the
// first configured layer and reports whether that value equals the default.
func (store *Store) IsSetToDefault(name string) bool {
	store.mu.RLock()
	defer store.mu.RUnlock()
	spec, exists := store.byName[name]
	if !exists {
		return false
	}
	for _, layer := range []map[string]any{
		store.runtime, store.arguments, store.environment, store.persisted,
	} {
		if value, configured := layer[name]; configured {
			return reflect.DeepEqual(value, spec.Default)
		}
	}
	return false
}

// Default returns the validated setting default without consulting any
// configured layer.
func (store *Store) Default(name string) (any, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	spec, exists := store.byName[name]
	if !exists {
		return nil, false
	}
	return exposedValue(spec, spec.Default), true
}

func (store *Store) Set(name string, value any) (any, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	spec, exists := store.byName[name]
	if !exists {
		return nil, &PythonError{
			Name:    "AttributeError",
			Message: fmt.Sprintf("type object 'Config' has no attribute '%s'", name),
		}
	}
	cleaned, err := cleanAndValidate(spec, value)
	if err != nil {
		return nil, err
	}
	previousRuntime, hadRuntime := store.runtime[name]
	previousPersisted, hadPersisted := store.persisted[name]
	store.runtime[name] = cloneValue(cleaned)
	store.persisted[name] = cloneValue(cleaned)
	if err := store.saveLocked(); err != nil {
		restoreValue(store.runtime, name, previousRuntime, hadRuntime)
		restoreValue(store.persisted, name, previousPersisted, hadPersisted)
		return nil, err
	}
	return cloneValue(cleaned), nil
}

func (store *Store) Clear(name string) (any, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	spec, exists := store.byName[name]
	if !exists {
		return nil, &PythonError{Name: "KeyError", Message: "'" + name + "'"}
	}
	previousRuntime, hadRuntime := store.runtime[name]
	previousPersisted, hadPersisted := store.persisted[name]
	delete(store.runtime, name)
	delete(store.persisted, name)
	if err := store.saveLocked(); err != nil {
		restoreValue(store.runtime, name, previousRuntime, hadRuntime)
		restoreValue(store.persisted, name, previousPersisted, hadPersisted)
		return nil, err
	}
	value, _ := store.effectiveLocked(name)
	return exposedValue(spec, value), nil
}

func restoreValue(layer map[string]any, name string, previous any, existed bool) {
	if existed {
		layer[name] = previous
		return
	}
	delete(layer, name)
}

func (store *Store) effectiveLocked(name string) (any, bool) {
	for _, layer := range []map[string]any{store.runtime, store.arguments, store.environment, store.persisted} {
		if value, exists := layer[name]; exists {
			return value, true
		}
	}
	spec, exists := store.byName[name]
	if !exists {
		return nil, false
	}
	return spec.Default, true
}

func (store *Store) saveLocked() error {
	if store.inMemory || store.persistPath == "" {
		return nil
	}
	serialized := make(map[string]any, len(store.persisted))
	for name, value := range store.persisted {
		spec := store.byName[name]
		serialized[name] = serialize(spec, value)
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	encoder.CompactSeqIndent()
	if err := encoder.Encode(serialized); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	configFile, err := os.OpenFile(store.persistPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := configFile.Write(buffer.Bytes()); err != nil {
		configFile.Close()
		return err
	}
	return configFile.Close()
}

func cleanAndValidate(spec Spec, value any) (any, error) {
	cleaned, err := deserialize(spec, value)
	if err != nil {
		return nil, err
	}
	if err := validate(spec, cleaned); err != nil {
		return nil, err
	}
	return cleaned, nil
}

func deserialize(spec Spec, value any) (any, error) {
	switch spec.Kind {
	case KindInteger:
		return deserializeInteger(value)
	case KindFloat:
		return deserializeFloat(value)
	case KindServers:
		return deserializeServers(value), nil
	case KindMaxKeyFee:
		return deserializeMaxKeyFee(value)
	default:
		return cloneValue(value), nil
	}
}

func validate(spec Spec, value any) error {
	assertion := func(message string) error {
		return &PythonError{Name: "AssertionError", Message: message}
	}
	switch spec.Kind {
	case KindString, KindPath:
		if _, ok := value.(string); !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a string.", spec.Name))
		}
	case KindInteger:
		if !isPythonInteger(value) {
			return assertion(fmt.Sprintf("Setting '%s' must be an integer.", spec.Name))
		}
	case KindFloat:
		if _, ok := value.(float64); !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a decimal.", spec.Name))
		}
	case KindToggle:
		if _, ok := value.(bool); !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a true/false value.", spec.Name))
		}
	case KindStrings:
		stringsValue, ok := asAnySlice(value)
		if !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a tuple or list of strings.", spec.Name))
		}
		for index, item := range stringsValue {
			if _, ok := item.(string); !ok {
				return assertion(fmt.Sprintf(
					"Value of '%s' at index %d in setting '%s' must be a string.",
					pythonString(item), index, spec.Name,
				))
			}
		}
	case KindServers:
		servers, ok := value.([]Server)
		if !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a tuple or list of servers.", spec.Name))
		}
		for index, server := range servers {
			if !isPythonInteger(server.Port) {
				return assertion(fmt.Sprintf(
					"Server defined '(%s, %s)' at index %d in setting '%s' must be have port as int in second position.",
					pythonRepr(server.Host), pythonRepr(server.Port), index, spec.Name,
				))
			}
		}
	case KindStringChoice:
		text, ok := value.(string)
		if !ok {
			return assertion(fmt.Sprintf("Setting '%s' must be a string.", spec.Name))
		}
		if !contains(spec.Choices, text) {
			return &PythonError{
				Name:    "ValueError",
				Message: fmt.Sprintf("Setting '%s' value must be one of: %s", spec.Name, strings.Join(spec.Choices, ", ")),
			}
		}
	case KindMaxKeyFee:
		if value == nil {
			return nil
		}
		fee, ok := value.(map[string]any)
		amount, hasAmount := fee["amount"]
		currencyValue, hasCurrency := fee["currency"]
		if !ok || len(fee) != 2 || !hasAmount || !hasCurrency {
			return assertion(fmt.Sprintf(
				"Setting '%s' must be a dict like \"{'amount': 50.0, 'currency': 'USD'}\".",
				spec.Name,
			))
		}
		_ = amount
		currency, ok := currencyValue.(string)
		if !ok || !contains([]string{"BTC", "LBC", "USD"}, currency) {
			return invalidCurrency(currencyValue)
		}
	}
	return nil
}

func deserializeInteger(value any) (any, error) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return nativeOrBigInteger(big.NewInt(typed)), nil
	case uint:
		return nativeOrBigInteger(new(big.Int).SetUint64(uint64(typed))), nil
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return nativeOrBigInteger(new(big.Int).SetUint64(uint64(typed))), nil
	case uint64:
		integer := new(big.Int).SetUint64(typed)
		return nativeOrBigInteger(integer), nil
	case float32:
		return deserializeInteger(float64(typed))
	case float64:
		if math.IsNaN(typed) {
			return nil, &PythonError{Name: "ValueError", Message: "cannot convert float NaN to integer"}
		}
		if math.IsInf(typed, 0) {
			return nil, &PythonError{Name: "OverflowError", Message: "cannot convert float infinity to integer"}
		}
		integer, _ := new(big.Float).SetFloat64(typed).Int(nil)
		return nativeOrBigInteger(integer), nil
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			parsed, err := strconv.ParseFloat(text, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return nil, &PythonError{Name: "ValueError", Message: err.Error()}
			}
			return deserializeInteger(parsed)
		}
		integer, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, invalidIntegerLiteral(text)
		}
		return nativeOrBigInteger(integer), nil
	case BigInteger:
		integer, ok := new(big.Int).SetString(string(typed), 10)
		if !ok {
			return nil, invalidIntegerLiteral(string(typed))
		}
		return nativeOrBigInteger(integer), nil
	case *big.Int:
		if typed == nil {
			return nil, &PythonError{
				Name:    "TypeError",
				Message: "int() argument must be a string, a bytes-like object or a real number, not 'NoneType'",
			}
		}
		return nativeOrBigInteger(new(big.Int).Set(typed)), nil
	case string:
		integer, ok := parsePythonDecimalInteger(typed)
		if !ok {
			return nil, invalidIntegerLiteral(typed)
		}
		return nativeOrBigInteger(integer), nil
	default:
		return nil, &PythonError{
			Name:    "TypeError",
			Message: fmt.Sprintf("int() argument must be a string, a bytes-like object or a real number, not '%s'", pythonTypeName(value)),
		}
	}
}

func parsePythonDecimalInteger(value string) (*big.Int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}
	digitStart := 0
	if trimmed[0] == '+' || trimmed[0] == '-' {
		digitStart = 1
	}
	if digitStart == len(trimmed) {
		return nil, false
	}
	for index := digitStart; index < len(trimmed); index++ {
		character := trimmed[index]
		if character >= '0' && character <= '9' {
			continue
		}
		if character != '_' || index == digitStart || index+1 == len(trimmed) ||
			trimmed[index-1] < '0' || trimmed[index-1] > '9' ||
			trimmed[index+1] < '0' || trimmed[index+1] > '9' {
			return nil, false
		}
	}
	integer, ok := new(big.Int).SetString(strings.ReplaceAll(trimmed, "_", ""), 10)
	return integer, ok
}

func nativeOrBigInteger(value *big.Int) any {
	if value.IsInt64() {
		number := value.Int64()
		if strconv.IntSize == 64 || number >= math.MinInt32 && number <= math.MaxInt32 {
			return int(number)
		}
	}
	return BigInteger(value.String())
}

func invalidIntegerLiteral(value string) error {
	return &PythonError{
		Name:    "ValueError",
		Message: fmt.Sprintf("invalid literal for int() with base 10: '%s'", value),
	}
}

func isPythonInteger(value any) bool {
	switch typed := value.(type) {
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, BigInteger:
		return true
	case *big.Int:
		return typed != nil
	default:
		return false
	}
}

func deserializeFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1.0, nil
		}
		return 0.0, nil
	case int:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case float64:
		return typed, nil
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, &PythonError{Name: "ValueError", Message: err.Error()}
		}
		return parsed, nil
	case BigInteger:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if errors.Is(err, strconv.ErrRange) {
			return 0, &PythonError{Name: "OverflowError", Message: "int too large to convert to float"}
		}
		if err != nil {
			return 0, &PythonError{Name: "ValueError", Message: err.Error()}
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, &PythonError{Name: "ValueError", Message: fmt.Sprintf("could not convert string to float: '%s'", typed)}
		}
		return parsed, nil
	default:
		return 0, &PythonError{Name: "TypeError", Message: fmt.Sprintf("float() argument must be a string or a real number, not '%s'", pythonTypeName(value))}
	}
}

func deserializeServers(value any) []Server {
	items, ok := asAnySlice(value)
	if !ok {
		return []Server{}
	}
	servers := make([]Server, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || strings.Count(text, ":") != 1 {
			continue
		}
		host, portText, _ := strings.Cut(text, ":")
		port, err := deserializeInteger(portText)
		if err == nil {
			servers = append(servers, Server{Host: host, Port: port})
		}
	}
	return servers
}

func deserializeMaxKeyFee(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if fee, ok := value.(map[string]any); ok {
		currency, currencyExists := fee["currency"]
		amount, amountExists := fee["amount"]
		if !currencyExists {
			return nil, &PythonError{Name: "KeyError", Message: "'currency'"}
		}
		if !amountExists {
			return nil, &PythonError{Name: "KeyError", Message: "'amount'"}
		}
		floatAmount, err := deserializeFloat(amount)
		if err != nil {
			return nil, err
		}
		return map[string]any{"currency": currency, "amount": floatAmount}, nil
	}
	if text, ok := value.(string); ok {
		parts := strings.Fields(text)
		value = anySlice(parts)
	}
	items, ok := asAnySlice(value)
	if !ok {
		return nil, &PythonError{Name: "AssertionError", Message: "Invalid max key fee."}
	}
	if len(items) == 1 && items[0] == "null" {
		return nil, nil
	}
	if len(items) != 2 {
		return nil, &PythonError{
			Name:    "AssertionError",
			Message: "Max key fee is made up of either two values: \"AMOUNT CURRENCY\", or \"null\" (to set no limit)",
		}
	}
	amount, err := deserializeFloat(items[0])
	if err != nil {
		if python, ok := err.(*PythonError); !ok || python.Name != "ValueError" {
			return nil, err
		}
		return nil, &PythonError{Name: "AssertionError", Message: "First value in max key fee is a decimal: \"AMOUNT CURRENCY\""}
	}
	currency := strings.ToUpper(pythonString(items[1]))
	if !contains([]string{"BTC", "LBC", "USD"}, currency) {
		return nil, invalidCurrency(currency)
	}
	return map[string]any{"amount": amount, "currency": currency}, nil
}

func invalidCurrency(currency any) error {
	return &PythonError{
		Name:    "InvalidCurrencyError",
		Message: fmt.Sprintf("Invalid currency: %s is not a supported currency.", pythonString(currency)),
	}
}

func serialize(spec Spec, value any) any {
	if spec.Kind == KindString || spec.Kind == KindPath || spec.Kind == KindStringChoice {
		if text, ok := value.(string); ok {
			return pythonYAMLString(text)
		}
	}
	if spec.Kind == KindStrings {
		if items, ok := asAnySlice(value); ok {
			serialized := make([]any, len(items))
			for index, item := range items {
				if text, ok := item.(string); ok {
					serialized[index] = pythonYAMLString(text)
				} else {
					serialized[index] = cloneValue(item)
				}
			}
			return serialized
		}
	}
	if spec.Kind == KindFloat {
		if number, ok := value.(float64); ok {
			return yamlFloat(number)
		}
	}
	if spec.Kind == KindMaxKeyFee {
		if fee, ok := value.(map[string]any); ok {
			serialized := cloneValue(fee).(map[string]any)
			if amount, ok := serialized["amount"].(float64); ok {
				serialized["amount"] = yamlFloat(amount)
			}
			if currency, ok := serialized["currency"].(string); ok {
				serialized["currency"] = pythonYAMLString(currency)
			}
			return serialized
		}
	}
	if spec.Kind != KindServers {
		return cloneValue(value)
	}
	servers, ok := value.([]Server)
	if !ok {
		return cloneValue(value)
	}
	serializedValues := make([]pythonYAMLString, len(servers))
	for index, server := range servers {
		serializedValues[index] = pythonYAMLString(server.Host + ":" + pythonString(server.Port))
	}
	return serializedValues
}

func (integer BigInteger) MarshalJSON() ([]byte, error) {
	if _, ok := new(big.Int).SetString(string(integer), 10); !ok {
		return nil, fmt.Errorf("invalid big integer %q", integer)
	}
	return []byte(integer), nil
}

func (integer BigInteger) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: string(integer)}, nil
}

type yamlFloat float64

type pythonYAMLString string

var pythonYAMLTimestamp = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt]|[ \t]+)[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]*)?(?:[ \t]*(?:Z|[-+][0-9]{1,2}(?::[0-9]{2})?))?)$`)

func (value pythonYAMLString) MarshalYAML() (any, error) {
	text := string(value)
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: text}
	if text == "" || pythonYAMLTimestamp.MatchString(text) {
		node.Style = yaml.SingleQuotedStyle
	} else if _, resolved := decodePythonYAMLScalar(text); resolved {
		node.Style = yaml.SingleQuotedStyle
	}
	return node, nil
}

func (value yamlFloat) MarshalYAML() (any, error) {
	number := float64(value)
	formatted := ""
	switch {
	case math.IsNaN(number):
		formatted = ".nan"
	case math.IsInf(number, 1):
		formatted = ".inf"
	case math.IsInf(number, -1):
		formatted = "-.inf"
	default:
		absolute := math.Abs(number)
		if absolute >= 1e-4 && absolute < 1e16 {
			formatted = strconv.FormatFloat(number, 'f', -1, 64)
		} else {
			formatted = strconv.FormatFloat(number, 'g', -1, 64)
		}
		if exponent := strings.IndexAny(formatted, "eE"); exponent >= 0 {
			if !strings.Contains(formatted[:exponent], ".") {
				formatted = formatted[:exponent] + ".0" + formatted[exponent:]
			}
		} else if !strings.Contains(formatted, ".") {
			formatted += ".0"
		}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: formatted}, nil
}

func pythonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, BigInteger:
		return "int"
	case *big.Int:
		if value.(*big.Int) == nil {
			return "NoneType"
		}
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "str"
	case []any, []string, []Server:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func pythonString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return typed
	case BigInteger:
		return string(typed)
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = pythonRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, pythonRepr(key)+": "+pythonRepr(typed[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(value)
	}
}

func pythonRepr(value any) string {
	if text, ok := value.(string); ok {
		return "'" + strings.ReplaceAll(text, "'", "\\'") + "'"
	}
	return pythonString(value)
}

func exposedValue(spec Spec, value any) any {
	if spec.Kind == KindPath {
		if text, ok := value.(string); ok {
			return ExpandPath(text)
		}
	}
	return cloneValue(value)
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case []Server:
		if typed == nil {
			return []Server(nil)
		}
		cloned := make([]Server, len(typed))
		for index, server := range typed {
			cloned[index] = server
			cloned[index].Port = cloneValue(server.Port)
		}
		return cloned
	case *big.Int:
		if typed == nil {
			return (*big.Int)(nil)
		}
		return new(big.Int).Set(typed)
	case []string:
		if typed == nil {
			return []string(nil)
		}
		cloned := make([]string, len(typed))
		copy(cloned, typed)
		return cloned
	case []any:
		if typed == nil {
			return []any(nil)
		}
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for name, item := range typed {
			cloned[name] = cloneValue(item)
		}
		return cloned
	default:
		return value
	}
}

func asAnySlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		return anySlice(typed), true
	default:
		return nil, false
	}
}

func anySlice[T any](values []T) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func SortedNames(store *Store) []string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	names := make([]string, 0, len(store.specs))
	for _, spec := range store.specs {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}
