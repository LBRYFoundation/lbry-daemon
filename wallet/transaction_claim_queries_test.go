package wallet

import (
	"context"
	"reflect"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionClaimFamilyQueriesApplyPinnedTypesAndUnspentState(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	accountID, address := "claims-account", "claims-address"
	if err := ledger.Database.AddKeys(ctx, accountID, []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}

	rows := make([]ledgerdb.TransactionIORow, 0, 7)
	fixtures := make(map[int64]*Transaction)
	for outputType := int64(0); outputType <= TransactionOutputTypeRepost; outputType++ {
		transaction := transactionHistoryUnitCoinbase(
			t, uint32(2_000+outputType),
			NewPayPubKeyHashOutput(uint64(outputType+1), make([]byte, 20)),
		)
		fixtures[outputType] = transaction
		rows = append(rows, ledgerdb.TransactionIORow{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				Height: outputType + 1, Position: outputType,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &address,
				Position: 0, Amount: int64(outputType + 1),
				Script:  append([]byte(nil), transaction.Outputs[0].Script.Source...),
				TXOType: outputType,
			}},
		})
	}
	if err := ledger.Database.SaveTransactionIOBatch(ctx, rows, address, ""); err != nil {
		t.Fatal(err)
	}

	query := ledgerdb.OutputQuery{AccountIDs: []string{accountID}}
	claims, err := ledger.GetClaims(ctx, ClaimListOptions{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	wantClaimIDs := []string{
		fixtures[TransactionOutputTypeRepost].Outputs[0].ID(),
		fixtures[TransactionOutputTypeCollection].Outputs[0].ID(),
		fixtures[TransactionOutputTypeChannel].Outputs[0].ID(),
		fixtures[TransactionOutputTypeStream].Outputs[0].ID(),
	}
	if got := transactionClaimQueryOutputIDs(claims); !reflect.DeepEqual(got, wantClaimIDs) {
		t.Fatalf("claim family IDs = %v, want %v", got, wantClaimIDs)
	}
	count, err := ledger.CountClaims(ctx, query)
	if err != nil || count != 4 {
		t.Fatalf("claim count = %d, %v", count, err)
	}

	tests := []struct {
		name     string
		get      func() ([]*TransactionOutput, error)
		count    func() (int64, error)
		wantType int64
	}{
		{"streams", func() ([]*TransactionOutput, error) { return ledger.GetStreams(ctx, ClaimListOptions{Query: query}) }, func() (int64, error) { return ledger.CountStreams(ctx, query) }, TransactionOutputTypeStream},
		{"channels", func() ([]*TransactionOutput, error) { return ledger.GetChannels(ctx, ClaimListOptions{Query: query}) }, func() (int64, error) { return ledger.CountChannels(ctx, query) }, TransactionOutputTypeChannel},
		{"collections", func() ([]*TransactionOutput, error) {
			return ledger.GetCollections(ctx, ClaimListOptions{Query: query})
		}, func() (int64, error) { return ledger.CountCollections(ctx, query) }, TransactionOutputTypeCollection},
		{"supports", func() ([]*TransactionOutput, error) { return ledger.GetSupports(ctx, ClaimListOptions{Query: query}) }, func() (int64, error) { return ledger.CountSupports(ctx, query) }, TransactionOutputTypeSupport},
	}
	for _, test := range tests {
		outputs, err := test.get()
		if err != nil || len(outputs) != 1 || outputs[0].ID() != fixtures[test.wantType].Outputs[0].ID() {
			t.Fatalf("%s = %#v, %v", test.name, outputs, err)
		}
		count, err := test.count()
		if err != nil || count != 1 {
			t.Fatalf("%s count = %d, %v", test.name, count, err)
		}
	}
}

func TestTransactionClaimQueryEmptyExplicitTypesAndAccountWrappers(t *testing.T) {
	database, _ := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	account := &Account{ID: "account-a", ledger: ledger}
	NewWallet(WithWalletAccounts([]*Account{account}))

	// An explicitly supplied empty txo_type__in is not replaced with claim
	// defaults by Database.constrain_claims, and constraints_to_sql omits it.
	outputs, err := account.GetClaims(context.Background(), ClaimListOptions{
		Query: ledgerdb.OutputQuery{Types: []int64{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) == 0 {
		t.Fatal("explicit empty claim types unexpectedly produced no unspent outputs")
	}
	count, err := account.CountClaims(context.Background(), ledgerdb.OutputQuery{Types: []int64{}})
	if err != nil || count != int64(len(outputs)) {
		t.Fatalf("explicit empty claim type count = %d, outputs %d, %v", count, len(outputs), err)
	}
}

func transactionClaimQueryOutputIDs(outputs []*TransactionOutput) []string {
	ids := make([]string, len(outputs))
	for index, output := range outputs {
		ids[index] = output.ID()
	}
	return ids
}
