package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const (
	headerFetchOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	headerFetchOraclePinnedVersion = "0.113.0"
)

var headerFetchOraclePinnedSources = map[string]string{
	"lbry/__init__.py":       "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/header.py":  "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
	"lbry/wallet/ledger.py":  "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/network.py": "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
}

type headerFetchOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion       string `json:"python_version"`
		PythonAssertions    bool   `json:"python_assertions"`
		RawDeflate          bool   `json:"raw_deflate"`
		Base64Validate      bool   `json:"base64_validate"`
		ZlibBufsizeIsLimit  bool   `json:"zlib_bufsize_is_limit"`
		RPCMethod           string `json:"rpc_method"`
		RPCParamsTail       []any  `json:"rpc_params_tail"`
		RestrictionDistance int    `json:"restriction_distance"`
		ResponseKey         string `json:"response_key"`
	} `json:"metadata"`
	Cases []headerFetchOracleCase `json:"cases"`
}

type headerFetchOracleCase struct {
	Name               string  `json:"name"`
	Seed               int     `json:"seed"`
	Length             int     `json:"length"`
	Height             int     `json:"height"`
	Checkpointed       bool    `json:"checkpointed"`
	CheckpointHash     *string `json:"checkpoint_hash"`
	Encoding           string  `json:"encoding"`
	Encoded            string  `json:"encoded"`
	InflatedLength     *int    `json:"inflated_length"`
	InflatedSHA256     *string `json:"inflated_sha256"`
	WroteLength        *int    `json:"wrote_length"`
	WroteSHA256        *string `json:"wrote_sha256"`
	MissingAfter       []int   `json:"missing_after"`
	ErrorType          *string `json:"error_type"`
	ErrorMessage       *string `json:"error_message"`
	HardenedDivergence bool    `json:"hardened_divergence"`
}

func TestCheckpointFetchMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runHeaderFetchOracle(t)
	if oracle.Reference.Commit != headerFetchOraclePinnedCommit ||
		oracle.Reference.Version != headerFetchOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, headerFetchOraclePinnedSources) {
		t.Fatalf("fetch oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.PythonAssertions || !oracle.Metadata.RawDeflate ||
		oracle.Metadata.Base64Validate || oracle.Metadata.ZlibBufsizeIsLimit ||
		oracle.Metadata.RPCMethod != SPVHeaderRPCMethod ||
		!reflect.DeepEqual(oracle.Metadata.RPCParamsTail, []any{float64(1000), float64(0), true}) ||
		oracle.Metadata.RestrictionDistance != SPVHeaderRPCRestrictionDistance ||
		oracle.Metadata.ResponseKey != "base64" {
		t.Fatalf("fetch oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && oracle.Metadata.PythonVersion != want {
		t.Fatalf("fetch oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
	if len(oracle.Cases) != 7 {
		t.Fatalf("fetch oracle cases = %d, want 7", len(oracle.Cases))
	}

	for _, fixture := range oracle.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			raw := checkpointFetchFixtureLength(byte(fixture.Seed), fixture.Length)
			digest := sha256.Sum256(raw)
			if fixture.InflatedLength == nil || *fixture.InflatedLength != len(raw) ||
				fixture.InflatedSHA256 == nil || *fixture.InflatedSHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("oracle inflated result = length %v SHA %v", fixture.InflatedLength, fixture.InflatedSHA256)
			}

			table := emptyCheckpoints
			if fixture.Checkpointed {
				if fixture.CheckpointHash == nil {
					t.Fatal("checkpointed case has no expected hash")
				}
				table = checkpointTableFromHashes(t, *fixture.CheckpointHash)
			}
			headers := NewHeaders(":memory:", withHeaderCheckpoints(table))
			if err := headers.Open(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := headers.Close(); err != nil {
					t.Error(err)
				}
			})
			var starts []int
			headers.SetChunkGetter(func(_ context.Context, start int) (HeaderChunkResponse, error) {
				starts = append(starts, start)
				return HeaderChunkResponse{Base64: fixture.Encoded}, nil
			})
			err := headers.FetchChunk(context.Background(), fixture.Height)
			wantStart := (fixture.Height / CheckpointChunkHeaders) * CheckpointChunkHeaders
			if !reflect.DeepEqual(starts, []int{wantStart}) {
				t.Fatalf("getter starts = %v, want [%d]", starts, wantStart)
			}

			if fixture.HardenedDivergence {
				if fixture.ErrorType != nil || fixture.WroteLength == nil || *fixture.WroteLength != fixture.Length {
					t.Fatalf("legacy divergence was not accepted by Python: %+v", fixture)
				}
				wantErr := ErrInvalidCheckpointChunkLength
				if fixture.Length > CheckpointChunkBytes {
					wantErr = ErrCheckpointOutputTooLarge
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("hardened fetch error = %v, want %v", err, wantErr)
				}
				if got := headers.MissingCheckpointedChunks(); !reflect.DeepEqual(got, []int{0}) {
					t.Fatalf("hardened rejection missing set = %v", got)
				}
				return
			}

			if fixture.ErrorType != nil {
				if err == nil || fixture.ErrorMessage == nil || err.Error() != *fixture.ErrorMessage ||
					!errors.Is(err, ErrCheckpointMismatch) {
					t.Fatalf("fetch error = %v, want %v/%v", err, fixture.ErrorType, fixture.ErrorMessage)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := headers.MissingCheckpointedChunks(); !reflect.DeepEqual(got, fixture.MissingAfter) {
				t.Fatalf("missing checkpoints = %v, want %v", got, fixture.MissingAfter)
			}
			if fixture.WroteLength != nil {
				stored := make([]byte, *fixture.WroteLength)
				headers.mu.RLock()
				read, readErr := headers.storage.ReadAt(stored, 0)
				headers.mu.RUnlock()
				if readErr != nil || read != len(stored) || !bytes.Equal(stored, raw) {
					t.Fatalf("stored checkpoint = %d bytes, %v", read, readErr)
				}
			} else if !fixture.Checkpointed && headers.Len() != 0 {
				t.Fatalf("noncheckpoint fetch wrote %d headers", headers.Len())
			}
		})
	}
}

func runHeaderFetchOracle(t *testing.T) headerFetchOracleResponse {
	t.Helper()
	sdkRoot, script := headerFetchOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python checkpoint fetch oracle failed: %v\n%s", err, output)
	}
	var oracle headerFetchOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode checkpoint fetch oracle: %v\n%s", err, output)
	}
	return oracle
}

func headerFetchOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate checkpoint fetch oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "header_fetch_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "header.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "ledger.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "network.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local checkpoint fetch source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func checkpointFetchFixtureLength(seed byte, length int) []byte {
	chunk := make([]byte, length)
	for index := range chunk {
		chunk[index] = byte((int(seed) + index*37) % 251)
	}
	return chunk
}
