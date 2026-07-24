package mnemonic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestEnglishWordListMatchesPinnedSDK(t *testing.T) {
	mnemonic := NewEnglish()
	words := mnemonic.Words()
	if len(words) != 2048 {
		t.Fatalf("word count = %d, want 2048", len(words))
	}
	if words[0] != "abandon" || words[len(words)-1] != "zoo" {
		t.Fatalf("word list bounds = (%q, %q)", words[0], words[len(words)-1])
	}
	hash := sha256.Sum256([]byte(strings.Join(words, "\n") + "\n"))
	if got, want := hex.EncodeToString(hash[:]), "2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda"; got != want {
		t.Fatalf("word list hash = %s, want %s", got, want)
	}

	words[0] = "changed"
	if mnemonic.Words()[0] != "abandon" {
		t.Fatal("Words exposed the internal word list")
	}
}

func TestLanguageBehaviorMatchesPinnedSDK(t *testing.T) {
	for _, language := range []string{"", "en", "fr", "EN"} {
		mnemonic, err := New(language)
		if err != nil {
			t.Fatalf("New(%q): %v", language, err)
		}
		if mnemonic.Words()[0] != "abandon" {
			t.Fatalf("New(%q) did not fall back to English", language)
		}
	}
	for _, language := range []string{"es", "ja", "pt", "zh"} {
		_, err := New(language)
		if !errors.Is(err, ErrLanguageUnavailable) {
			t.Fatalf("New(%q) error = %v, want ErrLanguageUnavailable", language, err)
		}
	}
}

func TestNormalizeTextMatchesPythonVectors(t *testing.T) {
	tests := map[string]string{
		"  H\u00e9LLo\t W\u00d6RLD  ": "hello world",
		"\u3300 \u3301":               "\u30a2\u30cf\u30fc\u30c8\u30a2\u30eb\u30d5\u30a1",
		"\u6f22 \u5b57":               "\u6f22\u5b57",
		"\u6f22 a \u5b57":             "\u6f22 a \u5b57",
		"\uff21\uff22\uff23":          "abc",
		"\u00a0foo\u2003bar":          "foo bar",
		"\u039f\u03a3":                "\u03bf\u03c2",
		"A\u03a3":                     "a\u03c2",
		"\u0130stanbul":               "istanbul",
		"\u1f88":                      "\u03b1",
		"\ud55c \uae00":               "\u1112\u1161\u11ab\u1100\u1173\u11af",
		"\u306f \u3072":               "\u306f\u3072",
		"A\u034f  B":                  "a\u034f b",
		"a\u001cb":                    "a b",
	}
	for input, want := range tests {
		if got := NormalizeText(input); got != want {
			t.Errorf("NormalizeText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeTextHasNoStreamSafeLimit(t *testing.T) {
	combiningRun := "A" + strings.Repeat("\u0301", 64) + "B"
	if got := NormalizeText(combiningRun); got != "ab" {
		t.Fatalf("long combining run = %q, want %q", got, "ab")
	}

	caseIgnorables := strings.Repeat(".", 64)
	input := "\u039f\u03a3" + caseIgnorables + "A"
	want := "\u03bf\u03c3" + caseIgnorables + "a"
	if got := NormalizeText(input); got != want {
		t.Fatalf("long final-sigma context = %q, want %q", got, want)
	}
}

func TestNormalizeTextUsesPython311UnicodeVersion(t *testing.T) {
	// U+1E4EC was assigned in Unicode 15 with CCC 232. Python 3.11's
	// Unicode 14 database treats it as unassigned and therefore preserves it.
	const addedInUnicode15 = "\U0001e4ec"
	if got := NormalizeText("A" + addedInUnicode15 + "B"); got != "a"+addedInUnicode15+"b" {
		t.Fatalf("Unicode 15 assignment normalized as %q", got)
	}
	for _, addedLater := range []rune{'\u1c89', '\U0001e5ee', '\u088f'} {
		if !isPostUnicode14Assignment(addedLater) {
			t.Fatalf("post-Unicode-14 assignment U+%04X is not masked", addedLater)
		}
		input := "A" + string(addedLater) + "B"
		if got, want := NormalizeText(input), "a"+string(addedLater)+"b"; got != want {
			t.Fatalf("post-Unicode-14 U+%04X normalized as %q, want %q", addedLater, got, want)
		}
	}
}

func TestNormalizeTextIsSafeForConcurrentDerivation(t *testing.T) {
	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if got := NormalizeText("  H\u00e9LLo \u039f\u03a3  "); got != "hello \u03bf\u03c2" {
				errors <- got
			}
		}()
	}
	wait.Wait()
	close(errors)
	for got := range errors {
		t.Errorf("concurrent NormalizeText = %q", got)
	}
}

func TestToSeedMatchesPinnedSDK(t *testing.T) {
	seed := ToSeed("foobar", "torba")
	want := "475a419db4e991cab14f08bde2d357e52b3e7241f72c6d8a2f92782367feeee9" +
		"f403dc6a37c26a3f02ab9dec7f5063161eb139cea00da64cd77fba2f07c49ddc"
	if got := hex.EncodeToString(seed); got != want {
		t.Fatalf("ToSeed = %s, want %s", got, want)
	}
	if len(seed) != 64 {
		t.Fatalf("seed length = %d, want 64", len(seed))
	}
}

func TestEncodeDecodeMatchesLittleEndianPythonScheme(t *testing.T) {
	mnemonic := NewEnglish()
	tests := []struct {
		value  string
		phrase string
	}{
		{"0", ""},
		{"1", "ability"},
		{"2047", "zoo"},
		{"2048", "abandon ability"},
		{"2049", "ability ability"},
		{"4194303", "zoo zoo"},
		{"4194304", "abandon abandon ability"},
		{"5444517870735015415413993718908291383296", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon ability"},
	}
	for _, test := range tests {
		value, ok := new(big.Int).SetString(test.value, 10)
		if !ok {
			t.Fatal("invalid test integer")
		}
		phrase, err := mnemonic.Encode(value)
		if err != nil {
			t.Fatalf("Encode(%s): %v", test.value, err)
		}
		if phrase != test.phrase {
			t.Errorf("Encode(%s) = %q, want %q", test.value, phrase, test.phrase)
		}
		decoded, err := mnemonic.Decode(phrase)
		if err != nil {
			t.Fatalf("Decode(%q): %v", phrase, err)
		}
		if decoded.Cmp(value) != 0 {
			t.Errorf("Decode(%q) = %s, want %s", phrase, decoded, value)
		}
	}
}

func TestDecodeUsesPythonWhitespaceAndExactWords(t *testing.T) {
	mnemonic := NewEnglish()
	decoded, err := mnemonic.Decode("abandon\u001cability")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Cmp(big.NewInt(2048)) != 0 {
		t.Fatalf("decoded = %s, want 2048", decoded)
	}

	for _, phrase := range []string{"Abandon", "notaword", "abandon notaword"} {
		_, err := mnemonic.Decode(phrase)
		var unknown *UnknownWordError
		if !errors.As(err, &unknown) {
			t.Fatalf("Decode(%q) error = %v, want UnknownWordError", phrase, err)
		}
	}
}

func TestIsNewSeedMatchesPinnedWalletSeed(t *testing.T) {
	phrase := "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent"
	if !IsNewSeed(phrase, StandardPrefix) {
		t.Fatal("known standard seed was rejected")
	}
	if IsNewSeed(phrase, TwoFactorPrefix) || IsNewSeed(phrase, SegWitPrefix) {
		t.Fatal("known standard seed matched another prefix")
	}
	if !IsNewSeed(phrase, "") {
		t.Fatal("empty prefix must match Python bytes.startswith behavior")
	}
}

func TestMakeSeedMatchesPythonEntropyAndNonceSearch(t *testing.T) {
	reader := bytes.NewReader(append([]byte{2}, make([]byte, 16)...))
	mnemonic, err := newWithReader("en", reader)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := mnemonic.MakeDefaultSeed()
	if err != nil {
		t.Fatal(err)
	}
	want := "cave abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon cactus"
	if seed != want {
		t.Fatalf("MakeDefaultSeed = %q, want %q", seed, want)
	}
	if !IsNewSeed(seed, StandardPrefix) || len(strings.Fields(seed)) != 12 {
		t.Fatalf("generated invalid seed %q", seed)
	}
}

func TestMakeSeedPreservesZeroEntropyExit(t *testing.T) {
	mnemonic, err := newWithReader("en", zeroReader{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := mnemonic.MakeDefaultSeed()
	if err != nil {
		t.Fatal(err)
	}
	if seed != "best" {
		t.Fatalf("zero-entropy seed = %q, want pinned Python result %q", seed, "best")
	}
}

func TestMakeSeedPropagatesRandomFailure(t *testing.T) {
	want := errors.New("random failed")
	mnemonic, err := newWithReader("en", errorReader{err: want})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mnemonic.MakeDefaultSeed()
	if !errors.Is(err, want) {
		t.Fatalf("MakeDefaultSeed error = %v, want wrapped random failure", err)
	}
}

func TestEncodeRejectsUnsafeGoOnlyInputs(t *testing.T) {
	mnemonic := NewEnglish()
	for _, value := range []*big.Int{nil, big.NewInt(-1)} {
		if _, err := mnemonic.Encode(value); err == nil {
			t.Fatalf("Encode(%v) succeeded", value)
		}
	}
}

func TestWordsAreStableAcrossInstances(t *testing.T) {
	if !reflect.DeepEqual(NewEnglish().Words(), NewEnglish().Words()) {
		t.Fatal("English word lists differ")
	}
}

type zeroReader struct{}

func (zeroReader) Read(destination []byte) (int, error) {
	clear(destination)
	return len(destination), nil
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}
