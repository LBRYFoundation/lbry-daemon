package mnemonic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	mnemonicOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	mnemonicOraclePinnedVersion = "0.113.0"
)

type mnemonicTextCase struct {
	Name       string `json:"name"`
	Text       string `json:"text"`
	Mnemonic   string `json:"mnemonic"`
	Passphrase string `json:"passphrase"`
	Integer    string `json:"integer"`
	Seed       string `json:"seed"`
	Prefix     string `json:"prefix"`
	Language   string `json:"language"`
}

type mnemonicMakeCase struct {
	Name     string   `json:"name"`
	Entropy  []string `json:"entropy"`
	Prefix   string   `json:"prefix"`
	NumBits  int      `json:"num_bits"`
	Language string   `json:"language"`
}

type mnemonicLanguageCase struct {
	Name     string `json:"name"`
	Language string `json:"language"`
}

type mnemonicOutcome struct {
	Name      string  `json:"name"`
	Result    any     `json:"result"`
	ErrorType *string `json:"error_type"`
	Error     *string `json:"error"`
}

type mnemonicMakeOutcome struct {
	Name            string   `json:"name"`
	Result          any      `json:"result"`
	ErrorType       *string  `json:"error_type"`
	Error           *string  `json:"error"`
	RandbelowLimits []string `json:"randbelow_limits"`
}

type mnemonicLanguageOutcome struct {
	Name      string  `json:"name"`
	WordCount *int    `json:"word_count"`
	FirstWord *string `json:"first_word"`
	LastWord  *string `json:"last_word"`
	ErrorType *string `json:"error_type"`
	Error     *string `json:"error"`
}

type mnemonicOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion      string `json:"python_version"`
		UnicodeVersion     string `json:"unicode_version"`
		SeedPrefix         string `json:"seed_prefix"`
		SeedPrefix2FA      string `json:"seed_prefix_2fa"`
		SeedPrefixSW       string `json:"seed_prefix_sw"`
		EnglishWordCount   int    `json:"english_word_count"`
		EnglishWordsSHA256 string `json:"english_words_sha256"`
	} `json:"metadata"`
	NormalizeCases []mnemonicOutcome         `json:"normalize_cases"`
	SeedCases      []mnemonicOutcome         `json:"seed_cases"`
	EncodeCases    []mnemonicOutcome         `json:"encode_cases"`
	DecodeCases    []mnemonicOutcome         `json:"decode_cases"`
	VersionCases   []mnemonicOutcome         `json:"version_cases"`
	MakeCases      []mnemonicMakeOutcome     `json:"make_cases"`
	LanguageCases  []mnemonicLanguageOutcome `json:"language_cases"`
}

func TestMnemonicMatchesPinnedPythonOracle(t *testing.T) {
	sdkRoot, script := mnemonicOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}

	normalizeCases := []mnemonicTextCase{
		{Name: "accents and whitespace", Text: "  H\u00e9LLo\t W\u00d6RLD  "},
		{Name: "compatibility forms", Text: "\uff21\uff22\uff23 \u1f88"},
		{Name: "CJK spaces", Text: "\u6f22 \u5b57 \u306f \u3072"},
		{Name: "final sigma", Text: "\u039f\u03a3 A\u03a3"},
		{Name: "Hangul", Text: "\ud55c \uae00"},
		{Name: "combining class zero", Text: "A\u034f  B"},
		{Name: "Python information separator", Text: "a\u001cb"},
		{Name: "long combining run", Text: "A" + strings.Repeat("\u0301", 64) + "B"},
		{Name: "unbounded final sigma", Text: "\u039f\u03a3" + strings.Repeat(".", 64) + "A"},
	}
	seedCases := []mnemonicTextCase{
		{Name: "SDK vector", Mnemonic: "foobar", Passphrase: "torba"},
		{Name: "normalized", Mnemonic: " H\u00c9LLO  ", Passphrase: " W\u00d6RLD\t"},
	}
	encodeCases := []mnemonicTextCase{
		{Name: "zero", Integer: "0"},
		{Name: "one", Integer: "1"},
		{Name: "base", Integer: "2048"},
		{Name: "132-bit boundary", Integer: "5444517870735015415413993718908291383296"},
	}
	decodeCases := []mnemonicTextCase{
		{Name: "empty", Seed: ""},
		{Name: "base", Seed: "abandon ability"},
		{Name: "Python whitespace", Seed: "abandon\u001cability"},
		{Name: "wallet fixture", Seed: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent"},
	}
	versionCases := []mnemonicTextCase{
		{Name: "standard", Seed: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent", Prefix: StandardPrefix},
		{Name: "two factor mismatch", Seed: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent", Prefix: TwoFactorPrefix},
		{Name: "empty prefix", Seed: "anything", Prefix: ""},
	}
	makeCases := []mnemonicMakeCase{
		{Name: "default", Entropy: []string{"680564733841876926926749214863536422912"}, Prefix: StandardPrefix, NumBits: DefaultBits},
		{Name: "retry", Entropy: []string{"1", "680564733841876926926749214863536422912"}, Prefix: StandardPrefix, NumBits: DefaultBits},
		{Name: "zero entropy exit", Entropy: []string{"0"}, Prefix: StandardPrefix, NumBits: DefaultBits},
		{Name: "explicit empty prefix and zero bits", Entropy: []string{}, Prefix: "", NumBits: 0},
	}
	languageCases := []mnemonicLanguageCase{
		{Name: "English", Language: "en"},
		{Name: "unknown falls back", Language: "fr"},
	}

	oracle := runMnemonicOracle(t, python, script, sdkRoot, map[string]any{
		"normalize_cases": normalizeCases,
		"seed_cases":      seedCases,
		"encode_cases":    encodeCases,
		"decode_cases":    decodeCases,
		"version_cases":   versionCases,
		"make_cases":      makeCases,
		"language_cases":  languageCases,
	})
	if oracle.Reference.Commit != mnemonicOraclePinnedCommit || oracle.Reference.Version != mnemonicOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	for environment, got := range map[string]string{
		"LBRY_ORACLE_PYTHON_VERSION":  oracle.Metadata.PythonVersion,
		"LBRY_ORACLE_UNICODE_VERSION": oracle.Metadata.UnicodeVersion,
	} {
		if want := os.Getenv(environment); want != "" && got != want {
			t.Fatalf("oracle %s = %q, want %q", environment, got, want)
		}
	}
	if oracle.Metadata.PythonVersion != "3.11" || oracle.Metadata.UnicodeVersion != "14.0.0" {
		t.Logf("mnemonic oracle runtime is Python %s/Unicode %s; version-specific behavior targets CI Python 3.11/Unicode 14.0.0",
			oracle.Metadata.PythonVersion, oracle.Metadata.UnicodeVersion)
	}
	words := NewEnglish().Words()
	wordHash := sha256.Sum256([]byte(strings.Join(words, "\n")))
	if oracle.Metadata.SeedPrefix != StandardPrefix ||
		oracle.Metadata.SeedPrefix2FA != TwoFactorPrefix ||
		oracle.Metadata.SeedPrefixSW != SegWitPrefix ||
		oracle.Metadata.EnglishWordCount != len(words) ||
		oracle.Metadata.EnglishWordsSHA256 != hex.EncodeToString(wordHash[:]) {
		t.Fatalf("oracle metadata = %#v", oracle.Metadata)
	}

	assertMnemonicOutcomes(t, "normalize", executeNormalizeCases(normalizeCases), oracle.NormalizeCases)
	assertMnemonicOutcomes(t, "seed", executeSeedCases(seedCases), oracle.SeedCases)
	assertMnemonicOutcomes(t, "encode", executeEncodeCases(t, encodeCases), oracle.EncodeCases)
	assertMnemonicOutcomes(t, "decode", executeDecodeCases(decodeCases), oracle.DecodeCases)
	assertMnemonicOutcomes(t, "version", executeVersionCases(versionCases), oracle.VersionCases)
	if got := executeMakeCases(t, makeCases); !reflect.DeepEqual(got, oracle.MakeCases) {
		t.Fatalf("make outcomes differ\nGo:     %#v\nPython: %#v", got, oracle.MakeCases)
	}
	if got := executeLanguageCases(t, languageCases); !reflect.DeepEqual(got, oracle.LanguageCases) {
		t.Fatalf("language outcomes differ\nGo:     %#v\nPython: %#v", got, oracle.LanguageCases)
	}
}

func executeNormalizeCases(cases []mnemonicTextCase) []mnemonicOutcome {
	result := make([]mnemonicOutcome, len(cases))
	for index, fixture := range cases {
		result[index] = mnemonicOutcome{Name: fixture.Name, Result: NormalizeText(fixture.Text)}
	}
	return result
}

func executeSeedCases(cases []mnemonicTextCase) []mnemonicOutcome {
	result := make([]mnemonicOutcome, len(cases))
	for index, fixture := range cases {
		result[index] = mnemonicOutcome{Name: fixture.Name, Result: hex.EncodeToString(ToSeed(fixture.Mnemonic, fixture.Passphrase))}
	}
	return result
}

func executeEncodeCases(t *testing.T, cases []mnemonicTextCase) []mnemonicOutcome {
	t.Helper()
	mnemonic := NewEnglish()
	result := make([]mnemonicOutcome, len(cases))
	for index, fixture := range cases {
		integer, ok := new(big.Int).SetString(fixture.Integer, 10)
		if !ok {
			t.Fatalf("invalid encode fixture integer %q", fixture.Integer)
		}
		encoded, err := mnemonic.Encode(integer)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = mnemonicOutcome{Name: fixture.Name, Result: encoded}
	}
	return result
}

func executeDecodeCases(cases []mnemonicTextCase) []mnemonicOutcome {
	mnemonic := NewEnglish()
	result := make([]mnemonicOutcome, len(cases))
	for index, fixture := range cases {
		decoded, err := mnemonic.Decode(fixture.Seed)
		if err != nil {
			message := err.Error()
			errorType := "ValueError"
			result[index] = mnemonicOutcome{Name: fixture.Name, ErrorType: &errorType, Error: &message}
			continue
		}
		result[index] = mnemonicOutcome{Name: fixture.Name, Result: decoded.String()}
	}
	return result
}

func executeVersionCases(cases []mnemonicTextCase) []mnemonicOutcome {
	result := make([]mnemonicOutcome, len(cases))
	for index, fixture := range cases {
		result[index] = mnemonicOutcome{Name: fixture.Name, Result: IsNewSeed(fixture.Seed, fixture.Prefix)}
	}
	return result
}

func executeMakeCases(t *testing.T, cases []mnemonicMakeCase) []mnemonicMakeOutcome {
	t.Helper()
	result := make([]mnemonicMakeOutcome, len(cases))
	for index, fixture := range cases {
		reader := newFixtureEntropyReader(t, fixture.Entropy, fixture.NumBits)
		mnemonic, err := newWithReader(fixture.Language, reader)
		if err != nil {
			t.Fatal(err)
		}
		seed, err := mnemonic.MakeSeed(fixture.Prefix, fixture.NumBits)
		outcome := mnemonicMakeOutcome{Name: fixture.Name, RandbelowLimits: reader.limits}
		if err != nil {
			message := err.Error()
			errorType := "error"
			outcome.ErrorType, outcome.Error = &errorType, &message
		} else {
			outcome.Result = seed
		}
		result[index] = outcome
	}
	return result
}

func executeLanguageCases(t *testing.T, cases []mnemonicLanguageCase) []mnemonicLanguageOutcome {
	t.Helper()
	result := make([]mnemonicLanguageOutcome, len(cases))
	for index, fixture := range cases {
		mnemonic, err := New(fixture.Language)
		outcome := mnemonicLanguageOutcome{Name: fixture.Name}
		if err != nil {
			message := err.Error()
			errorType := "ModuleNotFoundError"
			outcome.ErrorType, outcome.Error = &errorType, &message
		} else {
			words := mnemonic.Words()
			count, first, last := len(words), words[0], words[len(words)-1]
			outcome.WordCount, outcome.FirstWord, outcome.LastWord = &count, &first, &last
		}
		result[index] = outcome
	}
	return result
}

func assertMnemonicOutcomes(t *testing.T, name string, got, want []mnemonicOutcome) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s outcomes differ\nGo:     %#v\nPython: %#v", name, got, want)
	}
}

type fixtureEntropyReader struct {
	data   *bytes.Reader
	limits []string
	width  int
	limit  string
}

func newFixtureEntropyReader(t *testing.T, values []string, numBits int) *fixtureEntropyReader {
	t.Helper()
	roundedBits := ((numBits + 10) / 11) * 11
	width := (roundedBits + 7) / 8
	maximum := new(big.Int).Lsh(big.NewInt(1), uint(roundedBits))
	data := make([]byte, 0, width*len(values))
	for _, text := range values {
		value, ok := new(big.Int).SetString(text, 10)
		if !ok || value.Sign() < 0 || value.Cmp(maximum) >= 0 {
			t.Fatalf("invalid entropy fixture %q for %d bits", text, roundedBits)
		}
		data = append(data, value.FillBytes(make([]byte, width))...)
	}
	return &fixtureEntropyReader{
		data: bytes.NewReader(data), limits: make([]string, 0), width: width, limit: maximum.String(),
	}
}

func (reader *fixtureEntropyReader) Read(destination []byte) (int, error) {
	if len(destination) == reader.width {
		reader.limits = append(reader.limits, reader.limit)
	}
	return reader.data.Read(destination)
}

func mnemonicOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate mnemonic oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(filepath.Dir(sourceFile)))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "mnemonic_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "crypto", "hash.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "mnemonic.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "words", "english.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK mnemonic source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func runMnemonicOracle(t *testing.T, python, script, sdkRoot string, fixtures any) mnemonicOracleOutput {
	t.Helper()
	payload, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python mnemonic oracle failed: %v\n%s", err, stderr.String())
	}
	var result mnemonicOracleOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Python mnemonic oracle: %v\n%s", err, output)
	}
	return result
}
