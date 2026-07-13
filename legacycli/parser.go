package legacycli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	docopt "github.com/docopt/docopt-go"
)

// Invocation is one parsed legacy CLI RPC call.
type Invocation struct {
	RequestedMethod string
	Method          string
	Params          map[string]any
}

// ParseResult contains either an invocation or help text. Notice is populated
// when a deprecated command was mapped to its replacement.
type ParseResult struct {
	Invocation *Invocation
	Help       string
	Notice     string
}

// UsageError represents invalid command selection or command arguments.
type UsageError struct {
	Message string
	Help    string
}

func (err *UsageError) Error() string {
	return err.Message
}

// Parse resolves a legacy root/group command and applies its pinned docopt
// grammar. Leading global configuration options are parsed by config.CommandLine
// and are intentionally outside this function.
func Parse(arguments []string) (ParseResult, error) {
	manifest, err := loadManifest()
	if err != nil {
		return ParseResult{}, err
	}
	if len(arguments) == 0 || len(arguments) == 1 && arguments[0] == "--help" {
		return ParseResult{Help: RootHelp()}, nil
	}

	var definition commandDefinition
	var commandArgs []string
	if grouped, exists := manifest.grouped[arguments[0]]; exists {
		groupHelp, _ := GroupHelp(arguments[0])
		if len(arguments) == 1 || arguments[1] == "--help" {
			return ParseResult{Help: groupHelp}, nil
		}
		var found bool
		definition, found = grouped[arguments[1]]
		if !found {
			return ParseResult{}, &UsageError{
				Message: fmt.Sprintf("unknown %s command %q", arguments[0], arguments[1]),
				Help:    groupHelp,
			}
		}
		commandArgs = arguments[2:]
	} else {
		var found bool
		definition, found = manifest.root[arguments[0]]
		if !found {
			return ParseResult{}, &UsageError{
				Message: fmt.Sprintf("unknown command %q", arguments[0]),
				Help:    RootHelp(),
			}
		}
		commandArgs = arguments[1:]
	}

	requestedMethod := definition.Method
	notice := ""
	if definition.Replacement != nil {
		replacement, exists := manifest.commands[*definition.Replacement]
		if !exists {
			return ParseResult{}, fmt.Errorf("deprecated CLI method %q has unknown replacement %q", definition.Method, *definition.Replacement)
		}
		notice = fmt.Sprintf("%s is deprecated, using %s.", definition.Method, replacement.Method)
		definition = replacement
	}
	for _, argument := range commandArgs {
		if argument == "--" {
			break
		}
		if argument == "--help" {
			return ParseResult{Help: definition.Doc, Notice: notice}, nil
		}
	}

	parsed, usage, err := parseDocopt(definition.Doc, commandArgs)
	if err != nil {
		if usage != "" {
			return ParseResult{}, &UsageError{Message: strings.TrimSpace(usage), Help: definition.Doc}
		}
		return ParseResult{}, fmt.Errorf("parse %s command grammar: %w", definition.Method, err)
	}
	return ParseResult{
		Notice: notice,
		Invocation: &Invocation{
			RequestedMethod: requestedMethod,
			Method:          definition.Method,
			Params:          normalizeDocopt(parsed),
		},
	}, nil
}

func parseDocopt(document string, arguments []string) (docopt.Opts, string, error) {
	usage := ""
	parser := &docopt.Parser{
		HelpHandler: func(_ error, output string) {
			usage = output
		},
		SkipHelpFlags: true,
	}
	parsed, err := parser.ParseArgs(compatibleDocoptDocument(document), arguments, "")
	usage = restorePinnedDocTypos(usage)
	return parsed, usage, err
}

func compatibleDocoptDocument(document string) string {
	document = strings.ReplaceAll(
		document,
		"--trending_global=<trending_global]",
		"--trending_global=<trending_global>]",
	)
	return strings.ReplaceAll(
		document,
		"--trending_score=<trending_score]",
		"--trending_score=<trending_score>]",
	)
}

func restorePinnedDocTypos(document string) string {
	document = strings.ReplaceAll(
		document,
		"--trending_global=<trending_global>]",
		"--trending_global=<trending_global]",
	)
	return strings.ReplaceAll(
		document,
		"--trending_score=<trending_score>]",
		"--trending_score=<trending_score]",
	)
}

func normalizeDocopt(parsed docopt.Opts) map[string]any {
	params := make(map[string]any)
	for key, value := range parsed {
		if value == nil {
			continue
		}
		name := normalizeDocoptKey(key)
		if _, exists := params[name]; exists {
			// Docopt alternatives expose both the positional and option name,
			// but at most one has a non-nil value in a valid invocation.
			continue
		}
		params[name] = normalizeCLIValue(value, name)
	}
	return params
}

func normalizeDocoptKey(key string) string {
	key = strings.TrimPrefix(key, "--")
	if strings.HasPrefix(key, "<") && strings.HasSuffix(key, ">") {
		return key[1 : len(key)-1]
	}
	return key
}

var protectedStringKeys = map[string]struct{}{
	"uri":                {},
	"channel_name":       {},
	"name":               {},
	"file_name":          {},
	"claim_name":         {},
	"download_directory": {},
}

func normalizeCLIValue(value any, key string) any {
	text, isString := value.(string)
	if !isString {
		return value
	}
	if _, protected := protectedStringKeys[key]; protected {
		return text
	}
	switch strings.ToLower(text) {
	case "true":
		return true
	case "false":
		return false
	}
	digits, ok := decimalDigits(text)
	if !ok {
		return text
	}
	if number, err := strconv.Atoi(digits); err == nil {
		return number
	}
	number := strings.TrimLeft(digits, "0")
	if number == "" {
		number = "0"
	}
	return json.Number(number)
}

func decimalDigits(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	var normalized strings.Builder
	for _, character := range value {
		digit, ok := unicodeDecimalDigit(character)
		if !ok {
			return "", false
		}
		normalized.WriteByte(byte('0' + digit))
	}
	return normalized.String(), true
}

func unicodeDecimalDigit(character rune) (int, bool) {
	if character >= '0' && character <= '9' {
		return int(character - '0'), true
	}
	for _, item := range unicode.Nd.R16 {
		value := uint32(character)
		if value >= uint32(item.Lo) && value <= uint32(item.Hi) &&
			(value-uint32(item.Lo))%uint32(item.Stride) == 0 {
			return int((value-uint32(item.Lo))/uint32(item.Stride)) % 10, true
		}
	}
	for _, item := range unicode.Nd.R32 {
		value := uint32(character)
		if value >= item.Lo && value <= item.Hi && (value-item.Lo)%item.Stride == 0 {
			return int((value-item.Lo)/item.Stride) % 10, true
		}
	}
	return 0, false
}
