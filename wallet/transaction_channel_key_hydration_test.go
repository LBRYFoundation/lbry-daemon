package wallet

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionChannelKeyHydrationRequiresExplicitWalletAccounts(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	fixture.store(t, fixture.stream, []uint32{0}, nil, nil)
	account, wallet := transactionChannelKeyHydrationWallet(t, fixture, fixture.channelKey)

	walletless := fixture.get(t, fixture.stream)
	if walletless.Outputs[0].PrivateKey != nil || walletless.Outputs[0].Channel == nil ||
		walletless.Outputs[0].Channel.PrivateKey != nil {
		t.Fatalf("walletless signed output keys = output %v, channel %v",
			walletless.Outputs[0].PrivateKey, walletless.Outputs[0].Channel.PrivateKey)
	}

	streamID := fixture.stream.ID
	walletAware, err := account.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &streamID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(walletAware) != 1 || walletAware[0].Outputs[0].PrivateKey != nil ||
		walletAware[0].Outputs[0].Channel == nil ||
		!equalTransactionChannelHydrationKey(
			walletAware[0].Outputs[0].Channel.PrivateKey, fixture.channelKey,
		) {
		t.Fatalf("wallet-aware signed output = %#v", walletAware)
	}

	manager := &WalletManager{
		Wallets: []*Wallet{wallet}, Ledgers: map[keys.Network]*Ledger{keys.MainNet: fixture.ledger},
	}
	channelID := fixture.channel.ID
	shown, err := manager.GetTransaction(context.Background(), channelID)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Transaction == nil || len(shown.Transaction.Outputs) != 1 ||
		shown.Transaction.Outputs[0].PrivateKey != nil {
		t.Fatalf("transaction_show local channel key = %#v", shown.Transaction)
	}
	fixture.ledger.Headers = newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	wire, err := shown.Ledger.LegacyTransactionJSON(shown.Transaction)
	if err != nil {
		t.Fatal(err)
	}
	outputs, ok := wire["outputs"].([]any)
	if !ok || len(outputs) != 1 {
		t.Fatalf("transaction_show outputs = %#v", wire["outputs"])
	}
	encoded, ok := outputs[0].(map[string]any)
	if !ok || encoded["has_signing_key"] != false {
		t.Fatalf("transaction_show has_signing_key = %#v", outputs[0])
	}
}

func TestTransactionChannelKeyHydrationUsesWholeWalletOrder(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	fixture.store(t, fixture.channel, []uint32{0}, nil, nil)
	first, _ := transactionChannelKeyHydrationWallet(t, fixture, nil)
	second := transactionChannelKeyHydrationAccount(t, fixture, fixture.channelKey)
	wallet := NewWallet(WithWalletAccounts([]*Account{first, second}))
	first.wallet, second.wallet = wallet, wallet

	channelID := fixture.channel.ID
	transactions, err := first.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &channelID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 || !equalTransactionChannelHydrationKey(
		transactions[0].Outputs[0].PrivateKey, fixture.channelKey,
	) {
		t.Fatalf("second-account channel key = %#v", transactions)
	}

	malformed := transactionChannelKeyHydrationAccount(t, fixture, nil)
	malformed.ChannelKeys.Set(fixture.channelKey.Address(), int64(1))
	wallet = NewWallet(WithWalletAccounts([]*Account{second, malformed}))
	second.wallet, malformed.wallet = wallet, wallet
	transactions, err = second.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &channelID},
	})
	if err != nil || len(transactions) != 1 || !equalTransactionChannelHydrationKey(
		transactions[0].Outputs[0].PrivateKey, fixture.channelKey,
	) {
		t.Fatalf("first matching account did not stop lookup: %#v, %v", transactions, err)
	}

	wallet = NewWallet(WithWalletAccounts([]*Account{malformed, second}))
	malformed.wallet, second.wallet = wallet, wallet
	transactions, err = malformed.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &channelID},
	})
	if err == nil || transactions != nil || !strings.Contains(err.Error(), "want PEM string") {
		t.Fatalf("malformed first account = transactions %#v, error %v", transactions, err)
	}
}

func TestTransactionChannelKeyHydrationRetainsEarlierMutationOnError(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	account := transactionChannelKeyHydrationAccount(t, fixture, fixture.channelKey)
	valid := NewClaimNameOutput(
		1, "@valid", makeV2ChannelClaim(fixture.channelKey.PublicKey().CompressedBytes()),
		bytes.Repeat([]byte{0x81}, 20),
	)
	invalid := NewClaimNameOutput(
		1, "@invalid", makeV2ChannelClaim([]byte{1}), bytes.Repeat([]byte{0x82}, 20),
	)
	transaction := transactionChannelHydrationTransaction(t, 0x8101, nil, valid, invalid)
	outputs := map[string]*TransactionOutput{
		transaction.Outputs[0].ID(): &transaction.Outputs[0],
		transaction.Outputs[1].ID(): &transaction.Outputs[1],
	}
	rows := []ledgerdb.OutputRow{
		{TXOID: transaction.Outputs[0].ID()},
		{TXOID: transaction.Outputs[1].ID()},
	}
	err := newTransactionChannelHydrationState(
		fixture.ledger, context.Background(), []*Account{account},
	).HydrateRows(rows, outputs)
	if !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("invalid later channel error = %v", err)
	}
	if !equalTransactionChannelHydrationKey(transaction.Outputs[0].PrivateKey, fixture.channelKey) ||
		transaction.Outputs[1].PrivateKey != nil {
		t.Fatalf("partial key mutation = first %v, second %v",
			transaction.Outputs[0].PrivateKey, transaction.Outputs[1].PrivateKey)
	}
}

func TestTransactionChannelKeyHydrationSupportsLegacyV1Channels(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	payload := transactionWireLegacyV1ChannelPayload(
		t, fixture.channelKey.PublicKey().CompressedBytes(),
	)
	legacy := transactionChannelHydrationTransaction(
		t, 0x8201, nil,
		NewClaimNameOutput(1, "@legacy", payload, bytes.Repeat([]byte{0x82}, 20)),
	)
	fixture.store(t, legacy, []uint32{0}, nil, nil)
	account, _ := transactionChannelKeyHydrationWallet(t, fixture, fixture.channelKey)
	legacyID := legacy.ID
	transactions, err := account.GetTransactions(context.Background(), TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &legacyID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 || !equalTransactionChannelHydrationKey(
		transactions[0].Outputs[0].PrivateKey, fixture.channelKey,
	) {
		t.Fatalf("legacy v1 channel key = %#v", transactions)
	}
}

func transactionChannelKeyHydrationWallet(
	t *testing.T, fixture *transactionChannelHydrationFixture, channelKey *keys.PrivateKey,
) (*Account, *Wallet) {
	t.Helper()
	account := transactionChannelKeyHydrationAccount(t, fixture, channelKey)
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	account.wallet = wallet
	return account, wallet
}

func transactionChannelKeyHydrationAccount(
	t *testing.T, fixture *transactionChannelHydrationFixture, channelKey *keys.PrivateKey,
) *Account {
	t.Helper()
	root, err := keys.PrivateKeyFromSeed(keys.MainNet, bytes.Repeat([]byte{byte(0x90 + len(fixture.ledger.Accounts))}, 32))
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{
		Network: keys.MainNet, ID: root.PublicKey().Address(), PrivateKey: root,
		PublicKey: root.PublicKey(), ChannelKeys: NewObject(), ledger: fixture.ledger,
	}
	account.DeterministicChannelKeys = NewDeterministicChannelKeyManager(account)
	fixture.ledger.Accounts = append(fixture.ledger.Accounts, account)
	if channelKey != nil {
		if err := account.AddChannelPrivateKey(channelKey); err != nil {
			t.Fatal(err)
		}
	}
	return account
}

func equalTransactionChannelHydrationKey(left, right *keys.PrivateKey) bool {
	return left != nil && right != nil && bytes.Equal(left.PrivateKeyBytes(), right.PrivateKeyBytes())
}
