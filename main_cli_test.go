package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"lbry/daemon/config"
	"lbry/daemon/legacycli"
	"lbry/daemon/rpc"
)

func TestRunCLIHelpAndVersionDoNotStartDaemon(t *testing.T) {
	start := func(config.CommandLine) error {
		t.Fatal("daemon started")
		return nil
	}

	var stdout, stderr bytes.Buffer
	if code := runCLI(nil, &stdout, &stderr, start); code != 0 {
		t.Fatalf("no-argument exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage:  lbrynet [-v] [--api HOST:PORT]") || stderr.Len() != 0 {
		t.Fatalf("root help stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"--version"}, &stdout, &stderr, start); code != 0 {
		t.Fatalf("version exit code = %d", code)
	}
	if got, want := stdout.String(), "lbrynet 0.113.0\n"; got != want || stderr.Len() != 0 {
		t.Fatalf("version stdout=%q stderr=%q, want %q", got, stderr.String(), want)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"start", "--help"}, &stdout, &stderr, start); code != 0 {
		t.Fatalf("start help exit code = %d", code)
	}
	for _, option := range config.StartupOptionNames() {
		if !strings.Contains(stdout.String(), "  "+option+"\n") {
			t.Errorf("start help is missing %s", option)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("start help stderr = %q", stderr.String())
	}
}

func TestRunCLIStartsWithParsedSettings(t *testing.T) {
	var received config.CommandLine
	start := func(commandLine config.CommandLine) error {
		received = commandLine
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{
		"start", "--tcp-port", "4455", "--no-use-upnp",
		"--initial-headers", "headers.bin", "--unknown",
	}, &stdout, &stderr, start)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if received.Command != "start" || received.Settings["tcp_port"] != "4455" || received.Settings["use_upnp"] != false {
		t.Fatalf("received command line = %#v", received)
	}
	if received.InitialHeaders != "headers.bin" || len(received.Unknown) != 1 || received.Unknown[0] != "--unknown" {
		t.Fatalf("received startup extras = %#v", received)
	}
}

func TestRunCLIExitCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"start", "--tcp-port"}, &stdout, &stderr, func(config.CommandLine) error {
		t.Fatal("daemon started after parse error")
		return nil
	})
	if code != 2 || !strings.Contains(stderr.String(), "argument --tcp-port: expected one argument") {
		t.Fatalf("parse failure exit=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	wantErr := errors.New("startup failed")
	code = runCLI([]string{"start"}, &stdout, &stderr, func(config.CommandLine) error { return wantErr })
	if code != 1 || stdout.Len() != 0 || stderr.String() != "startup failed\n" {
		t.Fatalf("startup failure exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLILegacyRPCCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))
	t.Setenv("XDG_DOWNLOAD_DIR", filepath.Join(root, "downloads"))

	var receivedEndpoint string
	var received legacycli.Invocation
	call := func(_ context.Context, endpoint string, invocation legacycli.Invocation) (json.RawMessage, error) {
		receivedEndpoint = endpoint
		received = invocation
		return json.RawMessage(`{"changed":true}`), nil
	}
	var stdout, stderr bytes.Buffer
	code := runCLIWithClient([]string{
		"--api", "daemon.test:9999",
		"--config", filepath.Join(root, "settings.yml"),
		"settings", "set", "udp_port", "5000",
	}, &stdout, &stderr, func(config.CommandLine) error {
		t.Fatal("start called for RPC command")
		return nil
	}, call)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if receivedEndpoint != "http://daemon.test:9999/lbryapi" || received.Method != "settings_set" {
		t.Fatalf("endpoint=%q invocation=%#v", receivedEndpoint, received)
	}
	if received.Params["key"] != "udp_port" || received.Params["value"] != 5000 {
		t.Fatalf("params = %#v", received.Params)
	}
	if stdout.String() != "{\n  \"changed\": true\n}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCLILegacyHelpConnectionAndUsage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))
	t.Setenv("XDG_DOWNLOAD_DIR", filepath.Join(root, "downloads"))

	start := func(config.CommandLine) error {
		t.Fatal("start called for RPC command")
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runCLIWithClient([]string{"account", "--help"}, &stdout, &stderr, start,
		func(context.Context, string, legacycli.Invocation) (json.RawMessage, error) {
			t.Fatal("RPC called for help")
			return nil, nil
		})
	if code != 0 || !strings.Contains(stdout.String(), "account") || stderr.Len() != 0 {
		t.Fatalf("help exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLIWithClient([]string{"status"}, &stdout, &stderr, start,
		func(context.Context, string, legacycli.Invocation) (json.RawMessage, error) {
			return nil, &legacycli.ConnectionError{Cause: errors.New("refused")}
		})
	if code != 0 || stdout.String() != legacycli.ConnectionErrorMessage+"\n" || stderr.Len() != 0 {
		t.Fatalf("connection exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLIWithClient([]string{"publish"}, &stdout, &stderr, start, nil)
	if code != 1 || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("usage exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLIWithClient([]string{"account", "wat"}, &stdout, &stderr, start, nil)
	if code != 2 || !strings.Contains(stderr.String(), "unknown account command") {
		t.Fatalf("group routing exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestLegacyCLIQueriesRunningDaemon(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config-home"))
	t.Setenv("XDG_DOWNLOAD_DIR", filepath.Join(root, "downloads"))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	status := newComponentStatus("cli-installation", []string{"wallet"}, map[string]any{
		"available": false,
		"which":     nil,
	})
	app := newDaemonApp(status)
	server := rpc.CreateServer(rpc.WithStatusProvider(status), rpc.WithShutdown(app.RequestStop))
	app.services = []managedService{{
		name:     "rpc",
		listener: listener,
		serve:    server.Serve,
		shutdown: server.Shutdown,
	}}
	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(context.Background()) }()

	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"--api", listener.Addr().String(), "status"}, &stdout, &stderr,
		func(config.CommandLine) error {
			t.Fatal("start called for status command")
			return nil
		})
	app.RequestStop()
	if err := <-runResult; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode CLI output: %v\n%s", err, stdout.String())
	}
	if result["installation_id"] != "cli-installation" || result["is_running"] != false {
		t.Fatalf("status output = %#v", result)
	}
}
