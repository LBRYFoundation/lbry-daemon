package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sync"

	"golang.org/x/crypto/ripemd160"

	"lbry/daemon/wallet/keys"
)

const (
	HeaderSize                   = 112
	defaultTargetTimespan        = int64(150)
	defaultFirstBlockTimestamp   = int64(1466646588)
	defaultTimestampAverageDelta = 160.6855883050695
	checkpointEmptyHash          = "56944c5d3f98413ef45cf54545538103cc9f298e0575820ad3591376e2e0f65d"
	checkpointZeroHeaderHash     = "789d737d4f448e554b318c94063bbfa63e9ccda6e208f5648ca76ee68896557b"
)

var (
	ErrHeadersNotOpen      = errors.New("headers are not open")
	ErrInvalidHeaderLength = errors.New("invalid header length")
	ErrHeaderOutOfBounds   = errors.New("header height is out of bounds")
	uint256Modulus         = new(big.Int).Lsh(big.NewInt(1), 256)
	defaultMaxTarget       = mustHeaderInteger("0000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	regtestMaxTarget       = mustHeaderInteger("7fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	headerHashBufferPool   = sync.Pool{New: func() any { return make([]byte, 32*1024) }}
)

var (
	defaultGenesisHash = []byte("9c89283ba0f3227f6c03b70216b9f665f0118d5e0fa729cedf4fb34d6a34f463")
	regtestGenesisHash = []byte("6e3fcf1299d4ec5d79c3a4c91d624a4acf9e2e173d95a1a0504f677669687556")
)

// BlockHeader is the typed counterpart of the dictionaries returned by
// lbry.wallet.header.Headers.deserialize. Hash fields contain lowercase ASCII
// hex in display byte order, just like the Python bytes values.
type BlockHeader struct {
	Version       uint32
	PreviousHash  []byte
	MerkleRoot    []byte
	ClaimTrieRoot []byte
	Timestamp     uint32
	Bits          uint32
	Nonce         uint32
	BlockHeight   int
}

type InvalidHeaderError struct {
	Height  int
	Message string
}

func (err *InvalidHeaderError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

// ArithUint256 preserves the pinned SDK's compact-target arithmetic, including
// its 256-bit multiplication wrap and float-rounded division.
type ArithUint256 struct {
	value *big.Int
}

func NewArithUint256(value *big.Int) *ArithUint256 {
	if value == nil {
		value = new(big.Int)
	}
	return &ArithUint256{value: new(big.Int).Set(value)}
}

func ArithUint256FromCompact(compact uint32) *ArithUint256 {
	size := uint(compact >> 24)
	word := uint64(compact & 0x007fffff)
	value := new(big.Int).SetUint64(word)
	if size <= 3 {
		value.Rsh(value, 8*(3-size))
	} else {
		value.Lsh(value, 8*(size-3))
	}
	return NewArithUint256(value)
}

func (number *ArithUint256) Value() *big.Int {
	if number == nil || number.value == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(number.value)
}

func (number *ArithUint256) Bits() int {
	// Python iterates bin(value)[2:] and tests each one-character string for
	// truthiness. The first character is therefore always selected, including
	// the string "0", yielding the digit count plus one.
	return len(number.Value().Text(2)) + 1
}

func (number *ArithUint256) Compact() uint32 {
	return number.calculateCompact(false)
}

func (number *ArithUint256) Negative() uint32 {
	return number.calculateCompact(true)
}

func (number *ArithUint256) calculateCompact(negative bool) uint32 {
	value := number.Value()
	size := (number.Bits() + 7) / 8
	var compact uint64
	if size <= 3 {
		compact = value.Uint64() << uint(8*(3-size))
	} else {
		shifted := new(big.Int).Rsh(value, uint(8*(size-3)))
		compact = shifted.Uint64()
	}
	if compact&0x00800000 != 0 {
		compact >>= 8
		size++
	}
	compact |= uint64(size) << 24
	if negative && compact&0x007fffff != 0 {
		compact |= 0x00800000
	}
	return uint32(compact)
}

func (number *ArithUint256) Multiply(multiplier int64) *ArithUint256 {
	value := new(big.Int).Mul(number.Value(), big.NewInt(multiplier))
	value.Mod(value, uint256Modulus)
	return NewArithUint256(value)
}

func (number *ArithUint256) Divide(divisor int64) *ArithUint256 {
	if divisor == 0 {
		panic("division by zero")
	}
	// Python int / int first rounds through an IEEE-754 double, after which
	// int() truncates toward zero. big.Float at 53 bits reproduces that path.
	numerator := new(big.Float).SetPrec(53).SetMode(big.ToNearestEven).SetInt(number.Value())
	denominator := new(big.Float).SetPrec(53).SetMode(big.ToNearestEven).SetInt64(divisor)
	quotient := new(big.Float).SetPrec(53).SetMode(big.ToNearestEven).Quo(numerator, denominator)
	value, _ := quotient.Int(nil)
	return NewArithUint256(value)
}

func (number *ArithUint256) Compare(other *ArithUint256) int {
	return number.Value().Cmp(other.Value())
}

func SerializeHeader(header BlockHeader) ([]byte, error) {
	previous, err := decodeReversedHeaderHash(header.PreviousHash)
	if err != nil {
		return nil, fmt.Errorf("previous block hash: %w", err)
	}
	merkle, err := decodeReversedHeaderHash(header.MerkleRoot)
	if err != nil {
		return nil, fmt.Errorf("merkle root: %w", err)
	}
	claimTrie, err := decodeReversedHeaderHash(header.ClaimTrieRoot)
	if err != nil {
		return nil, fmt.Errorf("claim trie root: %w", err)
	}
	serialized := make([]byte, 0, 16+len(previous)+len(merkle)+len(claimTrie))
	var integer [4]byte
	binary.LittleEndian.PutUint32(integer[:], header.Version)
	serialized = append(serialized, integer[:]...)
	serialized = append(serialized, previous...)
	serialized = append(serialized, merkle...)
	serialized = append(serialized, claimTrie...)
	for _, value := range []uint32{header.Timestamp, header.Bits, header.Nonce} {
		binary.LittleEndian.PutUint32(integer[:], value)
		serialized = append(serialized, integer[:]...)
	}
	return serialized, nil
}

func DeserializeHeader(height int, serialized []byte) (BlockHeader, error) {
	if len(serialized) < HeaderSize {
		return BlockHeader{}, fmt.Errorf("%w: got %d bytes, need at least %d", ErrInvalidHeaderLength, len(serialized), HeaderSize)
	}
	return BlockHeader{
		Version:       binary.LittleEndian.Uint32(serialized[:4]),
		PreviousHash:  encodeReversedHeaderHash(serialized[4:36]),
		MerkleRoot:    encodeReversedHeaderHash(serialized[36:68]),
		ClaimTrieRoot: encodeReversedHeaderHash(serialized[68:100]),
		Timestamp:     binary.LittleEndian.Uint32(serialized[100:104]),
		Bits:          binary.LittleEndian.Uint32(serialized[104:108]),
		Nonce:         binary.LittleEndian.Uint32(serialized[108:112]),
		BlockHeight:   height,
	}, nil
}

func HashHeader(serialized []byte) []byte {
	if serialized == nil {
		return bytes.Repeat([]byte{'0'}, 64)
	}
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return encodeReversedHeaderHash(second[:])
}

func HeaderHashToPoWHash(headerHash []byte) ([]byte, error) {
	decoded, err := hex.DecodeString(string(headerHash))
	if err != nil {
		return nil, err
	}
	reverseBytes(decoded)
	wide := sha512.Sum512(decoded)
	left := ripemd160.New()
	_, _ = left.Write(wide[:len(wide)/2])
	right := ripemd160.New()
	_, _ = right.Write(wide[len(wide)/2:])
	combined := append(left.Sum(nil), right.Sum(nil)...)
	first := sha256.Sum256(combined)
	second := sha256.Sum256(first[:])
	return encodeReversedHeaderHash(second[:]), nil
}

func ProofOfWork(headerHash []byte) (*ArithUint256, error) {
	powHash, err := HeaderHashToPoWHash(headerHash)
	if err != nil {
		return nil, err
	}
	value, ok := new(big.Int).SetString(string(powHash), 16)
	if !ok {
		return nil, errors.New("proof-of-work hash is not hexadecimal")
	}
	return NewArithUint256(value), nil
}

func NextBlockTarget(
	maxTarget *ArithUint256, targetTimespan int64, previous, current *BlockHeader,
) *ArithUint256 {
	if previous == nil && current == nil {
		return NewArithUint256(maxTarget.Value())
	}
	if previous == nil {
		previous = current
	}
	actualTimespan := int64(current.Timestamp) - int64(previous.Timestamp)
	modulatedTimespan := targetTimespan + (actualTimespan-targetTimespan)/8
	minimumTimespan := targetTimespan - targetTimespan/8
	maximumTimespan := targetTimespan + targetTimespan/2
	clampedTimespan := max(minimumTimespan, min(modulatedTimespan, maximumTimespan))
	target := ArithUint256FromCompact(current.Bits)
	newTarget := target.Multiply(clampedTimespan).Divide(targetTimespan)
	if newTarget.Compare(maxTarget) > 0 {
		return NewArithUint256(maxTarget.Value())
	}
	return newTarget
}

type HeaderOption func(*Headers)

func WithHeaderValidation(enabled bool) HeaderOption {
	return func(headers *Headers) {
		headers.validateDifficulty = enabled
	}
}

func withHeaderCheckpoints(checkpoints checkpointTable) HeaderOption {
	return func(headers *Headers) {
		headers.checkpoints = checkpoints
	}
}

// Headers is the 112-byte linear header store with network checkpoint sizing,
// verification, and missing-chunk discovery. Chunk fetching remains in the
// separate SPV network layer.
type Headers struct {
	mu sync.RWMutex

	path                   string
	storage                headerStorage
	size                   int
	opened                 bool
	checkpoints            checkpointTable
	missingCheckpoints     map[int]struct{}
	chunkGetter            HeaderChunkGetter
	chunkFetchLock         chan struct{}
	validateDifficulty     bool
	genesisHash            []byte
	maxTarget              *ArithUint256
	targetTimespan         int64
	firstBlockTimestamp    int64
	timestampAverageOffset float64
}

func NewHeaders(path string, options ...HeaderOption) *Headers {
	headers := &Headers{
		path:                   path,
		checkpoints:            mainnetCheckpoints,
		missingCheckpoints:     make(map[int]struct{}),
		chunkFetchLock:         make(chan struct{}, 1),
		validateDifficulty:     true,
		genesisHash:            append([]byte(nil), defaultGenesisHash...),
		maxTarget:              NewArithUint256(defaultMaxTarget),
		targetTimespan:         defaultTargetTimespan,
		firstBlockTimestamp:    defaultFirstBlockTimestamp,
		timestampAverageOffset: defaultTimestampAverageDelta,
	}
	for _, option := range options {
		if option != nil {
			option(headers)
		}
	}
	return headers
}

func NewHeadersForNetwork(path string, network keys.Network) *Headers {
	headers := NewHeaders(path)
	if network != keys.MainNet {
		headers.checkpoints = emptyCheckpoints
	}
	if network == keys.RegTest {
		headers.validateDifficulty = false
		headers.genesisHash = append([]byte(nil), regtestGenesisHash...)
		headers.maxTarget = NewArithUint256(regtestMaxTarget)
	}
	return headers
}

func (headers *Headers) Open() error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	headers.mu.Lock()
	defer headers.mu.Unlock()
	var (
		storage headerStorage
		err     error
	)
	if headers.path == ":memory:" {
		storage = newMemoryHeaderStorage()
	} else {
		storage, err = openStagedFileHeaderStorage(headers.path)
		if err != nil {
			return err
		}
	}
	rawSize := storage.Size()
	if rawSize/HeaderSize > int64(maxInt()) {
		_ = storage.Close()
		return fmt.Errorf("header count %d exceeds the Go integer range", rawSize/HeaderSize)
	}
	previousStorage, previousSize, previousOpened := headers.storage, headers.size, headers.opened
	headers.storage = storage
	headers.size = int(rawSize / HeaderSize)
	headers.opened = true
	repairStart := headers.checkpoints.lastHeight() + checkpointInterval
	if rawSize%HeaderSize != 0 {
		if err := headers.repairLocked(0); err != nil {
			headers.storage, headers.size, headers.opened = previousStorage, previousSize, previousOpened
			_ = storage.Close()
			return err
		}
	} else if err := headers.repairLocked(repairStart); err != nil {
		headers.storage, headers.size, headers.opened = previousStorage, previousSize, previousOpened
		_ = storage.Close()
		return err
	}
	if err := headers.ensureCheckpointedSizeLocked(); err != nil {
		headers.storage, headers.size, headers.opened = previousStorage, previousSize, previousOpened
		_ = storage.Close()
		return err
	}
	if err := headers.findMissingCheckpointsLocked(); err != nil {
		headers.storage, headers.size, headers.opened = previousStorage, previousSize, previousOpened
		_ = storage.Close()
		return err
	}
	if previousStorage != nil {
		_ = previousStorage.Close()
	}
	return nil
}

func (headers *Headers) Close() error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	headers.mu.Lock()
	defer headers.mu.Unlock()
	if !headers.opened {
		return nil
	}
	if headers.storage == nil {
		return errors.New("header storage is nil")
	}
	if err := headers.storage.Commit(); err != nil {
		return err
	}
	closeErr := headers.storage.Close()
	headers.storage = nil
	headers.size = 0
	headers.opened = false
	return closeErr
}

func (headers *Headers) Len() int {
	if headers == nil {
		return 0
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	return headers.size
}

func (headers *Headers) Height() int {
	return headers.Len() - 1
}

func (headers *Headers) BytesSize() int {
	return headers.Len() * HeaderSize
}

func (headers *Headers) Get(height int) (BlockHeader, error) {
	raw, err := headers.GetRaw(height)
	if err != nil {
		return BlockHeader{}, err
	}
	return DeserializeHeader(height, raw)
}

func (headers *Headers) GetRaw(height int) ([]byte, error) {
	return headers.GetRawContext(context.Background(), height)
}

func (headers *Headers) GetRawContext(ctx context.Context, height int) ([]byte, error) {
	if headers == nil {
		return nil, errors.New("headers are nil")
	}
	if ctx == nil {
		return nil, errors.New("header read context is nil")
	}
	headers.mu.RLock()
	hasGetter := headers.chunkGetter != nil
	headers.mu.RUnlock()
	if hasGetter {
		if err := headers.EnsureChunkAt(ctx, height); err != nil {
			return nil, err
		}
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	if !headers.opened {
		return nil, ErrHeadersNotOpen
	}
	return headers.readLocked(height)
}

func (headers *Headers) Hash(height *int) ([]byte, error) {
	if height == nil {
		current := headers.Height()
		height = &current
	}
	raw, err := headers.GetRaw(*height)
	if err != nil {
		return nil, err
	}
	return HashHeader(raw), nil
}

func (headers *Headers) BestHash() (string, error) {
	if headers == nil {
		return "", errors.New("headers are nil")
	}
	if headers.Height() < 0 {
		headers.mu.RLock()
		defer headers.mu.RUnlock()
		return string(headers.genesisHash), nil
	}
	hash, err := headers.Hash(nil)
	return string(hash), err
}

func (headers *Headers) EstimatedTimestamp(height int, tryRealHeaders bool) (int64, bool) {
	if height <= 0 {
		return 0, false
	}
	if tryRealHeaders {
		hasHeader, err := headers.HasHeader(height)
		if err == nil && hasHeader {
			raw, err := headers.GetRaw(height)
			if err == nil {
				return int64(binary.LittleEndian.Uint32(raw[100:104])), true
			}
		}
	}
	estimated := float64(headers.firstBlockTimestamp) + float64(height)*headers.timestampAverageOffset
	return int64(math.Trunc(estimated)), true
}

// ChunkHash returns the reversed double-SHA256 of up to count raw headers.
// Reads beyond the current logical byte length are silently short, matching
// Python BytesIO slicing at checkpoint boundaries.
func (headers *Headers) ChunkHash(start, count int) (string, error) {
	if headers == nil {
		return "", errors.New("headers are nil")
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	if !headers.opened {
		return "", ErrHeadersNotOpen
	}
	return headers.chunkHashLocked(start, count)
}

func (headers *Headers) chunkHashLocked(start, count int) (string, error) {
	if start < 0 || count < 0 {
		return "", fmt.Errorf("%w: start %d, count %d", ErrHeaderOutOfBounds, start, count)
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if int64(start) > maxInt64/HeaderSize || int64(count) > maxInt64/HeaderSize {
		return "", ErrHeaderOutOfBounds
	}
	offset := int64(start) * HeaderSize
	length := int64(count) * HeaderSize
	available := headers.storage.Size() - offset
	if available < 0 {
		available = 0
	}
	length = min(length, available)
	first := sha256.New()
	if length > 0 {
		reader := io.NewSectionReader(headers.storage, offset, length)
		buffer := headerHashBufferPool.Get().([]byte)
		_, err := io.CopyBuffer(first, reader, buffer)
		headerHashBufferPool.Put(buffer)
		if err != nil {
			return "", err
		}
	}
	second := sha256.Sum256(first.Sum(nil))
	return string(encodeReversedHeaderHash(second[:])), nil
}

func (headers *Headers) HasHeader(height int) (bool, error) {
	if headers == nil {
		return false, errors.New("headers are nil")
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	if !headers.opened {
		return false, ErrHeadersNotOpen
	}
	return headers.hasHeaderLocked(height)
}

func (headers *Headers) hasHeaderLocked(height int) (bool, error) {
	if height < 0 {
		return false, fmt.Errorf("%w: %d", ErrHeaderOutOfBounds, height)
	}
	normalized := (height / checkpointInterval) * checkpointInterval
	if _, checkpointed := headers.checkpoints.lookup(normalized); checkpointed {
		_, missing := headers.missingCheckpoints[normalized]
		return !missing, nil
	}
	hash, err := headers.chunkHashLocked(height, 1)
	if err != nil {
		return false, err
	}
	return hash != checkpointEmptyHash && hash != checkpointZeroHeaderHash, nil
}

func (headers *Headers) MissingCheckpointedChunks() []int {
	if headers == nil {
		return []int{}
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	missing := make([]int, 0, len(headers.missingCheckpoints))
	for index := headers.checkpoints.len() - 1; index >= 0; index-- {
		height := index * checkpointInterval
		if _, exists := headers.missingCheckpoints[height]; exists {
			missing = append(missing, height)
		}
	}
	return missing
}

func (headers *Headers) ensureCheckpointedSizeLocked() error {
	lastHeight := headers.checkpoints.lastHeight()
	if headers.size-1 >= lastHeight {
		return nil
	}
	checkpointOffset := int64(lastHeight) * HeaderSize
	if err := headers.storage.Resize(checkpointOffset); err != nil {
		return err
	}
	if err := headers.storage.Resize(checkpointOffset + checkpointInterval*HeaderSize); err != nil {
		return err
	}
	headers.size = int(headers.storage.Size() / HeaderSize)
	return nil
}

func (headers *Headers) findMissingCheckpointsLocked() error {
	for index := headers.checkpoints.len() - 1; index >= 0; index-- {
		height := index * checkpointInterval
		if _, knownMissing := headers.missingCheckpoints[height]; knownMissing {
			continue
		}
		expected, _ := headers.checkpoints.at(index)
		actual, err := headers.chunkHashLocked(height, checkpointInterval)
		if err != nil {
			return err
		}
		if actual != expected {
			headers.missingCheckpoints[height] = struct{}{}
		}
	}
	return nil
}

func (headers *Headers) Connect(start int, serialized []byte) (int, error) {
	return headers.ConnectContext(context.Background(), start, serialized)
}

func (headers *Headers) ConnectContext(ctx context.Context, start int, serialized []byte) (int, error) {
	if headers == nil {
		return 0, errors.New("headers are nil")
	}
	if ctx == nil {
		return 0, errors.New("header connect context is nil")
	}
	if start < 0 {
		return 0, fmt.Errorf("%w: %d", ErrHeaderOutOfBounds, start)
	}
	if len(serialized)%HeaderSize != 0 {
		return 0, fmt.Errorf("%w: %d is not divisible by %d", ErrInvalidHeaderLength, len(serialized), HeaderSize)
	}
	if len(serialized) == 0 {
		return 0, nil
	}
	if err := headers.ensureValidationPredecessors(ctx, start); err != nil {
		return 0, err
	}
	headers.mu.Lock()
	defer headers.mu.Unlock()
	if !headers.opened {
		return 0, ErrHeadersNotOpen
	}
	if err := headers.validateChunkLocked(start, serialized); err != nil {
		// Headers.connect swallows InvalidHeader and rejects the entire practical
		// chunk because validate_chunk reports the chunk start as its height.
		var invalid *InvalidHeaderError
		if errors.As(err, &invalid) {
			return 0, nil
		}
		return 0, err
	}
	return headers.writeLocked(start, serialized)
}

func (headers *Headers) ValidateChunk(start int, serialized []byte) error {
	return headers.ValidateChunkContext(context.Background(), start, serialized)
}

func (headers *Headers) ValidateChunkContext(ctx context.Context, start int, serialized []byte) error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	if ctx == nil {
		return errors.New("header validation context is nil")
	}
	if start < 0 {
		return fmt.Errorf("%w: %d", ErrHeaderOutOfBounds, start)
	}
	if len(serialized)%HeaderSize != 0 {
		return fmt.Errorf("%w: %d is not divisible by %d", ErrInvalidHeaderLength, len(serialized), HeaderSize)
	}
	if err := headers.ensureValidationPredecessors(ctx, start); err != nil {
		return err
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	if !headers.opened {
		return ErrHeadersNotOpen
	}
	return headers.validateChunkLocked(start, serialized)
}

func (headers *Headers) ensureValidationPredecessors(ctx context.Context, start int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	headers.mu.RLock()
	hasGetter := headers.chunkGetter != nil
	headers.mu.RUnlock()
	if !hasGetter {
		return nil
	}
	if start > 0 {
		if err := headers.EnsureChunkAt(ctx, start-1); err != nil {
			return err
		}
	}
	if start > 1 {
		if err := headers.EnsureChunkAt(ctx, start-2); err != nil {
			return err
		}
	}
	return nil
}

func (headers *Headers) validateChunkLocked(start int, serialized []byte) error {
	var previousHash []byte
	var previous, previousPrevious *BlockHeader
	if start > 0 {
		raw, err := headers.readLocked(start - 1)
		if err != nil {
			return err
		}
		decoded, err := DeserializeHeader(start-1, raw)
		if err != nil {
			return err
		}
		previous = &decoded
		previousHash = HashHeader(raw)
	}
	if start > 1 {
		raw, err := headers.readLocked(start - 2)
		if err != nil {
			return err
		}
		decoded, err := DeserializeHeader(start-2, raw)
		if err != nil {
			return err
		}
		previousPrevious = &decoded
	}
	for index := 0; index < len(serialized)/HeaderSize; index++ {
		begin := index * HeaderSize
		raw := serialized[begin : begin+HeaderSize]
		current, err := DeserializeHeader(start+index, raw)
		if err != nil {
			return err
		}
		currentHash := HashHeader(raw)
		target := NextBlockTarget(headers.maxTarget, headers.targetTimespan, previousPrevious, previous)
		if err := headers.validateHeader(start, currentHash, &current, previousHash, target); err != nil {
			return err
		}
		previousPrevious = previous
		previous = &current
		previousHash = currentHash
	}
	return nil
}

func (headers *Headers) validateHeader(
	height int, currentHash []byte, header *BlockHeader, previousHash []byte, target *ArithUint256,
) error {
	if previousHash == nil {
		if headers.genesisHash != nil && !bytes.Equal(headers.genesisHash, currentHash) {
			return &InvalidHeaderError{Height: height, Message: fmt.Sprintf(
				"genesis header doesn't match: %s vs expected %s", currentHash, headers.genesisHash,
			)}
		}
		return nil
	}
	if !bytes.Equal(header.PreviousHash, previousHash) {
		return &InvalidHeaderError{Height: height, Message: fmt.Sprintf(
			"previous hash mismatch: %s vs expected %s", header.PreviousHash, previousHash,
		)}
	}
	if !headers.validateDifficulty {
		return nil
	}
	if header.Bits != target.Compact() {
		return &InvalidHeaderError{Height: height, Message: fmt.Sprintf(
			"bits mismatch: %d vs expected %d", header.Bits, target.Compact(),
		)}
	}
	proof, err := ProofOfWork(currentHash)
	if err != nil {
		return err
	}
	if proof.Compare(target) > 0 {
		return &InvalidHeaderError{Height: height, Message: fmt.Sprintf(
			"insufficient proof of work: %s vs target %s", proof.Value(), target.Value(),
		)}
	}
	return nil
}

func (headers *Headers) Repair(startHeight int) error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	headers.mu.Lock()
	defer headers.mu.Unlock()
	if !headers.opened {
		return ErrHeadersNotOpen
	}
	return headers.repairLocked(startHeight)
}

func (headers *Headers) repairLocked(startHeight int) error {
	if startHeight < 0 || startHeight >= headers.size-1 {
		return nil
	}
	var previousHash []byte
	for height := startHeight; height < headers.size; height++ {
		raw, err := headers.readLocked(height)
		if err != nil {
			return err
		}
		currentHash := HashHeader(raw)
		current, err := DeserializeHeader(height, raw)
		if err != nil {
			return headers.truncateForRepairLocked(height)
		}
		failed := false
		switch {
		case previousHash != nil:
			failed = !bytes.Equal(current.PreviousHash, previousHash)
		case height == 0:
			failed = headers.genesisHash != nil && !bytes.Equal(currentHash, headers.genesisHash)
		default:
			// Repair from the middle trusts the first requested header.
		}
		if failed {
			return headers.truncateForRepairLocked(height)
		}
		previousHash = currentHash
	}
	return nil
}

func (headers *Headers) truncateForRepairLocked(failedHeight int) error {
	newSize := max(0, failedHeight-1)
	if err := headers.storage.Resize(int64(newSize) * HeaderSize); err != nil {
		return err
	}
	headers.size = newSize
	return nil
}

func (headers *Headers) readLocked(height int) ([]byte, error) {
	if height < 0 || height >= headers.size {
		return nil, fmt.Errorf("%w: %d, current height: %d", ErrHeaderOutOfBounds, height, headers.size-1)
	}
	if headers.storage == nil {
		return nil, errors.New("header storage is nil")
	}
	start := int64(height) * HeaderSize
	raw := make([]byte, HeaderSize)
	read, err := headers.storage.ReadAt(raw, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if read != HeaderSize {
		return nil, fmt.Errorf("%w: read %d bytes at height %d", ErrInvalidHeaderLength, read, height)
	}
	return raw, nil
}

func (headers *Headers) writeLocked(height int, serialized []byte) (int, error) {
	if headers.storage == nil {
		return 0, errors.New("header storage is nil")
	}
	offset := int64(height) * HeaderSize
	if offset > headers.storage.Size() {
		if err := headers.storage.Resize(offset); err != nil {
			return 0, err
		}
	}
	written, err := headers.storage.WriteAt(serialized, offset)
	if err == nil && written != len(serialized) {
		err = io.ErrShortWrite
	}
	headers.size = max(headers.size, int(headers.storage.Size()/HeaderSize))
	return written / HeaderSize, err
}

func decodeReversedHeaderHash(value []byte) ([]byte, error) {
	decoded, err := hex.DecodeString(string(value))
	if err != nil {
		return nil, err
	}
	reverseBytes(decoded)
	return decoded, nil
}

func encodeReversedHeaderHash(value []byte) []byte {
	reversed := append([]byte(nil), value...)
	reverseBytes(reversed)
	encoded := make([]byte, hex.EncodedLen(len(reversed)))
	hex.Encode(encoded, reversed)
	return encoded
}

func reverseBytes(value []byte) {
	for left, right := 0, len(value)-1; left < right; left, right = left+1, right-1 {
		value[left], value[right] = value[right], value[left]
	}
}

func mustHeaderInteger(value string) *big.Int {
	parsed, ok := new(big.Int).SetString(value, 16)
	if !ok {
		panic("invalid header integer")
	}
	return parsed
}
