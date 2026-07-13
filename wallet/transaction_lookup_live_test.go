package wallet

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/spv"
)

func TestWalletManagerGetTransactionLiveHub(t *testing.T) {
	hubAddress := os.Getenv("LBRY_LIVE_HUB")
	txid := os.Getenv("LBRY_LIVE_TXID")
	if hubAddress == "" || txid == "" {
		t.Skip("set LBRY_LIVE_HUB and LBRY_LIVE_TXID to run the live SPV lookup")
	}
	host, portText, err := net.SplitHostPort(hubAddress)
	if err != nil {
		t.Fatalf("parse LBRY_LIVE_HUB: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse LBRY_LIVE_HUB port: %v", err)
	}

	network, err := spv.NewNetwork(spv.NetworkConfig{
		ExplicitServers: []spv.Server{{Host: host, Port: port}},
		Client: spv.ClientConfig{
			ConnectTimeout: 10 * time.Second,
			RequestTimeout: 20 * time.Second,
		},
		ReconnectDelay:   time.Second,
		VersionTimeout:   10 * time.Second,
		KeepaliveIdle:    time.Hour,
		KeepaliveTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	networkContext, cancelNetwork := context.WithCancel(context.Background())
	if err := network.Start(networkContext); err != nil {
		cancelNetwork()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelNetwork()
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := network.Stop(stopContext); err != nil {
			t.Errorf("stop live SPV network: %v", err)
		}
	})
	waitForLiveTransactionHub(t, network, 30*time.Second)

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
	lookupContext, cancelLookup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelLookup()
	if err := ledger.Database.Open(lookupContext); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ledger.Database.Close(closeContext); err != nil {
			t.Errorf("close live lookup database: %v", err)
		}
	})
	ledger.SPVNetwork = network
	account := &Account{ID: "live", ledger: ledger}
	manager := &WalletManager{Wallets: []*Wallet{
		NewWallet(WithWalletAccounts([]*Account{account})),
	}}

	result, err := manager.GetTransaction(lookupContext, txid)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != nil || result.Transaction == nil || result.Ledger != ledger {
		t.Fatalf("live lookup result = %#v", result)
	}
	transaction := result.Transaction
	if transaction.ID != txid || transaction.Height <= 0 || transaction.HeightMissing {
		t.Fatalf("live transaction = id %s height %d missing %v", transaction.ID, transaction.Height, transaction.HeightMissing)
	}
	stored, err := ledger.Database.GetTransaction(lookupContext, txid)
	if err != nil || stored != nil {
		t.Fatalf("live transaction was persisted: %#v, %v", stored, err)
	}
	wire, err := ledger.LegacyTransactionJSONWithOptions(
		transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatalf("encode supplied live transaction: %v", err)
	}
	outputs, ok := wire["outputs"].([]any)
	if !ok || len(outputs) != 2 {
		t.Fatalf("live wire outputs = %#v", wire["outputs"])
	}
	claim, ok := outputs[0].(map[string]any)
	if !ok {
		t.Fatalf("live claim wire type = %T", outputs[0])
	}
	if claim["type"] != "claim" || claim["claim_op"] != "create" ||
		claim["name"] != "T4NG3RIN-ASMR--02" || claim["normalized_name"] != "t4ng3rin-asmr--02" ||
		claim["claim_id"] != "bb9a6185f017956711c05f44d71f0bc4ae20a27a" ||
		claim["value_type"] != "stream" || claim["amount"] != "0.001" ||
		claim["protobuf"] != hex.EncodeToString(transaction.Outputs[0].Script.Claim) {
		t.Fatalf("live claim envelope = %#v", claim)
	}
	claimValue, ok := claim["value"].(map[string]any)
	if !ok || claimValue["title"] != "T4NG3R1N ASMR #03" ||
		claimValue["release_time"] != "1783777307" || claimValue["stream_type"] != "video" {
		t.Fatalf("live claim value = %#v", claim["value"])
	}
	sourceValue, ok := claimValue["source"].(map[string]any)
	if !ok || sourceValue["hash"] != "727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c16280e1bd4380dd05" ||
		sourceValue["sd_hash"] != "36d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601a58" ||
		sourceValue["size"] != "201549988" {
		t.Fatalf("live claim source = %#v", claimValue["source"])
	}

	infoValue, err := network.OneShotValue(
		lookupContext, SPVTransactionInfoMethod, []any{txid}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, merkle, err := parseTransactionInfoResult(infoValue)
	if err != nil {
		t.Fatal(err)
	}
	rawHex, err := transactionInfoRawHex(rawValue)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		t.Fatal(err)
	}
	proofTransaction, err := ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	headerValue, err := network.OneShotValue(
		lookupContext, SPVHeaderRPCMethod,
		[]any{transaction.Height, 1, 0, false}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	headerObject, ok := headerValue.(map[string]any)
	if !ok {
		t.Fatalf("live header response = %T, want object", headerValue)
	}
	headerHex, ok := headerObject["hex"].(string)
	if !ok {
		t.Fatalf("live header hex = %#v", headerObject["hex"])
	}
	headerRaw, err := hex.DecodeString(headerHex)
	if err != nil {
		t.Fatal(err)
	}
	header, err := DeserializeHeader(int(transaction.Height), headerRaw)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ApplyTransactionMerkleVerification(
		proofTransaction, transaction.Height, int(transaction.Height)+1,
		header.MerkleRoot, merkle,
	)
	if err != nil || status != TransactionMerkleMatched || !proofTransaction.IsVerified {
		t.Fatalf("live merkle verification = %q, verified %v, error %v", status, proofTransaction.IsVerified, err)
	}

	notFound, err := manager.GetTransaction(lookupContext, strings.Repeat("0", 64))
	if err != nil || notFound.Failure == nil || notFound.Failure.Code != 404 ||
		notFound.Failure.Message != "transaction not found" {
		t.Fatalf("live not-found result = %#v, %v", notFound, err)
	}
	t.Logf(
		"validated %s at height %d through %s with merkle position %d",
		txid, transaction.Height, hubAddress, proofTransaction.Position,
	)
}

func waitForLiveTransactionHub(t *testing.T, network *spv.Network, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !network.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("live SPV hub did not connect: %#v", network.Snapshot())
		}
		time.Sleep(25 * time.Millisecond)
	}
}
