package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	mainnetCheckpointCount      = 1243
	mainnetCheckpointLastHeight = 1242000
	mainnetCheckpointDataSize   = 39776
	mainnetCheckpointDataSHA256 = "f6fbdebca0b20149dd1fdfcb8e1369b5e9769a30b65b35b7c96638aa4101ad81"
	mainnetCheckpointFirstHash  = "bf3ff54138625c56737509f080e7e7f3c55972f0f80e684f8e25d2ad83bedbe2"
	mainnetCheckpointSecondHash = "4ec1f9aebc8f7f75d5d05430d1512e38598188d56a8b51510c9e47656c4ffad9"
	mainnetCheckpointLastHash   = "a74d7c0b104eaf76c53a3a31ce51b75bbd8e05b5e84c31f593f505a13d83634c"
)

func TestMainnetCheckpointTableArtifact(t *testing.T) {
	if got := len(mainnetCheckpointData); got != mainnetCheckpointDataSize {
		t.Fatalf("embedded checkpoint data size = %d, want %d", got, mainnetCheckpointDataSize)
	}
	digest := sha256.Sum256([]byte(mainnetCheckpointData))
	if got := hex.EncodeToString(digest[:]); got != mainnetCheckpointDataSHA256 {
		t.Fatalf("embedded checkpoint data SHA-256 = %s, want %s", got, mainnetCheckpointDataSHA256)
	}
	if got := mainnetCheckpoints.len(); got != mainnetCheckpointCount {
		t.Fatalf("checkpoint count = %d, want %d", got, mainnetCheckpointCount)
	}
	if got := mainnetCheckpoints.lastHeight(); got != mainnetCheckpointLastHeight {
		t.Fatalf("last checkpoint height = %d, want %d", got, mainnetCheckpointLastHeight)
	}

	assertCheckpointAt(t, 0, mainnetCheckpointFirstHash)
	assertCheckpointAt(t, 1, mainnetCheckpointSecondHash)
	assertCheckpointAt(t, mainnetCheckpointCount-1, mainnetCheckpointLastHash)
	assertCheckpointLookup(t, 0, mainnetCheckpointFirstHash)
	assertCheckpointLookup(t, checkpointInterval, mainnetCheckpointSecondHash)
	assertCheckpointLookup(t, mainnetCheckpointLastHeight, mainnetCheckpointLastHash)
}

func TestCheckpointTableValidation(t *testing.T) {
	for _, size := range []int{1, checkpointDigestSize - 1, checkpointDigestSize + 1} {
		if _, err := newCheckpointTable(strings.Repeat("x", size)); err == nil {
			t.Fatalf("newCheckpointTable() accepted %d-byte unaligned data", size)
		}
	}

	empty, err := newCheckpointTable("")
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.len(); got != 0 {
		t.Fatalf("empty checkpoint count = %d, want 0", got)
	}
	if got := empty.lastHeight(); got != -1 {
		t.Fatalf("empty last checkpoint height = %d, want -1", got)
	}
	if value, ok := empty.at(0); ok || value != "" {
		t.Fatalf("empty at(0) = %q, %t, want empty/false", value, ok)
	}
	if value, ok := empty.lookup(0); ok || value != "" {
		t.Fatalf("empty lookup(0) = %q, %t, want empty/false", value, ok)
	}
}

func TestCheckpointTableRejectsInvalidCoordinates(t *testing.T) {
	for _, index := range []int{-1, mainnetCheckpoints.len(), mainnetCheckpoints.len() + 1} {
		if value, ok := mainnetCheckpoints.at(index); ok || value != "" {
			t.Fatalf("at(%d) = %q, %t, want empty/false", index, value, ok)
		}
	}
	for _, height := range []int{
		-1000,
		-1,
		1,
		checkpointInterval - 1,
		mainnetCheckpointLastHeight + 1,
		mainnetCheckpointLastHeight + checkpointInterval,
	} {
		if value, ok := mainnetCheckpoints.lookup(height); ok || value != "" {
			t.Fatalf("lookup(%d) = %q, %t, want empty/false", height, value, ok)
		}
	}
}

func assertCheckpointAt(t *testing.T, index int, want string) {
	t.Helper()
	got, ok := mainnetCheckpoints.at(index)
	if !ok || got != want {
		t.Fatalf("at(%d) = %q, %t, want %q/true", index, got, ok, want)
	}
}

func assertCheckpointLookup(t *testing.T, height int, want string) {
	t.Helper()
	got, ok := mainnetCheckpoints.lookup(height)
	if !ok || got != want {
		t.Fatalf("lookup(%d) = %q, %t, want %q/true", height, got, ok, want)
	}
}
