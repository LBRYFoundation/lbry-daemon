package rpc

import "crypto/sha256"
import "encoding/binary"
import "encoding/hex"
import "fmt"

import "golang.org/x/crypto/ripemd160"

// LBRY script opcodes.
const (
	opClaimName   = 0xb5
	opUpdateClaim = 0xb7
)

// TxOutput represents a parsed transaction output.
type TxOutput struct {
	Amount uint64
	Script []byte
}

// ClaimScript holds parsed data from an LBRY claim script.
type ClaimScript struct {
	Name      string
	ClaimID   []byte // 20 bytes, present for update claims
	ClaimData []byte // protobuf-encoded claim
	IsUpdate  bool
}

// readCompactSize reads a Bitcoin compact size integer.
func readCompactSize(data []byte, offset int) (uint64, int) {
	if offset >= len(data) {
		return 0, 0
	}
	first := data[offset]
	if first < 0xFD {
		return uint64(first), 1
	}
	switch first {
	case 0xFD:
		if offset+3 > len(data) {
			return 0, 0
		}
		return uint64(binary.LittleEndian.Uint16(data[offset+1 : offset+3])), 3
	case 0xFE:
		if offset+5 > len(data) {
			return 0, 0
		}
		return uint64(binary.LittleEndian.Uint32(data[offset+1 : offset+5])), 5
	default: // 0xFF
		if offset+9 > len(data) {
			return 0, 0
		}
		return binary.LittleEndian.Uint64(data[offset+1 : offset+9]), 9
	}
}

// readPushData reads a Bitcoin script push data operation.
func readPushData(script []byte, offset int) ([]byte, int) {
	if offset >= len(script) {
		return nil, 0
	}
	op := script[offset]

	if op >= 1 && op < 0x4c {
		length := int(op)
		end := offset + 1 + length
		if end > len(script) {
			return nil, 0
		}
		return script[offset+1 : end], 1 + length
	}

	switch op {
	case 0x4c: // OP_PUSHDATA1
		if offset+2 > len(script) {
			return nil, 0
		}
		length := int(script[offset+1])
		end := offset + 2 + length
		if end > len(script) {
			return nil, 0
		}
		return script[offset+2 : end], 2 + length

	case 0x4d: // OP_PUSHDATA2
		if offset+3 > len(script) {
			return nil, 0
		}
		length := int(binary.LittleEndian.Uint16(script[offset+1 : offset+3]))
		end := offset + 3 + length
		if end > len(script) {
			return nil, 0
		}
		return script[offset+3 : end], 3 + length

	case 0x4e: // OP_PUSHDATA4
		if offset+5 > len(script) {
			return nil, 0
		}
		length := int(binary.LittleEndian.Uint32(script[offset+1 : offset+5]))
		end := offset + 5 + length
		if end > len(script) {
			return nil, 0
		}
		return script[offset+5 : end], 5 + length
	}

	return nil, 0
}

// parseTxOutputs parses a raw transaction hex string and returns all outputs.
func parseTxOutputs(rawHex string) ([]TxOutput, error) {
	data, err := hex.DecodeString(rawHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if len(data) < 10 {
		return nil, fmt.Errorf("transaction too short")
	}

	offset := 4 // skip version

	// Detect segwit marker
	if offset+2 <= len(data) && data[offset] == 0x00 && data[offset+1] != 0x00 {
		offset += 2
	}

	// Skip inputs
	inCount, n := readCompactSize(data, offset)
	if n == 0 {
		return nil, fmt.Errorf("bad input count")
	}
	offset += n

	for i := uint64(0); i < inCount; i++ {
		offset += 32 + 4 // prev_hash + prev_index
		scriptLen, n := readCompactSize(data, offset)
		if n == 0 {
			return nil, fmt.Errorf("bad input script length")
		}
		offset += n + int(scriptLen) + 4 // script + sequence
		if offset > len(data) {
			return nil, fmt.Errorf("unexpected end in inputs")
		}
	}

	// Parse outputs
	outCount, n := readCompactSize(data, offset)
	if n == 0 {
		return nil, fmt.Errorf("bad output count")
	}
	offset += n

	outputs := make([]TxOutput, 0, outCount)
	for i := uint64(0); i < outCount; i++ {
		if offset+8 > len(data) {
			return nil, fmt.Errorf("unexpected end reading output amount")
		}
		amount := binary.LittleEndian.Uint64(data[offset : offset+8])
		offset += 8

		scriptLen, n := readCompactSize(data, offset)
		if n == 0 {
			return nil, fmt.Errorf("bad output script length")
		}
		offset += n

		end := offset + int(scriptLen)
		if end > len(data) {
			return nil, fmt.Errorf("unexpected end reading output script")
		}
		script := make([]byte, scriptLen)
		copy(script, data[offset:end])
		offset = end

		outputs = append(outputs, TxOutput{Amount: amount, Script: script})
	}

	return outputs, nil
}

// parseClaimScript parses an LBRY claim script and extracts the claim data.
func parseClaimScript(script []byte) (*ClaimScript, error) {
	if len(script) == 0 {
		return nil, fmt.Errorf("empty script")
	}

	switch script[0] {
	case opClaimName:
		// OP_CLAIM_NAME <name> <claim_data> OP_2DROP OP_DROP
		nameBytes, n := readPushData(script, 1)
		if n == 0 {
			return nil, fmt.Errorf("failed to read claim name")
		}
		claimData, n2 := readPushData(script, 1+n)
		if n2 == 0 {
			return nil, fmt.Errorf("failed to read claim data")
		}
		return &ClaimScript{
			Name:      string(nameBytes),
			ClaimData: claimData,
		}, nil

	case opUpdateClaim:
		// OP_UPDATE_CLAIM <name> <claim_id_20b> <claim_data> OP_2DROP OP_2DROP
		nameBytes, n := readPushData(script, 1)
		if n == 0 {
			return nil, fmt.Errorf("failed to read claim name")
		}
		claimIDBytes, n2 := readPushData(script, 1+n)
		if n2 == 0 {
			return nil, fmt.Errorf("failed to read claim id")
		}
		claimData, n3 := readPushData(script, 1+n+n2)
		if n3 == 0 {
			return nil, fmt.Errorf("failed to read claim data")
		}
		return &ClaimScript{
			Name:      string(nameBytes),
			ClaimID:   claimIDBytes,
			ClaimData: claimData,
			IsUpdate:  true,
		}, nil

	default:
		return nil, fmt.Errorf("not a claim script (opcode 0x%02x)", script[0])
	}
}

// hash160 computes RIPEMD160(SHA256(data)).
func hash160(data []byte) []byte {
	s := sha256.Sum256(data)
	r := ripemd160.New()
	r.Write(s[:])
	return r.Sum(nil)
}

// reverseBytes returns a reversed copy of b.
func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, j := 0, len(b)-1; j >= 0; i, j = i+1, j-1 {
		out[i] = b[j]
	}
	return out
}

// computeClaimID computes claim_id from tx_hash (internal byte order) and output index.
func ComputeClaimID(txHash []byte, nout uint32) []byte {
	buf := make([]byte, 36)
	copy(buf, txHash)
	binary.BigEndian.PutUint32(buf[32:], nout)
	return reverseBytes(hash160(buf))
}

// claimIDFromBytes converts raw claim_id bytes (from script) to display hex.
func claimIDFromBytes(b []byte) string {
	return hex.EncodeToString(reverseBytes(b))
}

// txHashToTxID converts internal tx_hash bytes to the display txid.
func txHashToTxID(txHash []byte) string {
	return hex.EncodeToString(reverseBytes(txHash))
}
