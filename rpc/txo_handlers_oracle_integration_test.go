package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

type txoHandlerOracleFixture struct {
	manager   *walletpkg.WalletManager
	server    *RPCServer
	walletA   *walletpkg.Wallet
	walletB   *walletpkg.Wallet
	accountA1 *walletpkg.Account
	accountA2 *walletpkg.Account
	accountB  *walletpkg.Account
	outputs   map[string]txoHandlerOracleOutput
	labels    map[string]string
}

type txoHandlerOracleOutput struct {
	TXID    string
	ClaimID string
	Address string
	Amount  int64
}

type txoHandlerOracleMetadata struct {
	OutputType      int64
	ClaimID         string
	ClaimName       string
	HasSource       bool
	ChannelID       string
	RepostedClaimID string
}

func TestTXOHandlersAgainstPinnedOracleWithSQLite(t *testing.T) {
	oracle := runTXORPCOracle(t)
	oracleCases := make(map[string]txoRPCOracleCase, len(oracle.Cases))
	for _, fixture := range oracle.Cases {
		oracleCases[fixture.Name] = fixture
	}
	fixture := newTXOHandlerOracleFixture(t)

	t.Run("pagination and no totals", func(t *testing.T) {
		payload := fixture.request(t, "txo_list", map[string]any{"page": 2, "page_size": 2})
		result := txoHandlerOracleResult(t, payload)
		oracleResult := txoRPCDecodeObject(t, oracleCases["list middle page"].Result)
		if fmt.Sprint(result["page"]) != fmt.Sprint(oracleResult["page"]) ||
			fmt.Sprint(result["page_size"]) != fmt.Sprint(oracleResult["page_size"]) ||
			result["total_items"] != json.Number("9") || result["total_pages"] != json.Number("5") {
			t.Fatalf("paginated TXO result = %#v", result)
		}
		if items := txoHandlerOracleItems(t, result); len(items) != 2 {
			t.Fatalf("paginated items = %d, want 2", len(items))
		}
		if !reflect.DeepEqual(txoHandlerOracleMapKeys(result), txoHandlerOracleMapKeys(oracleResult)) {
			t.Fatalf("pagination envelope keys = %v, oracle %v",
				txoHandlerOracleMapKeys(result), txoHandlerOracleMapKeys(oracleResult))
		}

		payload = fixture.request(t, "txo_list", map[string]any{
			"page": 2, "page_size": 2, "no_totals": true,
		})
		result = txoHandlerOracleResult(t, payload)
		oracleResult = txoRPCDecodeObject(t, oracleCases["list no totals"].Result)
		if !reflect.DeepEqual(txoHandlerOracleMapKeys(result), txoHandlerOracleMapKeys(oracleResult)) ||
			len(txoHandlerOracleItems(t, result)) != 2 {
			t.Fatalf("no-totals result = %#v, oracle keys %v", result, txoHandlerOracleMapKeys(oracleResult))
		}

		payload = fixture.request(t, "txo_list", map[string]any{
			"page_size": 2, "no_totals": "yes",
		})
		result = txoHandlerOracleResult(t, payload)
		if _, exists := result["total_items"]; exists || len(txoHandlerOracleItems(t, result)) != 2 {
			t.Fatalf("truthy no-totals result = %#v", result)
		}
	})

	t.Run("wallet and account selection", func(t *testing.T) {
		assertLabels := func(params map[string]any, expected ...string) {
			t.Helper()
			payload := fixture.request(t, "txo_list", params)
			result := txoHandlerOracleResult(t, payload)
			if got := fixture.resultLabels(t, result); !reflect.DeepEqual(got, expected) {
				t.Fatalf("params %#v labels = %v, want %v", params, got, expected)
			}
		}
		assertLabels(map[string]any{"wallet_id": fixture.walletB.ID}, "b-main")
		assertLabels(map[string]any{
			"wallet_id": fixture.walletB.ID, "account_id": fixture.accountB.ID,
		}, "b-test")

		payload := fixture.request(t, "txo_sum", map[string]any{"wallet_id": fixture.walletB.ID})
		if got := txoHandlerOracleResultNumber(t, payload); got != 70_000_000 {
			t.Fatalf("selected-wallet sum = %d, want default-ledger 70000000", got)
		}
		payload = fixture.request(t, "txo_sum", map[string]any{
			"wallet_id": fixture.walletB.ID, "account_id": fixture.accountB.ID,
		})
		if got := txoHandlerOracleResultNumber(t, payload); got != 70_000_000 {
			t.Fatalf("selected-account sum = %d, want default-ledger 70000000", got)
		}
	})

	t.Run("scalar and list filters", func(t *testing.T) {
		tests := []struct {
			name   string
			params map[string]any
			want   []string
		}{
			{name: "scalar type", params: map[string]any{"type": "stream"}, want: []string{"alpha"}},
			{name: "list type", params: map[string]any{
				"type": []string{"channel", "collection"},
			}, want: []string{"beta", "gamma"}},
			{name: "scalar txid", params: map[string]any{
				"txid": fixture.outputs["gamma"].TXID,
			}, want: []string{"gamma"}},
			{name: "list txid", params: map[string]any{
				"txid": []string{fixture.outputs["alpha"].TXID, fixture.outputs["beta"].TXID},
			}, want: []string{"alpha", "beta"}},
			{name: "scalar claim id", params: map[string]any{
				"claim_id": fixture.outputs["alpha"].ClaimID, "type": "stream",
			}, want: []string{"alpha"}},
			{name: "list claim id", params: map[string]any{
				"claim_id": []string{fixture.outputs["beta"].ClaimID, fixture.outputs["gamma"].ClaimID},
			}, want: []string{"beta", "gamma"}},
			{name: "channel", params: map[string]any{
				"channel_id": "channel-red",
			}, want: []string{"alpha"}},
			{name: "not channel includes null", params: map[string]any{
				"not_channel_id": "channel-red",
				"type":           []string{"stream", "channel", "collection", "repost"},
			}, want: []string{"beta", "gamma", "repost"}},
			{name: "scalar name", params: map[string]any{
				"name": "Gamma",
			}, want: []string{"gamma"}},
			{name: "list name", params: map[string]any{
				"name": []string{"Alpha", "Gamma"},
				"type": []string{"stream", "collection"},
			}, want: []string{"alpha", "gamma"}},
			{name: "reposted claim", params: map[string]any{
				"reposted_claim_id": fixture.outputs["alpha"].ClaimID,
			}, want: []string{"repost"}},
			{name: "spent", params: map[string]any{
				"type": "support", "is_spent": true,
			}, want: []string{"tip-spent"}},
			{name: "not spent", params: map[string]any{
				"type": "support", "is_not_spent": true,
			}, want: []string{"tip-a1", "tip-a2"}},
			{name: "has source", params: map[string]any{
				"type": []string{"stream", "channel", "collection", "repost"}, "has_source": true,
			}, want: []string{"alpha", "repost"}},
			{name: "has no source", params: map[string]any{
				"type": []string{"stream", "channel", "collection", "repost"}, "has_no_source": true,
			}, want: []string{"beta", "gamma"}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				payload := fixture.request(t, "txo_list", test.params)
				got := fixture.resultLabels(t, txoHandlerOracleResult(t, payload))
				sort.Strings(got)
				sort.Strings(test.want)
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("labels = %v, want %v; params %#v", got, test.want, test.params)
				}
			})
		}
	})

	t.Run("ownership and internal transfer filters", func(t *testing.T) {
		tests := []struct {
			name   string
			params map[string]any
			want   []string
		}{
			{name: "input or output", params: map[string]any{
				"type": "other", "is_my_input_or_output": true,
			}, want: []string{"incoming", "internal", "sent"}},
			{name: "my input and output", params: map[string]any{
				"type": "other", "is_my_input": true, "is_my_output": true,
			}, want: []string{"internal"}},
			{name: "sent", params: map[string]any{
				"type": "other", "is_my_input": true, "is_not_my_output": true,
			}, want: []string{"sent"}},
			{name: "received", params: map[string]any{
				"type": "other", "is_not_my_input": true, "is_my_output": true,
			}, want: []string{"incoming"}},
			{name: "exclude internal", params: map[string]any{
				"type": "other", "exclude_internal_transfers": true,
			}, want: []string{"incoming"}},
			{name: "numeric flags are not exactly true", params: map[string]any{
				"type": "other", "is_my_input_or_output": 1,
			}, want: []string{"incoming", "internal"}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				payload := fixture.request(t, "txo_list", test.params)
				got := fixture.resultLabels(t, txoHandlerOracleResult(t, payload))
				sort.Strings(got)
				sort.Strings(test.want)
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("labels = %v, want %v; params %#v", got, test.want, test.params)
				}
			})
		}

		internal := fixture.singleOutput(t, map[string]any{"txid": fixture.outputs["internal"].TXID})
		if internal["is_my_input"] != true || internal["is_my_output"] != true ||
			internal["is_internal_transfer"] != true {
			t.Fatalf("internal annotations = %#v", internal)
		}
		incoming := fixture.singleOutput(t, map[string]any{"txid": fixture.outputs["incoming"].TXID})
		if incoming["is_my_input"] != false || incoming["is_my_output"] != true ||
			incoming["is_internal_transfer"] != false {
			t.Fatalf("incoming annotations = %#v", incoming)
		}
	})

	t.Run("ordering", func(t *testing.T) {
		claimTypes := []string{"stream", "channel", "collection", "repost"}
		for _, test := range []struct {
			order string
			want  []string
		}{
			{order: "height", want: []string{"Alpha", "@Beta", "Gamma", "Repost"}},
			{order: "amount", want: []string{"Repost", "Gamma", "@Beta", "Alpha"}},
			{order: "name", want: []string{"@Beta", "Alpha", "Gamma", "Repost"}},
		} {
			t.Run(test.order, func(t *testing.T) {
				payload := fixture.request(t, "txo_list", map[string]any{
					"type": claimTypes, "order_by": test.order,
				})
				items := txoHandlerOracleItems(t, txoHandlerOracleResult(t, payload))
				got := make([]string, len(items))
				for index, item := range items {
					got[index], _ = item["name"].(string)
				}
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("%s order = %v, want %v", test.order, got, test.want)
				}
			})
		}
	})

	t.Run("received tips and output wire", func(t *testing.T) {
		alphaParams := map[string]any{"txid": fixture.outputs["alpha"].TXID, "type": "stream"}
		alpha := fixture.singleOutput(t, alphaParams)
		if _, exists := alpha["received_tips"]; exists {
			t.Fatalf("unrequested received tips = %#v", alpha)
		}
		alphaParams["include_received_tips"] = true
		alpha = fixture.singleOutput(t, alphaParams)
		if alpha["received_tips"] != "0.33" {
			t.Fatalf("received tips = %#v, want 0.33", alpha["received_tips"])
		}
		for key, value := range map[string]any{
			"txid": fixture.outputs["alpha"].TXID, "nout": json.Number("0"),
			"amount": "5.0", "type": "claim", "claim_op": "create",
			"name": "Alpha", "normalized_name": "alpha", "value_type": "stream",
			"is_spent": false, "is_my_output": true, "is_my_input": true,
		} {
			if alpha[key] != value {
				t.Fatalf("alpha wire %s = %#v, want %#v; output %#v", key, alpha[key], value, alpha)
			}
		}
		if _, ok := alpha["value"].(map[string]any); !ok {
			t.Fatalf("alpha value = %#v", alpha["value"])
		}

		protobufParams := map[string]any{
			"txid": fixture.outputs["alpha"].TXID, "type": "stream", "include_protobuf": true,
		}
		if got := fixture.singleOutput(t, protobufParams)["protobuf"]; got != "000a00420746697874757265" {
			t.Fatalf("claim protobuf = %#v", got)
		}

		incoming := fixture.singleOutput(t, map[string]any{"txid": fixture.outputs["incoming"].TXID})
		wantKeys := []string{
			"address", "amount", "confirmations", "height", "is_internal_transfer",
			"is_my_input", "is_my_output", "is_spent", "nout", "timestamp", "txid", "type",
		}
		if got := txoHandlerOracleMapKeys(incoming); !reflect.DeepEqual(got, wantKeys) {
			t.Fatalf("payment wire keys = %v, want %v; output %#v", got, wantKeys, incoming)
		}
		if incoming["amount"] != "0.3" || incoming["address"] != fixture.outputs["incoming"].Address ||
			incoming["type"] != "payment" || incoming["height"] != json.Number("0") ||
			incoming["confirmations"] != json.Number("0") || incoming["timestamp"] != nil {
			t.Fatalf("payment wire = %#v", incoming)
		}
	})

	t.Run("claim channel stream and support wrappers", func(t *testing.T) {
		tests := []struct {
			name   string
			method string
			params map[string]any
			want   []string
		}{
			{name: "claim defaults", method: "claim_list", want: []string{"alpha", "beta", "gamma", "repost"}},
			{name: "claim type", method: "claim_list", params: map[string]any{
				"claim_type": "stream",
			}, want: []string{"alpha"}},
			{name: "channel", method: "channel_list", want: []string{"beta"}},
			{name: "stream", method: "stream_list", want: []string{"alpha"}},
			{name: "support defaults", method: "support_list", want: []string{"tip-a1", "tip-a2"}},
			{name: "support received", method: "support_list", params: map[string]any{
				"received": true,
			}, want: []string{"tip-a2"}},
			{name: "support sent", method: "support_list", params: map[string]any{
				"sent": true,
			}, want: []string{"tip-sent"}},
			{name: "support staked", method: "support_list", params: map[string]any{
				"staked": true,
			}, want: []string{"tip-a1"}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				payload := fixture.request(t, test.method, test.params)
				got := fixture.resultLabels(t, txoHandlerOracleResult(t, payload))
				sort.Strings(got)
				sort.Strings(test.want)
				if !reflect.DeepEqual(got, test.want) {
					t.Fatalf("%s labels = %v, want %v", test.method, got, test.want)
				}
			})
		}
	})

	t.Run("sum filters", func(t *testing.T) {
		tests := []struct {
			params map[string]any
			want   int64
		}{
			{params: map[string]any{}, want: 1_297_000_000},
			{params: map[string]any{"type": "stream"}, want: 500_000_000},
			{params: map[string]any{"type": []string{"stream", "channel"}}, want: 800_000_000},
			{params: map[string]any{"account_id": fixture.accountA1.ID}, want: 781_000_000},
			{params: map[string]any{"type": "support", "is_not_spent": true}, want: 33_000_000},
			{params: map[string]any{
				"type": "other", "is_my_input_or_output": true,
			}, want: 90_000_000},
			{params: map[string]any{
				"type": "other", "exclude_internal_transfers": true,
			}, want: 30_000_000},
		}
		for _, test := range tests {
			payload := fixture.request(t, "txo_sum", test.params)
			if got := txoHandlerOracleResultNumber(t, payload); got != test.want {
				t.Fatalf("sum params %#v = %d, want %d", test.params, got, test.want)
			}
		}
	})

	t.Run("oracle errors and precedence", func(t *testing.T) {
		tests := []struct {
			oracleName string
			method     string
			params     map[string]any
		}{
			{oracleName: "list invalid order", method: "txo_list", params: map[string]any{"order_by": "txid"}},
			{oracleName: "list invalid type", method: "txo_list", params: map[string]any{"type": "video"}},
			{oracleName: "list unknown filter", method: "txo_list", params: map[string]any{"height": 10}},
			{oracleName: "list missing wallet", method: "txo_list", params: map[string]any{"wallet_id": "missing"}},
			{oracleName: "list missing account", method: "txo_list", params: map[string]any{"account_id": "missing"}},
			{oracleName: "list wallet error precedes order", method: "txo_list", params: map[string]any{
				"wallet_id": "missing", "order_by": "txid", "page": "2",
			}},
			{oracleName: "list account error precedes type", method: "txo_list", params: map[string]any{
				"account_id": "missing", "type": "video", "page": "2",
			}},
			{oracleName: "sum invalid type", method: "txo_sum", params: map[string]any{"type": "video"}},
			{oracleName: "sum unknown filter", method: "txo_sum", params: map[string]any{"resolve": true}},
			{oracleName: "sum missing wallet", method: "txo_sum", params: map[string]any{"wallet_id": "missing"}},
			{oracleName: "sum missing account", method: "txo_sum", params: map[string]any{"account_id": "missing"}},
		}
		for _, test := range tests {
			t.Run(test.oracleName, func(t *testing.T) {
				payload := fixture.request(t, test.method, test.params)
				txoHandlerOracleAssertError(t, payload, oracleCases[test.oracleName].Error)
			})
		}

		payload := fixture.request(t, "txo_list", map[string]any{
			"type": "video", "order_by": "txid",
		})
		txoHandlerOracleAssertErrorNameMessage(
			t, payload, "ValueError", "'txid' is not a valid --order_by value.",
		)
	})
}

func TestSupportListResolveHydratesSigningChannelEndToEnd(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	beta := fixture.outputs["beta"]
	stored, err := ledger.Database.GetTransaction(context.Background(), beta.TXID)
	if err != nil || stored == nil {
		t.Fatalf("load beta transaction = %#v, %v", stored, err)
	}
	transaction, err := walletpkg.ParseTransaction(stored.Raw)
	if err != nil {
		t.Fatal(err)
	}
	network := &claimSearchPublicNetwork{
		searchResult: claimSearchPublicResultBase64(transaction.Hash[:], 0, 1),
		transactionRaw: map[string]string{
			beta.TXID: hex.EncodeToString(stored.Raw),
		},
	}
	ledger.SPVNetwork = network
	displayChannelHash, err := hex.DecodeString(beta.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	channelHash := make([]byte, len(displayChannelHash))
	for index := range displayChannelHash {
		channelHash[len(displayChannelHash)-1-index] = displayChannelHash[index]
	}
	supportPayload := append([]byte{1}, channelHash...)
	supportPayload = append(supportPayload, bytes.Repeat([]byte{1}, 64)...)
	support, err := walletpkg.NewSupportDataOutput(
		13_000_000, "Alpha", fixture.outputs["alpha"].ClaimID,
		supportPayload, bytes.Repeat([]byte{0xa1}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	storedSupport := txoHandlerOraclePersist(
		t, ledger, "signed-support", 620, 9, 9, support,
		fixture.outputs["alpha"].Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeSupport,
			ClaimID:    fixture.outputs["alpha"].ClaimID,
			ClaimName:  "Alpha",
		},
	)

	result := txoHandlerOracleResult(t, fixture.request(t, "support_list", map[string]any{
		"resolve": true, "txid": storedSupport.TXID, "include_protobuf": true,
	}))
	items := txoHandlerOracleItems(t, result)
	if len(items) != 1 || items[0]["protobuf"] != hex.EncodeToString(supportPayload) ||
		items[0]["is_channel_signature_valid"] != false {
		t.Fatalf("resolved signed support = %#v", items)
	}
	channel, ok := items[0]["signing_channel"].(map[string]any)
	if !ok || channel["txid"] != beta.TXID || channel["name"] != "@Beta" {
		t.Fatalf("resolved support channel = %#v", items[0]["signing_channel"])
	}
	named, _ := network.snapshotCalls()
	if len(named) != 1 || !reflect.DeepEqual(
		named[0].params, map[string]any{"claim_ids": []string{beta.ClaimID}},
	) {
		t.Fatalf("support channel lookup calls = %#v", named)
	}
}

func newTXOHandlerOracleFixture(t *testing.T) *txoHandlerOracleFixture {
	t.Helper()
	ctx := context.Background()
	manager := walletpkg.NewWalletManager()
	root := t.TempDir()
	mainLedger := txoHandlerOracleLedger(t, manager, keys.MainNet, root)
	testLedger := txoHandlerOracleLedger(t, manager, keys.TestNet, root)
	a1 := txoHandlerOracleAccount(t, keys.MainNet, 0x11, "a1")
	a2 := txoHandlerOracleAccount(t, keys.MainNet, 0x22, "a2")
	b1 := txoHandlerOracleAccount(t, keys.TestNet, 0x33, "b1")
	walletA := walletpkg.NewWallet(
		walletpkg.WithWalletName("wallet-a"), walletpkg.WithWalletAccounts([]*walletpkg.Account{a1, a2}),
	)
	walletB := walletpkg.NewWallet(
		walletpkg.WithWalletName("wallet-b"), walletpkg.WithWalletAccounts([]*walletpkg.Account{b1}),
	)
	manager.Wallets = []*walletpkg.Wallet{walletA, walletB}
	for _, registration := range []struct {
		network keys.Network
		account *walletpkg.Account
	}{{keys.MainNet, a1}, {keys.MainNet, a2}, {keys.TestNet, b1}} {
		if err := manager.RegisterAccount(registration.network.ID(), registration.account); err != nil {
			t.Fatal(err)
		}
	}

	a1Hash, a2Hash := bytes.Repeat([]byte{0xa1}, 20), bytes.Repeat([]byte{0xa2}, 20)
	bHash, foreignHash := bytes.Repeat([]byte{0xb1}, 20), bytes.Repeat([]byte{0xf1}, 20)
	a1Address := txoHandlerOracleOutputAddress(t, mainLedger, walletpkg.NewPayPubKeyHashOutput(1, a1Hash))
	a2Address := txoHandlerOracleOutputAddress(t, mainLedger, walletpkg.NewPayPubKeyHashOutput(1, a2Hash))
	bMainAddress := txoHandlerOracleOutputAddress(t, mainLedger, walletpkg.NewPayPubKeyHashOutput(1, bHash))
	bTestAddress := txoHandlerOracleOutputAddress(t, testLedger, walletpkg.NewPayPubKeyHashOutput(1, bHash))
	foreignAddress := txoHandlerOracleOutputAddress(t, mainLedger, walletpkg.NewPayPubKeyHashOutput(1, foreignHash))
	for index, owned := range []struct {
		ledger  *walletpkg.Ledger
		account *walletpkg.Account
		address string
	}{{mainLedger, a1, a1Address}, {mainLedger, a2, a2Address},
		{mainLedger, b1, bMainAddress}, {testLedger, b1, bTestAddress}} {
		if err := owned.ledger.Database.AddKeys(ctx, owned.account.PublicKey.Address(), []ledgerdb.AddressKey{{
			Address: owned.address, PublicKey: []byte{byte(index + 1)}, ChainCode: []byte{byte(index + 11)},
		}}); err != nil {
			t.Fatal(err)
		}
	}

	streamPayload := txoHandlerOracleMustHex(t, "000a00420746697874757265")
	outputs := make(map[string]txoHandlerOracleOutput)
	labels := make(map[string]string)
	persist := func(
		ledger *walletpkg.Ledger, label string, nonce uint32, height, position int64,
		output walletpkg.TransactionOutput, address, createdBy string, metadata txoHandlerOracleMetadata,
	) txoHandlerOracleOutput {
		t.Helper()
		stored := txoHandlerOraclePersist(
			t, ledger, label, nonce, height, position, output, address, createdBy, metadata,
		)
		outputs[label], labels[stored.TXID] = stored, label
		return stored
	}

	alpha := persist(mainLedger, "alpha", 101, 8, 8,
		walletpkg.NewClaimNameOutput(500_000_000, "Alpha", streamPayload, a1Hash),
		a1Address, a1Address, txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeStream, ClaimName: "Alpha",
			HasSource: true, ChannelID: "channel-red",
		})
	channelSPKI := txoHandlerOracleMustHex(t,
		"3056301006072a8648ce3d020106052b8104000a03420004"+
			"d7fa13fd8e57f3a0b878eaaf3d179144d25ddbe4a3e4440a661f51b4134c6a13"+
			"c9c98678ff8411932e60fd97d7baf03ea67ebcc21097230cfb2241348aadb55e",
	)
	channelPayload := append([]byte{0, 0x12, 0x5a, 0x0a, 0x58}, channelSPKI...)
	persist(mainLedger, "beta", 102, 7, 7,
		walletpkg.NewClaimNameOutput(300_000_000, "@Beta", channelPayload, a2Hash),
		a2Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeChannel, ClaimName: "@Beta",
		})
	persist(mainLedger, "gamma", 103, 6, 6,
		walletpkg.NewClaimNameOutput(200_000_000, "Gamma", txoHandlerOracleMustHex(t, "001a00"), a1Hash),
		a1Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeCollection, ClaimName: "Gamma",
			ChannelID: "channel-blue",
		})
	repostPayload := append(txoHandlerOracleMustHex(t, "0022160a14"), bytes.Repeat([]byte{0x44}, 20)...)
	persist(mainLedger, "repost", 104, 5, 5,
		walletpkg.NewClaimNameOutput(150_000_000, "Repost", repostPayload, a2Hash),
		a2Address, "", txoHandlerOracleMetadata{
			OutputType: walletpkg.TransactionOutputTypeRepost, ClaimName: "Repost",
			HasSource: true, RepostedClaimID: alpha.ClaimID,
		})

	tipA1, err := walletpkg.NewSupportOutput(11_000_000, "Alpha", alpha.ClaimID, a1Hash)
	if err != nil {
		t.Fatal(err)
	}
	persist(mainLedger, "tip-a1", 105, 4, 4, tipA1, a1Address, a1Address,
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeSupport, ClaimID: alpha.ClaimID, ClaimName: "Alpha"})
	tipA2, err := walletpkg.NewSupportOutput(22_000_000, "Alpha", alpha.ClaimID, a2Hash)
	if err != nil {
		t.Fatal(err)
	}
	persist(mainLedger, "tip-a2", 106, 3, 3, tipA2, a2Address, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeSupport, ClaimID: alpha.ClaimID, ClaimName: "Alpha"})
	foreignTip, err := walletpkg.NewSupportOutput(33_000_000, "Alpha", alpha.ClaimID, foreignHash)
	if err != nil {
		t.Fatal(err)
	}
	persist(mainLedger, "tip-foreign", 107, 3, 2, foreignTip, foreignAddress, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeSupport, ClaimID: alpha.ClaimID, ClaimName: "Alpha"})
	sentTip, err := walletpkg.NewSupportOutput(5_000_000, "Alpha", alpha.ClaimID, foreignHash)
	if err != nil {
		t.Fatal(err)
	}
	persist(mainLedger, "tip-sent", 115, 3, 1, sentTip, foreignAddress, a1Address,
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeSupport, ClaimID: alpha.ClaimID, ClaimName: "Alpha"})
	spentTipOutput, err := walletpkg.NewSupportOutput(44_000_000, "Alpha", alpha.ClaimID, a2Hash)
	if err != nil {
		t.Fatal(err)
	}
	spentTip := persist(mainLedger, "tip-spent", 108, 2, 2, spentTipOutput, a2Address, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeSupport, ClaimID: alpha.ClaimID, ClaimName: "Alpha"})
	txoHandlerOracleMarkSpent(t, mainLedger, spentTip.TXID+":0", a2Address, 900)

	persist(mainLedger, "internal", 109, 1, 3,
		walletpkg.NewPayPubKeyHashOutput(40_000_000, a1Hash), a1Address, a1Address,
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})
	persist(mainLedger, "incoming", 110, 0, 2,
		walletpkg.NewPayPubKeyHashOutput(30_000_000, a1Hash), a1Address, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})
	persist(mainLedger, "sent", 111, 0, 1,
		walletpkg.NewPayPubKeyHashOutput(20_000_000, foreignHash), foreignAddress, a1Address,
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})
	persist(mainLedger, "foreign", 112, -1, 0,
		walletpkg.NewPayPubKeyHashOutput(10_000_000, foreignHash), foreignAddress, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})
	persist(mainLedger, "b-main", 113, 9, 9,
		walletpkg.NewPayPubKeyHashOutput(70_000_000, bHash), bMainAddress, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})
	persist(testLedger, "b-test", 114, 9, 9,
		walletpkg.NewPayPubKeyHashOutput(80_000_000, bHash), bTestAddress, "",
		txoHandlerOracleMetadata{OutputType: walletpkg.TransactionOutputTypeOther})

	fixture := &txoHandlerOracleFixture{
		manager: manager, walletA: walletA, walletB: walletB,
		accountA1: a1, accountA2: a2, accountB: b1, outputs: outputs, labels: labels,
	}
	fixture.server = CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return fixture.manager
	}))
	return fixture
}

func txoHandlerOracleLedger(
	t *testing.T, manager *walletpkg.WalletManager, network keys.Network, root string,
) *walletpkg.Ledger {
	t.Helper()
	ledger, err := manager.GetOrCreateLedger(network.ID(), walletpkg.LedgerConfig{"data_path": root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger.Database.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Database.Close(context.Background()); err != nil {
			t.Errorf("close %s TXO handler database: %v", network.ID(), err)
		}
	})
	return ledger
}

func txoHandlerOracleAccount(
	t *testing.T, network keys.Network, seedByte byte, accountID string,
) *walletpkg.Account {
	t.Helper()
	privateKey, err := keys.PrivateKeyFromSeed(network, bytes.Repeat([]byte{seedByte}, 32))
	if err != nil {
		t.Fatal(err)
	}
	account, err := walletpkg.NewAccount(network, walletpkg.NewObject(
		walletpkg.Member{Key: "name", Value: accountID},
		walletpkg.Member{Key: "public_key", Value: privateKey.PublicKey().ExtendedKeyString()},
		walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(
			walletpkg.Member{Key: "name", Value: walletpkg.SingleAddressGenerator},
		)},
		walletpkg.Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	account.ID = accountID
	return account
}

func txoHandlerOraclePersist(
	t *testing.T, ledger *walletpkg.Ledger, label string, nonce uint32, height, position int64,
	output walletpkg.TransactionOutput, address, createdBy string, metadata txoHandlerOracleMetadata,
) txoHandlerOracleOutput {
	t.Helper()
	transaction := walletpkg.NewTransaction()
	transaction.LockTime = nonce
	transaction.AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32,
		Coinbase: []byte(label),
	}})
	transaction.AddOutputs([]walletpkg.TransactionOutput{output})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	claimID := metadata.ClaimID
	if claimID == "" && transaction.Outputs[0].Script.IsClaimName() {
		var err error
		claimID, err = transaction.Outputs[0].ClaimID()
		if err != nil {
			t.Fatal(err)
		}
	}
	claimName := metadata.ClaimName
	if claimName == "" && transaction.Outputs[0].Script.IsClaimInvolved() {
		claimName = string(transaction.Outputs[0].Script.ClaimName)
	}
	row := ledgerdb.TransactionIORow{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: height, Position: position, IsVerified: height > 0,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
			Amount:  int64(transaction.Outputs[0].Amount),
			Script:  append([]byte(nil), transaction.Outputs[0].Script.Source...),
			TXOType: metadata.OutputType, HasSource: metadata.HasSource,
		}},
	}
	if claimID != "" {
		row.Outputs[0].ClaimID = &claimID
	}
	if claimName != "" {
		row.Outputs[0].ClaimName = &claimName
	}
	if metadata.ChannelID != "" {
		row.Outputs[0].ChannelID = &metadata.ChannelID
	}
	if metadata.RepostedClaimID != "" {
		row.Outputs[0].RepostedClaimID = &metadata.RepostedClaimID
	}
	if createdBy != "" {
		row.Inputs = []ledgerdb.TransactionInputRow{{TXOID: "fund-" + label, Position: 0}}
	}
	if err := ledger.Database.SaveTransactionIOBatch(
		context.Background(), []ledgerdb.TransactionIORow{row}, createdBy, "",
	); err != nil {
		t.Fatal(err)
	}
	return txoHandlerOracleOutput{
		TXID: transaction.ID, ClaimID: claimID, Address: address,
		Amount: int64(transaction.Outputs[0].Amount),
	}
}

func txoHandlerOracleMarkSpent(
	t *testing.T, ledger *walletpkg.Ledger, txoid, address string, nonce uint32,
) {
	t.Helper()
	transaction := walletpkg.NewTransaction()
	transaction.LockTime = nonce
	transaction.AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32, Coinbase: []byte("spender"),
	}})
	transaction.AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewReturnDataOutput([]byte("spent")),
	})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.SaveTransactionIOBatch(context.Background(), []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{TXID: transaction.ID, Raw: transaction.Raw, Height: 10},
		Inputs:      []ledgerdb.TransactionInputRow{{TXOID: txoid, Position: 0}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
}

func txoHandlerOracleOutputAddress(
	t *testing.T, ledger *walletpkg.Ledger, output walletpkg.TransactionOutput,
) string {
	t.Helper()
	address, err := output.Address(ledger.Network)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func txoHandlerOracleMustHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func (fixture *txoHandlerOracleFixture) request(
	t *testing.T, method string, params map[string]any,
) map[string]any {
	t.Helper()
	request := map[string]any{"method": method}
	if params != nil {
		request["params"] = params
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.server, http.MethodPost, "/", string(body), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("%s HTTP status = %d, body %s", method, response.Code, response.Body.String())
	}
	payload := decodeResponse(t, response)
	if payload["jsonrpc"] != "2.0" {
		t.Fatalf("%s JSON-RPC version = %#v", method, payload["jsonrpc"])
	}
	if _, exists := payload["id"]; exists {
		t.Fatalf("%s legacy response includes id: %#v", method, payload)
	}
	return payload
}

func (fixture *txoHandlerOracleFixture) resultLabels(
	t *testing.T, result map[string]any,
) []string {
	t.Helper()
	items := txoHandlerOracleItems(t, result)
	labels := make([]string, len(items))
	for index, item := range items {
		txid, _ := item["txid"].(string)
		label, exists := fixture.labels[txid]
		if !exists {
			t.Fatalf("unknown TXO response txid %q: %#v", txid, item)
		}
		labels[index] = label
	}
	return labels
}

func (fixture *txoHandlerOracleFixture) singleOutput(
	t *testing.T, params map[string]any,
) map[string]any {
	t.Helper()
	items := txoHandlerOracleItems(t, txoHandlerOracleResult(t, fixture.request(t, "txo_list", params)))
	if len(items) != 1 {
		t.Fatalf("single TXO params %#v returned %d items: %#v", params, len(items), items)
	}
	return items[0]
}

func txoHandlerOracleResult(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	if errorObject, exists := payload["error"]; exists {
		t.Fatalf("unexpected TXO RPC error: %#v", errorObject)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("TXO RPC result = %#v, want object", payload["result"])
	}
	return result
}

func txoHandlerOracleResultNumber(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	if errorObject, exists := payload["error"]; exists {
		t.Fatalf("unexpected TXO RPC error: %#v", errorObject)
	}
	number, ok := payload["result"].(json.Number)
	if !ok {
		t.Fatalf("TXO RPC scalar result = %#v", payload["result"])
	}
	value, err := number.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func txoHandlerOracleItems(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	values, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("TXO items = %#v", result["items"])
	}
	items := make([]map[string]any, len(values))
	for index, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("TXO item %d = %#v", index, value)
		}
		items[index] = item
	}
	return items
}

func txoHandlerOracleMapKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func txoHandlerOracleAssertError(
	t *testing.T, payload map[string]any, expected *txoRPCOracleError,
) {
	t.Helper()
	if expected == nil {
		t.Fatal("pinned oracle case has no error")
	}
	txoHandlerOracleAssertErrorNameMessage(t, payload, expected.Data.Name, expected.Message)
	errorObject := payload["error"].(map[string]any)
	if errorObject["code"] != json.Number(fmt.Sprint(expected.Code)) {
		t.Fatalf("TXO error code = %#v, want %d", errorObject["code"], expected.Code)
	}
	data := errorObject["data"].(map[string]any)
	if data["command"] != expected.Data.Command {
		t.Fatalf("TXO error command = %#v, want %q", data["command"], expected.Data.Command)
	}
}

func txoHandlerOracleAssertErrorNameMessage(
	t *testing.T, payload map[string]any, name, message string,
) {
	t.Helper()
	if _, exists := payload["result"]; exists {
		t.Fatalf("TXO error response includes result: %#v", payload)
	}
	errorObject, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("TXO error object = %#v", payload["error"])
	}
	data, ok := errorObject["data"].(map[string]any)
	if !ok || data["name"] != name || errorObject["message"] != message {
		t.Fatalf("TXO error = %#v, want %s %q", errorObject, name, message)
	}
	traceback, ok := data["traceback"].([]any)
	if !ok || len(traceback) == 0 {
		t.Fatalf("TXO error traceback = %#v", data["traceback"])
	}
}
