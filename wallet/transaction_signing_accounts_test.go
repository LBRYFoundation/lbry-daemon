package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestLedgerGetPrivateKeyForAddressUsesPersistedAccountChainAndIndex(t *testing.T) {
	ctx := context.Background()
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := ledger.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ledgerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	account, err := NewAccount(keys.MainNet, NewObject(Member{
		Key: "seed", Value: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent",
	}))
	if err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	ledger.addAccount(account)
	privateKey, err := account.Receiving.GetPrivateKey(7)
	if err != nil {
		t.Fatal(err)
	}
	address := privateKey.Address()
	if err := ledger.Database.AddKeys(ctx, account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: address, Chain: ReceiveChain,
		PublicKey: privateKey.PublicKey().CompressedBytes(), ChainCode: []byte{0}, N: 7, Depth: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	records, err := ledger.Database.GetAddresses(ctx, ledgerdb.AddressQuery{})
	if err != nil || len(records) != 1 {
		t.Fatalf("persisted address records = %+v, %v", records, err)
	}

	resolved, err := ledger.GetPrivateKeyForAddress(ctx, wallet, address)
	if err != nil || resolved == nil ||
		!bytes.Equal(resolved.PrivateKeyBytes(), privateKey.PrivateKeyBytes()) {
		t.Fatalf("resolved private key = %v, %v", resolved, err)
	}
	missing, err := ledger.GetPrivateKeyForAddress(ctx, wallet, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing private key = %v, %v", missing, err)
	}
	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.GetPrivateKeyForAddress(ctx, wallet, address); !errors.Is(err, ErrEncryptedAccountPrivateKey) {
		t.Fatalf("encrypted account lookup error = %v", err)
	}
}

func TestLedgerGetPrivateKeyForAddressBoundaries(t *testing.T) {
	ledger := &Ledger{}
	if _, err := ledger.GetPrivateKeyForAddress(nil, NewWallet(), "address"); !errors.Is(err, ErrTransactionKeyLookupUnavailable) {
		t.Fatalf("missing database error = %v", err)
	}
	ledger.Database = ledgerdb.New(":memory:")
	if _, err := ledger.GetPrivateKeyForAddress(nil, nil, "address"); !errors.Is(err, ErrTransactionKeyLookupUnavailable) {
		t.Fatalf("nil wallet error = %v", err)
	}
	if _, err := ledger.GetPrivateKeyForAddress(nil, NewWallet(), "address"); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("closed database error = %v", err)
	}
}

func TestTransactionSignWithAccountsMatchesPinnedUnitSignature(t *testing.T) {
	ctx := context.Background()
	ledger, account, wallet := transactionSigningTestWallet(t, ctx)
	firstKey, err := account.Receiving.GetPrivateKey(0)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := account.Receiving.GetPrivateKey(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.AddKeys(ctx, account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: firstKey.Address(), Chain: ReceiveChain,
		PublicKey: firstKey.PublicKey().CompressedBytes(), ChainCode: []byte{0}, N: 0, Depth: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	firstHash := firstKey.Identifier()
	secondHash := secondKey.Identifier()
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(200_000_000, firstHash[:]),
	})
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction().
		AddInputs([]TransactionInput{input}).
		AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(190_000_000, secondHash[:]),
		})
	preimage, err := transaction.SignaturePreimage(0)
	if err != nil {
		t.Fatal(err)
	}
	wantPreimage := "0100000001a9f894c5a7c8493625f883cbd4e28b9f757b6fc2b5e3eb09c49725c66f7cc7dd" +
		"000000001976a91401244bd9f88fab49355f927b105d5650a8db344888acffffffff01802b530b" +
		"000000001976a91415a5ba33e2057819330e043b6b0b27b6f292c50c88ac0000000001000000"
	if got := hex.EncodeToString(preimage); got != wantPreimage {
		t.Fatalf("signature preimage = %s, want pinned Python %s", got, wantPreimage)
	}
	digest, err := transaction.SignatureDigest(0)
	if err != nil || hex.EncodeToString(digest[:]) != "680389ba7b86796509bfed4a8a0f2a33c7168bfa4524f34351de06ab929ba33f" {
		t.Fatalf("signature digest = %x, %v", digest, err)
	}
	if err := transaction.SignWithAccounts(ctx, []*Account{account}, nil); err != nil {
		t.Fatal(err)
	}
	wantSignature := "304402200dafa26ad7cf38c5a971c8a25ce7d85a076235f146126762296b1223c42ae21e" +
		"022020ef9eeb8398327891008c5c0be4357683f12cb22346691ff23914f457bf679601"
	if got := hex.EncodeToString(transaction.Inputs[0].Script.Signature); got != wantSignature {
		t.Fatalf("transaction signature = %s, want pinned Python %s", got, wantSignature)
	}
	if !bytes.Equal(transaction.Inputs[0].Script.PublicKey, firstKey.PublicKey().CompressedBytes()) ||
		account.wallet != wallet || account.ledger != ledger ||
		transaction.ID != "70243c617cffb4ea1575999ce65131b187b5d0bd8410eb4b0762704de26ebb75" {
		t.Fatalf("signed input/account ownership mismatch: %#v", transaction.Inputs[0])
	}
}

func TestTransactionSignWithAccountsExtraKeyAndValidationReset(t *testing.T) {
	account, err := NewAccount(keys.MainNet, NewObject(Member{
		Key: "seed", Value: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent",
	}))
	if err != nil {
		t.Fatal(err)
	}
	NewWallet(WithWalletAccounts([]*Account{account}))
	ledger := &Ledger{Network: keys.MainNet}
	ledger.addAccount(account)
	extraKey, err := keys.NewPrivateKey(
		keys.MainNet, append(make([]byte, 31), 9), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ignoredExtraKey, err := keys.NewPrivateKey(
		keys.MainNet, append(make([]byte, 31), 10), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	extraHash := extraKey.Identifier()
	redeemScript, err := NewTimeLockInputSubscript(big.NewInt(500), extraHash[:])
	if err != nil {
		t.Fatal(err)
	}
	scriptHash := keys.Hash160(redeemScript.Source)
	parent := NewTransaction().AddOutputs([]TransactionOutput{
		NewPayScriptHashOutput(1_000_000, scriptHash[:]),
	})
	input, err := NewTimeLockSpendInput(&parent.Outputs[0], redeemScript.Source)
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction().AddInputs([]TransactionInput{input})
	if err := transaction.SignWithAccounts(
		nil, []*Account{account}, []*keys.PrivateKey{extraKey, ignoredExtraKey},
	); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(transaction.Inputs[0].Script.PublicKey, extraKey.PublicKey().CompressedBytes()) {
		t.Fatalf("extra key public key = %x", transaction.Inputs[0].Script.PublicKey)
	}

	invalid := NewTransaction()
	if invalid.Raw == nil || invalid.ID == "" {
		t.Fatal("new transaction caches are unavailable")
	}
	if err := invalid.SignWithAccounts(nil, nil, nil); !errors.Is(err, ErrTransactionSigningLedger) ||
		!errors.Is(err, ErrTransactionSigning) || invalid.Raw != nil || invalid.ID != "" {
		t.Fatalf("empty-account signing = raw %x id %q error %v", invalid.Raw, invalid.ID, err)
	}
}

func TestTransactionSignWithAccountsPreservesPartialMissingKeyMutation(t *testing.T) {
	ctx := context.Background()
	ledger, account, _ := transactionSigningTestWallet(t, ctx)
	privateKeys := make([]*keys.PrivateKey, 2)
	inputs := make([]TransactionInput, 2)
	for index := range privateKeys {
		privateKey, err := account.Receiving.GetPrivateKey(int64(index))
		if err != nil {
			t.Fatal(err)
		}
		privateKeys[index] = privateKey
		identifier := privateKey.Identifier()
		parent := NewTransaction().AddOutputs([]TransactionOutput{
			NewPayPubKeyHashOutput(1_000_000, identifier[:]),
		})
		inputs[index], err = NewSpendInput(&parent.Outputs[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.Database.AddKeys(ctx, account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: privateKeys[0].Address(), Chain: ReceiveChain,
		PublicKey: privateKeys[0].PublicKey().CompressedBytes(), ChainCode: []byte{0}, N: 0, Depth: 2,
	}}); err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction().AddInputs(inputs).AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(1_900_000, bytes.Repeat([]byte{0x81}, 20)),
	})
	err := transaction.SignWithAccounts(ctx, []*Account{account}, nil)
	if !errors.Is(err, ErrTransactionSigningKeyUnavailable) ||
		bytes.Equal(transaction.Inputs[0].Script.Signature, make([]byte, transactionNullSignatureSize)) ||
		!bytes.Equal(transaction.Inputs[1].Script.Signature, make([]byte, transactionNullSignatureSize)) ||
		transaction.Raw != nil || transaction.ID != "" {
		t.Fatalf("partial account signing = %#v, error %v", transaction, err)
	}
}

func TestTransactionSigningAccountOwnershipValidation(t *testing.T) {
	firstLedger := &Ledger{}
	secondLedger := &Ledger{}
	firstWallet := NewWallet()
	secondWallet := NewWallet()
	first := &Account{ledger: firstLedger, wallet: firstWallet}
	same := &Account{ledger: firstLedger, wallet: firstWallet}
	otherLedger := &Account{ledger: secondLedger, wallet: firstWallet}
	otherWallet := &Account{ledger: firstLedger, wallet: secondWallet}

	ledger, wallet, err := transactionSigningLedgerAndWallet([]*Account{first, same})
	if err != nil || ledger != firstLedger || wallet != firstWallet {
		t.Fatalf("matching ownership = %p/%p, %v", ledger, wallet, err)
	}
	if _, _, err := transactionSigningLedgerAndWallet([]*Account{first, otherLedger}); !errors.Is(err, ErrTransactionSigningLedger) {
		t.Fatalf("ledger mismatch error = %v", err)
	}
	if _, _, err := transactionSigningLedgerAndWallet([]*Account{first, otherWallet}); !errors.Is(err, ErrTransactionSigningWallet) {
		t.Fatalf("wallet mismatch error = %v", err)
	}
	if _, _, err := transactionSigningLedgerAndWallet([]*Account{nil}); !errors.Is(err, ErrTransactionSigning) {
		t.Fatalf("nil account error = %v", err)
	}
}

func transactionSigningTestWallet(
	t *testing.T, ctx context.Context,
) (*Ledger, *Account, *Wallet) {
	t.Helper()
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := ledger.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ledgerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	account, err := NewAccount(keys.MainNet, NewObject(Member{
		Key: "seed", Value: "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent",
	}))
	if err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	ledger.addAccount(account)
	return ledger, account, wallet
}
