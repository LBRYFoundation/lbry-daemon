package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// settingsFileName lives in data_dir. It holds only the settings that differ from the defaults.
// (lbrynet used daemon_settings.yml; reading that file belongs with the lbrynet migration.)
const settingsFileName = "daemon_settings.json"

// Configuration is the daemon's settings. Copies share one store, so it is safe to pass by value and
// to use from several requests at once.
type Configuration struct {
	s *Store
}

type Store struct {
	mu        sync.RWMutex
	env       Environment
	path      string
	defaults  map[string]any
	overrides map[string]any
	// extra is anything in the settings file this daemon does not understand: unknown keys, and
	// values that failed validation. It is written back untouched, so a newer or older daemon
	// sharing the file does not lose data.
	extra map[string]json.RawMessage
}

// InitializeConfiguration loads the settings from the default data directory. A problem reading
// the file is logged and the defaults are used, so a bad file can't stop the daemon starting.
func InitializeConfiguration() Configuration {
	c, err := Open(realEnvironment(runtime.GOOS))
	if err != nil {
		log.Printf("settings: %v (using the default settings)", err)
	}
	return c
}

// Open builds a Configuration for env, reading the settings file if there is one. It always returns
// a usable Configuration; the error describes anything wrong with the file.
func Open(env Environment) (Configuration, error) {
	dataDir, walletDir, downloadDir := defaultDirs(env)
	s := &Store{
		env:       env,
		path:      filepath.Join(dataDir, settingsFileName),
		defaults:  map[string]any{},
		overrides: map[string]any{},
		extra:     map[string]json.RawMessage{},
	}
	for _, d := range definitions {
		switch d.name {
		case "data_dir":
			s.defaults[d.name] = dataDir
		case "wallet_dir":
			s.defaults[d.name] = walletDir
		case "download_dir":
			s.defaults[d.name] = downloadDir
		default:
			s.defaults[d.name] = d.def
		}
	}
	return Configuration{s: s}, s.load()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		// Keep the unreadable file for the user instead of overwriting it on the next change.
		bad := s.path + ".bad"
		if renameErr := os.Rename(s.path, bad); renameErr != nil {
			return fmt.Errorf("%s is not valid JSON (%v) and could not be moved aside: %w", s.path, err, renameErr)
		}
		return fmt.Errorf("%s was not valid JSON (%v); moved to %s", s.path, err, bad)
	}

	var problems []string
	for key, rawValue := range file {
		d, known := definitionsByName[key]
		if !known || d.readOnly {
			s.extra[key] = rawValue
			continue
		}
		var decoded any
		if err := json.Unmarshal(rawValue, &decoded); err != nil {
			s.extra[key] = rawValue
			problems = append(problems, key)
			continue
		}
		value, err := coerce(d, decoded)
		if err != nil {
			s.extra[key] = rawValue
			problems = append(problems, key)
			continue
		}
		s.overrides[key] = value
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("ignored invalid values in %s for: %v", s.path, problems)
	}
	return nil
}

func (s *Store) effective(d *Definition) any {
	if v, ok := s.overrides[d.name]; ok {
		return v
	}
	return s.defaults[d.name]
}

func (s *Store) exported(d *Definition) any {
	value := s.effective(d)
	if d.kind == kindPath {
		if p, ok := value.(string); ok {
			value = expandPath(s.env, p)
		}
	}
	return export(d, value)
}

// All returns every setting with its current value, like lbrynet's settings_get.
func (c Configuration) All() map[string]any {
	c.s.mu.RLock()
	defer c.s.mu.RUnlock()
	out := make(map[string]any, len(definitions))
	for i := range definitions {
		out[definitions[i].name] = c.s.exported(&definitions[i])
	}
	return out
}

// Get returns the current value of a setting, or nil if there is no such setting.
func (c Configuration) Get(key string) any {
	value, _ := c.Lookup(key)
	return value
}

// Lookup is Get that also says whether the setting exists.
func (c Configuration) Lookup(key string) (any, bool) {
	d, ok := definitionsByName[key]
	if !ok {
		return nil, false
	}
	c.s.mu.RLock()
	defer c.s.mu.RUnlock()
	return c.s.exported(d), true
}

// Set validates and stores a setting, writes the settings file, and returns the stored value. If the
// file can't be written the change is undone and the error returned.
func (c Configuration) Set(key string, value any) (any, error) {
	d, ok := definitionsByName[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	if d.readOnly {
		return nil, fmt.Errorf("%w: %q (set the LBRY_%s environment variable and restart)", ErrReadOnly, key, strings.ToUpper(key))
	}
	cleaned, err := coerce(d, parseJSONString(value))
	if err != nil {
		return nil, err
	}

	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	previous, hadPrevious := c.s.overrides[key]
	c.s.overrides[key] = cleaned
	if err := c.s.save(); err != nil {
		if hadPrevious {
			c.s.overrides[key] = previous
		} else {
			delete(c.s.overrides, key)
		}
		return nil, err
	}
	return c.s.exported(d), nil
}

// Clear removes a setting's stored value, so it goes back to its default, and returns the default.
func (c Configuration) Clear(key string) (any, error) {
	d, ok := definitionsByName[key]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	if d.readOnly {
		return nil, fmt.Errorf("%w: %q", ErrReadOnly, key)
	}

	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	previous, hadPrevious := c.s.overrides[key]
	delete(c.s.overrides, key)
	if err := c.s.save(); err != nil {
		if hadPrevious {
			c.s.overrides[key] = previous
		}
		return nil, err
	}
	return c.s.exported(d), nil
}

// parseJSONString decodes a string value that looks like a JSON list or object, as lbrynet's
// settings_set does, so `settings_set lbryum_servers '["host:50001"]'` works from a command line.
// A string that isn't valid JSON is returned unchanged.
func parseJSONString(value any) any {
	text, ok := value.(string)
	if !ok || text == "" || (text[0] != '[' && text[0] != '{') {
		return value
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return value
	}
	return decoded
}

// save writes the overrides and the preserved extras. The caller holds the write lock.
func (s *Store) save() error {
	file := make(map[string]any, len(s.overrides)+len(s.extra))
	for key, raw := range s.extra {
		file[key] = raw
	}
	for key, value := range s.overrides {
		file[key] = persist(value)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}
	data = append(bytes.TrimRight(data, "\n"), '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, settingsFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // harmless after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return nil
}
