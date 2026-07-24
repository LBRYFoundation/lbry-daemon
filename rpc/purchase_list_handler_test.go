package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func TestPurchaseListHandlerUsesDefaultLedgerAndEncodesOutputs(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	claimID := "00112233445566778899aabbccddeeff00112233"
	purchaseID := seedPurchaseListHandlerTransaction(t, manager, 1, 1, claimID)
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return manager
	}))

	response := performRequest(
		server, http.MethodPost, "/",
		`{"method":"purchase_list","params":{"wallet_id":"wallet-b","account_id":"b2"}}`, nil,
	)
	payload := decodeResponse(t, response)
	result := payload["result"].(map[string]any)
	items := result["items"].([]any)
	if len(items) != 1 || result["page"].(json.Number).String() != "1" ||
		result["page_size"].(json.Number).String() != "20" ||
		result["total_items"].(json.Number).String() != "1" ||
		result["total_pages"].(json.Number).String() != "1" {
		t.Fatalf("purchase page = %#v", result)
	}
	output := items[0].(map[string]any)
	if output["txid"] != purchaseID || output["type"] != "purchase" ||
		output["claim_id"] != claimID || output["amount"] != "3.0" {
		t.Fatalf("encoded purchase output = %#v", output)
	}

	filtered := performRequest(
		server, http.MethodPost, "/",
		`{"method":"purchase_list","params":{"wallet_id":"wallet-b","account_id":"b2","claim_id":"missing"}}`, nil,
	)
	filteredResult := decodeResponse(t, filtered)["result"].(map[string]any)
	if len(filteredResult["items"].([]any)) != 0 ||
		filteredResult["total_items"].(json.Number).String() != "0" {
		t.Fatalf("filtered purchase page = %#v", filteredResult)
	}

	defaultWallet := performRequest(
		server, http.MethodPost, "/", `{"method":"purchase_list"}`, nil,
	)
	defaultResult := decodeResponse(t, defaultWallet)["result"].(map[string]any)
	if len(defaultResult["items"].([]any)) != 0 {
		t.Fatalf("default-wallet purchases = %#v", defaultResult)
	}
}

func TestPurchaseListHandlerResolvesAfterPinnedSelectionValidation(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return manager
	}))

	tests := []struct {
		name     string
		body     string
		wantName string
		wantText string
	}{
		{
			"missing wallet wins", `{"method":"purchase_list","params":{"wallet_id":"missing","resolve":true}}`,
			"WalletNotLoadedError", "Wallet missing is not loaded.",
		},
		{
			"missing account wins", `{"method":"purchase_list","params":{"account_id":"missing","resolve":true}}`,
			"ValueError", "Couldn't find account: missing.",
		},
		{
			"pagination wins", `{"method":"purchase_list","params":{"page":"bad","resolve":true}}`,
			"TypeError", "'>' not supported between instances of 'str' and 'int'",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, http.MethodPost, "/", test.body, nil)
			payload := decodeResponse(t, response)
			errorObject := payload["error"].(map[string]any)
			data := errorObject["data"].(map[string]any)
			if data["name"] != test.wantName || errorObject["message"] != test.wantText {
				t.Fatalf("purchase resolve error = %#v", errorObject)
			}
		})
	}

	resolved := performRequest(
		server, http.MethodPost, "/", `{"method":"purchase_list","params":{"resolve":true}}`, nil,
	)
	result := decodeResponse(t, resolved)["result"].(map[string]any)
	if len(result["items"].([]any)) != 0 || result["total_items"] != json.Number("0") {
		t.Fatalf("empty resolved purchase page = %#v", result)
	}
}

func TestPurchaseListHandlerResolveHydratesPurchasedClaimEndToEnd(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	alpha := fixture.outputs["alpha"]
	stored, err := ledger.Database.GetTransaction(context.Background(), alpha.TXID)
	if err != nil || stored == nil {
		t.Fatalf("load alpha transaction = %#v, %v", stored, err)
	}
	transaction, err := walletpkg.ParseTransaction(stored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	ledger.SPVNetwork = &claimSearchPublicNetwork{
		searchResult: claimSearchPublicResultBase64(transaction.Hash[:], 0, 1),
		transactionRaw: map[string]string{
			alpha.TXID: hex.EncodeToString(stored.Raw),
		},
	}
	purchaseID := seedPurchaseListHandlerTransaction(
		t, fixture.manager, 0, 0, alpha.ClaimID,
	)

	result := txoHandlerOracleResult(t, fixture.request(t, "purchase_list", map[string]any{
		"resolve": true, "claim_id": alpha.ClaimID, "include_protobuf": true,
	}))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 || items[0]["txid"] != purchaseID {
		t.Fatalf("resolved purchase items = %#v", items)
	}
	claim, ok := items[0]["claim"].(map[string]any)
	if !ok || claim["txid"] != alpha.TXID || claim["name"] != "Alpha" ||
		claim["short_url"] != "lbry://alpha#short" || claim["protobuf"] == nil {
		t.Fatalf("resolved purchased claim = %#v", items[0]["claim"])
	}
}

func seedPurchaseListHandlerTransaction(
	t *testing.T, manager *walletpkg.WalletManager,
	walletIndex, accountIndex int, claimID string,
) string {
	t.Helper()
	ctx := context.Background()
	ledger := manager.DefaultLedger()
	account := manager.Wallets[walletIndex].Accounts[accountIndex]
	accountID := account.ID
	if account.PublicKey != nil {
		accountID = account.PublicKey.Address()
	}
	address := "purchase-list-buyer"
	if err := ledger.Database.AddKeys(ctx, accountID, []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}

	parent := walletpkg.NewTransaction()
	parent.LockTime = 91
	parent.AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32, Coinbase: []byte("purchase-parent"),
	}})
	parent.AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewPayPubKeyHashOutput(400_000_000, bytes.Repeat([]byte{0x31}, 20)),
	})
	if err := parent.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: parent.ID, Raw: append([]byte(nil), parent.Raw...), Height: 20, Position: 1,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: parent.Outputs[0].ID(), Address: &address, Position: 0,
			Amount: int64(parent.Outputs[0].Amount),
			Script: append([]byte(nil), parent.Outputs[0].Script.Source...),
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}

	input, err := walletpkg.NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	purchaseData, err := walletpkg.NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	purchase := walletpkg.NewTransaction()
	purchase.LockTime = 92
	purchase.AddInputs([]walletpkg.TransactionInput{input})
	purchase.AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewPayPubKeyHashOutput(300_000_000, bytes.Repeat([]byte{0x32}, 20)),
		purchaseData,
	})
	if err := purchase.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: purchase.ID, Raw: append([]byte(nil), purchase.Raw...), Height: 21, Position: 2,
			PurchasedClaimID: &claimID,
		},
		Inputs: []ledgerdb.TransactionInputRow{{
			TXOID: purchase.Inputs[0].PreviousOutputID(), Position: 0,
		}},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: purchase.Outputs[0].ID(), Position: 0,
			Amount:  int64(purchase.Outputs[0].Amount),
			Script:  append([]byte(nil), purchase.Outputs[0].Script.Source...),
			TXOType: walletpkg.TransactionOutputTypePurchase,
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
	return purchase.ID
}
