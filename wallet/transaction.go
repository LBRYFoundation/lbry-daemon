package wallet

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"lbry/daemon/wallet/keys"
)

var (
	ErrInvalidWalletTransaction = errors.New("invalid wallet transaction")
	ErrTransactionHasNoAddress  = errors.New("transaction output has no address")
	ErrTransactionHasNoClaimID  = errors.New("transaction output has no claim ID")
)

type Transaction struct {
	Raw           []byte
	RawSansSegWit []byte
	Hash          [sha256.Size]byte
	ID            string
	Version       uint32
	LockTime      uint32
	SegWitFlag    byte
	Witnesses     [][]byte
	Inputs        []TransactionInput
	Outputs       []TransactionOutput
	Trailing      []byte
	Height        int64
	HeightMissing bool
	Position      int64
	IsVerified    bool
	JulianDay     *float64
}

type TransactionInput struct {
	Position       uint32
	PreviousHash   [sha256.Size]byte
	PreviousTxID   string
	PreviousIndex  uint32
	Sequence       uint32
	Coinbase       []byte
	Script         TransactionInputScript
	ResolvedOutput *TransactionOutput
	owner          *Transaction
}

func (input TransactionInput) IsCoinbase() bool {
	return input.PreviousHash == [sha256.Size]byte{}
}

func (input TransactionInput) PreviousOutputID() string {
	return fmt.Sprintf("%s:%d", input.PreviousTxID, input.PreviousIndex)
}

// IsMyInput mirrors Input.is_my_input: an unresolved outpoint is false, while
// a resolved output inherits its nullable wallet-ownership annotation.
func (input TransactionInput) IsMyInput() *bool {
	if input.ResolvedOutput == nil {
		value := false
		return &value
	}
	return currentTransactionOutput(input.ResolvedOutput).IsMyOutput
}

// TransactionID returns the mutable parent transaction ID associated with this
// input, equivalent to input.tx_ref.id in the SDK.
func (input TransactionInput) TransactionID() string {
	if input.owner == nil {
		return ""
	}
	if input.owner.ID == "" {
		_ = input.owner.RebuildDerived()
	}
	return input.owner.ID
}

type TransactionOutput struct {
	TransactionID      string
	TransactionHash    [sha256.Size]byte
	Position           uint32
	Amount             uint64
	Script             TransactionOutputScript
	IsInternalTransfer *bool
	IsSpent            *bool
	IsMyOutput         *bool
	IsMyInput          *bool
	SentSupports       *int64
	SentTips           *int64
	ReceivedTips       *int64
	Meta               map[string]any
	Purchase           *TransactionOutput
	PurchasedClaim     *TransactionOutput
	PurchaseReceipt    *TransactionOutput
	RepostedClaim      *TransactionOutput
	Claims             []*TransactionOutput
	Channel            *TransactionOutput
	PrivateKey         *keys.PrivateKey
	owner              *Transaction
	transactionHeight  *int64
}

func (output TransactionOutput) ID() string {
	transactionID := output.TransactionID
	if output.owner != nil {
		if output.owner.ID == "" {
			_ = output.owner.RebuildDerived()
		}
		transactionID = output.owner.ID
	}
	return fmt.Sprintf("%s:%d", transactionID, output.Position)
}

// TransactionHeight returns the height carried by the output's parent
// transaction. Detached outputs mirror the legacy wire default of -1.
func (output TransactionOutput) TransactionHeight() int64 {
	if output.owner == nil {
		if output.transactionHeight != nil {
			return *output.transactionHeight
		}
		return -1
	}
	return output.owner.Height
}

func (output TransactionOutput) Address(network keys.Network) (string, error) {
	if output.Script.Err != nil {
		return "", output.Script.Err
	}
	var prefix byte
	var hash []byte
	switch {
	case output.Script.HasPubKeyHash:
		prefix, hash = network.PubKeyAddressPrefix(), output.Script.PubKeyHash
	case output.Script.HasScriptHash:
		prefix, hash = network.ScriptAddressPrefix(), output.Script.ScriptHash
	default:
		return "", ErrTransactionHasNoAddress
	}
	if network.ID() == "" {
		return "", keys.ErrUnknownNetwork
	}
	payload := make([]byte, 1+len(hash))
	payload[0] = prefix
	copy(payload[1:], hash)
	return keys.EncodeBase58Check(payload), nil
}

func (output TransactionOutput) ClaimID() (string, error) {
	if output.Script.Err != nil {
		return "", output.Script.Err
	}
	var claimHash []byte
	switch {
	case output.Script.IsClaimName():
		transactionHash := output.TransactionHash
		if output.owner != nil {
			if output.owner.ID == "" {
				_ = output.owner.RebuildDerived()
			}
			transactionHash = output.owner.Hash
		}
		material := make([]byte, 0, sha256.Size+4)
		material = append(material, transactionHash[:]...)
		var position [4]byte
		binary.BigEndian.PutUint32(position[:], output.Position)
		material = append(material, position[:]...)
		hash := keys.Hash160(material)
		claimHash = hash[:]
	case output.Script.IsUpdateClaim(), output.Script.IsSupportClaim():
		claimHash = output.Script.ClaimID
	default:
		return "", ErrTransactionHasNoClaimID
	}
	reversed := reverseTransactionBytes(claimHash)
	return hex.EncodeToString(reversed), nil
}

func ParseTransaction(raw []byte) (*Transaction, error) {
	reader := transactionReader{raw: raw}
	transaction := &Transaction{
		Raw:       append([]byte(nil), raw...),
		Witnesses: make([][]byte, 0),
		Inputs:    make([]TransactionInput, 0),
		Outputs:   make([]TransactionOutput, 0),
		Height:    -2,
		Position:  -1,
	}
	var err error
	if transaction.Version, err = reader.uint32(); err != nil {
		return nil, transactionParseError("version", err)
	}
	inputCount, err := reader.compactSize()
	if err != nil {
		return nil, transactionParseError("input count", err)
	}
	if inputCount == 0 {
		if transaction.SegWitFlag, err = reader.uint8(); err != nil {
			return nil, transactionParseError("segwit flag", err)
		}
		if inputCount, err = reader.compactSize(); err != nil {
			return nil, transactionParseError("segwit input count", err)
		}
	}
	for index := uint64(0); index < inputCount; index++ {
		input, err := parseTransactionInput(&reader, index)
		if err != nil {
			return nil, err
		}
		transaction.Inputs = append(transaction.Inputs, input)
	}
	outputCount, err := reader.compactSize()
	if err != nil {
		return nil, transactionParseError("output count", err)
	}
	for index := uint64(0); index < outputCount; index++ {
		output, err := parseTransactionOutput(&reader, index)
		if err != nil {
			return nil, err
		}
		transaction.Outputs = append(transaction.Outputs, output)
	}
	if transaction.SegWitFlag != 0 {
		for inputIndex := uint64(0); inputIndex < inputCount; inputIndex++ {
			witnessCount, err := reader.compactSize()
			if err != nil {
				return nil, transactionParseError("witness count", err)
			}
			for witnessIndex := uint64(0); witnessIndex < witnessCount; witnessIndex++ {
				witness, err := reader.varBytes()
				if err != nil {
					return nil, transactionParseError("witness", err)
				}
				transaction.Witnesses = append(transaction.Witnesses, witness)
			}
		}
	}
	if transaction.LockTime, err = reader.uint32(); err != nil {
		return nil, transactionParseError("locktime", err)
	}
	transaction.Trailing = append([]byte(nil), raw[reader.offset:]...)
	if transaction.SegWitFlag != 0 {
		transaction.RawSansSegWit = transaction.serializeSansSegWit()
	} else {
		transaction.RawSansSegWit = append([]byte(nil), transaction.Raw...)
	}
	first := sha256.Sum256(transaction.RawSansSegWit)
	transaction.Hash = sha256.Sum256(first[:])
	transaction.ID = hex.EncodeToString(reverseTransactionBytes(transaction.Hash[:]))
	for index := range transaction.Outputs {
		transaction.Outputs[index].Position = uint32(index)
		transaction.Outputs[index].TransactionID = transaction.ID
		transaction.Outputs[index].TransactionHash = transaction.Hash
		transaction.Outputs[index].owner = transaction
	}
	for index := range transaction.Inputs {
		transaction.Inputs[index].owner = transaction
	}
	return transaction, nil
}

func parseTransactionInput(reader *transactionReader, index uint64) (TransactionInput, error) {
	if index > uint64(^uint32(0)) {
		return TransactionInput{}, transactionParseError("input position", errors.New("too many inputs"))
	}
	input := TransactionInput{Position: uint32(index)}
	previousHash, err := reader.read(sha256.Size)
	if err != nil {
		return TransactionInput{}, transactionParseError("previous hash", err)
	}
	copy(input.PreviousHash[:], previousHash)
	input.PreviousTxID = hex.EncodeToString(reverseTransactionBytes(previousHash))
	if input.PreviousIndex, err = reader.uint32(); err != nil {
		return TransactionInput{}, transactionParseError("previous output position", err)
	}
	source, err := reader.varBytes()
	if err != nil {
		return TransactionInput{}, transactionParseError("input script", err)
	}
	if input.Sequence, err = reader.uint32(); err != nil {
		return TransactionInput{}, transactionParseError("input sequence", err)
	}
	if input.IsCoinbase() {
		input.Coinbase = source
	} else {
		input.Script = ParseTransactionInputScript(source)
	}
	return input, nil
}

func parseTransactionOutput(reader *transactionReader, index uint64) (TransactionOutput, error) {
	if index > uint64(^uint32(0)) {
		return TransactionOutput{}, transactionParseError("output position", errors.New("too many outputs"))
	}
	output := TransactionOutput{Position: uint32(index)}
	var err error
	if output.Amount, err = reader.uint64(); err != nil {
		return TransactionOutput{}, transactionParseError("output amount", err)
	}
	source, err := reader.varBytes()
	if err != nil {
		return TransactionOutput{}, transactionParseError("output script", err)
	}
	output.Script = ParseTransactionOutputScript(source)
	return output, nil
}

func (transaction *Transaction) serializeSansSegWit() []byte {
	buffer := bytes.NewBuffer(make([]byte, 0, len(transaction.Raw)))
	_ = binary.Write(buffer, binary.LittleEndian, transaction.Version)
	writeTransactionCompactSize(buffer, uint64(len(transaction.Inputs)))
	for _, input := range transaction.Inputs {
		buffer.Write(input.PreviousHash[:])
		_ = binary.Write(buffer, binary.LittleEndian, input.PreviousIndex)
		source := input.Coinbase
		if !input.IsCoinbase() {
			source = input.Script.Source
		}
		writeTransactionVarBytes(buffer, source)
		_ = binary.Write(buffer, binary.LittleEndian, input.Sequence)
	}
	writeTransactionCompactSize(buffer, uint64(len(transaction.Outputs)))
	for _, output := range transaction.Outputs {
		_ = binary.Write(buffer, binary.LittleEndian, output.Amount)
		writeTransactionVarBytes(buffer, output.Script.Source)
	}
	_ = binary.Write(buffer, binary.LittleEndian, transaction.LockTime)
	return buffer.Bytes()
}

type transactionReader struct {
	raw    []byte
	offset int
}

func (reader *transactionReader) read(size int) ([]byte, error) {
	if size < 0 || size > len(reader.raw)-reader.offset {
		return nil, io.ErrUnexpectedEOF
	}
	value := reader.raw[reader.offset : reader.offset+size]
	reader.offset += size
	return value, nil
}

func (reader *transactionReader) uint8() (byte, error) {
	value, err := reader.read(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (reader *transactionReader) uint16() (uint16, error) {
	value, err := reader.read(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(value), nil
}

func (reader *transactionReader) uint32() (uint32, error) {
	value, err := reader.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}

func (reader *transactionReader) uint64() (uint64, error) {
	value, err := reader.read(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}

func (reader *transactionReader) compactSize() (uint64, error) {
	prefix, err := reader.uint8()
	if err != nil {
		return 0, err
	}
	switch prefix {
	case 0xfd:
		value, err := reader.uint16()
		return uint64(value), err
	case 0xfe:
		value, err := reader.uint32()
		return uint64(value), err
	case 0xff:
		return reader.uint64()
	default:
		return uint64(prefix), nil
	}
}

func (reader *transactionReader) varBytes() ([]byte, error) {
	length, err := reader.compactSize()
	if err != nil {
		return nil, err
	}
	if length > uint64(len(reader.raw)-reader.offset) {
		return nil, io.ErrUnexpectedEOF
	}
	value, err := reader.read(int(length))
	return append([]byte{}, value...), err
}

func writeTransactionVarBytes(buffer *bytes.Buffer, value []byte) {
	writeTransactionCompactSize(buffer, uint64(len(value)))
	buffer.Write(value)
}

func writeTransactionCompactSize(buffer *bytes.Buffer, value uint64) {
	switch {
	case value < 0xfd:
		buffer.WriteByte(byte(value))
	case value <= 0xffff:
		buffer.WriteByte(0xfd)
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], uint16(value))
		buffer.Write(encoded[:])
	case value <= 0xffffffff:
		buffer.WriteByte(0xfe)
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], uint32(value))
		buffer.Write(encoded[:])
	default:
		buffer.WriteByte(0xff)
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[:], value)
		buffer.Write(encoded[:])
	}
}

func transactionParseError(field string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrInvalidWalletTransaction, field, err)
}

func reverseTransactionBytes(value []byte) []byte {
	reversed := make([]byte, len(value))
	for index := range value {
		reversed[len(value)-1-index] = value[index]
	}
	return reversed
}
