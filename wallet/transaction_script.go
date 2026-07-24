package wallet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
)

const (
	transactionOp0                   = 0x00
	transactionOpPushData1           = 0x4c
	transactionOpPushData2           = 0x4d
	transactionOpPushData4           = 0x4e
	transactionOp1                   = 0x51
	transactionOp16                  = 0x60
	transactionOpReturn              = 0x6a
	transactionOp2Drop               = 0x6d
	transactionOpDrop                = 0x75
	transactionOpDup                 = 0x76
	transactionOpEqual               = 0x87
	transactionOpEqualVerify         = 0x88
	transactionOpHash160             = 0xa9
	transactionOpCheckSig            = 0xac
	transactionOpCheckMultiSig       = 0xae
	transactionOpCheckLockTimeVerify = 0xb1
	transactionOpClaimName           = 0xb5
	transactionOpSupportClaim        = 0xb6
	transactionOpUpdateClaim         = 0xb7
	TransactionScriptNoScript        = "no_script"
	TransactionScriptPayPubKeyFull   = "pay_pubkey_full"
	TransactionScriptPayPubKeyHash   = "pay_pubkey_hash"
	TransactionScriptPayScriptHash   = "pay_script_hash"
	TransactionScriptPaySegWit       = "pay_script_hash+segwit"
	TransactionScriptReturnData      = "return_data"
	TransactionScriptClaimPubKeyHash = "claim_name+pay_pubkey_hash"
	TransactionScriptClaimScriptHash = "claim_name+pay_script_hash"
	TransactionScriptSupportPubKey   = "support_claim+pay_pubkey_hash"
	TransactionScriptSupportScript   = "support_claim+pay_script_hash"
	TransactionScriptSupportDataKey  = "support_claim+data+pay_pubkey_hash"
	TransactionScriptSupportDataHash = "support_claim+data+pay_script_hash"
	TransactionScriptUpdatePubKey    = "update_claim+pay_pubkey_hash"
	TransactionScriptUpdateScript    = "update_claim+pay_script_hash"
	TransactionInputNoScript         = "no_script"
	TransactionInputPubKey           = "pubkey"
	TransactionInputPubKeyHash       = "pubkey_hash"
	TransactionInputScriptHashTime   = "script_hash+timelock"
	TransactionInputScriptHashMulti  = "script_hash+multi_sig"
	TransactionInputTimeLock         = "timelock"
	TransactionInputMultiSig         = "multi_sig"
)

var ErrInvalidTransactionScript = errors.New("invalid wallet transaction script")

type TransactionOutputScript struct {
	Source         []byte
	Template       string
	PublicKey      []byte
	PubKeyHash     []byte
	ScriptHash     []byte
	Data           []byte
	ClaimName      []byte
	ClaimID        []byte
	Claim          []byte
	Support        []byte
	HasPubKeyHash  bool
	HasScriptHash  bool
	HasSupportData bool
	Err            error
}

func (script TransactionOutputScript) IsPayPubKeyHash() bool {
	return script.Err == nil && len(script.Template) >= len(TransactionScriptPayPubKeyHash) &&
		script.Template[len(script.Template)-len(TransactionScriptPayPubKeyHash):] ==
			TransactionScriptPayPubKeyHash
}

func (script TransactionOutputScript) IsPayScriptHash() bool {
	return script.Err == nil && len(script.Template) >= len(TransactionScriptPayScriptHash) &&
		script.Template[len(script.Template)-len(TransactionScriptPayScriptHash):] ==
			TransactionScriptPayScriptHash
}

func (script TransactionOutputScript) IsClaimName() bool {
	return script.Template == TransactionScriptClaimPubKeyHash ||
		script.Template == TransactionScriptClaimScriptHash
}

func (script TransactionOutputScript) IsUpdateClaim() bool {
	return script.Template == TransactionScriptUpdatePubKey ||
		script.Template == TransactionScriptUpdateScript
}

func (script TransactionOutputScript) IsSupportClaim() bool {
	return script.Template == TransactionScriptSupportPubKey ||
		script.Template == TransactionScriptSupportScript ||
		script.Template == TransactionScriptSupportDataKey ||
		script.Template == TransactionScriptSupportDataHash
}

func (script TransactionOutputScript) IsSupportData() bool {
	return script.Template == TransactionScriptSupportDataKey ||
		script.Template == TransactionScriptSupportDataHash
}

func (script TransactionOutputScript) IsClaimInvolved() bool {
	return script.IsClaimName() || script.IsUpdateClaim() || script.IsSupportClaim()
}

func (script TransactionOutputScript) HasAddress() bool {
	return script.Err == nil && (script.HasPubKeyHash || script.HasScriptHash)
}

type TransactionInputScript struct {
	Source     []byte
	Template   string
	Signature  []byte
	PublicKey  []byte
	Signatures [][]byte
	Script     *TransactionInputSubscript
	Err        error
}

type TransactionInputSubscript struct {
	Source          []byte
	Template        string
	Height          *big.Int
	PubKeyHash      []byte
	SignaturesCount uint8
	PublicKeys      [][]byte
	PublicKeysCount uint8
}

type transactionScriptToken struct {
	opcode byte
	data   []byte
	isData bool
}

func ParseTransactionOutputScript(source []byte) TransactionOutputScript {
	script := TransactionOutputScript{Source: cloneTransactionScriptBytes(source)}
	tokens, err := tokenizeTransactionScript(source)
	if err != nil {
		script.Err = err
		return script
	}
	if len(tokens) == 0 {
		script.Template = TransactionScriptNoScript
		return script
	}
	if data, ok := matchPayPubKeyFull(tokens); ok {
		script.Template, script.PublicKey = TransactionScriptPayPubKeyFull, data
		return script
	}
	if hash, ok := matchPayPubKeyHash(tokens); ok {
		script.Template, script.PubKeyHash, script.HasPubKeyHash =
			TransactionScriptPayPubKeyHash, hash, true
		return script
	}
	if hash, ok := matchPayScriptHash(tokens); ok {
		script.Template, script.ScriptHash, script.HasScriptHash =
			TransactionScriptPayScriptHash, hash, true
		return script
	}
	if len(tokens) == 2 && tokenOpcode(tokens[0], transactionOp0) {
		if data, ok := tokenData(tokens[1]); ok {
			script.Template, script.ScriptHash, script.HasScriptHash =
				TransactionScriptPaySegWit, data, true
			return script
		}
	}
	if len(tokens) == 2 && tokenOpcode(tokens[0], transactionOpReturn) {
		if data, ok := tokenData(tokens[1]); ok {
			script.Template, script.Data = TransactionScriptReturnData, data
			return script
		}
	}
	claimScript := TransactionOutputScript{Source: script.Source}
	if parseClaimTransactionScript(tokens, &claimScript) {
		return claimScript
	}
	script.Err = fmt.Errorf("%w: no matching output template", ErrInvalidTransactionScript)
	return script
}

func ParseTransactionInputScript(source []byte) TransactionInputScript {
	script := TransactionInputScript{Source: cloneTransactionScriptBytes(source)}
	tokens, err := tokenizeTransactionScript(source)
	if err != nil {
		script.Err = err
		return script
	}
	if len(tokens) == 0 {
		script.Template = TransactionInputNoScript
		return script
	}
	if len(tokens) == 1 {
		if signature, ok := tokenData(tokens[0]); ok {
			script.Template, script.Signature = TransactionInputPubKey, signature
			return script
		}
	}
	if len(tokens) == 2 {
		signature, signatureOK := tokenData(tokens[0])
		publicKey, publicKeyOK := tokenData(tokens[1])
		if signatureOK && publicKeyOK {
			script.Template = TransactionInputPubKeyHash
			script.Signature, script.PublicKey = signature, publicKey
			return script
		}
	}
	if len(tokens) == 3 {
		signature, signatureOK := tokenData(tokens[0])
		publicKey, publicKeyOK := tokenData(tokens[1])
		subscriptSource, subscriptOK := strictTokenData(tokens[2])
		if signatureOK && publicKeyOK && subscriptOK {
			if subscript, ok := parseTimeLockSubscript(subscriptSource); ok {
				script.Template = TransactionInputScriptHashTime
				script.Signature, script.PublicKey, script.Script = signature, publicKey, subscript
				return script
			}
		}
	}
	if len(tokens) >= 3 && tokenOpcode(tokens[0], transactionOp0) {
		subscriptSource, subscriptOK := strictTokenData(tokens[len(tokens)-1])
		if subscriptOK {
			signatures := make([][]byte, 0, len(tokens)-2)
			for _, token := range tokens[1 : len(tokens)-1] {
				signature, ok := strictTokenData(token)
				if !ok {
					signatures = nil
					break
				}
				signatures = append(signatures, signature)
			}
			if signatures != nil {
				if subscript, ok := parseMultiSigSubscript(subscriptSource); ok {
					script.Template = TransactionInputScriptHashMulti
					script.Signatures, script.Script = signatures, subscript
					return script
				}
			}
		}
	}
	script.Err = fmt.Errorf("%w: no matching input template", ErrInvalidTransactionScript)
	return script
}

func parseTimeLockSubscript(source []byte) (*TransactionInputSubscript, bool) {
	tokens, err := tokenizeTransactionScript(source)
	if err != nil || len(tokens) != 8 ||
		!tokenOpcode(tokens[1], transactionOpCheckLockTimeVerify) ||
		!tokenOpcode(tokens[2], transactionOpDrop) ||
		!tokenOpcode(tokens[3], transactionOpDup) ||
		!tokenOpcode(tokens[4], transactionOpHash160) ||
		!tokenOpcode(tokens[6], transactionOpEqualVerify) ||
		!tokenOpcode(tokens[7], transactionOpCheckSig) {
		return nil, false
	}
	heightSource, heightOK := strictTokenData(tokens[0])
	pubKeyHash, pubKeyHashOK := tokenData(tokens[5])
	if !heightOK || !pubKeyHashOK {
		return nil, false
	}
	return &TransactionInputSubscript{
		Source:     cloneTransactionScriptBytes(source),
		Template:   TransactionInputTimeLock,
		Height:     littleEndianTransactionInteger(heightSource),
		PubKeyHash: pubKeyHash,
	}, true
}

func parseMultiSigSubscript(source []byte) (*TransactionInputSubscript, bool) {
	tokens, err := tokenizeTransactionScript(source)
	if err != nil || len(tokens) < 4 ||
		!tokenOpcode(tokens[len(tokens)-1], transactionOpCheckMultiSig) {
		return nil, false
	}
	signaturesCount, signaturesCountOK := tokenSmallInteger(tokens[0])
	publicKeysCount, publicKeysCountOK := tokenSmallInteger(tokens[len(tokens)-2])
	if !signaturesCountOK || !publicKeysCountOK {
		return nil, false
	}
	publicKeys := make([][]byte, 0, len(tokens)-3)
	for _, token := range tokens[1 : len(tokens)-2] {
		publicKey, ok := strictTokenData(token)
		if !ok {
			return nil, false
		}
		publicKeys = append(publicKeys, publicKey)
	}
	if len(publicKeys) == 0 {
		return nil, false
	}
	return &TransactionInputSubscript{
		Source:          cloneTransactionScriptBytes(source),
		Template:        TransactionInputMultiSig,
		SignaturesCount: signaturesCount,
		PublicKeys:      publicKeys,
		PublicKeysCount: publicKeysCount,
	}, true
}

func littleEndianTransactionInteger(source []byte) *big.Int {
	reversed := make([]byte, len(source))
	for index := range source {
		reversed[len(source)-1-index] = source[index]
	}
	return new(big.Int).SetBytes(reversed)
}

func parseClaimTransactionScript(
	tokens []transactionScriptToken, script *TransactionOutputScript,
) bool {
	if len(tokens) < 8 || tokens[0].isData {
		return false
	}
	claimName, ok := tokenData(tokens[1])
	if !ok {
		return false
	}
	prefixLength := 0
	switch tokens[0].opcode {
	case transactionOpClaimName:
		if len(tokens) < 5 || !tokenOpcode(tokens[3], transactionOp2Drop) ||
			!tokenOpcode(tokens[4], transactionOpDrop) {
			return false
		}
		claim, claimOK := tokenData(tokens[2])
		if !claimOK {
			return false
		}
		script.ClaimName, script.Claim = claimName, claim
		prefixLength = 5
	case transactionOpSupportClaim:
		claimID, claimIDOK := tokenData(tokens[2])
		if !claimIDOK {
			return false
		}
		script.ClaimName, script.ClaimID = claimName, claimID
		if len(tokens) >= 6 && tokenOpcode(tokens[3], transactionOp2Drop) &&
			tokenOpcode(tokens[4], transactionOpDrop) {
			prefixLength = 5
		} else if len(tokens) >= 7 && tokenOpcode(tokens[4], transactionOp2Drop) &&
			tokenOpcode(tokens[5], transactionOp2Drop) {
			support, supportOK := tokenData(tokens[3])
			if !supportOK {
				return false
			}
			script.Support = support
			script.HasSupportData = true
			prefixLength = 6
		} else {
			return false
		}
	case transactionOpUpdateClaim:
		if len(tokens) < 6 || !tokenOpcode(tokens[4], transactionOp2Drop) ||
			!tokenOpcode(tokens[5], transactionOp2Drop) {
			return false
		}
		claimID, claimIDOK := tokenData(tokens[2])
		claim, claimOK := tokenData(tokens[3])
		if !claimIDOK || !claimOK {
			return false
		}
		script.ClaimName, script.ClaimID, script.Claim = claimName, claimID, claim
		prefixLength = 6
	default:
		return false
	}

	suffix := tokens[prefixLength:]
	if hash, ok := matchPayPubKeyHash(suffix); ok {
		script.PubKeyHash = hash
		script.HasPubKeyHash = true
		switch tokens[0].opcode {
		case transactionOpClaimName:
			script.Template = TransactionScriptClaimPubKeyHash
		case transactionOpSupportClaim:
			if !script.HasSupportData {
				script.Template = TransactionScriptSupportPubKey
			} else {
				script.Template = TransactionScriptSupportDataKey
			}
		case transactionOpUpdateClaim:
			script.Template = TransactionScriptUpdatePubKey
		}
		return true
	}
	if hash, ok := matchPayScriptHash(suffix); ok {
		script.ScriptHash = hash
		script.HasScriptHash = true
		switch tokens[0].opcode {
		case transactionOpClaimName:
			script.Template = TransactionScriptClaimScriptHash
		case transactionOpSupportClaim:
			if !script.HasSupportData {
				script.Template = TransactionScriptSupportScript
			} else {
				script.Template = TransactionScriptSupportDataHash
			}
		case transactionOpUpdateClaim:
			script.Template = TransactionScriptUpdateScript
		}
		return true
	}
	return false
}

func matchPayPubKeyFull(tokens []transactionScriptToken) ([]byte, bool) {
	if len(tokens) != 2 || !tokenOpcode(tokens[1], transactionOpCheckSig) {
		return nil, false
	}
	return tokenData(tokens[0])
}

func matchPayPubKeyHash(tokens []transactionScriptToken) ([]byte, bool) {
	if len(tokens) != 5 || !tokenOpcode(tokens[0], transactionOpDup) ||
		!tokenOpcode(tokens[1], transactionOpHash160) ||
		!tokenOpcode(tokens[3], transactionOpEqualVerify) ||
		!tokenOpcode(tokens[4], transactionOpCheckSig) {
		return nil, false
	}
	return tokenData(tokens[2])
}

func matchPayScriptHash(tokens []transactionScriptToken) ([]byte, bool) {
	if len(tokens) != 3 || !tokenOpcode(tokens[0], transactionOpHash160) ||
		!tokenOpcode(tokens[2], transactionOpEqual) {
		return nil, false
	}
	return tokenData(tokens[1])
}

func tokenOpcode(token transactionScriptToken, opcode byte) bool {
	return !token.isData && token.opcode == opcode
}

func tokenData(token transactionScriptToken) ([]byte, bool) {
	if token.isData {
		return cloneTransactionScriptBytes(token.data), true
	}
	// Parser.parse treats OP_0 as an empty DataToken whenever a PUSH_SINGLE
	// occupies the same template position.
	if token.opcode == transactionOp0 {
		return []byte{}, true
	}
	return nil, false
}

func strictTokenData(token transactionScriptToken) ([]byte, bool) {
	if !token.isData {
		return nil, false
	}
	return cloneTransactionScriptBytes(token.data), true
}

func tokenSmallInteger(token transactionScriptToken) (uint8, bool) {
	if token.isData || token.opcode < transactionOp1 || token.opcode > transactionOp16 {
		return 0, false
	}
	return token.opcode - transactionOp1 + 1, true
}

func tokenizeTransactionScript(source []byte) ([]transactionScriptToken, error) {
	tokens := make([]transactionScriptToken, 0)
	for offset := 0; offset < len(source); {
		opcode := source[offset]
		offset++
		if opcode < 1 || opcode > transactionOpPushData4 {
			tokens = append(tokens, transactionScriptToken{opcode: opcode})
			continue
		}
		var length uint64
		switch opcode {
		case transactionOpPushData1:
			if offset >= len(source) {
				length = uint64(len(source) - offset)
				break
			}
			length = uint64(source[offset])
			offset++
		case transactionOpPushData2:
			if len(source)-offset == 0 {
				length = 0
				break
			}
			if len(source)-offset < 2 {
				return nil, fmt.Errorf("%w: truncated PUSHDATA2", ErrInvalidTransactionScript)
			}
			length = uint64(binary.LittleEndian.Uint16(source[offset : offset+2]))
			offset += 2
		case transactionOpPushData4:
			if len(source)-offset == 0 {
				length = 0
				break
			}
			if len(source)-offset < 4 {
				return nil, fmt.Errorf("%w: truncated PUSHDATA4", ErrInvalidTransactionScript)
			}
			length = uint64(binary.LittleEndian.Uint32(source[offset : offset+4]))
			offset += 4
		default:
			length = uint64(opcode)
		}
		if length > uint64(len(source)-offset) {
			length = uint64(len(source) - offset)
		}
		end := offset + int(length)
		tokens = append(tokens, transactionScriptToken{
			data: cloneTransactionScriptBytes(source[offset:end]), isData: true,
		})
		offset = end
	}
	return tokens, nil
}

func cloneTransactionScriptBytes(value []byte) []byte {
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}
