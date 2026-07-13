package database

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedStreamStorageMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runManagedStreamStorageOracle(t)
	if oracle["rowid"] != float64(1) {
		t.Fatalf("Python file rowid = %#v", oracle["rowid"])
	}
	blobs := oracle["blob"].([]any)
	streams := oracle["stream"].([]any)
	streamBlobs := oracle["stream_blob"].([]any)
	files := oracle["file"].([]any)
	if len(blobs) != 3 || len(streams) != 1 || len(streamBlobs) != 4 || len(files) != 1 {
		t.Fatalf("Python storage counts = blobs %d streams %d stream_blobs %d files %d",
			len(blobs), len(streams), len(streamBlobs), len(files))
	}
	file := files[0].([]any)
	if file[0] != "stream" || file[2] != "6d6f7669652e6d7034" ||
		file[3] != "<DOWNLOAD>" || file[4] != 0.25 || file[5] != "running" ||
		file[6] != float64(1) || file[8] != float64(101) {
		t.Fatalf("Python managed file row = %#v", file)
	}
}

func runManagedStreamStorageOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate managed stream storage oracle")
	}
	daemonRoot := filepath.Dir(filepath.Dir(source))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python,
		filepath.Join(daemonRoot, "compat", "managed_stream_storage_oracle.py"),
		"--sdk-root", sdkRoot,
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python managed stream storage oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode managed stream storage oracle: %v\n%s", err, output)
	}
	return response
}
