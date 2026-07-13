package spv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	spvOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	spvOraclePinnedVersion = "0.113.0"
)

var spvOraclePinnedSources = map[string]string{
	"lbry/__init__.py":           "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/network.py":     "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/rpc/session.py": "1e4b2a1e49b6bc55f0978e2cb90d98b65533722760190556a60bed347f2eaed7",
	"lbry/wallet/rpc/jsonrpc.py": "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
	"lbry/wallet/rpc/framing.py": "8e18f9fe4c05344124ef92806ecba4563f979285303ef9382cbd0fdef943e0d6",
}

type spvOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion             string   `json:"python_version"`
		ClientName                string   `json:"client_name"`
		ProtocolMinimum           []int    `json:"protocol_minimum"`
		ProtocolMaximum           string   `json:"protocol_maximum"`
		ConnectTimeoutSeconds     int      `json:"connect_timeout_seconds"`
		VersionTimeoutSeconds     int      `json:"version_timeout_seconds"`
		RequestTimeoutSeconds     int      `json:"request_timeout_seconds"`
		Concurrency               int      `json:"concurrency"`
		NewlineFraming            bool     `json:"newline_framing"`
		JSONRPC                   string   `json:"jsonrpc"`
		LegacyMaxFrameSize        uint64   `json:"legacy_max_frame_size"`
		EffectiveReconnectSeconds int      `json:"effective_reconnect_seconds"`
		SleepDelayVariableUnused  bool     `json:"sleep_delay_variable_unused"`
		RetryExceptions           []string `json:"retry_exceptions"`
		RestrictedIsInert         bool     `json:"restricted_is_inert"`
		HandshakeMethods          []string `json:"handshake_methods"`
	} `json:"metadata"`
	Requests  []spvOracleRequest      `json:"requests"`
	Versions  []spvOracleVersion      `json:"versions"`
	Responses []spvOracleResponseCase `json:"responses"`
}

type spvOracleRequest struct {
	Method        string `json:"method"`
	ID            int64  `json:"id"`
	Params        []any  `json:"params"`
	ParamsPresent bool   `json:"params_present"`
	Encoded       string `json:"encoded"`
}

type spvOracleVersion struct {
	Version      string  `json:"version"`
	Compatible   *bool   `json:"compatible"`
	ErrorType    *string `json:"error_type"`
	ErrorMessage *string `json:"error_message"`
}

type spvOracleResponseCase struct {
	Name         string         `json:"name"`
	Payload      map[string]any `json:"payload"`
	Result       any            `json:"result"`
	ErrorType    *string        `json:"error_type"`
	ErrorMessage *string        `json:"error_message"`
}

func TestSPVNetworkMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runSPVNetworkOracle(t)
	if oracle.Reference.Commit != spvOraclePinnedCommit ||
		oracle.Reference.Version != spvOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, spvOraclePinnedSources) {
		t.Fatalf("SPV oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if metadata.ClientName != DefaultClientName ||
		!reflect.DeepEqual(metadata.ProtocolMinimum, []int{0, 65, 0}) ||
		metadata.ProtocolMaximum != DefaultProtocolMaximum ||
		metadata.ConnectTimeoutSeconds != int(DefaultConnectTimeout/time.Second) ||
		metadata.VersionTimeoutSeconds != int(DefaultVersionTimeout/time.Second) ||
		metadata.RequestTimeoutSeconds != int(DefaultRequestTimeout/time.Second) ||
		metadata.Concurrency != DefaultConcurrency || !metadata.NewlineFraming ||
		metadata.JSONRPC != "2.0" || metadata.LegacyMaxFrameSize != 1<<32 ||
		metadata.EffectiveReconnectSeconds != int(DefaultReconnectDelay/time.Second) ||
		!metadata.SleepDelayVariableUnused ||
		!reflect.DeepEqual(metadata.RetryExceptions, []string{"asyncio.TimeoutError", "ConnectionError"}) ||
		!metadata.RestrictedIsInert ||
		!reflect.DeepEqual(metadata.HandshakeMethods, []string{
			"server.version", "server.features", "server.peers.subscribe", "blockchain.headers.subscribe",
		}) {
		t.Fatalf("SPV oracle metadata = %+v", metadata)
	}
	if DefaultMaxFrameSize >= int(metadata.LegacyMaxFrameSize) {
		t.Fatalf("Go frame cap %d did not retain the documented safety bound", DefaultMaxFrameSize)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata.PythonVersion != want {
		t.Fatalf("SPV oracle Python version = %q, want %q", metadata.PythonVersion, want)
	}

	for _, fixture := range oracle.Requests {
		encoded, err := encodeRequest(fixture.Method, fixture.Params, fixture.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasSuffix(encoded, []byte{'\n'}) {
			t.Fatalf("Go request is not newline-framed: %q", encoded)
		}
		goPayload, err := decodeJSONValue(bytes.TrimSuffix(encoded, []byte{'\n'}))
		if err != nil {
			t.Fatal(err)
		}
		pythonPayload, err := decodeJSONValue(json.RawMessage(strings.TrimSuffix(fixture.Encoded, "\n")))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(goPayload, pythonPayload) {
			t.Fatalf("request %s differs\nGo:     %#v\nPython: %#v", fixture.Method, goPayload, pythonPayload)
		}
		mapping := goPayload.(map[string]any)
		_, paramsPresent := mapping["params"]
		if paramsPresent != fixture.ParamsPresent {
			t.Fatalf("request %s params presence = %t, want %t", fixture.Method, paramsPresent, fixture.ParamsPresent)
		}
	}

	for _, fixture := range oracle.Versions {
		compatible, err := protocolAtLeast(fixture.Version, DefaultProtocolMinimum)
		if fixture.ErrorType != nil {
			if err == nil {
				t.Fatalf("invalid Python version %q was accepted", fixture.Version)
			}
			continue
		}
		if err != nil || fixture.Compatible == nil || compatible != *fixture.Compatible {
			t.Fatalf("version %q = %t, %v; Python %v", fixture.Version, compatible, err, fixture.Compatible)
		}
	}

	for _, fixture := range oracle.Responses {
		t.Run(fixture.Name, func(t *testing.T) {
			client, server := newPipeClient(t, ClientConfig{})
			result := make(chan callResult, 1)
			go func() {
				value, err := client.Call(context.Background(), "oracle.response", nil)
				result <- callResult{value: value, err: err}
			}()
			request := readPipeRequest(t, server)
			fixture.Payload["id"] = *request.ID
			encoded, err := json.Marshal(fixture.Payload)
			if err != nil {
				t.Fatal(err)
			}
			writePipeRaw(t, server, string(append(encoded, '\n')))
			got := <-result
			switch {
			case fixture.ErrorType == nil:
				if got.err != nil || !reflect.DeepEqual(got.value, fixture.Result) {
					t.Fatalf("response = %#v, %v; want %#v", got.value, got.err, fixture.Result)
				}
			case *fixture.ErrorType == "RPCError":
				var rpcErr *RPCError
				if !errors.As(got.err, &rpcErr) || fixture.ErrorMessage == nil || rpcErr.Message != *fixture.ErrorMessage {
					t.Fatalf("RPC response error = %#v, %v", rpcErr, got.err)
				}
			case *fixture.ErrorType == "ProtocolError":
				var protocolErr *ProtocolError
				if !errors.As(got.err, &protocolErr) || fixture.ErrorMessage == nil || protocolErr.Message != *fixture.ErrorMessage {
					t.Fatalf("protocol response error = %#v, %v", protocolErr, got.err)
				}
			default:
				t.Fatalf("unknown oracle error type %q", *fixture.ErrorType)
			}
		})
	}
}

func runSPVNetworkOracle(t *testing.T) spvOracleResponse {
	t.Helper()
	sdkRoot, script := spvOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python SPV network oracle failed: %v\n%s", err, output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var oracle spvOracleResponse
	if err := decoder.Decode(&oracle); err != nil {
		t.Fatalf("decode SPV network oracle: %v\n%s", err, output)
	}
	return oracle
}

func spvOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate SPV oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "spv_network_oracle.py")
	for relative := range spvOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local SPV source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("SPV oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}
