package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"lbry/daemon/wallet/ledgerdb"
)

const (
	transactionSummaryOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionSummaryOraclePinnedVersion = "0.113.0"
)

var transactionSummaryOraclePinnedSources = map[string]string{
	"lbry/__init__.py":           "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/dewies.py":      "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
	"lbry/wallet/ledger.py":      "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
	"lbry/wallet/util.py":        "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
}

var transactionSummaryOraclePinnedMethods = map[string]string{
	"Input.amount":                         "ab8d93c4660ea7e42b32857b490008d04182917e50d8a83cef0fcb7f130f7e86",
	"Input.is_my_input":                    "1966d883a99c707deaa0ae1c47b43faaa3c25819a6c67f35f748b63447ee63a8",
	"Ledger.get_transaction_history":       "4408177ae1675b712065a212f704c21bfed9deacf63865912cbaba74d853c57b",
	"Ledger.get_transaction_history_count": "e07b9cf91656d840be3445202d9dd17261a9353267049ec3c585ea4c87573bce",
	"Output.claim_hash":                    "f1ad49634a725bc8bbd1a0f1a8d4e5a46ae4abf6b4fec56a7f59566a7f14c462",
	"Output.claim_id":                      "634d3a6a1c74ba1e666f93d345007fb3be4544496c72bb3c0f89510b883ab98a",
	"Output.claim_name":                    "c1c0ec08ebb4716c45bf7a07b02f92d52e0d578aecd51155c1df28cbadda047c",
	"Output.get_address":                   "031f5c186213ba42ed354461e31d3d7075fda2cb285384485077f4e7adab1e8e",
	"Output.is_claim":                      "cfee1c756e3c11e46a1ef913c1ab0c4467a9f8aabe438f62536df483cc49e48e",
	"Output.is_pubkey_hash":                "9ee9f33bac7e1e6fbd748dd79737073f799a7e64e516b81866c363d538b1f4d9",
	"Output.is_script_hash":                "bd52941646cbd63eb7eb2df43c0c9708938f26045b1293b9f3d262eb565d0773",
	"Output.pubkey_hash":                   "d693277604cac1e0861ab6ddf0655fac721f17c235f0aabcc9e9f6999df90099",
	"Output.purchased_claim_id":            "f2737848aa8850ab501dbbe429204a0b5f4d1bf9bae37f17dd91c0e4739375bf",
	"Output.script_hash":                   "54452f794077f1418dd41d56bc844bfaf44b3be0d422465d73b40ebdf0191a3f",
	"Transaction._filter_any_outputs":      "8b9f8d4abfe5323f30f5bc80f047bb05befc9babe3989e331a1ae19b97737e84",
	"Transaction._filter_my_outputs":       "7a0f573a6ad3eddfa3e9d5c799199e13baa0332473edc00cf77969ba9082c7f2",
	"Transaction._filter_other_outputs":    "91108966d00ce81cdf2bfdb2c4b4f9711c47d94b08d24a232d020e68c7297a55",
	"Transaction.any_purchase_outputs":     "f23fc63ae00d731bd1099e89b7c2cb09e5798fe272526a99a6667440e9bc19d0",
	"Transaction.fee":                      "6ac5b3a88a8bdf8a1d219c56ba0fee4e547c2d3ffa1c2cdff7ee35934e9eb608",
	"Transaction.input_sum":                "77c367ccf71154a325ebcef4f0731d04ddaccd41d3f5a0c90343fa6834d38295",
	"Transaction.my_abandon_outputs":       "acd69a62ebc395d9fb3402e624e5e454c1b5f4797e42aecaba8449e8bb4c71c5",
	"Transaction.my_claim_outputs":         "21c9abc95568752c6d3f233c5cc4a3ee058e794f15edb7b22654219967b2cbda",
	"Transaction.my_support_outputs":       "0486220c86b1a036c911cdfb1ceedd4d6cb4662d6035bd3e2a9907e04fddcb09",
	"Transaction.my_update_outputs":        "ffbae1348f309b203f85f3b1c2b3fc3f26b9cda6f1e29376508304f34fa46b3f",
	"Transaction.net_account_balance":      "2fde71a92ab994330517e7f18c88be9805c6a1a3f4f2d5697bfc4079bc050706",
	"Transaction.other_support_outputs":    "f73e063377668f697ec8f1f513030ffa38d8641c429aada4c8d37a3c84faa76f",
	"Transaction.output_sum":               "2dc8ce7917177c03dc33b87124b9106edafaa2dd6619455c44d04c974d7cf0c8",
	"dewies_to_lbc":                        "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
	"satoshis_to_coins":                    "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

type transactionSummaryOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		StdlibSQLiteUsed         bool   `json:"stdlib_sqlite_used"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
		Timezone                 string `json:"timezone"`
		FixtureTransactions      int    `json:"fixture_transactions"`
	} `json:"metadata"`
	TransactionIDs []string                               `json:"transaction_ids"`
	History        []transactionSummaryOracleHistoryItem  `json:"history"`
	Count          int64                                  `json:"count"`
	DatabaseCalls  []transactionSummaryOracleDatabaseCall `json:"database_calls"`
}

type transactionSummaryOracleDatabaseCall struct {
	Method      string         `json:"method"`
	Constraints map[string]any `json:"constraints"`
}

type transactionSummaryOracleHistoryItem struct {
	TXID          string                                 `json:"txid"`
	Timestamp     *int64                                 `json:"timestamp"`
	Date          *string                                `json:"date"`
	Confirmations int64                                  `json:"confirmations"`
	Value         string                                 `json:"value"`
	Fee           string                                 `json:"fee"`
	ClaimInfo     []transactionSummaryOracleClaimInfo    `json:"claim_info"`
	UpdateInfo    []transactionSummaryOracleClaimInfo    `json:"update_info"`
	SupportInfo   []transactionSummaryOracleSupportInfo  `json:"support_info"`
	AbandonInfo   []transactionSummaryOracleAbandonInfo  `json:"abandon_info"`
	PurchaseInfo  []transactionSummaryOraclePurchaseInfo `json:"purchase_info"`
}

type transactionSummaryOracleClaimInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

type transactionSummaryOracleSupportInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	IsTip        bool    `json:"is_tip"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

type transactionSummaryOracleAbandonInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	ClaimName    string  `json:"claim_name"`
	NOut         uint32  `json:"nout"`
}

type transactionSummaryOraclePurchaseInfo struct {
	Address      *string `json:"address"`
	BalanceDelta string  `json:"balance_delta"`
	Amount       string  `json:"amount"`
	ClaimID      string  `json:"claim_id"`
	NOut         uint32  `json:"nout"`
	IsSpent      *bool   `json:"is_spent"`
}

func TestTransactionSummaryMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionSummaryOracle(t)
	if oracle.Reference.Commit != transactionSummaryOraclePinnedCommit ||
		oracle.Reference.Version != transactionSummaryOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionSummaryOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionSummaryOraclePinnedMethods) {
		t.Fatalf("transaction summary oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || oracle.Metadata.StdlibSQLiteUsed ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.Timezone != "UTC" ||
		oracle.Metadata.FixtureTransactions != 6 {
		t.Fatalf("transaction summary oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction summary Python version = %q, want %q",
			oracle.Metadata.PythonVersion, want)
	}
	assertTransactionSummaryOracleContract(t, oracle)

	ledger, transactionIDs := transactionSummaryOracleFixture(t)
	if !reflect.DeepEqual(transactionIDs, oracle.TransactionIDs) {
		t.Fatalf("Go transaction summary IDs = %v, Python %v", transactionIDs, oracle.TransactionIDs)
	}
	zero, six := 0, 6
	query := ledgerdb.TransactionQuery{
		TXIDs: append([]string(nil), transactionIDs...), Offset: &zero, Limit: &six,
	}
	previousLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previousLocal })
	items, err := ledger.GetTransactionHistory(context.Background(), TransactionHistoryOptions{
		Query: query, AnnotationAccountIDs: []string{"account-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var got []transactionSummaryOracleHistoryItem
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, oracle.History) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(oracle.History, "", "  ")
		t.Fatalf("Go transaction summary differs from Python\nGo: %s\nPython: %s", gotJSON, wantJSON)
	}
	count, err := ledger.GetTransactionHistoryCount(context.Background(), query)
	if err != nil || count != oracle.Count {
		t.Fatalf("Go transaction summary count = %d, %v; Python %d", count, err, oracle.Count)
	}
}

func assertTransactionSummaryOracleContract(t *testing.T, oracle transactionSummaryOracleResponse) {
	t.Helper()
	if len(oracle.History) != 6 || oracle.Count != 6 || len(oracle.DatabaseCalls) != 2 {
		t.Fatalf("Python transaction summary fixture shape = history %d, count %d, calls %d",
			len(oracle.History), oracle.Count, len(oracle.DatabaseCalls))
	}
	if oracle.DatabaseCalls[0].Method != "get_transactions" ||
		oracle.DatabaseCalls[0].Constraints["include_is_my_output"] != true ||
		oracle.DatabaseCalls[0].Constraints["include_is_spent"] != true ||
		oracle.DatabaseCalls[0].Constraints["read_only"] != true ||
		oracle.DatabaseCalls[1].Method != "get_transaction_count" ||
		oracle.DatabaseCalls[1].Constraints["read_only"] != true {
		t.Fatalf("Python transaction summary database calls = %+v", oracle.DatabaseCalls)
	}
	incoming := oracle.History[0]
	if incoming.Timestamp != nil || incoming.Date != nil || incoming.Confirmations != 0 ||
		incoming.Value != "2.3" || incoming.Fee != "0.0" ||
		len(incoming.UpdateInfo) != 1 || incoming.UpdateInfo[0].BalanceDelta != "0.0" ||
		len(incoming.SupportInfo) != 1 || !incoming.SupportInfo[0].IsTip ||
		incoming.SupportInfo[0].BalanceDelta != "1.0" || len(incoming.PurchaseInfo) != 1 ||
		incoming.PurchaseInfo[0].BalanceDelta != "0.7" {
		t.Fatalf("Python incoming transaction summary = %+v", incoming)
	}
	claim, update, abandon := oracle.History[1], oracle.History[2], oracle.History[3]
	if claim.Timestamp == nil || claim.Date == nil || claim.Confirmations != 11 ||
		claim.Value != "0.0" || claim.Fee != "-0.0001" || len(claim.ClaimInfo) != 1 ||
		claim.ClaimInfo[0].BalanceDelta != "-2.0" || claim.ClaimInfo[0].IsSpent == nil ||
		!*claim.ClaimInfo[0].IsSpent || len(update.UpdateInfo) != 1 ||
		update.UpdateInfo[0].BalanceDelta != "1.5" || len(update.AbandonInfo) != 0 ||
		len(abandon.AbandonInfo) != 2 || abandon.AbandonInfo[0].BalanceDelta != "0.5" ||
		abandon.AbandonInfo[1].BalanceDelta != "1.0" {
		t.Fatalf("Python claim/update/abandon summaries = %+v / %+v / %+v", claim, update, abandon)
	}
	supports := oracle.History[4]
	if len(supports.SupportInfo) != 2 || supports.SupportInfo[0].NOut != 1 ||
		supports.SupportInfo[0].IsTip || supports.SupportInfo[0].BalanceDelta != "-1.0" ||
		supports.SupportInfo[1].NOut != 0 || !supports.SupportInfo[1].IsTip ||
		supports.SupportInfo[1].BalanceDelta != "-2.0" || supports.Value != "-2.0" {
		t.Fatalf("Python outgoing support summary = %+v", supports)
	}
	purchase := oracle.History[5]
	if purchase.Timestamp != nil || purchase.Date != nil || purchase.Value != "-3.0" ||
		purchase.Fee != "-0.0001" || len(purchase.PurchaseInfo) != 1 ||
		purchase.PurchaseInfo[0].BalanceDelta != "-3.0" {
		t.Fatalf("Python outgoing purchase summary = %+v", purchase)
	}
	for index, item := range oracle.History {
		if item.ClaimInfo == nil || item.UpdateInfo == nil || item.SupportInfo == nil ||
			item.AbandonInfo == nil || item.PurchaseInfo == nil {
			t.Fatalf("Python transaction summary %d has a null category list: %+v", index, item)
		}
	}
}

type transactionSummaryStoredOutput struct {
	stored     bool
	owned      bool
	outputType int64
}

func transactionSummaryOracleFixture(t *testing.T) (*Ledger, []string) {
	t.Helper()
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	ledger.Headers = NewHeaders("")
	ledger.Headers.size = 21
	if err := ledger.Database.AddKeys(ctx, "account-a", []ledgerdb.AddressKey{{
		Address: "owned", PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}

	parent := func(nonce uint32, output TransactionOutput) *Transaction {
		t.Helper()
		return transactionHistoryUnitCoinbase(t, nonce, output)
	}
	fundClaim := parent(101, NewPayPubKeyHashOutput(500_000_000, bytes.Repeat([]byte{1}, 20)))
	claim := transactionSummarySpend(t, 201, []*TransactionOutput{&fundClaim.Outputs[0]},
		NewClaimNameOutput(200_000_000, "alpha", []byte{0}, bytes.Repeat([]byte{11}, 20)),
		NewPayPubKeyHashOutput(299_990_000, bytes.Repeat([]byte{12}, 20)),
	)
	claimID, err := claim.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	updateOutput, err := NewUpdateClaimOutput(
		50_000_000, "alpha", claimID, []byte{0}, bytes.Repeat([]byte{13}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	update := transactionSummarySpend(t, 202, []*TransactionOutput{&claim.Outputs[0]},
		updateOutput, NewPayPubKeyHashOutput(149_990_000, bytes.Repeat([]byte{14}, 20)),
	)
	supportClaimID := strings.Repeat("22", 20)
	priorSupportOutput, err := NewSupportOutput(
		100_000_000, "beta", supportClaimID, bytes.Repeat([]byte{2}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	priorSupport := parent(102, priorSupportOutput)
	abandon := transactionSummarySpend(t, 203, []*TransactionOutput{
		&update.Outputs[0], &priorSupport.Outputs[0],
	}, NewPayPubKeyHashOutput(149_990_000, bytes.Repeat([]byte{15}, 20)))

	fundSupports := parent(103, NewPayPubKeyHashOutput(700_000_000, bytes.Repeat([]byte{3}, 20)))
	otherSupport, err := NewSupportOutput(
		200_000_000, "beta", supportClaimID, bytes.Repeat([]byte{16}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	mySupport, err := NewSupportOutput(
		100_000_000, "beta", supportClaimID, bytes.Repeat([]byte{17}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	supports := transactionSummarySpend(t, 204, []*TransactionOutput{&fundSupports.Outputs[0]},
		otherSupport, mySupport,
		NewPayPubKeyHashOutput(399_990_000, bytes.Repeat([]byte{18}, 20)),
	)

	incomingData, err := NewPurchaseDataOutput(strings.Repeat("44", 20))
	if err != nil {
		t.Fatal(err)
	}
	incomingUpdate, err := NewUpdateClaimOutput(
		60_000_000, "gamma", strings.Repeat("33", 20), []byte{0}, bytes.Repeat([]byte{20}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	incomingSupport, err := NewSupportOutput(
		100_000_000, "gamma", strings.Repeat("33", 20), bytes.Repeat([]byte{21}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	incomingOtherSupport, err := NewSupportOutput(
		50_000_000, "gamma", strings.Repeat("33", 20), bytes.Repeat([]byte{22}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	incoming := transactionSummaryMissing(t, 205, []TransactionOutput{
		NewPayPubKeyHashOutput(70_000_000, bytes.Repeat([]byte{19}, 20)),
		incomingData, incomingUpdate, incomingSupport, incomingOtherSupport,
	})

	fundPurchase := parent(104, NewPayPubKeyHashOutput(400_000_000, bytes.Repeat([]byte{4}, 20)))
	outgoingData, err := NewPurchaseDataOutput(strings.Repeat("55", 20))
	if err != nil {
		t.Fatal(err)
	}
	purchase := transactionSummarySpend(t, 206, []*TransactionOutput{&fundPurchase.Outputs[0]},
		NewPayPubKeyHashOutput(300_000_000, bytes.Repeat([]byte{23}, 20)),
		outgoingData,
		NewPayPubKeyHashOutput(99_990_000, bytes.Repeat([]byte{24}, 20)),
	)

	persist := func(transaction *Transaction, height, position int64, outputs []transactionSummaryStoredOutput) {
		t.Helper()
		row := ledgerdb.TransactionIORow{Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...), Height: height,
			Position: position, IsVerified: height > 0,
		}}
		for inputIndex := range transaction.Inputs {
			input := &transaction.Inputs[inputIndex]
			if input.IsCoinbase() {
				continue
			}
			row.Inputs = append(row.Inputs, ledgerdb.TransactionInputRow{
				TXOID: input.PreviousOutputID(), Position: int64(input.Position),
			})
		}
		if len(outputs) != len(transaction.Outputs) {
			t.Fatalf("stored output plan has %d entries for %d outputs", len(outputs), len(transaction.Outputs))
		}
		for outputIndex, plan := range outputs {
			if !plan.stored {
				continue
			}
			address := "foreign"
			if plan.owned {
				address = "owned"
			}
			output := &transaction.Outputs[outputIndex]
			row.Outputs = append(row.Outputs, ledgerdb.TransactionOutputRow{
				TXOID: output.ID(), Address: &address, Position: int64(output.Position),
				Amount: int64(output.Amount), Script: append([]byte(nil), output.Script.Source...),
				TXOType: plan.outputType,
			})
		}
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{row}, "owned", ""); err != nil {
			t.Fatal(err)
		}
	}
	stored := func(owned bool, outputType int64) transactionSummaryStoredOutput {
		return transactionSummaryStoredOutput{stored: true, owned: owned, outputType: outputType}
	}
	unstored := transactionSummaryStoredOutput{}
	persist(fundClaim, 1, 101, []transactionSummaryStoredOutput{stored(true, TransactionOutputTypeOther)})
	persist(priorSupport, 1, 102, []transactionSummaryStoredOutput{stored(true, TransactionOutputTypeSupport)})
	persist(fundSupports, 1, 103, []transactionSummaryStoredOutput{stored(true, TransactionOutputTypeOther)})
	persist(fundPurchase, 1, 104, []transactionSummaryStoredOutput{stored(true, TransactionOutputTypeOther)})
	persist(claim, 10, 1, []transactionSummaryStoredOutput{
		stored(true, TransactionOutputTypeStream), stored(true, TransactionOutputTypeOther),
	})
	persist(update, 9, 2, []transactionSummaryStoredOutput{
		stored(true, TransactionOutputTypeStream), stored(true, TransactionOutputTypeOther),
	})
	persist(abandon, 8, 3, []transactionSummaryStoredOutput{stored(true, TransactionOutputTypeOther)})
	persist(supports, 7, 4, []transactionSummaryStoredOutput{
		stored(false, TransactionOutputTypeSupport), stored(true, TransactionOutputTypeSupport),
		stored(true, TransactionOutputTypeOther),
	})
	persist(incoming, 0, 5, []transactionSummaryStoredOutput{
		stored(true, TransactionOutputTypePurchase), unstored,
		stored(true, TransactionOutputTypeStream), stored(true, TransactionOutputTypeSupport),
		stored(false, TransactionOutputTypeSupport),
	})
	persist(purchase, -1, 6, []transactionSummaryStoredOutput{
		stored(false, TransactionOutputTypePurchase), unstored,
		stored(true, TransactionOutputTypeOther),
	})
	return ledger, []string{
		incoming.ID, claim.ID, update.ID, abandon.ID, supports.ID, purchase.ID,
	}
}

func transactionSummarySpend(
	t *testing.T, lockTime uint32, previous []*TransactionOutput, outputs ...TransactionOutput,
) *Transaction {
	t.Helper()
	inputs := make([]TransactionInput, len(previous))
	for index, output := range previous {
		current := currentTransactionOutput(output)
		inputs[index] = TransactionInput{
			PreviousHash: current.TransactionHash, PreviousTxID: current.TransactionID,
			PreviousIndex: current.Position, Sequence: math.MaxUint32,
			Script: TransactionInputScript{Source: []byte{0x51}},
		}
	}
	transaction := NewTransaction()
	transaction.LockTime = lockTime
	transaction.AddInputs(inputs)
	transaction.AddOutputs(outputs)
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func transactionSummaryMissing(
	t *testing.T, lockTime uint32, outputs []TransactionOutput,
) *Transaction {
	t.Helper()
	var previousHash [32]byte
	copy(previousHash[:], bytes.Repeat([]byte{0x11}, 32))
	transaction := NewTransaction()
	transaction.LockTime = lockTime
	transaction.AddInputs([]TransactionInput{{
		PreviousHash: previousHash, PreviousTxID: strings.Repeat("11", 32), PreviousIndex: 4,
		Sequence: math.MaxUint32, Script: TransactionInputScript{Source: []byte{0x51}},
	}})
	transaction.AddOutputs(outputs)
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func runTransactionSummaryOracle(t *testing.T) transactionSummaryOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction summary oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionSummaryOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction summary source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_summary_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction summary oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction summary oracle failed: %v\n%s", err, output)
	}
	var oracle transactionSummaryOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction summary oracle: %v\n%s", err, output)
	}
	return oracle
}
