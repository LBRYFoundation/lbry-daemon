package wallet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
)

var ErrTransactionScriptGeneration = errors.New("wallet transaction script generation failed")

type transactionScriptBuilder struct {
	source []byte
}

func (builder *transactionScriptBuilder) opcodes(opcodes ...byte) {
	builder.source = append(builder.source, opcodes...)
}

func (builder *transactionScriptBuilder) push(data []byte) error {
	encoded, err := encodeTransactionPushData(data)
	if err != nil {
		return err
	}
	builder.source = append(builder.source, encoded...)
	return nil
}

func (builder *transactionScriptBuilder) integer(value *big.Int) error {
	encoded, err := encodeTransactionScriptInteger(value)
	if err != nil {
		return err
	}
	builder.source = append(builder.source, encoded...)
	return nil
}

func (builder *transactionScriptBuilder) smallInteger(value int) error {
	encoded, err := encodeTransactionSmallInteger(value)
	if err != nil {
		return err
	}
	builder.source = append(builder.source, encoded)
	return nil
}

func encodeTransactionPushData(data []byte) ([]byte, error) {
	size := uint64(len(data))
	encoded := make([]byte, 0, len(data)+5)
	switch {
	case size < transactionOpPushData1:
		encoded = append(encoded, byte(size))
	case size <= 0xff:
		encoded = append(encoded, transactionOpPushData1, byte(size))
	case size <= 0xffff:
		encoded = append(encoded, transactionOpPushData2, 0, 0)
		binary.LittleEndian.PutUint16(encoded[1:3], uint16(size))
	case size <= uint64(^uint32(0)):
		encoded = append(encoded, transactionOpPushData4, 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(encoded[1:5], uint32(size))
	default:
		return nil, fmt.Errorf("%w: pushed value is larger than uint32", ErrTransactionScriptGeneration)
	}
	return append(encoded, data...), nil
}

func encodeTransactionScriptInteger(value *big.Int) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: script integer is nil", ErrTransactionScriptGeneration)
	}
	// Python uses (bit_length + 8) // 8 signed bytes. This is deliberately
	// two's complement rather than Bitcoin's signed-magnitude ScriptNum.
	byteLength := value.BitLen()/8 + 1
	bigEndian := make([]byte, byteLength)
	if value.Sign() >= 0 {
		value.FillBytes(bigEndian)
	} else {
		modulus := new(big.Int).Lsh(big.NewInt(1), uint(byteLength*8))
		twosComplement := new(big.Int).Add(modulus, value)
		twosComplement.FillBytes(bigEndian)
	}
	for left, right := 0, len(bigEndian)-1; left < right; left, right = left+1, right-1 {
		bigEndian[left], bigEndian[right] = bigEndian[right], bigEndian[left]
	}
	return encodeTransactionPushData(bigEndian)
}

func encodeTransactionSmallInteger(value int) (byte, error) {
	if value < 1 || value > 16 {
		return 0, fmt.Errorf(
			"%w: small integer %d is outside 1..16", ErrTransactionScriptGeneration, value,
		)
	}
	return transactionOp1 + byte(value-1), nil
}

// Generate rebuilds an output script from its template fields. Generation is
// atomic: Source and derived flags change only after the template succeeds.
func (script *TransactionOutputScript) Generate() error {
	if script == nil {
		return fmt.Errorf("%w: output script is nil", ErrTransactionScriptGeneration)
	}
	builder := transactionScriptBuilder{source: make([]byte, 0)}
	hasPubKeyHash := false
	hasScriptHash := false
	hasSupportData := false
	var err error

	switch script.Template {
	case TransactionScriptNoScript:
		return fmt.Errorf(
			"%w: no_script output template has no generation opcodes",
			ErrTransactionScriptGeneration,
		)
	case TransactionScriptPayPubKeyFull:
		err = builder.push(script.PublicKey)
		builder.opcodes(transactionOpCheckSig)
	case TransactionScriptPayPubKeyHash:
		hasPubKeyHash = true
		err = generateTransactionPayPubKeyHash(&builder, script.PubKeyHash)
	case TransactionScriptPayScriptHash:
		hasScriptHash = true
		err = generateTransactionPayScriptHash(&builder, script.ScriptHash)
	case TransactionScriptPaySegWit:
		hasScriptHash = true
		builder.opcodes(transactionOp0)
		err = builder.push(script.ScriptHash)
	case TransactionScriptReturnData:
		builder.opcodes(transactionOpReturn)
		err = builder.push(script.Data)
	case TransactionScriptClaimPubKeyHash:
		hasPubKeyHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpClaimName, script)
		if err == nil {
			err = generateTransactionPayPubKeyHash(&builder, script.PubKeyHash)
		}
	case TransactionScriptClaimScriptHash:
		hasScriptHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpClaimName, script)
		if err == nil {
			err = generateTransactionPayScriptHash(&builder, script.ScriptHash)
		}
	case TransactionScriptSupportPubKey:
		hasPubKeyHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpSupportClaim, script)
		if err == nil {
			err = generateTransactionPayPubKeyHash(&builder, script.PubKeyHash)
		}
	case TransactionScriptSupportScript:
		hasScriptHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpSupportClaim, script)
		if err == nil {
			err = generateTransactionPayScriptHash(&builder, script.ScriptHash)
		}
	case TransactionScriptSupportDataKey:
		hasPubKeyHash, hasSupportData = true, true
		err = generateTransactionClaimPrefix(&builder, transactionOpSupportClaim, script)
		if err == nil {
			err = generateTransactionPayPubKeyHash(&builder, script.PubKeyHash)
		}
	case TransactionScriptSupportDataHash:
		hasScriptHash, hasSupportData = true, true
		err = generateTransactionClaimPrefix(&builder, transactionOpSupportClaim, script)
		if err == nil {
			err = generateTransactionPayScriptHash(&builder, script.ScriptHash)
		}
	case TransactionScriptUpdatePubKey:
		hasPubKeyHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpUpdateClaim, script)
		if err == nil {
			err = generateTransactionPayPubKeyHash(&builder, script.PubKeyHash)
		}
	case TransactionScriptUpdateScript:
		hasScriptHash = true
		err = generateTransactionClaimPrefix(&builder, transactionOpUpdateClaim, script)
		if err == nil {
			err = generateTransactionPayScriptHash(&builder, script.ScriptHash)
		}
	default:
		return fmt.Errorf(
			"%w: unknown output template %q", ErrTransactionScriptGeneration, script.Template,
		)
	}
	if err != nil {
		return err
	}
	script.Source = builder.source
	script.HasPubKeyHash = hasPubKeyHash
	script.HasScriptHash = hasScriptHash
	script.HasSupportData = hasSupportData
	script.Err = nil
	return nil
}

func generateTransactionPayPubKeyHash(builder *transactionScriptBuilder, hash []byte) error {
	builder.opcodes(transactionOpDup, transactionOpHash160)
	if err := builder.push(hash); err != nil {
		return err
	}
	builder.opcodes(transactionOpEqualVerify, transactionOpCheckSig)
	return nil
}

func generateTransactionPayScriptHash(builder *transactionScriptBuilder, hash []byte) error {
	builder.opcodes(transactionOpHash160)
	if err := builder.push(hash); err != nil {
		return err
	}
	builder.opcodes(transactionOpEqual)
	return nil
}

func generateTransactionClaimPrefix(
	builder *transactionScriptBuilder, opcode byte, script *TransactionOutputScript,
) error {
	builder.opcodes(opcode)
	if err := builder.push(script.ClaimName); err != nil {
		return err
	}
	switch opcode {
	case transactionOpClaimName:
		if err := builder.push(script.Claim); err != nil {
			return err
		}
		builder.opcodes(transactionOp2Drop, transactionOpDrop)
	case transactionOpSupportClaim:
		if err := builder.push(script.ClaimID); err != nil {
			return err
		}
		if script.Template == TransactionScriptSupportDataKey ||
			script.Template == TransactionScriptSupportDataHash {
			if err := builder.push(script.Support); err != nil {
				return err
			}
			builder.opcodes(transactionOp2Drop, transactionOp2Drop)
		} else {
			builder.opcodes(transactionOp2Drop, transactionOpDrop)
		}
	case transactionOpUpdateClaim:
		if err := builder.push(script.ClaimID); err != nil {
			return err
		}
		if err := builder.push(script.Claim); err != nil {
			return err
		}
		builder.opcodes(transactionOp2Drop, transactionOp2Drop)
	default:
		return fmt.Errorf("%w: unknown claim opcode %x", ErrTransactionScriptGeneration, opcode)
	}
	return nil
}

// Generate rebuilds an input script from its template fields. Nested scripts
// are embedded from Source without being regenerated, matching PUSH_SUBSCRIPT.
func (script *TransactionInputScript) Generate() error {
	if script == nil {
		return fmt.Errorf("%w: input script is nil", ErrTransactionScriptGeneration)
	}
	builder := transactionScriptBuilder{source: make([]byte, 0)}
	var err error
	switch script.Template {
	case TransactionInputNoScript:
		return fmt.Errorf(
			"%w: no_script input template has no generation opcodes",
			ErrTransactionScriptGeneration,
		)
	case TransactionInputPubKey:
		err = builder.push(script.Signature)
	case TransactionInputPubKeyHash:
		if err = builder.push(script.Signature); err == nil {
			err = builder.push(script.PublicKey)
		}
	case TransactionInputScriptHashTime:
		if script.Script == nil {
			return fmt.Errorf("%w: timelock subscript is nil", ErrTransactionScriptGeneration)
		}
		if err = builder.push(script.Signature); err == nil {
			err = builder.push(script.PublicKey)
		}
		if err == nil {
			err = builder.push(script.Script.Source)
		}
	case TransactionInputScriptHashMulti:
		if script.Script == nil {
			return fmt.Errorf("%w: multisig subscript is nil", ErrTransactionScriptGeneration)
		}
		builder.opcodes(transactionOp0)
		for _, signature := range script.Signatures {
			if err = builder.push(signature); err != nil {
				break
			}
		}
		if err == nil {
			err = builder.push(script.Script.Source)
		}
	default:
		return fmt.Errorf(
			"%w: unknown input template %q", ErrTransactionScriptGeneration, script.Template,
		)
	}
	if err != nil {
		return err
	}
	script.Source = builder.source
	script.Err = nil
	return nil
}

// Generate rebuilds a nested timelock or multisig redeem script.
func (script *TransactionInputSubscript) Generate() error {
	if script == nil {
		return fmt.Errorf("%w: input subscript is nil", ErrTransactionScriptGeneration)
	}
	builder := transactionScriptBuilder{source: make([]byte, 0)}
	var err error
	switch script.Template {
	case TransactionInputTimeLock:
		if err = builder.integer(script.Height); err == nil {
			builder.opcodes(
				transactionOpCheckLockTimeVerify, transactionOpDrop,
				transactionOpDup, transactionOpHash160,
			)
			err = builder.push(script.PubKeyHash)
		}
		if err == nil {
			builder.opcodes(transactionOpEqualVerify, transactionOpCheckSig)
		}
	case TransactionInputMultiSig:
		if err = builder.smallInteger(int(script.SignaturesCount)); err == nil {
			for _, publicKey := range script.PublicKeys {
				if err = builder.push(publicKey); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = builder.smallInteger(int(script.PublicKeysCount))
		}
		if err == nil {
			builder.opcodes(transactionOpCheckMultiSig)
		}
	default:
		return fmt.Errorf(
			"%w: unknown input subscript template %q", ErrTransactionScriptGeneration, script.Template,
		)
	}
	if err != nil {
		return err
	}
	script.Source = builder.source
	return nil
}

func NewRedeemPubKeyInputScript(signature []byte) (TransactionInputScript, error) {
	return newGeneratedTransactionInputScript(TransactionInputScript{
		Template:  TransactionInputPubKey,
		Signature: cloneTransactionScriptBytes(signature),
	})
}

func NewRedeemPubKeyHashInputScript(signature, publicKey []byte) (TransactionInputScript, error) {
	return newGeneratedTransactionInputScript(TransactionInputScript{
		Template:  TransactionInputPubKeyHash,
		Signature: cloneTransactionScriptBytes(signature),
		PublicKey: cloneTransactionScriptBytes(publicKey),
	})
}

func NewRedeemMultiSigScriptHashInputScript(
	signatures, publicKeys [][]byte,
) (TransactionInputScript, error) {
	if _, err := encodeTransactionSmallInteger(len(signatures)); err != nil {
		return TransactionInputScript{}, err
	}
	if _, err := encodeTransactionSmallInteger(len(publicKeys)); err != nil {
		return TransactionInputScript{}, err
	}
	subscript, err := NewMultiSigInputSubscript(
		uint8(len(signatures)), publicKeys, uint8(len(publicKeys)),
	)
	if err != nil {
		return TransactionInputScript{}, err
	}
	return newGeneratedTransactionInputScript(TransactionInputScript{
		Template:   TransactionInputScriptHashMulti,
		Signatures: cloneTransactionScriptByteSlices(signatures),
		Script:     &subscript,
	})
}

func NewRedeemTimeLockScriptHashInputScript(
	signature, publicKey []byte, height *big.Int, pubKeyHash, scriptSource []byte,
) (TransactionInputScript, error) {
	var subscript *TransactionInputSubscript
	if height != nil && height.Sign() != 0 && len(pubKeyHash) > 0 {
		generated, err := NewTimeLockInputSubscript(height, pubKeyHash)
		if err != nil {
			return TransactionInputScript{}, err
		}
		subscript = &generated
	} else if len(scriptSource) > 0 {
		parsed, ok := parseTimeLockSubscript(scriptSource)
		if !ok {
			return TransactionInputScript{}, fmt.Errorf(
				"%w: invalid timelock script source", ErrTransactionScriptGeneration,
			)
		}
		subscript = parsed
	} else {
		return TransactionInputScript{}, fmt.Errorf(
			"%w: script source or both height and pubkey hash are required",
			ErrTransactionScriptGeneration,
		)
	}
	return newGeneratedTransactionInputScript(TransactionInputScript{
		Template:  TransactionInputScriptHashTime,
		Signature: cloneTransactionScriptBytes(signature),
		PublicKey: cloneTransactionScriptBytes(publicKey),
		Script:    subscript,
	})
}

func NewTimeLockInputSubscript(
	height *big.Int, pubKeyHash []byte,
) (TransactionInputSubscript, error) {
	var clonedHeight *big.Int
	if height != nil {
		clonedHeight = new(big.Int).Set(height)
	}
	script := TransactionInputSubscript{
		Template:   TransactionInputTimeLock,
		Height:     clonedHeight,
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	}
	if err := script.Generate(); err != nil {
		return TransactionInputSubscript{}, err
	}
	return script, nil
}

func NewMultiSigInputSubscript(
	signaturesCount uint8, publicKeys [][]byte, publicKeysCount uint8,
) (TransactionInputSubscript, error) {
	script := TransactionInputSubscript{
		Template:        TransactionInputMultiSig,
		SignaturesCount: signaturesCount,
		PublicKeys:      cloneTransactionScriptByteSlices(publicKeys),
		PublicKeysCount: publicKeysCount,
	}
	if err := script.Generate(); err != nil {
		return TransactionInputSubscript{}, err
	}
	return script, nil
}

func newGeneratedTransactionInputScript(
	script TransactionInputScript,
) (TransactionInputScript, error) {
	if err := script.Generate(); err != nil {
		return TransactionInputScript{}, err
	}
	return script, nil
}

func NewPayPubKeyFullOutputScript(publicKey []byte) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:  TransactionScriptPayPubKeyFull,
		PublicKey: cloneTransactionScriptBytes(publicKey),
	})
}

func NewPayPubKeyHashOutputScript(pubKeyHash []byte) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptPayPubKeyHash,
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	})
}

func NewPayScriptHashOutputScript(scriptHash []byte) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptPayScriptHash,
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func NewSegWitOutputScript(scriptHash []byte) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptPaySegWit,
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func NewReturnDataOutputScript(data []byte) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template: TransactionScriptReturnData,
		Data:     cloneTransactionScriptBytes(data),
	})
}

func NewClaimNamePubKeyHashOutputScript(
	claimName, claim, pubKeyHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptClaimPubKeyHash,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		Claim:      cloneTransactionScriptBytes(claim),
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	})
}

func NewClaimNameScriptHashOutputScript(
	claimName, claim, scriptHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptClaimScriptHash,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		Claim:      cloneTransactionScriptBytes(claim),
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func NewUpdateClaimPubKeyHashOutputScript(
	claimName, claimID, claim, pubKeyHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptUpdatePubKey,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		Claim:      cloneTransactionScriptBytes(claim),
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	})
}

func NewUpdateClaimScriptHashOutputScript(
	claimName, claimID, claim, scriptHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptUpdateScript,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		Claim:      cloneTransactionScriptBytes(claim),
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func NewSupportPubKeyHashOutputScript(
	claimName, claimID, pubKeyHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptSupportPubKey,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	})
}

func NewSupportScriptHashOutputScript(
	claimName, claimID, scriptHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptSupportScript,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func NewSupportDataPubKeyHashOutputScript(
	claimName, claimID, support, pubKeyHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptSupportDataKey,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		Support:    cloneTransactionScriptBytes(support),
		PubKeyHash: cloneTransactionScriptBytes(pubKeyHash),
	})
}

func NewSupportDataScriptHashOutputScript(
	claimName, claimID, support, scriptHash []byte,
) (TransactionOutputScript, error) {
	return newGeneratedTransactionOutputScript(TransactionOutputScript{
		Template:   TransactionScriptSupportDataHash,
		ClaimName:  cloneTransactionScriptBytes(claimName),
		ClaimID:    cloneTransactionScriptBytes(claimID),
		Support:    cloneTransactionScriptBytes(support),
		ScriptHash: cloneTransactionScriptBytes(scriptHash),
	})
}

func newGeneratedTransactionOutputScript(
	script TransactionOutputScript,
) (TransactionOutputScript, error) {
	if err := script.Generate(); err != nil {
		return TransactionOutputScript{}, err
	}
	return script, nil
}

func cloneTransactionScriptByteSlices(values [][]byte) [][]byte {
	cloned := make([][]byte, len(values))
	for index, value := range values {
		cloned[index] = cloneTransactionScriptBytes(value)
	}
	return cloned
}
