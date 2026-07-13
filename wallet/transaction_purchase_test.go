package wallet

import (
	"context"
	"errors"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestCreatePurchaseTransactionValidation(t *testing.T) {
	if _, err := CreatePurchaseTransaction(context.Background(), nil, nil, 1, ""); !errors.Is(err, ErrPurchaseClaimUnavailable) {
		t.Fatalf("nil claim error = %v", err)
	}
	claim := &TransactionOutput{}
	if _, err := CreatePurchaseTransaction(context.Background(), nil, claim, 1, ""); !errors.Is(err, ErrPurchaseFundingAccount) {
		t.Fatalf("nil accounts error = %v", err)
	}
}

func TestCreatePurchaseTransactionBuildsPaymentThenMetadata(t *testing.T) {
	ledger, account, address := newTransactionBalancingAccountLedger(t)
	privateKey, err := keys.ParseExtendedKey(keys.MainNet, accountEncryptionXPrv)
	if err != nil {
		t.Fatal(err)
	}
	account.PrivateKey = privateKey.(*keys.PrivateKey)
	ledger.FeePerByte = 0
	persistTransactionBalancingUTXO(t, ledger, address, 5*uint64(TransactionCoin), 1, 1, true)
	claimTransaction := NewTransaction()
	claimTransaction.AddOutputs([]TransactionOutput{{
		Amount: 1,
		Script: NewClaimNameOutput(1, "paid", []byte{1}, make([]byte, 20)).Script,
	}})
	if err := claimTransaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	claim := &claimTransaction.Outputs[0]

	transaction, err := CreatePurchaseTransaction(
		context.Background(), []*Account{account}, claim, uint64(TransactionCoin), address,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction.Outputs) < 2 || transaction.Outputs[0].Amount != uint64(TransactionCoin) ||
		transaction.Outputs[0].Script.Template != TransactionScriptPayPubKeyHash ||
		transaction.Outputs[1].Script.Template != TransactionScriptReturnData {
		t.Fatalf("purchase outputs = %#v", transaction.Outputs)
	}
	claimID, err := claim.ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	purchasedID, ok := decodeTransactionPurchase(transaction.Outputs[1].Script)
	if !ok || purchasedID != claimID {
		t.Fatalf("purchase claim id = %q, %v, want %q", purchasedID, ok, claimID)
	}
}
