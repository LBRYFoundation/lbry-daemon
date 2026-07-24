package wallet

import (
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
	transactionShowLegacyClaimOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionShowLegacyClaimOracleVersion = "0.113.0"
)

var transactionShowLegacyClaimOracleMethods = map[string]string{
	"Claim.from_bytes":                      "3ce060b4daf38073681481493f45c6bbfc1f6b132466617f514b38772dd23ad5",
	"JSONResponseEncoder.__init__":          "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
	"JSONResponseEncoder.default":           "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
	"JSONResponseEncoder.encode_claim":      "c537d439cc940682b1954615726587d125615e5d4bda62f26b9e78085c5ed088",
	"JSONResponseEncoder.encode_claim_meta": "7998df829f2f3a45d3f851ec1fa08910d4c6106c58a0a5a22690390ff8371c05",
	"JSONResponseEncoder.encode_output":     "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
	"Signable.from_bytes":                   "9a21b3bc622983084470ddef59d5f0a057e618bfc39cd399cd64bf4c6e52360c",
	"Signable.is_signed":                    "e8366b90739fe59a0844b1cbd87c31c10993f6206d7f43db618c04ebd35005d1",
	"Signable.signing_channel_id":           "e5d2e50f1fdf9f2a7cb055ef9b6c79d33de65bea2fbda924d90000adb307feba",
	"Signable.to_bytes":                     "c8b13a227f46397354450ae1921ac124d5bc61896c724dd1bf2361f51b75d5bf",
	"Signable.to_message_bytes":             "abba7749d2619869c1bd3266bc307bf06f43d26172ed497a2ac4c67ca2fc740f",
	"dewies_to_lbc":                         "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
	"from_old_json_schema":                  "fb2ad0944c9d930475dfa58dc272daf1ca2f55dbe611300108a9ed0a38d51f9a",
	"from_types_v1":                         "ffbc2cf47f628bef0580606ee8c09f3ee83ee64dc17d4c27bb143d0f0c26dc69",
	"satoshis_to_coins":                     "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

type transactionShowLegacyClaimOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion              string `json:"python_version"`
		ProtobufVersion            string `json:"protobuf_version"`
		RealClaimFromBytesExecuted bool   `json:"real_claim_from_bytes_executed"`
		RealLegacyProto2Messages   bool   `json:"real_legacy_proto2_messages_executed"`
		ExtractedEncoderMethods    bool   `json:"extracted_encoder_methods_executed"`
		ExternalNetworkUsed        bool   `json:"external_network_used"`
		CaseCount                  int    `json:"case_count"`
		MatrixCaseCount            int    `json:"matrix_case_count"`
		MalformedCaseCount         int    `json:"malformed_case_count"`
	} `json:"metadata"`
	Cases []transactionShowLegacyClaimOracleCase `json:"cases"`
}

type transactionShowLegacyClaimOracleCase struct {
	Name                string                                      `json:"name"`
	LegacyFormat        string                                      `json:"legacy_format"`
	Operation           string                                      `json:"operation"`
	IncludeProtobuf     bool                                        `json:"include_protobuf"`
	OriginalPayloadHex  string                                      `json:"original_payload_hex"`
	Conversion          *transactionShowLegacyClaimOracleConversion `json:"conversion"`
	ConversionError     *transactionShowLegacyClaimOracleError      `json:"conversion_error"`
	EncodedOutput       map[string]any                              `json:"encoded_output"`
	EncodedOutputFields []string                                    `json:"encoded_output_fields"`
	EncodingError       *transactionShowLegacyClaimOracleError      `json:"encoding_error"`
}

type transactionShowLegacyClaimOracleConversion struct {
	SourceVersion         int            `json:"source_version"`
	ValueType             *string        `json:"value_type"`
	Value                 map[string]any `json:"value"`
	IsSigned              bool           `json:"is_signed"`
	SignatureType         string         `json:"signature_type"`
	SignatureHex          *string        `json:"signature_hex"`
	SigningChannelHashHex *string        `json:"signing_channel_hash_hex"`
	SigningChannelID      *string        `json:"signing_channel_id"`
	UnsignedV1PayloadHex  *string        `json:"unsigned_v1_payload_hex"`
	V2MessageHex          string         `json:"v2_message_hex"`
	CanonicalV2Hex        string         `json:"canonical_v2_hex"`
}

type transactionShowLegacyClaimOracleError struct {
	Stage   string `json:"stage"`
	Type    string `json:"type"`
	Module  string `json:"module"`
	Message string `json:"message"`
}

func TestTransactionShowLegacyClaimConversionPinnedOracle(t *testing.T) {
	oracle := runTransactionShowLegacyClaimOracle(t)
	if oracle.Reference.Commit != transactionShowLegacyClaimOracleCommit ||
		oracle.Reference.Version != transactionShowLegacyClaimOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionShowLegacyClaimOracleMethods) {
		t.Fatalf("legacy claim oracle reference = %+v", oracle.Reference)
	}
	if len(oracle.Reference.SourceSHA256) != 25 {
		t.Fatalf("legacy claim oracle source count = %d, want 25", len(oracle.Reference.SourceSHA256))
	}
	metadata := oracle.Metadata
	if metadata.PythonVersion == "" || metadata.ProtobufVersion == "" ||
		!metadata.RealClaimFromBytesExecuted || !metadata.RealLegacyProto2Messages ||
		!metadata.ExtractedEncoderMethods || metadata.ExternalNetworkUsed ||
		metadata.CaseCount != 27 || metadata.MatrixCaseCount != 16 ||
		metadata.MalformedCaseCount != 11 || len(oracle.Cases) != 27 {
		t.Fatalf("legacy claim oracle metadata = %+v, cases %d", metadata, len(oracle.Cases))
	}

	wantNames := []string{
		"v0_json_stream_create_plain", "v0_json_stream_create_protobuf",
		"v0_json_stream_update_plain", "v0_json_stream_update_protobuf",
		"v1_stream_unsigned_create_plain", "v1_stream_unsigned_create_protobuf",
		"v1_stream_unsigned_update_plain", "v1_stream_unsigned_update_protobuf",
		"v1_stream_signed_create_plain", "v1_stream_signed_create_protobuf",
		"v1_stream_signed_update_plain", "v1_stream_signed_update_protobuf",
		"v1_channel_create_plain", "v1_channel_create_protobuf",
		"v1_channel_update_plain", "v1_channel_update_protobuf",
		"empty_payload", "malformed_v2", "short_signed_v2", "truncated_v1",
		"malformed_v0_json", "v0_json_missing_sources", "v0_json_unknown_currency",
		"v0_json_odd_sd_hash", "v0_json_leading_space", "v1_signed_missing_required",
		"v1_unknown_fee_currency",
	}
	for index, fixture := range oracle.Cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("legacy claim case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		if fixture.OriginalPayloadHex == "" && fixture.Name != "empty_payload" {
			t.Fatalf("legacy claim case %q has no original payload", fixture.Name)
		}
	}

	assertTransactionShowLegacyClaimMatrix(t, oracle.Cases[:16])
	assertTransactionShowLegacyClaimFailures(t, oracle.Cases[16:])
}

func assertTransactionShowLegacyClaimMatrix(
	t *testing.T, fixtures []transactionShowLegacyClaimOracleCase,
) {
	t.Helper()
	for _, fixture := range fixtures {
		if fixture.Conversion == nil || fixture.ConversionError != nil ||
			fixture.EncodedOutput == nil || fixture.EncodingError != nil {
			t.Fatalf("legacy claim matrix case %q failed: conversion=%+v/%+v output=%v/%+v",
				fixture.Name, fixture.Conversion, fixture.ConversionError,
				fixture.EncodedOutput, fixture.EncodingError)
		}
		conversion := fixture.Conversion
		if conversion.CanonicalV2Hex == fixture.OriginalPayloadHex ||
			conversion.CanonicalV2Hex == "" || conversion.V2MessageHex == "" {
			t.Fatalf("legacy claim case %q conversion bytes = %+v", fixture.Name, conversion)
		}
		if got := fixture.EncodedOutput["claim_op"]; got != fixture.Operation {
			t.Fatalf("legacy claim case %q claim_op = %v, want %q", fixture.Name, got, fixture.Operation)
		}
		if got := fixture.EncodedOutput["value_type"]; conversion.ValueType == nil || got != *conversion.ValueType {
			t.Fatalf("legacy claim case %q value_type = %v, conversion %+v", fixture.Name, got, conversion.ValueType)
		}
		protobuf, hasProtobuf := fixture.EncodedOutput["protobuf"]
		if hasProtobuf != fixture.IncludeProtobuf {
			t.Fatalf("legacy claim case %q protobuf present = %v", fixture.Name, hasProtobuf)
		}
		if hasProtobuf && protobuf != conversion.CanonicalV2Hex {
			t.Fatalf("legacy claim case %q protobuf = %v, want canonical %s",
				fixture.Name, protobuf, conversion.CanonicalV2Hex)
		}

		switch fixture.LegacyFormat {
		case "v0_json":
			if conversion.SourceVersion != 0 || conversion.IsSigned || *conversion.ValueType != "stream" {
				t.Fatalf("v0 conversion for %q = %+v", fixture.Name, conversion)
			}
		case "v1_proto2":
			if conversion.SourceVersion != 1 {
				t.Fatalf("v1 source version for %q = %d", fixture.Name, conversion.SourceVersion)
			}
		}
		if fixture.Name[:16] == "v1_stream_signed" {
			assertTransactionShowLegacySignedConversion(t, fixture)
		}
		if *conversion.ValueType == "channel" {
			if fixture.EncodedOutput["has_signing_key"] != false {
				t.Fatalf("legacy v1 channel %q signing key = %v", fixture.Name, fixture.EncodedOutput["has_signing_key"])
			}
		}
	}
}

func assertTransactionShowLegacySignedConversion(
	t *testing.T, fixture transactionShowLegacyClaimOracleCase,
) {
	t.Helper()
	conversion := fixture.Conversion
	const channelID = "00112233445566778899aabbccddeeff00112233"
	if !conversion.IsSigned || conversion.SignatureHex == nil ||
		conversion.SigningChannelID == nil || *conversion.SigningChannelID != channelID ||
		conversion.SigningChannelHashHex == nil ||
		*conversion.SigningChannelHashHex != "33221100ffeeddccbbaa99887766554433221100" ||
		conversion.UnsignedV1PayloadHex == nil {
		t.Fatalf("signed v1 conversion for %q = %+v", fixture.Name, conversion)
	}
	stub, ok := fixture.EncodedOutput["signing_channel"].(map[string]any)
	if !ok || stub["channel_id"] != channelID ||
		fixture.EncodedOutput["is_channel_signature_valid"] != false {
		t.Fatalf("signed fallback for %q = %v/%v", fixture.Name, stub,
			fixture.EncodedOutput["is_channel_signature_valid"])
	}
}

func assertTransactionShowLegacyClaimFailures(
	t *testing.T, fixtures []transactionShowLegacyClaimOracleCase,
) {
	t.Helper()
	suppressed := map[string]string{
		"malformed_v2":             "DecodeError",
		"truncated_v1":             "DecodeError",
		"malformed_v0_json":        "DecodeError",
		"v0_json_unknown_currency": "DecodeError",
		"v0_json_leading_space":    "DecodeError",
		"v1_unknown_fee_currency":  "DecodeError",
	}
	propagated := map[string]string{
		"empty_payload":              "IndexError",
		"short_signed_v2":            "TypeError",
		"v0_json_missing_sources":    "KeyError",
		"v0_json_odd_sd_hash":        "Error",
		"v1_signed_missing_required": "EncodeError",
	}
	for _, fixture := range fixtures {
		if want, ok := suppressed[fixture.Name]; ok {
			if fixture.Conversion != nil || fixture.ConversionError == nil ||
				fixture.ConversionError.Type != want || fixture.EncodingError != nil ||
				fixture.EncodedOutput == nil {
				t.Fatalf("suppressed legacy failure %q = %+v", fixture.Name, fixture)
			}
			for _, absent := range []string{
				"value", "value_type", "protobuf", "signing_channel", "is_channel_signature_valid",
			} {
				if _, ok := fixture.EncodedOutput[absent]; ok {
					t.Fatalf("suppressed legacy failure %q includes %q", fixture.Name, absent)
				}
			}
			continue
		}
		want, ok := propagated[fixture.Name]
		if !ok || fixture.Conversion != nil || fixture.ConversionError == nil ||
			fixture.ConversionError.Type != want || fixture.EncodingError == nil ||
			fixture.EncodingError.Type != want || fixture.EncodedOutput != nil {
			t.Fatalf("propagated legacy failure %q = %+v", fixture.Name, fixture)
		}
	}
}

func runTransactionShowLegacyClaimOracle(t *testing.T) transactionShowLegacyClaimOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction show legacy claim oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_show_legacy_claim_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("transaction show legacy claim oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python transaction show legacy claim oracle failed: %v\n%s", err, output)
	}
	var oracle transactionShowLegacyClaimOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction show legacy claim oracle: %v\n%s", err, output)
	}
	return oracle
}
