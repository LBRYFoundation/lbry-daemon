package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrUnknownSetting is returned for a key that is not a daemon setting.
var ErrUnknownSetting = errors.New("unknown setting")

// ErrReadOnly is returned when a setting that cannot change while running is set or cleared.
var ErrReadOnly = errors.New("setting cannot be changed while the daemon is running")

// InvalidValueError means the caller sent a value the setting cannot hold.
type InvalidValueError struct{ Setting, Reason string }

func (e *InvalidValueError) Error() string {
	return fmt.Sprintf("Setting '%s' %s", e.Setting, e.Reason)
}

// IsUserError reports whether err was caused by the request (a bad key or value) rather than by the
// daemon, so a handler can answer 400 instead of 500.
func IsUserError(err error) bool {
	var invalid *InvalidValueError
	return errors.Is(err, ErrUnknownSetting) || errors.Is(err, ErrReadOnly) || errors.As(err, &invalid)
}

func invalid(d *definition, reason string) error {
	return &InvalidValueError{Setting: d.name, Reason: reason}
}

// coerce turns what a client sent (JSON-decoded, or a string from the command line) into the
// canonical Go value for the setting, following the SDK's deserialize and validate rules.
func coerce(d *definition, raw any) (any, error) {
	switch d.kind {
	case kindString, kindPath:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid(d, "must be a string.")
		}
		return s, nil

	case kindChoice:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid(d, "must be a string.")
		}
		for _, c := range d.choices {
			if s == c {
				return s, nil
			}
		}
		return nil, invalid(d, "value must be one of: "+strings.Join(d.choices, ", "))

	case kindInt:
		switch v := raw.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
				return int(v), nil
			}
		case json.Number:
			if n, err := strconv.Atoi(v.String()); err == nil {
				return n, nil
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n, nil
			}
		}
		return nil, invalid(d, "must be an integer.")

	case kindFloat:
		switch v := raw.(type) {
		case float64:
			return v, nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, nil
			}
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f, nil
			}
		}
		return nil, invalid(d, "must be a decimal.")

	case kindBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
		return nil, invalid(d, "must be a true/false value.")

	case kindStrings:
		list, ok := raw.([]any)
		if !ok {
			if ss, isStrings := raw.([]string); isStrings {
				return append([]string{}, ss...), nil
			}
			return nil, invalid(d, "must be a list of strings.")
		}
		out := make([]string, 0, len(list))
		for i, item := range list {
			s, isString := item.(string)
			if !isString {
				return nil, invalid(d, fmt.Sprintf("must be a list of strings; item %d is not a string.", i))
			}
			out = append(out, s)
		}
		return out, nil

	case kindServers:
		list, ok := raw.([]any)
		if !ok {
			if ss, isStrings := raw.([]string); isStrings {
				list = make([]any, len(ss))
				for i, s := range ss {
					list[i] = s
				}
			} else if ds, isServers := raw.([]server); isServers {
				return append([]server{}, ds...), nil
			} else {
				return nil, invalid(d, `must be a list of servers like "host:port".`)
			}
		}
		out := make([]server, 0, len(list))
		for i, item := range list {
			s, err := parseServer(item)
			if err != nil {
				return nil, invalid(d, fmt.Sprintf("has a bad server at index %d: %v", i, err))
			}
			out = append(out, s)
		}
		return out, nil

	case kindMaxKeyFee:
		return coerceMaxKeyFee(d, raw)
	}
	return nil, invalid(d, "has an unsupported type.")
}

// parseServer accepts "host:port" or a [host, port] pair.
func parseServer(item any) (server, error) {
	switch v := item.(type) {
	case string:
		host, portText, found := strings.Cut(v, ":")
		if !found || host == "" || strings.Contains(portText, ":") {
			return server{}, fmt.Errorf("%q is not host:port", v)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return server{}, fmt.Errorf("%q has a bad port", v)
		}
		return server{host, port}, nil
	case []any:
		if len(v) != 2 {
			return server{}, fmt.Errorf("a server is [host, port]")
		}
		host, ok := v[0].(string)
		if !ok || host == "" {
			return server{}, fmt.Errorf("the host must be a string")
		}
		port, ok := v[1].(float64)
		if !ok || port != math.Trunc(port) || port < 1 || port > 65535 {
			return server{}, fmt.Errorf("the port must be a whole number from 1 to 65535")
		}
		return server{host, int(port)}, nil
	}
	return server{}, fmt.Errorf("a server is \"host:port\"")
}

func validCurrency(c string) bool {
	for _, known := range currencies {
		if c == known {
			return true
		}
	}
	return false
}

// coerceMaxKeyFee accepts null (no limit), {"currency", "amount"}, "AMOUNT CURRENCY", "null" or
// [AMOUNT, CURRENCY].
func coerceMaxKeyFee(d *definition, raw any) (any, error) {
	badFormat := invalid(d, `must be null, or like {"amount": 50.0, "currency": "USD"}.`)
	build := func(amount any, currency any) (any, error) {
		cur, ok := currency.(string)
		if !ok {
			return nil, badFormat
		}
		cur = strings.ToUpper(cur)
		if !validCurrency(cur) {
			return nil, invalid(d, "has an unknown currency "+strconv.Quote(cur)+"; use one of "+strings.Join(currencies, ", ")+".")
		}
		f, err := coerce(&definition{name: d.name, kind: kindFloat}, amount)
		if err != nil {
			return nil, invalid(d, "amount must be a decimal.")
		}
		if f.(float64) < 0 {
			return nil, invalid(d, "amount cannot be negative.")
		}
		return map[string]any{"currency": cur, "amount": f.(float64)}, nil
	}

	switch v := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		if len(v) != 2 {
			return nil, badFormat
		}
		amount, hasAmount := v["amount"]
		currency, hasCurrency := v["currency"]
		if !hasAmount || !hasCurrency {
			return nil, badFormat
		}
		return build(amount, currency)
	case string:
		fields := strings.Fields(v)
		if len(fields) == 1 && fields[0] == "null" {
			return nil, nil
		}
		if len(fields) != 2 {
			return nil, badFormat
		}
		return build(fields[0], fields[1])
	case []any:
		if len(v) == 1 && v[0] == "null" {
			return nil, nil
		}
		if len(v) != 2 {
			return nil, badFormat
		}
		return build(v[0], v[1])
	}
	return nil, badFormat
}

// export is the shape settings_get and settings_set return: servers as [host, port] pairs, like
// lbrynet's settings_dict.
func export(d *definition, value any) any {
	switch v := value.(type) {
	case []server:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = []any{s.host, s.port}
		}
		return out
	case []string:
		return append([]string{}, v...)
	case map[string]any:
		return map[string]any{"currency": v["currency"], "amount": v["amount"]}
	}
	return value
}

// persist is the shape written to the settings file: servers as "host:port", like lbrynet's yml.
func persist(value any) any {
	if v, ok := value.([]server); ok {
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = s.host + ":" + strconv.Itoa(s.port)
		}
		return out
	}
	return value
}
