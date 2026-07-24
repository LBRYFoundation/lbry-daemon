package ledgerdb

import (
	"context"
	"reflect"
	"testing"
)

func TestTransactionPurchaseQueriesUseSpendingInputAccounts(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	addTransactionQueryTestAccount(t, database, "buyer", "mine")
	addTransactionQueryTestAccount(t, database, "seller", "theirs")

	addTransactionQueryTestPurchase(t, database, "mine-a", "claim-a", 5)
	addTransactionQueryTestInput(t, database, "mine-a", "previous:0", "mine")
	addTransactionQueryTestOutput(t, database, "mine-a", "mine-a:0", "theirs")
	addTransactionQueryTestPurchase(t, database, "mine-b", "claim-b", 7)
	addTransactionQueryTestInput(t, database, "mine-b", "previous:1", "mine")
	addTransactionQueryTestPurchase(t, database, "received", "claim-a", 9)
	addTransactionQueryTestInput(t, database, "received", "previous:2", "theirs")
	addTransactionQueryTestOutput(t, database, "received", "received:0", "mine")
	addTransactionQueryTestTransaction(t, database, "ordinary", 11, 0, false)
	addTransactionQueryTestInput(t, database, "ordinary", "previous:3", "mine")

	query := TransactionQuery{
		InputAccountIDs:         []string{"buyer"},
		RequirePurchasedClaimID: true,
	}
	transactions, err := database.ListTransactions(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"mine-b", "mine-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("buyer purchases = %v, want %v", got, want)
	}
	count, err := database.CountTransactions(context.Background(), query)
	if err != nil || count != 2 {
		t.Fatalf("buyer purchase count = %d, %v", count, err)
	}

	claimA := "claim-a"
	query.PurchasedClaimID = &claimA
	query.RequirePurchasedClaimID = false
	transactions, err = database.ListTransactions(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"mine-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim-filtered purchases = %v, want %v", got, want)
	}
}

func TestTransactionPurchaseQueryEmptyClaimIDsPreservePinnedConstraintOmission(t *testing.T) {
	t.Parallel()
	database := openTransactionQueryTestDB(t)
	addTransactionQueryTestAccount(t, database, "buyer", "mine")
	addTransactionQueryTestPurchase(t, database, "purchase", "claim", 1)
	addTransactionQueryTestInput(t, database, "purchase", "previous:0", "mine")
	addTransactionQueryTestTransaction(t, database, "ordinary", 2, 0, false)
	addTransactionQueryTestInput(t, database, "ordinary", "previous:1", "mine")

	transactions, err := database.ListTransactions(context.Background(), TransactionQuery{
		InputAccountIDs:   []string{"buyer"},
		PurchasedClaimIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := transactionQueryTestIDs(transactions), []string{"ordinary", "purchase"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty purchased claim IDs = %v, want %v", got, want)
	}
}

func addTransactionQueryTestPurchase(
	t *testing.T, database *DB, txid, claimID string, height int64,
) {
	t.Helper()
	addTransactionQueryTestTransaction(t, database, txid, height, 0, false)
	database.mu.Lock()
	defer database.mu.Unlock()
	if _, err := database.sql.Exec(
		"UPDATE tx SET purchased_claim_id = ? WHERE txid = ?", claimID, txid,
	); err != nil {
		t.Fatal(err)
	}
}
