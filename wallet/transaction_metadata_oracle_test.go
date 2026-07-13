package wallet

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type transactionMetadataOracleResponse struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Fixtures []struct {
		Name       string  `json:"name"`
		PayloadHex string  `json:"payload_hex"`
		Decoded    bool    `json:"decoded"`
		ClaimType  *string `json:"claim_type"`
		HasSource  bool    `json:"has_source"`
		ChannelID  *string `json:"channel_id"`
		ErrorType  *string `json:"error_type"`
	} `json:"fixtures"`
}

func TestTransactionMetadataLegacyOracle(t *testing.T) {
	oracle := runTransactionMetadataOracle(t)
	if oracle.Reference.Commit != transactionOraclePinnedCommit ||
		oracle.Reference.Version != transactionOraclePinnedVersion {
		t.Fatalf("oracle reference = %s/%s, want %s/%s",
			oracle.Reference.Commit, oracle.Reference.Version,
			transactionOraclePinnedCommit, transactionOraclePinnedVersion)
	}
	if len(oracle.Fixtures) != 19 {
		t.Fatalf("oracle fixture count = %d, want 19", len(oracle.Fixtures))
	}

	for _, fixture := range oracle.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(fixture.PayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			got, decoded := decodeTransactionClaim(payload)
			if decoded != fixture.Decoded {
				t.Fatalf("decoded = %v, want %v (Python error %v)", decoded, fixture.Decoded, fixture.ErrorType)
			}
			if !decoded {
				return
			}
			wantType := TransactionOutputTypeStream
			if fixture.ClaimType != nil {
				switch *fixture.ClaimType {
				case "channel":
					wantType = TransactionOutputTypeChannel
				case "collection":
					wantType = TransactionOutputTypeCollection
				case "repost":
					wantType = TransactionOutputTypeRepost
				}
			}
			if got.TXOType != wantType || got.HasSource != fixture.HasSource {
				t.Fatalf("metadata = type %d, source %v; want type %d, source %v",
					got.TXOType, got.HasSource, wantType, fixture.HasSource)
			}
			assertMetadataString(t, "channel ID", got.ChannelID, fixture.ChannelID)
		})
	}
}

func runTransactionMetadataOracle(t *testing.T) transactionMetadataOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction metadata oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_metadata_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("transaction metadata oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python transaction metadata oracle failed: %v\n%s", err, output)
	}
	var oracle transactionMetadataOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction metadata oracle: %v\n%s", err, output)
	}
	return oracle
}
