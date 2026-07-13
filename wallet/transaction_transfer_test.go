package wallet

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestCreateTimeLockDepositTransactionBuildsLockAndSignsExtraKey(t *testing.T) {
	ledger, account := transactionTransferTestAccount(t)
	privateKey, err := keys.NewPrivateKey(keys.MainNet, bytes.Repeat([]byte{1}, 32), make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	identifier := privateKey.Identifier()
	redeem, err := NewTimeLockInputSubscript(big.NewInt(600), identifier[:])
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := keys.Hash160(redeem.Source)
	parent := NewTransaction().AddInputs([]TransactionInput{{PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{1}}}).
		AddOutputs([]TransactionOutput{NewPayScriptHashOutput(100_000_000, scriptHash[:])})
	if err := parent.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	wif := keys.EncodeBase58Check(privateKey.LegacyWIFBytes())
	transaction, err := CreateTimeLockDepositTransaction(
		context.Background(), parent, 0, redeem.Source, wif, account,
	)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.LockTime != 600 || transaction.Inputs[0].Sequence != 0xfffffffe ||
		len(transaction.Inputs[0].Script.Signature) == 0 || len(transaction.Outputs) != 1 {
		t.Fatalf("deposit = %#v", transaction)
	}
	_ = ledger
}

func transactionTransferTestAccount(t *testing.T) (*Ledger, *Account) {
	t.Helper()
	manager := NewWalletManager()
	ledger, err := manager.GetOrCreateLedger(keys.MainNet.ID(), LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	account, err := GenerateAccount(keys.MainNet, "deposit", DeterministicChainGenerator)
	if err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	manager.Wallets = []*Wallet{wallet}
	if err := manager.RegisterAccount(keys.MainNet.ID(), account); err != nil {
		t.Fatal(err)
	}
	ledger.Database = transactionBroadcastWaitOracleDatabase(t)
	if _, err := account.EnsureAddressGap(context.Background()); err != nil {
		t.Fatal(err)
	}
	return ledger, account
}
