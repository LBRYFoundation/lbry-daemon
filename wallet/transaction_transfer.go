package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransferAddress       = errors.New("invalid transfer address")
	ErrTransferAmount        = errors.New("An amount is required.")
	ErrTransferLedger        = errors.New("Can only transfer between accounts of the same ledger.")
	ErrTransferOutputCount   = errors.New("transfer output count must be positive")
	ErrTransferOutputSplit   = errors.New("Using --everything along with --outputs is not supported.")
	ErrTimeLockOutputIndex   = errors.New("timelock output index is out of range")
	ErrTimeLockPrivateKeyWIF = errors.New("invalid timelock private key")
)

func PayPubKeyHashOutputForAddress(amount uint64, address string) (TransactionOutput, error) {
	decoded, err := keys.DecodeBase58Check(address)
	if err != nil || len(decoded) != 21 {
		return TransactionOutput{}, fmt.Errorf("%w: %s", ErrTransferAddress, address)
	}
	switch decoded[0] {
	case keys.MainNet.PubKeyAddressPrefix(), keys.TestNet.PubKeyAddressPrefix():
		return NewPayPubKeyHashOutput(amount, decoded[1:]), nil
	case keys.MainNet.ScriptAddressPrefix(), keys.TestNet.ScriptAddressPrefix():
		return NewPayScriptHashOutput(amount, decoded[1:]), nil
	default:
		return TransactionOutput{}, fmt.Errorf("%w: %s", ErrTransferAddress, address)
	}
}

func CreatePaymentTransaction(
	ctx context.Context, amount uint64, addresses []string,
	fundingAccounts []*Account, changeAccount *Account,
) (*Transaction, error) {
	outputs := make([]TransactionOutput, len(addresses))
	for index, address := range addresses {
		output, err := PayPubKeyHashOutputForAddress(amount, address)
		if err != nil {
			return nil, err
		}
		outputs[index] = output
	}
	return CreateTransaction(ctx, nil, outputs, fundingAccounts, changeAccount, true)
}

func FundAccount(
	ctx context.Context, fromAccount, toAccount *Account, amount *uint64,
	everything bool, outputCount int, broadcast bool,
) (*Transaction, error) {
	if fromAccount == nil || toAccount == nil || fromAccount.ledger != toAccount.ledger {
		return nil, ErrTransferLedger
	}
	if outputCount < 1 {
		return nil, ErrTransferOutputCount
	}
	if everything && outputCount > 1 {
		return nil, ErrTransferOutputSplit
	}
	ledger := fromAccount.ledger
	var transaction *Transaction
	var err error
	if everything {
		utxos, getErr := fromAccount.GetUTXOs(ctx, ledgerdb.OutputQuery{})
		if getErr != nil {
			return nil, getErr
		}
		if reserveErr := ledger.ReserveTransactionOutputs(ctx, utxos, true); reserveErr != nil {
			return nil, reserveErr
		}
		inputs := make([]TransactionInput, len(utxos))
		for index, output := range utxos {
			inputs[index], err = NewSpendInput(output)
			if err != nil {
				_ = ledger.ReserveTransactionOutputs(context.WithoutCancel(ctx), utxos, false)
				return nil, err
			}
		}
		transaction, err = CreateTransaction(ctx, inputs, nil, []*Account{fromAccount}, toAccount, true)
	} else if amount != nil && *amount > 0 {
		address, addressErr := toAccount.Change.GetOrCreateUsableAddress(ctx)
		if addressErr != nil {
			return nil, addressErr
		}
		outputs := make([]TransactionOutput, outputCount)
		for index := range outputs {
			outputs[index], err = PayPubKeyHashOutputForAddress(*amount/uint64(outputCount), address)
			if err != nil {
				return nil, err
			}
		}
		transaction, err = CreateTransaction(ctx, nil, outputs, []*Account{fromAccount}, fromAccount, true)
	} else {
		return nil, ErrTransferAmount
	}
	if err != nil {
		return nil, err
	}
	if broadcast {
		_, err = ledger.BroadcastTransaction(ctx, transaction)
	} else {
		err = ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
	}
	return transaction, err
}

func CreateTimeLockDepositTransaction(
	ctx context.Context, previous *Transaction, outputIndex uint32,
	redeemScript []byte, privateKeyWIF string, account *Account,
) (*Transaction, error) {
	if previous == nil || uint64(outputIndex) >= uint64(len(previous.Outputs)) {
		return nil, ErrTimeLockOutputIndex
	}
	input, err := NewTimeLockSpendInput(&previous.Outputs[outputIndex], redeemScript)
	if err != nil {
		return nil, err
	}
	input.Sequence = 0xfffffffe
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input}, nil, []*Account{account}, account, false,
	)
	if err != nil {
		return nil, err
	}
	if input.Script.Script == nil || input.Script.Script.Height == nil || !input.Script.Script.Height.IsUint64() ||
		input.Script.Script.Height.Uint64() > uint64(^uint32(0)) {
		_ = account.ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
		return nil, ErrTimeLockOutputIndex
	}
	transaction.LockTime = uint32(input.Script.Script.Height.Uint64())
	transaction.ResetDerived()
	payload, err := keys.DecodeBase58Check(privateKeyWIF)
	if err != nil || len(payload) != 34 {
		_ = account.ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
		return nil, ErrTimeLockPrivateKeyWIF
	}
	privateKey, err := keys.NewPrivateKey(account.Network, payload[1:33], make([]byte, 32), 0, 0, nil)
	if err != nil {
		_ = account.ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
		return nil, err
	}
	if err := transaction.SignWithAccounts(ctx, []*Account{account}, []*keys.PrivateKey{privateKey}); err != nil {
		_ = account.ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction)
		return nil, err
	}
	return transaction, nil
}
