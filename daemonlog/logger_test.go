package daemonlog

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureStandardLoggerRoutesDefaultOutput(t *testing.T) {
	directory := t.TempDir()
	var previous bytes.Buffer
	var console bytes.Buffer
	log.SetOutput(&previous)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	controller, err := ConfigureStandardLogger(Options{DataDir: directory, Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	log.Print("routed message")
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	log.Print("restored message")

	fileContents, err := os.ReadFile(filepath.Join(directory, logFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileContents), "routed message") || strings.Contains(string(fileContents), "restored message") {
		t.Fatalf("file log = %q", fileContents)
	}
	if !strings.Contains(console.String(), "routed message") {
		t.Fatalf("console log = %q", console.String())
	}
	if !strings.Contains(previous.String(), "restored message") {
		t.Fatalf("restored output = %q", previous.String())
	}
}

func TestConfigureStandardLoggerQuietKeepsOnlyFile(t *testing.T) {
	directory := t.TempDir()
	var console bytes.Buffer
	previous := log.Writer()
	defer log.SetOutput(previous)

	controller, err := ConfigureStandardLogger(Options{DataDir: directory, Quiet: true, Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	log.Print("quiet message")
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}

	fileContents, err := os.ReadFile(filepath.Join(directory, logFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileContents), "quiet message") {
		t.Fatalf("file log = %q", fileContents)
	}
	if console.Len() != 0 {
		t.Fatalf("quiet console log = %q", console.String())
	}
}

func TestConfigureStandardLoggerNoLoggingCreatesNoOutput(t *testing.T) {
	directory := t.TempDir()
	var console bytes.Buffer
	previous := log.Writer()
	defer log.SetOutput(previous)

	controller, err := ConfigureStandardLogger(Options{DataDir: directory, NoLogging: true, Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	log.Print("discarded message")
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}

	if console.Len() != 0 {
		t.Fatalf("no-logging console = %q", console.String())
	}
	if _, err := os.Stat(filepath.Join(directory, logFilename)); !os.IsNotExist(err) {
		t.Fatalf("no-logging file exists: %v", err)
	}
}

func TestControllerDebugSelection(t *testing.T) {
	allLBRY := &Controller{verboseSet: true}
	if !allLBRY.DebugEnabled("lbry") || !allLBRY.DebugEnabled("lbry.wallet") || allLBRY.DebugEnabled("aiohttp") {
		t.Fatal("empty --verbose selection does not match the lbry logger hierarchy")
	}
	selective := &Controller{verboseSet: true, verbose: []string{"lbry.wallet", "aiohttp"}}
	if !selective.DebugEnabled("lbry.wallet") || !selective.DebugEnabled("lbry.wallet.server") ||
		!selective.DebugEnabled("aiohttp") || selective.DebugEnabled("lbry") {
		t.Fatal("selective --verbose matching is incorrect")
	}
	disabled := &Controller{verboseSet: true, noLogging: true}
	if disabled.DebugEnabled("lbry") {
		t.Fatal("no-logging enabled debug output")
	}
}

func TestRotatingFileKeepsConfiguredBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lbrynet.log")
	file, err := NewRotatingFile(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first\n", "second\n", "third\n", "fourth\n"} {
		if _, err := file.Write([]byte(message)); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContents(t, path, "fourth\n")
	assertFileContents(t, path+".1", "third\n")
	assertFileContents(t, path+".2", "second\n")
	if _, err := file.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close error = %v", err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s = %q, want %q", path, contents, want)
	}
}
