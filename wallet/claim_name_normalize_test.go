package wallet

import (
	"strings"
	"sync"
	"testing"
)

func TestNormalizeClaimNameMatchesPython311Unicode14Probes(t *testing.T) {
	// Expected values were probed with Python 3.11 and
	// unicodedata.unidata_version == "14.0.0".
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "ascii", input: "LBRY-Name", want: "lbry-name"},
		{name: "sharp s expansion", input: "Stra\u00dfe\u1e9e", want: "strassess"},
		{name: "dotted capital i", input: "\u0130", want: "i\u0307"},
		{name: "ligature expansion", input: "\ufb03", want: "ffi"},
		{name: "kelvin canonical decomposition", input: "\u212a", want: "k"},
		{name: "canonical decomposition", input: "\u00c9", want: "e\u0301"},
		{name: "hangul decomposition", input: "\uac01", want: "\u1100\u1161\u11a8"},
		{
			name:  "casefold runs after decomposition",
			input: "\u1f88",
			want:  "\u03b1\u0313\u03b9",
		},
		{name: "combining casefold expansion", input: "\u0345", want: "\u03b9"},
		{
			name:  "canonical combining order",
			input: "A\u0315\u0300\u05ae\u0301",
			want:  "a\u05ae\u0300\u0301\u0315",
		},
		{
			name:  "equal combining classes are stable",
			input: "A\u0301\u0300",
			want:  "a\u0301\u0300",
		},
		{
			name:  "leading nonstarters reorder",
			input: "\u0315\u0300\u05ae\u0301",
			want:  "\u05ae\u0300\u0301\u0315",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeClaimName(test.input); got != test.want {
				t.Fatalf("normalizeClaimName(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeClaimNameHasNoStreamSafeLimit(t *testing.T) {
	input := "A" + strings.Repeat("\u0315", 64) + "\u0300B"
	want := "a\u0300" + strings.Repeat("\u0315", 64) + "b"
	got := normalizeClaimName(input)
	if got != want {
		t.Fatalf("long non-starter run = %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '\u034f') {
		t.Fatalf("long non-starter run contains stream-safe U+034F: %q", got)
	}
}

func TestNormalizeClaimNameUsesPython311Unicode14Assignments(t *testing.T) {
	for _, value := range []rune{
		'\U0001e4ec', // Unicode 15.0, combining class 232 in newer tables.
		'\u2ffc',     // Unicode 15.1.
		'\u1c89',     // Unicode 16.0, folds to U+1C8A in newer tables.
		'\u088f',     // Unicode 17.0.
	} {
		if !isPostUnicode14ClaimNameAssignment(value) {
			t.Fatalf("post-Unicode-14 U+%04X is not masked", value)
		}
		input := "A" + string(value) + "B"
		want := "a" + string(value) + "b"
		if got := normalizeClaimName(input); got != want {
			t.Fatalf("post-Unicode-14 U+%04X normalized as %q, want %q", value, got, want)
		}
	}

	// Python 3.11 treats U+1E4EC as an unassigned CCC-zero starter. It must
	// split the two surrounding canonical-ordering segments.
	input := "A\u0315\U0001e4ec\u0300B"
	want := "a\u0315\U0001e4ec\u0300b"
	if got := normalizeClaimName(input); got != want {
		t.Fatalf("Unicode-15 combining boundary = %q, want %q", got, want)
	}
}

func TestNormalizeClaimNameIsSafeForConcurrentUse(t *testing.T) {
	const workers = 32
	const input = "Stra\u00dfe\u1f88\u0315\u0300"
	const want = "strasse\u03b1\u0313\u0300\u0315\u03b9"

	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if got := normalizeClaimName(input); got != want {
				errors <- got
			}
		}()
	}
	wait.Wait()
	close(errors)
	for got := range errors {
		t.Errorf("concurrent normalizeClaimName = %q, want %q", got, want)
	}
}
