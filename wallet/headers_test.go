package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"lbry/daemon/wallet/keys"
)

const firstThreeHeadersHex = "010000000000000000000000000000000000000000000000000000000000000000000000cc59e59ff97ac092b55e423aa5495151ed6fb80570a5bb78cd5bd1c3821c21b8010000000000000000000000000000000000000000000000000000000000000033193156ffff001f07050000" +
	"0000002063f4346a4db34fdfce29a70f5e8d11f065f6b91602b7036c7f22f3a03b28899cba888e2f9c037f831046f8ad09f6d378f79c728d003b177a64d29621f481da5d01000000000000000000000000000000000000000000000000000000000000003c406b5746e1001f5b4f0000" +
	"00000020246cb85843ac936d55388f2ff288b011add5b1b20cca9cfd19a403ca2c9ecbde09d8734d81b5f2eb1b653caf17491544ddfbc72f2f4c0c3f22a3362db5ba9d4701000000000000000000000000000000000000000000000000000000000000003d406b57ffff001f4ff20000"

var firstThreeHeaderHashes = []string{
	"9c89283ba0f3227f6c03b70216b9f665f0118d5e0fa729cedf4fb34d6a34f463",
	"decb9e2cca03a419fd9cca0cb2b1d5ad11b088f22f8f38556d93ac4358b86c24",
	"28547a040490cc48c6c130622631bb52bc5388f98fc28725265d868b25e14400",
}

func TestHeaderCodecAndHashMatchPinnedFixture(t *testing.T) {
	t.Parallel()

	serialized := decodeHeaderFixture(t)
	first := serialized[:HeaderSize]
	header, err := DeserializeHeader(0, append(append([]byte(nil), first...), 0xff))
	if err != nil {
		t.Fatal(err)
	}
	if header.Version != 1 || header.Timestamp != 1446058291 ||
		header.Bits != 520159231 || header.Nonce != 1287 || header.BlockHeight != 0 {
		t.Fatalf("unexpected scalar fields: %+v", header)
	}
	if got, want := string(header.PreviousHash), string(bytes.Repeat([]byte{'0'}, 64)); got != want {
		t.Fatalf("previous hash = %q, want %q", got, want)
	}
	if got, want := string(header.MerkleRoot), "b8211c82c3d15bcd78bba57005b86fed515149a53a425eb592c07af99fe559cc"; got != want {
		t.Fatalf("merkle root = %q, want %q", got, want)
	}
	if got, want := string(header.ClaimTrieRoot), string(bytes.Repeat([]byte{'0'}, 63))+"1"; got != want {
		t.Fatalf("claim trie root = %q, want %q", got, want)
	}
	roundTrip, err := SerializeHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip, first) {
		t.Fatalf("serialized header differs:\n got %x\nwant %x", roundTrip, first)
	}
	for index, want := range firstThreeHeaderHashes {
		raw := serialized[index*HeaderSize : (index+1)*HeaderSize]
		if got := string(HashHeader(raw)); got != want {
			t.Errorf("header %d hash = %q, want %q", index, got, want)
		}
	}
	if got := HashHeader(nil); !bytes.Equal(got, bytes.Repeat([]byte{'0'}, 64)) {
		t.Fatalf("nil hash = %q", got)
	}
	if _, err := DeserializeHeader(0, first[:HeaderSize-1]); !errors.Is(err, ErrInvalidHeaderLength) {
		t.Fatalf("short deserialize error = %v", err)
	}
	header.PreviousHash = []byte("not hex")
	if _, err := SerializeHeader(header); err == nil {
		t.Fatal("invalid hexadecimal hash serialized without an error")
	}
}

func TestArithUint256MatchesPinnedCompactVectors(t *testing.T) {
	t.Parallel()

	zeroCompacts := []uint32{
		0, 0x00123456, 0x01003456, 0x02000056, 0x03000000, 0x04000000,
		0x00923456, 0x01803456, 0x02800056, 0x03800000, 0x04800000,
	}
	for _, compact := range zeroCompacts {
		if got := ArithUint256FromCompact(compact).Value(); got.Sign() != 0 {
			t.Errorf("from compact %#08x = %s, want 0", compact, got)
		}
	}

	cases := []struct {
		compact       uint32
		value         string
		roundTrip     uint32
		negative      uint32
		checkNegative bool
	}{
		{compact: 0x01123456, value: "12", roundTrip: 0x01120000},
		{compact: 0x01fedcba, value: "7e", roundTrip: 0x017e0000, negative: 0x01fe0000, checkNegative: true},
		{compact: 0x02123456, value: "1234", roundTrip: 0x02123400},
		{compact: 0x03123456, value: "123456", roundTrip: 0x03123456},
		{compact: 0x04123456, value: "12345600", roundTrip: 0x04123456},
		{compact: 0x04923456, value: "12345600", roundTrip: 0x04123456, negative: 0x04923456, checkNegative: true},
		{compact: 0x05009234, value: "92340000", roundTrip: 0x05009234},
		{compact: 0x20123456, value: "1234560000000000000000000000000000000000000000000000000000000000", roundTrip: 0x20123456},
	}
	for _, test := range cases {
		number := ArithUint256FromCompact(test.compact)
		if got := number.Value().Text(16); got != test.value {
			t.Errorf("from compact %#08x = %s, want %s", test.compact, got, test.value)
		}
		if got := number.Compact(); got != test.roundTrip {
			t.Errorf("compact(%#08x) = %#08x, want %#08x", test.compact, got, test.roundTrip)
		}
		if test.checkNegative && number.Negative() != test.negative {
			t.Errorf("negative(%#08x) = %#08x, want %#08x", test.compact, number.Negative(), test.negative)
		}
	}
	if got := NewArithUint256(big.NewInt(0x80)).Compact(); got != 0x02008000 {
		t.Fatalf("compact(0x80) = %#08x", got)
	}
	if got := NewArithUint256(new(big.Int)).Bits(); got != 2 {
		t.Fatalf("pinned zero bits quirk = %d, want 2", got)
	}
}

func TestNextBlockTargetMatchesPinnedVectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		previousTimestamp uint32
		currentTimestamp  uint32
		bits              uint32
		want              uint32
	}{
		{name: "max retarget from difficulty one", previousTimestamp: 1386475638, currentTimestamp: 1386475638, bits: 0x1f00ffff, want: 0x1f00e146},
		{name: "maximum difficulty increase", previousTimestamp: 1386475638, currentTimestamp: 1386475638, bits: 0x1f00a000, want: 0x1f008ccc},
		{name: "minimum difficulty decrease", previousTimestamp: 1386475638, currentTimestamp: 1386475638 + 60*20, bits: 0x1f00a000, want: 0x1f00f000},
		{name: "pow limit", previousTimestamp: 1386475638, currentTimestamp: 1386475638 + 600, bits: 0x1f00ffff, want: 0x1f00ffff},
	}
	maximum := NewArithUint256(defaultMaxTarget)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			previous := &BlockHeader{Timestamp: test.previousTimestamp}
			current := &BlockHeader{Timestamp: test.currentTimestamp, Bits: test.bits}
			if got := NextBlockTarget(maximum, defaultTargetTimespan, previous, current).Compact(); got != test.want {
				t.Fatalf("target = %#08x, want %#08x", got, test.want)
			}
		})
	}
	if got := NextBlockTarget(maximum, defaultTargetTimespan, nil, nil); got.Compare(maximum) != 0 {
		t.Fatalf("initial target = %s, want %s", got.Value(), maximum.Value())
	}
}

func TestHeaderProofOfWorkMatchesPinnedVectors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input []byte
		want  string
	}{
		{input: []byte("test string"), want: "485f3920d48a0448034b0852d1489cfa475341176838c7d36896765221be35ce"},
		{input: bytes.Repeat([]byte{'a'}, 70), want: "eb44af2f41e7c6522fb8be4773661be5baa430b8b2c3a670247e9ab060608b75"},
		{input: bytes.Repeat([]byte{'d'}, 140), want: "74044747b7c1ff867eb09a84d026b02d8dc539fb6adcec3536f3dfa9266495d9"},
	}
	for _, test := range cases {
		headerHash := HashHeader(test.input)
		got, err := HeaderHashToPoWHash(headerHash)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.want {
			t.Errorf("pow hash for %q = %s, want %s", test.input, got, test.want)
		}
		proof, err := ProofOfWork(headerHash)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := new(big.Int).SetString(test.want, 16)
		if proof.Value().Cmp(want) != 0 {
			t.Errorf("pow integer = %s, want %s", proof.Value(), want)
		}
	}
	if _, err := HeaderHashToPoWHash([]byte("not hex")); err == nil {
		t.Fatal("invalid header hash decoded without an error")
	}
}

func TestHeadersConnectBoundsRepairAndPersistence(t *testing.T) {
	serialized := decodeHeaderFixture(t)
	headers := newCheckpointIndependentHeaders(":memory:")
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if got := headers.Height(); got != -1 {
		t.Fatalf("empty height = %d, want -1", got)
	}
	added, err := headers.Connect(0, serialized)
	if err != nil || added != 3 {
		t.Fatalf("connect = %d, %v", added, err)
	}
	if headers.Len() != 3 || headers.Height() != 2 || headers.BytesSize() != 3*HeaderSize {
		t.Fatalf("store dimensions = len %d height %d bytes %d", headers.Len(), headers.Height(), headers.BytesSize())
	}
	if _, err := headers.GetRaw(-1); !errors.Is(err, ErrHeaderOutOfBounds) {
		t.Fatalf("negative bound error = %v", err)
	}
	if _, err := headers.GetRaw(3); !errors.Is(err, ErrHeaderOutOfBounds) {
		t.Fatalf("upper bound error = %v", err)
	}
	if _, err := headers.Connect(-1, serialized[:HeaderSize]); !errors.Is(err, ErrHeaderOutOfBounds) {
		t.Fatalf("negative connect error = %v", err)
	}
	if _, err := headers.Connect(3, []byte{0}); !errors.Is(err, ErrInvalidHeaderLength) {
		t.Fatalf("misaligned connect error = %v", err)
	}
	if got, ok := headers.EstimatedTimestamp(0, true); ok || got != 0 {
		t.Fatalf("unconfirmed timestamp = %d, %t", got, ok)
	}
	if got, ok := headers.EstimatedTimestamp(2, true); !ok || got != 1466646589 {
		t.Fatalf("real timestamp = %d, %t", got, ok)
	}
	if got, ok := headers.EstimatedTimestamp(10, false); !ok || got != 1466648194 {
		t.Fatalf("estimated timestamp = %d, %t", got, ok)
	}
	if got, err := headers.Hash(nil); err != nil || string(got) != firstThreeHeaderHashes[2] {
		t.Fatalf("tip hash = %q, %v", got, err)
	}

	bad := append([]byte(nil), serialized...)
	bad[2*HeaderSize+4] ^= 0xff
	rejected := newCheckpointIndependentHeaders(":memory:")
	if err := rejected.Open(); err != nil {
		t.Fatal(err)
	}
	if added, err := rejected.Connect(0, bad); err != nil || added != 0 || rejected.Len() != 0 {
		t.Fatalf("invalid practical chunk = added %d len %d err %v", added, rejected.Len(), err)
	}
	var invalid *InvalidHeaderError
	if err := rejected.ValidateChunk(0, bad); !errors.As(err, &invalid) || invalid.Height != 0 {
		t.Fatalf("validate error = %T %v", err, err)
	}

	headers.mu.Lock()
	var corrupted [1]byte
	corruptionOffset := int64(2*HeaderSize + 4)
	if _, err := headers.storage.ReadAt(corrupted[:], corruptionOffset); err != nil {
		headers.mu.Unlock()
		t.Fatal(err)
	}
	corrupted[0] ^= 0xff
	if _, err := headers.storage.WriteAt(corrupted[:], corruptionOffset); err != nil {
		headers.mu.Unlock()
		t.Fatal(err)
	}
	headers.mu.Unlock()
	if err := headers.Repair(0); err != nil {
		t.Fatal(err)
	}
	if got := headers.Len(); got != 1 {
		t.Fatalf("repair retained %d headers, want 1", got)
	}

	path := filepath.Join(t.TempDir(), "headers")
	persistent := newCheckpointIndependentHeaders(path)
	if err := persistent.Open(); err != nil {
		t.Fatal(err)
	}
	if added, err := persistent.Connect(0, serialized); err != nil || added != 3 {
		t.Fatalf("persistent connect = %d, %v", added, err)
	}
	if err := persistent.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(len(serialized)) {
		t.Fatalf("persisted size = %v, %v", info, err)
	}
	reopened := newCheckpointIndependentHeaders(path)
	if err := reopened.Open(); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Len(); got != 3 {
		t.Fatalf("reopened length = %d, want 3", got)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHeadersRegtestSkipsDifficultyButKeepsLinks(t *testing.T) {
	t.Parallel()

	serialized := decodeHeaderFixture(t)
	headers := NewHeadersForNetwork(":memory:", keys.RegTest)
	headers.genesisHash = nil
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	modified := append([]byte(nil), serialized...)
	modified[HeaderSize+104] = 0
	if added, err := headers.Connect(0, modified[:2*HeaderSize]); err != nil || added != 2 {
		t.Fatalf("unvalidated connect = %d, %v", added, err)
	}
	broken := append([]byte(nil), serialized[2*HeaderSize:]...)
	broken[4] ^= 0xff
	if added, err := headers.Connect(2, broken); err != nil || added != 0 {
		t.Fatalf("broken link connect = %d, %v", added, err)
	}
}

func TestHeadersConcurrentReadWhileConnecting(t *testing.T) {
	t.Parallel()

	serialized := decodeHeaderFixture(t)
	headers := newCheckpointIndependentHeaders(":memory:")
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for attempt := 0; attempt < 100; attempt++ {
				_ = headers.Height()
				if headers.Len() > 0 {
					_, _ = headers.Get(0)
				}
			}
		}()
	}
	for height := 0; height < 3; height++ {
		start := height * HeaderSize
		if added, err := headers.Connect(height, serialized[start:start+HeaderSize]); err != nil || added != 1 {
			t.Fatalf("connect %d = %d, %v", height, added, err)
		}
	}
	readers.Wait()
}

func decodeHeaderFixture(t *testing.T) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(firstThreeHeadersHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3*HeaderSize {
		t.Fatalf("fixture length = %d, want %d", len(decoded), 3*HeaderSize)
	}
	return decoded
}

func newCheckpointIndependentHeaders(path string, options ...HeaderOption) *Headers {
	return NewHeaders(path, append(options, withHeaderCheckpoints(emptyCheckpoints))...)
}
