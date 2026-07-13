package stream

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"lbry/daemon/blob"
)

func TestPrepareStreamRangeMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runStreamRangeOracle(t)
	cases := oracle["cases"].(map[string]any)
	fixtures := map[string]struct {
		header  string
		lengths []int
	}{
		"default":       {"bytes=0-", []int{11, 6}},
		"bounded":       {"bytes=3-7", []int{11, 6}},
		"second_blob":   {"bytes=4194303-4194306", []int{4194304, 20}},
		"start_at_size": {"bytes=15-", []int{11, 6}},
		"end_past_size": {"bytes=3-15", []int{11, 6}},
	}
	for name, fixture := range fixtures {
		descriptor := &blob.StreamDescriptor{}
		for index, length := range fixture.lengths {
			descriptor.Blobs = append(descriptor.Blobs, blob.BlobInfo{
				BlobHash: name, BlobNum: index, Length: length,
			})
		}
		descriptor.Blobs = append(descriptor.Blobs, blob.BlobInfo{BlobNum: len(fixture.lengths)})
		got, err := prepareStreamRange(fixture.header, descriptor)
		want := cases[name].(map[string]any)
		if pythonError, failed := want["error"].(string); failed {
			if err == nil || pythonError != "HTTPRequestRangeNotSatisfiable" {
				t.Fatalf("%s Go range = %#v, %v; Python = %#v", name, got, err, want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s Go range error = %v", name, err)
		}
		headers := want["headers"].(map[string]any)
		if got.total != int(want["size"].(float64)) ||
			headers["Content-Range"] != streamRangeContentRange(got) ||
			headers["Content-Length"] != streamRangeContentLength(got) {
			t.Fatalf("%s Go range = %#v; Python = %#v", name, got, want)
		}
	}
}

func streamRangeContentRange(value preparedStreamRange) string {
	return "bytes " + strconv.Itoa(value.start) + "-" +
		strconv.Itoa(value.end) + "/" + strconv.Itoa(value.total)
}

func streamRangeContentLength(value preparedStreamRange) string {
	return strconv.Itoa(value.end - value.start + 1)
}

func runStreamRangeOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate stream range oracle")
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
	command := exec.Command(python, filepath.Join(daemonRoot, "compat", "stream_range_oracle.py"), "--sdk-root", sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python stream range oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode stream range oracle: %v\n%s", err, output)
	}
	return response
}
