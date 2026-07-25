package settings

// TODO: fill defaults, make allow list (maybe reuse defaults), auto-save, hooks

var DEFAULTS map[string]any = map[string]any{}

type Configuration struct {
	settings map[string]any
}

func InitializeConfiguration() Configuration {
	return Configuration{
		settings: map[string]any{},
	}
}

func (config Configuration) All() map[string]any {
	return config.settings
}

func (config Configuration) Clear(key string) (any, error) {
	delete(config.settings, key)
	return config.Get(key), nil
}

func (config Configuration) Get(key string) any {
	value, ok := DEFAULTS[key]
	if ok {
		return value
	}
	return nil
}

func (config Configuration) Set(key string, value any) (any, error) {

	return config.Get(key), nil
}
