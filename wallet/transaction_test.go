package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

const genesisTransactionHex = "01000000010000000000000000000000000000000000000000000000000000000000000000" +
	"ffffffff1f04ffff001d010417696e736572742074696d657374616d7020737472696e67ffffffff" +
	"01000004bfc91b8e001976a914345991dbf57bfb014b87006acdfafbfc5fe8292f88ac00000000"

const segWitTransactionHex = "020000000001011111111111111111111111111111111111111111111111111111111111111111" +
	"0200000000feffffff01e8030000000000001976a9142222222222222222222222222222222222222222" +
	"88ac0201aa02bbcc03000000"

const timeLockTransactionHex = "0200000001409223c2405238fdc516d4f2e8aa57637ce52d3b1ac42b26f1accdcda9697e79" +
	"010000008a4730440220033d5286f161da717d9d1bc3c2bc28da7636b38fc0c6aefb1e0864212f05282c02205df3" +
	"ce135e79c76d44489212f77ad4e3a838562e601e6377704fa6206a6ae44f012102261773e7eebe9da80a5653d865" +
	"cc600362f8e7b2b598661139dd902b5b01ea101f03aaf30ab17576a914a3328f18ac1892a6667f713d7020ff3437" +
	"d973c888acfeffffff0180ed3e17000000001976a914353352b7ce1e3c9c05ffcd6ae97609de2999744488accdf5" +
	"0a00"

func TestParseWalletTransactionGenesisFixture(t *testing.T) {
	raw := mustTransactionHex(t, genesisTransactionHex)
	transaction, err := ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.ID != "b8211c82c3d15bcd78bba57005b86fed515149a53a425eb592c07af99fe559cc" ||
		transaction.Version != 1 || transaction.LockTime != 0 || transaction.SegWitFlag != 0 ||
		transaction.Height != -2 || transaction.Position != -1 || transaction.IsVerified ||
		!bytes.Equal(transaction.Raw, raw) || !bytes.Equal(transaction.RawSansSegWit, raw) ||
		len(transaction.Inputs) != 1 || len(transaction.Outputs) != 1 {
		t.Fatalf("genesis transaction = %#v", transaction)
	}
	input := transaction.Inputs[0]
	if !input.IsCoinbase() || input.PreviousIndex != ^uint32(0) || input.Sequence != ^uint32(0) ||
		input.Script.Template != "" ||
		hex.EncodeToString(input.Coinbase) != "04ffff001d010417696e736572742074696d657374616d7020737472696e67" {
		t.Fatalf("genesis input = %#v", input)
	}
	output := transaction.Outputs[0]
	if output.Amount != 40_000_000_000_000_000 || output.Position != 0 ||
		output.TransactionID != transaction.ID || output.TransactionHash != transaction.Hash ||
		output.ID() != transaction.ID+":0" ||
		output.Script.Template != TransactionScriptPayPubKeyHash ||
		hex.EncodeToString(output.Script.PubKeyHash) != "345991dbf57bfb014b87006acdfafbfc5fe8292f" {
		t.Fatalf("genesis output = %#v", output)
	}
}

func TestParseWalletTransactionSegWitUsesCanonicalSansWitnessID(t *testing.T) {
	transaction, err := ParseTransaction(mustTransactionHex(t, segWitTransactionHex))
	if err != nil {
		t.Fatal(err)
	}
	sansWitness := "0200000001" + strings.Repeat("11", 32) +
		"0200000000feffffff01e8030000000000001976a914" + strings.Repeat("22", 20) +
		"88ac03000000"
	if transaction.ID != "f3eeec15efc6c8f87c32b5aab055c435f10f714607a3259dbcfd16ca724a2c07" ||
		transaction.SegWitFlag != 1 || transaction.Version != 2 || transaction.LockTime != 3 ||
		!bytes.Equal(transaction.RawSansSegWit, mustTransactionHex(t, sansWitness)) ||
		!reflect.DeepEqual(transaction.Witnesses, [][]byte{{0xaa}, {0xbb, 0xcc}}) {
		t.Fatalf("segwit transaction = %#v", transaction)
	}
	input := transaction.Inputs[0]
	if input.IsCoinbase() || input.PreviousIndex != 2 || input.Sequence != 0xfffffffe ||
		input.Script.Template != TransactionInputNoScript ||
		input.PreviousTxID != "1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("segwit input = %#v", input)
	}
	output := transaction.Outputs[0]
	if address, err := output.Address(keys.MainNet); err != nil ||
		address != "bFqkZNujBMnPh2wJtFVQ9g77KH8mqkDSZy" {
		t.Fatalf("segwit output address = %q, %v", address, err)
	}
}

func TestParseWalletTransactionTimeLockFixture(t *testing.T) {
	transaction, err := ParseTransaction(mustTransactionHex(t, timeLockTransactionHex))
	if err != nil {
		t.Fatal(err)
	}
	if transaction.ID != "e466881128889d1cc4110627753051c22e72a81d11229a1a1337da06940bebcf" ||
		transaction.Version != 2 || transaction.LockTime != 718285 ||
		len(transaction.Inputs) != 1 || len(transaction.Outputs) != 1 {
		t.Fatalf("timelock transaction = %#v", transaction)
	}
	input := transaction.Inputs[0]
	if input.PreviousOutputID() != "797e69a9cdcdacf1262bc41a3b2de57c6357aae8f2d416c5fd385240c2239240:1" ||
		input.Sequence != 0xfffffffe || input.Script.Err != nil ||
		input.Script.Template != TransactionInputScriptHashTime || input.Script.Script == nil ||
		input.Script.Script.Template != TransactionInputTimeLock ||
		input.Script.Script.Height == nil || input.Script.Script.Height.Uint64() != 717738 ||
		hex.EncodeToString(input.Script.Script.PubKeyHash) != "a3328f18ac1892a6667f713d7020ff3437d973c8" {
		t.Fatalf("timelock input = %#v", input)
	}
}

func TestTransactionOutputScriptTemplates(t *testing.T) {
	pubkeyHash := bytes.Repeat([]byte{0x11}, 20)
	scriptHash := bytes.Repeat([]byte{0x22}, 20)
	claimID := bytes.Repeat([]byte{0x33}, 20)
	claimName := []byte("name")
	claim := []byte{1, 2, 3}
	support := []byte{4, 5}
	tests := []struct {
		name     string
		source   []byte
		template string
	}{
		{"pay pubkey", append(transactionPush([]byte{2, 3}), transactionOpCheckSig), TransactionScriptPayPubKeyFull},
		{"pay pubkey hash", transactionP2PKH(pubkeyHash), TransactionScriptPayPubKeyHash},
		{"pay script hash", transactionP2SH(scriptHash), TransactionScriptPayScriptHash},
		{"segwit", append([]byte{transactionOp0}, transactionPush(scriptHash)...), TransactionScriptPaySegWit},
		{"return empty", []byte{transactionOpReturn, transactionOp0}, TransactionScriptReturnData},
		{"claim pubkey", transactionClaimScript(transactionOpClaimName, claimName, nil, claim, nil, transactionP2PKH(pubkeyHash)), TransactionScriptClaimPubKeyHash},
		{"claim script", transactionClaimScript(transactionOpClaimName, claimName, nil, claim, nil, transactionP2SH(scriptHash)), TransactionScriptClaimScriptHash},
		{"support pubkey", transactionClaimScript(transactionOpSupportClaim, claimName, claimID, nil, nil, transactionP2PKH(pubkeyHash)), TransactionScriptSupportPubKey},
		{"support data script", transactionClaimScript(transactionOpSupportClaim, claimName, claimID, nil, support, transactionP2SH(scriptHash)), TransactionScriptSupportDataHash},
		{"support empty data", transactionClaimScript(transactionOpSupportClaim, claimName, claimID, nil, []byte{}, transactionP2PKH(pubkeyHash)), TransactionScriptSupportDataKey},
		{"update pubkey", transactionClaimScript(transactionOpUpdateClaim, claimName, claimID, claim, nil, transactionP2PKH(pubkeyHash)), TransactionScriptUpdatePubKey},
	}
	for _, fixture := range tests {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			script := ParseTransactionOutputScript(fixture.source)
			if script.Err != nil || script.Template != fixture.template {
				t.Fatalf("script = %#v", script)
			}
		})
	}
	if script := ParseTransactionOutputScript([]byte{0xff}); !errors.Is(script.Err, ErrInvalidTransactionScript) || script.Template != "" {
		t.Fatalf("unknown output script = %#v", script)
	}
	if script := ParseTransactionOutputScript(nil); script.Err != nil || script.Template != TransactionScriptNoScript {
		t.Fatalf("empty output script = %#v", script)
	}
	if script := ParseTransactionOutputScript([]byte{transactionOpReturn, 5, 0xaa, 0xbb}); script.Err != nil || script.Template != TransactionScriptReturnData ||
		!bytes.Equal(script.Data, []byte{0xaa, 0xbb}) {
		t.Fatalf("short pushed return data = %#v", script)
	}
	invalidClaim := transactionClaimScript(
		transactionOpClaimName, claimName, nil, claim, nil, []byte{transactionOpCheckSig},
	)
	if script := ParseTransactionOutputScript(invalidClaim); !errors.Is(script.Err, ErrInvalidTransactionScript) ||
		script.ClaimName != nil || script.Claim != nil || script.ClaimID != nil ||
		script.Support != nil || script.Template != "" {
		t.Fatalf("invalid claim script retained parsed values = %#v", script)
	}
}

func TestTransactionInputScriptsAndClaimIDs(t *testing.T) {
	signature := []byte{1, 2, 3}
	publicKey := bytes.Repeat([]byte{4}, 33)
	script := ParseTransactionInputScript(append(transactionPush(signature), transactionPush(publicKey)...))
	if script.Err != nil || script.Template != TransactionInputPubKeyHash ||
		!bytes.Equal(script.Signature, signature) || !bytes.Equal(script.PublicKey, publicKey) {
		t.Fatalf("input script = %#v", script)
	}
	if script := ParseTransactionInputScript([]byte{transactionOpPushData1}); script.Err != nil || script.Template != TransactionInputPubKey || script.Signature == nil {
		t.Fatalf("missing pushdata length input = %#v", script)
	}
	multiSigSubscript := []byte{transactionOp1}
	multiSigSubscript = append(multiSigSubscript, transactionPush(publicKey)...)
	multiSigSubscript = append(multiSigSubscript, transactionOp1, transactionOpCheckMultiSig)
	multiSigSource := []byte{transactionOp0}
	multiSigSource = append(multiSigSource, transactionPush(signature)...)
	multiSigSource = append(multiSigSource, transactionPush(multiSigSubscript)...)
	multiSig := ParseTransactionInputScript(multiSigSource)
	if multiSig.Err != nil || multiSig.Template != TransactionInputScriptHashMulti ||
		len(multiSig.Signatures) != 1 || !bytes.Equal(multiSig.Signatures[0], signature) ||
		multiSig.Script == nil || multiSig.Script.Template != TransactionInputMultiSig ||
		multiSig.Script.SignaturesCount != 1 || multiSig.Script.PublicKeysCount != 1 ||
		len(multiSig.Script.PublicKeys) != 1 || !bytes.Equal(multiSig.Script.PublicKeys[0], publicKey) {
		t.Fatalf("multisig input script = %#v", multiSig)
	}
	claimOutput := TransactionOutput{
		Position:        7,
		TransactionHash: transactionHashFixture(),
		Script: ParseTransactionOutputScript(transactionClaimScript(
			transactionOpClaimName, []byte("x"), nil, []byte{1}, nil,
			transactionP2PKH(bytes.Repeat([]byte{2}, 20)),
		)),
	}
	claimID, err := claimOutput.ClaimID()
	if err != nil || claimID != "104bda2587e6c8fef25920e94ab5de384d785970" {
		t.Fatalf("new claim ID = %q, %v", claimID, err)
	}
	updateOutput := TransactionOutput{
		TransactionHash: transactionHashFixture(),
		Script: ParseTransactionOutputScript(transactionClaimScript(
			transactionOpUpdateClaim, []byte("x"), bytes.Repeat([]byte{0x33}, 20), []byte{1}, nil,
			transactionP2PKH(bytes.Repeat([]byte{2}, 20)),
		)),
	}
	claimID, err = updateOutput.ClaimID()
	if err != nil || claimID != "3333333333333333333333333333333333333333" {
		t.Fatalf("update claim ID = %q, %v", claimID, err)
	}
}

func transactionHashFixture() [32]byte {
	var hash [32]byte
	for index := range hash {
		hash[index] = byte(index)
	}
	return hash
}

func TestParseWalletTransactionRejectsTruncationButRetainsLazyScriptFailure(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 0, 0}, mustTransactionHex(t, genesisTransactionHex)[:50]} {
		if _, err := ParseTransaction(raw); !errors.Is(err, ErrInvalidWalletTransaction) {
			t.Fatalf("truncated transaction error = %v", err)
		}
	}
	raw := mustTransactionHex(t,
		"01000000010000000000000000000000000000000000000000000000000000000000000000"+
			"ffffffff00ffffffff01010000000000000001ff00000000",
	)
	transaction, err := ParseTransaction(raw)
	if err != nil || len(transaction.Outputs) != 1 ||
		!errors.Is(transaction.Outputs[0].Script.Err, ErrInvalidTransactionScript) {
		t.Fatalf("lazy output script failure = %#v, %v", transaction, err)
	}
	if _, err := transaction.Outputs[0].Address(keys.MainNet); !errors.Is(err, ErrInvalidTransactionScript) {
		t.Fatalf("unknown output address error = %v", err)
	}
}

func mustTransactionHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func transactionPush(value []byte) []byte {
	if len(value) >= transactionOpPushData1 {
		panic("test push only supports direct data")
	}
	return append([]byte{byte(len(value))}, value...)
}

func transactionP2PKH(hash []byte) []byte {
	script := []byte{transactionOpDup, transactionOpHash160}
	script = append(script, transactionPush(hash)...)
	return append(script, transactionOpEqualVerify, transactionOpCheckSig)
}

func transactionP2SH(hash []byte) []byte {
	script := []byte{transactionOpHash160}
	script = append(script, transactionPush(hash)...)
	return append(script, transactionOpEqual)
}

func transactionClaimScript(
	opcode byte, name, claimID, claim, support, payment []byte,
) []byte {
	script := []byte{opcode}
	script = append(script, transactionPush(name)...)
	switch opcode {
	case transactionOpClaimName:
		script = append(script, transactionPush(claim)...)
		script = append(script, transactionOp2Drop, transactionOpDrop)
	case transactionOpSupportClaim:
		script = append(script, transactionPush(claimID)...)
		if support == nil {
			script = append(script, transactionOp2Drop, transactionOpDrop)
		} else {
			script = append(script, transactionPush(support)...)
			script = append(script, transactionOp2Drop, transactionOp2Drop)
		}
	case transactionOpUpdateClaim:
		script = append(script, transactionPush(claimID)...)
		script = append(script, transactionPush(claim)...)
		script = append(script, transactionOp2Drop, transactionOp2Drop)
	}
	return append(script, payment...)
}
