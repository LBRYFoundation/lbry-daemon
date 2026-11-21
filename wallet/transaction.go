package wallet

import "encoding/binary"
import "encoding/hex"
import "errors"

// Script opcodes of LBRY claim outputs (lbry-sdk lbry/wallet/script.py).
const (
	opClaimName    = 0xb5
	opSupportClaim = 0xb6
	opUpdateClaim  = 0xb7
)

type outPoint struct {
	txid  string // hex, as the wallet server writes it
	index uint32
}

type txOutput struct {
	amount int64
	script []byte
}

type transaction struct {
	inputs  []outPoint
	outputs []txOutput
}

// kind of an unspent output, by its script: lbry-sdk's txo_type is a claim, a support or something else.
func (output txOutput) isClaim() bool {
	return len(output.script) > 0 && (output.script[0] == opClaimName || output.script[0] == opUpdateClaim)
}

func (output txOutput) isSupport() bool {
	return len(output.script) > 0 && output.script[0] == opSupportClaim
}

// hash160 is the public key hash a pay-to-public-key-hash script pays to, also when the script starts with a
// claim, update or support operation, or nil for any other script.
func (output txOutput) hash160() []byte {
	tail := output.script
	if len(tail) < 25 {
		return nil
	}
	tail = tail[len(tail)-25:]
	if tail[0] != 0x76 || tail[1] != 0xa9 || tail[2] != 0x14 || tail[23] != 0x88 || tail[24] != 0xac {
		return nil
	}
	return tail[3:23]
}

var errBadTransaction = errors.New("malformed transaction")

type reader struct {
	data []byte
	pos  int
	bad  bool
}

func (r *reader) take(count uint64) []byte {
	if r.bad || count > uint64(len(r.data)-r.pos) {
		r.bad = true
		return nil
	}
	out := r.data[r.pos : r.pos+int(count)]
	r.pos += int(count)
	return out
}

func (r *reader) compactSize() uint64 {
	first := r.take(1)
	if first == nil {
		return 0
	}
	switch first[0] {
	case 0xfd:
		if b := r.take(2); b != nil {
			return uint64(binary.LittleEndian.Uint16(b))
		}
	case 0xfe:
		if b := r.take(4); b != nil {
			return uint64(binary.LittleEndian.Uint32(b))
		}
	case 0xff:
		if b := r.take(8); b != nil {
			return binary.LittleEndian.Uint64(b)
		}
	default:
		return uint64(first[0])
	}
	return 0
}

// parseTransaction reads the inputs (as the outputs they spend) and the outputs of a raw transaction in hex.
func parseTransaction(rawHex string) (*transaction, error) {
	data, err := hex.DecodeString(rawHex)
	if err != nil {
		return nil, errBadTransaction
	}
	r := &reader{data: data}
	r.take(4) // version
	tx := &transaction{}
	inputCount := r.compactSize()
	for i := uint64(0); i < inputCount && !r.bad; i++ {
		hash := r.take(32)
		index := r.take(4)
		r.take(r.compactSize()) // signature script
		r.take(4)               // sequence
		if r.bad {
			break
		}
		reversed := make([]byte, 32)
		for j := range hash {
			reversed[31-j] = hash[j]
		}
		tx.inputs = append(tx.inputs, outPoint{txid: hex.EncodeToString(reversed), index: binary.LittleEndian.Uint32(index)})
	}
	outputCount := r.compactSize()
	for i := uint64(0); i < outputCount && !r.bad; i++ {
		amount := r.take(8)
		script := r.take(r.compactSize())
		if r.bad {
			break
		}
		tx.outputs = append(tx.outputs, txOutput{amount: int64(binary.LittleEndian.Uint64(amount)), script: script})
	}
	if r.bad || r.take(4) == nil { // lock time
		return nil, errBadTransaction
	}
	return tx, nil
}
