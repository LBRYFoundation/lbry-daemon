package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CommandLine is the startup portion of the legacy lbrynet command line. The
// values in Settings intentionally remain in their argparse representation so
// Store can apply the matching descriptor deserialization.
type CommandLine struct {
	Command          string
	Settings         map[string]any
	Help             bool
	Version          bool
	Quiet            bool
	NoLogging        bool
	Verbose          []string
	VerboseSet       bool
	InitialHeaders   string
	Unknown          []string
	CommandArguments []string
}

// UsageError identifies command-line failures for which argparse exited with
// status 2. Formatting the complete legacy help text is left to the caller.
type UsageError struct {
	Message string
}

func (err *UsageError) Error() string {
	return err.Message
}

type cliAction uint8

const (
	cliStoreSetting cliAction = iota
	cliAppendSetting
	cliEnableSetting
	cliDisableSetting
	cliStoreMaxKeyFee
	cliDisableMaxKeyFee
	cliHelp
	cliVersion
	cliQuiet
	cliNoLogging
	cliVerbose
	cliInitialHeaders
)

type cliOption struct {
	name    string
	action  cliAction
	setting string
}

var negativeArgumentPattern = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

var rootSettingNames = map[string]struct{}{
	"api":                   {},
	"audio_encoder":         {},
	"config":                {},
	"ffmpeg_path":           {},
	"video_bitrate_maximum": {},
	"video_encoder":         {},
	"video_scaler":          {},
	"volume_analysis_time":  {},
	"volume_filter":         {},
}

// ParseCommandLine parses the legacy CLI's startup command and configuration
// flags. API client subcommands are outside this startup parser's scope.
func ParseCommandLine(args []string) (CommandLine, error) {
	return parseCommandLine(args, nil)
}

// ParseCommandLineWithCommands extends ParseCommandLine with the root-level
// API command tokens known by the caller. The matching token and everything
// after it are returned untouched in CommandArguments for docopt parsing.
func ParseCommandLineWithCommands(
	args []string,
	isCommand func(string) bool,
) (CommandLine, error) {
	return parseCommandLine(args, isCommand)
}

func parseCommandLine(args []string, isCommand func(string) bool) (CommandLine, error) {
	result := CommandLine{Settings: make(map[string]any)}
	rootOptions := commandLineOptions(false)
	startOptions := commandLineOptions(true)
	inStart := false
	rootOptionsEnded := false
	startOptionsEnded := false

	for index := 0; index < len(args); index++ {
		token := args[index]
		if inStart && startOptionsEnded {
			result.Unknown = append(result.Unknown, token)
			continue
		}
		if token == "--" {
			if inStart {
				startOptionsEnded = true
				result.Unknown = append(result.Unknown, token)
			} else {
				rootOptionsEnded = true
			}
			continue
		}

		if !inStart && token == "start" {
			// argparse installs every start-parser default into the shared
			// namespace, overwriting root CLIConfig values and root help.
			inStart = true
			result.Command = "start"
			result.Settings = make(map[string]any)
			result.Help = false
			continue
		}
		if !inStart && isCommand != nil && isCommand(token) {
			result.Command = token
			result.CommandArguments = append([]string(nil), args[index:]...)
			result.CommandArguments = append(result.CommandArguments, result.Unknown...)
			result.Help = false
			return result, nil
		}
		if !inStart && rootOptionsEnded {
			return CommandLine{}, usageErrorf(
				"argument COMMAND: invalid choice: '%s' (choose from 'start')", token,
			)
		}

		if !strings.HasPrefix(token, "-") || token == "-" {
			if inStart {
				result.Unknown = append(result.Unknown, token)
				continue
			}
			return CommandLine{}, usageErrorf(
				"argument COMMAND: invalid choice: '%s' (choose from 'start')", token,
			)
		}

		options := rootOptions
		allowAbbreviation := false
		if inStart {
			options = startOptions
			allowAbbreviation = true
		}
		name, attached, hasAttached := splitCLIOption(token)
		option, found, err := resolveCLIOption(name, options, allowAbbreviation)
		if err != nil {
			return CommandLine{}, err
		}
		if !found {
			if !inStart && strings.HasPrefix(token, "-v") && token != "-v" {
				remainder := strings.TrimLeft(token[1:], "v")
				result.Version = true
				if remainder == "" {
					continue
				}
				if strings.HasPrefix(remainder, "=") {
					return CommandLine{}, usageErrorf(
						"argument -v/--version: ignored explicit argument '%s'", remainder[1:],
					)
				}
				result.Unknown = append(result.Unknown, "-"+remainder)
				continue
			}
			result.Unknown = append(result.Unknown, token)
			continue
		}

		next, err := applyCLIOption(&result, option, args, index, attached, hasAttached)
		if err != nil {
			return CommandLine{}, err
		}
		index = next
	}

	return result, nil
}

func commandLineOptions(start bool) []cliOption {
	options := []cliOption{{name: "--help", action: cliHelp}}
	if start {
		options = append(options,
			cliOption{name: "--quiet", action: cliQuiet},
			cliOption{name: "--no-logging", action: cliNoLogging},
			cliOption{name: "--verbose", action: cliVerbose},
			cliOption{name: "--initial-headers", action: cliInitialHeaders},
		)
	} else {
		options = append(options,
			cliOption{name: "-v", action: cliVersion},
			cliOption{name: "--version", action: cliVersion},
		)
	}

	for _, spec := range defaultSpecs(DefaultPaths()) {
		if !start {
			if _, exists := rootSettingNames[spec.Name]; !exists {
				continue
			}
		}
		name := "--" + strings.ReplaceAll(spec.Name, "_", "-")
		switch spec.Kind {
		case KindToggle:
			options = append(options,
				cliOption{name: name, action: cliEnableSetting, setting: spec.Name},
				cliOption{name: "--no-" + strings.TrimPrefix(name, "--"), action: cliDisableSetting, setting: spec.Name},
			)
		case KindMaxKeyFee:
			options = append(options,
				cliOption{name: name, action: cliStoreMaxKeyFee, setting: spec.Name},
				cliOption{name: "--no-" + strings.TrimPrefix(name, "--"), action: cliDisableMaxKeyFee, setting: spec.Name},
			)
		case KindServers, KindStrings:
			options = append(options, cliOption{name: name, action: cliAppendSetting, setting: spec.Name})
		default:
			options = append(options, cliOption{name: name, action: cliStoreSetting, setting: spec.Name})
		}
	}
	return options
}

// StartupOptionNames returns every option accepted by the legacy start parser
// in declaration order. It is used to keep generated help synchronized with
// the settings registry.
func StartupOptionNames() []string {
	options := commandLineOptions(true)
	names := make([]string, len(options))
	for index, option := range options {
		names[index] = option.name
	}
	return names
}

func splitCLIOption(token string) (name, attached string, hasAttached bool) {
	if strings.HasPrefix(token, "--") {
		if optionName, value, found := strings.Cut(token, "="); found {
			return optionName, value, true
		}
	}
	return token, "", false
}

func resolveCLIOption(name string, options []cliOption, abbreviate bool) (cliOption, bool, error) {
	for _, option := range options {
		if option.name == name {
			return option, true, nil
		}
	}
	if !abbreviate || !strings.HasPrefix(name, "--") {
		return cliOption{}, false, nil
	}

	matches := make([]cliOption, 0, 2)
	for _, option := range options {
		if strings.HasPrefix(option.name, "--") && strings.HasPrefix(option.name, name) {
			matches = append(matches, option)
		}
	}
	if len(matches) == 0 {
		return cliOption{}, false, nil
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	names := make([]string, len(matches))
	for index, match := range matches {
		names[index] = match.name
	}
	sort.Strings(names)
	return cliOption{}, false, usageErrorf("ambiguous option: %s could match %s", name, strings.Join(names, ", "))
}

func applyCLIOption(
	result *CommandLine, option cliOption, args []string, index int,
	attached string, hasAttached bool,
) (int, error) {
	switch option.action {
	case cliHelp:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Help = true
	case cliVersion:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Version = true
	case cliQuiet:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Quiet = true
	case cliNoLogging:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.NoLogging = true
	case cliEnableSetting:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Settings[option.setting] = true
	case cliDisableSetting:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Settings[option.setting] = false
	case cliDisableMaxKeyFee:
		if hasAttached {
			return index, explicitArgumentError(option, attached)
		}
		result.Settings[option.setting] = nil
	case cliStoreSetting, cliInitialHeaders, cliAppendSetting:
		value, next, err := oneCLIArgument(option, args, index, attached, hasAttached)
		if err != nil {
			return index, err
		}
		switch option.action {
		case cliStoreSetting:
			result.Settings[option.setting] = value
		case cliInitialHeaders:
			result.InitialHeaders = value
		case cliAppendSetting:
			values, _ := result.Settings[option.setting].([]string)
			result.Settings[option.setting] = append(values, value)
		}
		return next, nil
	case cliStoreMaxKeyFee, cliVerbose:
		values, next := manyCLIArguments(args, index, attached, hasAttached)
		if option.action == cliStoreMaxKeyFee {
			if len(values) == 0 {
				return index, usageErrorf("argument %s: expected at least one argument", option.name)
			}
			result.Settings[option.setting] = values
		} else {
			result.VerboseSet = true
			result.Verbose = values
		}
		return next, nil
	}
	return index, nil
}

func oneCLIArgument(
	option cliOption, args []string, index int, attached string, hasAttached bool,
) (string, int, error) {
	if hasAttached {
		return attached, index, nil
	}
	if index+1 >= len(args) || isCLIOptionToken(args[index+1]) {
		return "", index, usageErrorf("argument %s: expected one argument", option.name)
	}
	return args[index+1], index + 1, nil
}

func manyCLIArguments(
	args []string, index int, attached string, hasAttached bool,
) ([]string, int) {
	if hasAttached {
		return []string{attached}, index
	}
	values := make([]string, 0)
	next := index
	for next+1 < len(args) {
		candidate := args[next+1]
		if candidate == "--" || isCLIOptionToken(candidate) {
			break
		}
		values = append(values, candidate)
		next++
	}
	return values, next
}

func isCLIOptionToken(token string) bool {
	return len(token) > 1 && strings.HasPrefix(token, "-") && !negativeArgumentPattern.MatchString(token)
}

func explicitArgumentError(option cliOption, value string) error {
	return usageErrorf("argument %s: ignored explicit argument '%s'", option.name, value)
}

func usageErrorf(format string, values ...any) error {
	return &UsageError{Message: fmt.Sprintf(format, values...)}
}
