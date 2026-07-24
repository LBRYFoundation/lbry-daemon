package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"lbry/daemon/config"
	"lbry/daemon/daemonlog"
	"lbry/daemon/legacycli"
)

const legacyCLIVersion = "0.113.0"

const rootHelpHeader = `Usage:  lbrynet [-v] [--api HOST:PORT] COMMAND ...

An interface to the LBRY Network.

Options:
  --help              Show this help message and exit.
  -v, --version       Show lbrynet CLI version and exit.
  --api HOST:PORT     Host name and port for lbrynet daemon API.
`

func main() {
	exitCode := runCLI(os.Args[1:], os.Stdout, os.Stderr, runProductionDaemon)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func runCLI(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	start func(config.CommandLine) error,
) int {
	return runCLIWithClient(args, stdout, stderr, start, callLegacyRPC)
}

type legacyCallFunc func(context.Context, string, legacycli.Invocation) (json.RawMessage, error)

func runCLIWithClient(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	start func(config.CommandLine) error,
	call legacyCallFunc,
) int {
	commandLine, err := config.ParseCommandLineWithCommands(args, legacycli.IsCommandToken)
	if err != nil {
		if containsArgument(args, "start") {
			fmt.Fprint(stderr, formatStartHelp())
		} else {
			fmt.Fprint(stderr, formatRootHelp())
		}
		fmt.Fprintf(stderr, "\n%s\n", err)
		return 2
	}

	if commandLine.Version {
		fmt.Fprintf(stdout, "lbrynet %s\n", legacyCLIVersion)
		return 0
	}
	if commandLine.Command == "" {
		fmt.Fprint(stdout, formatRootHelp())
		return 0
	}
	if commandLine.Command == "start" {
		if commandLine.Help {
			fmt.Fprint(stdout, formatStartHelp())
			return 0
		}
		if err := start(commandLine); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return runLegacyCLICommand(commandLine, stdout, stderr, call)
}

func formatRootHelp() string {
	clientHelp := strings.TrimPrefix(legacycli.RootHelp(), "Usage:  lbrynet COMMAND\n\n")
	clientHelp = strings.Replace(clientHelp, "Commands:\n", "Commands:\n  start                Start LBRY Network interface.\n", 1)
	return rootHelpHeader + "\n" + clientHelp + "\nRun 'lbrynet COMMAND --help' for more information on a command or group.\n"
}

func formatStartHelp() string {
	var help strings.Builder
	help.WriteString("Usage:  lbrynet start [--config FILE] [--data-dir DIR] [--wallet-dir DIR] [--download-dir DIR] ...\n\n")
	help.WriteString("Options:\n")
	for _, name := range config.StartupOptionNames() {
		help.WriteString("  ")
		help.WriteString(name)
		help.WriteByte('\n')
	}
	return help.String()
}

func containsArgument(arguments []string, target string) bool {
	for _, argument := range arguments {
		if argument == target {
			return true
		}
	}
	return false
}

func runLegacyCLICommand(
	commandLine config.CommandLine,
	stdout io.Writer,
	stderr io.Writer,
	call legacyCallFunc,
) int {
	paths := config.DefaultPaths()
	settings, err := config.New(config.Options{Paths: &paths, Arguments: commandLine.Settings})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := prepareStartupDirectories(settings); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	api, err := stringSetting(settings, "api")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	parsed, err := legacycli.Parse(commandLine.CommandArguments)
	if err != nil {
		var usage *legacycli.UsageError
		if errors.As(err, &usage) && strings.HasPrefix(usage.Message, "unknown ") {
			fmt.Fprint(stderr, usage.Help)
			fmt.Fprintf(stderr, "\n%s\n", usage.Message)
			return 2
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if parsed.Notice != "" {
		fmt.Fprintln(stdout, parsed.Notice)
	}
	if parsed.Help != "" {
		fmt.Fprintln(stdout, parsed.Help)
		return 0
	}
	if parsed.Invocation == nil {
		fmt.Fprintln(stderr, "legacy command did not produce an invocation")
		return 1
	}
	payload, err := call(context.Background(), "http://"+api+"/lbryapi", *parsed.Invocation)
	if err != nil {
		var connection *legacycli.ConnectionError
		if errors.As(err, &connection) {
			fmt.Fprintln(stdout, connection.Error())
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := legacycli.WriteDisplay(stdout, payload); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func callLegacyRPC(
	ctx context.Context,
	endpoint string,
	invocation legacycli.Invocation,
) (json.RawMessage, error) {
	return (legacycli.Client{Endpoint: endpoint}).Call(ctx, invocation)
}

func runProductionDaemon(commandLine config.CommandLine) error {
	app, err := newProductionAppWithLogging(
		commandLine.Settings,
		commandLine.InitialHeaders,
		&daemonlog.Options{
			Quiet:      commandLine.Quiet,
			NoLogging:  commandLine.NoLogging,
			Verbose:    commandLine.Verbose,
			VerboseSet: commandLine.VerboseSet,
		},
	)
	if err != nil {
		return err
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return app.Run(ctx)
}
