package wallet

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidLBRYURL = errors.New("Invalid LBRY URL")

// LBRYURL is the resolve-relevant projection of schema.url.URL.
type LBRYURL struct {
	HasStream          bool
	HasStreamInChannel bool
}

// LBRYURLParseError preserves URL.parse's ValueError boundary.
type LBRYURLParseError struct{}

func (*LBRYURLParseError) Error() string           { return ErrInvalidLBRYURL.Error() }
func (*LBRYURLParseError) PythonErrorName() string { return "ValueError" }
func (*LBRYURLParseError) Unwrap() error           { return ErrInvalidLBRYURL }

// ParseLBRYURL mirrors the pinned schema URL grammar. A single final newline
// is ignored by Python's `$` regex anchor; the original caller string remains
// unchanged and is still sent to the Hub.
func ParseLBRYURL(url string) (LBRYURL, error) {
	if !utf8.ValidString(url) {
		return LBRYURL{}, &LBRYURLParseError{}
	}
	path := url
	if strings.HasSuffix(path, "\n") {
		path = strings.TrimSuffix(path, "\n")
	}
	if strings.HasPrefix(path, "lbry://") {
		path = strings.TrimPrefix(path, "lbry://")
	}
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 1:
		channel := strings.HasPrefix(parts[0], "@")
		if validLBRYURLSegment(parts[0], channel) {
			return LBRYURL{HasStream: !channel}, nil
		}
	case 2:
		if validLBRYURLSegment(parts[0], true) &&
			validLBRYURLSegment(parts[1], false) {
			return LBRYURL{HasStream: true, HasStreamInChannel: true}, nil
		}
	}
	return LBRYURL{}, &LBRYURLParseError{}
}

func validLBRYURLSegment(segment string, channel bool) bool {
	name := segment
	if channel {
		if !strings.HasPrefix(name, "@") {
			return false
		}
		name = strings.TrimPrefix(name, "@")
	} else if strings.HasPrefix(name, "@") {
		return false
	}
	selector := strings.IndexAny(name, ":#$")
	if selector >= 0 {
		suffix := name[selector+1:]
		switch name[selector] {
		case ':', '#':
			if len(suffix) < 1 || len(suffix) > 40 {
				return false
			}
			for _, character := range suffix {
				if !strings.ContainsRune("0123456789abcdef", character) {
					return false
				}
			}
		case '$':
			if len(suffix) < 1 || suffix[0] < '1' || suffix[0] > '9' {
				return false
			}
			for index := 1; index < len(suffix); index++ {
				if suffix[index] < '0' || suffix[index] > '9' {
					return false
				}
			}
		}
		name = name[:selector]
	}
	if name == "" {
		return false
	}
	for _, character := range name {
		if character <= 0x20 || character == 0xfffe || character == 0xffff ||
			strings.ContainsRune("=&#:$@%?;\"/\\<>%{}|^~`[]", character) {
			return false
		}
	}
	return true
}
