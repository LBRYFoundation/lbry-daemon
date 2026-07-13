package ledgerdb

import (
	"context"
	"reflect"
	"testing"
)

func TestPublicOutputQueryOwnershipAndInternalTransferPredicates(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "mine", "mine-address")
	addOutputQueryTestAccount(t, database, "foreign", "foreign-address")

	fixtures := []struct {
		txid, outputAddress, inputAddress string
		outputType                        int64
		amount                            int64
	}{
		{"owned-internal", "mine-address", "mine-address", 0, 10},
		{"owned-claim", "mine-address", "mine-address", 1, 20},
		{"owned-incoming", "mine-address", "foreign-address", 0, 30},
		{"foreign-outgoing", "foreign-address", "mine-address", 0, 40},
		{"foreign-external", "foreign-address", "foreign-address", 0, 50},
	}
	ids := make([]string, len(fixtures))
	for index, fixture := range fixtures {
		fundingID := "fund-" + fixture.txid
		addOutputQueryTestTransaction(t, database, fundingID, int64(index+1), 0, true)
		addOutputQueryTestOutput(
			t, database, fundingID, 0, fixture.inputAddress, 1, 0, false,
		)
		addOutputQueryTestTransaction(t, database, fixture.txid, int64(index+10), 0, true)
		addOutputQueryTestOutput(
			t, database, fixture.txid, 0, fixture.outputAddress,
			fixture.amount, fixture.outputType, false,
		)
		mustExec(t, database.sql, `INSERT INTO txi (txid, txoid, address, position)
			VALUES (?, ?, ?, 0)`, fixture.txid, fundingID+":0", fixture.inputAddress)
		ids[index] = fixture.txid
	}

	base := OutputQuery{AccountIDs: []string{"mine"}, TXIDs: ids, Order: OutputOrderHeight}
	assertOutputQueryTestIDs(t, database, base, []string{
		"owned-incoming:0", "owned-claim:0", "owned-internal:0",
	})

	withoutInternal := base
	withoutInternal.ExcludeInternalTransfers = true
	assertOutputQueryTestIDs(t, database, withoutInternal, []string{
		"owned-incoming:0", "owned-claim:0",
	})

	union := base
	union.SkipAccountOutputConstraint = true
	union.IsMyInputOrOutput = true
	assertOutputQueryTestIDs(t, database, union, []string{
		"foreign-outgoing:0", "owned-incoming:0", "owned-claim:0", "owned-internal:0",
	})

	createdByMe, notMine := true, false
	outgoing := base
	outgoing.IsMyInput = &createdByMe
	outgoing.IsMyOutput = &notMine
	assertOutputQueryTestIDs(t, database, outgoing, []string{"foreign-outgoing:0"})
	count, err := database.CountOutputs(context.Background(), outgoing)
	if err != nil || count != 1 {
		t.Fatalf("outgoing count = %d, %v, want 1", count, err)
	}
	total, err := database.SumOutputs(context.Background(), outgoing)
	if err != nil || total != 40 {
		t.Fatalf("outgoing sum = %d, %v, want 40", total, err)
	}

	outgoing.AnnotationAccountIDs = []string{"mine"}
	outgoing.IncludeIsMyInput = true
	outgoing.IncludeIsMyOutput = true
	rows, err := database.ListOutputs(context.Background(), outgoing)
	if err != nil || len(rows) != 1 || rows[0].IsMyInput == nil || !*rows[0].IsMyInput ||
		rows[0].IsMyOutput == nil || *rows[0].IsMyOutput {
		t.Fatalf("fixed ownership annotations = %#v, %v", rows, err)
	}
}

func TestPublicOutputQueryMetadataFiltersAndOrders(t *testing.T) {
	t.Parallel()
	database := openOutputQueryTestDB(t)
	addOutputQueryTestAccount(t, database, "mine", "mine-address")

	type metadataFixture struct {
		txid, name, claimID, channelID, repostID, purchaseID string
		hasSource                                            any
		amount                                               int64
		height                                               int64
	}
	fixtures := []metadataFixture{
		{"alpha", "zeta", "claim-a", "channel-a", "repost-a", "purchase-a", true, 30, 3},
		{"beta", "alpha", "claim-b", "channel-b", "repost-b", "purchase-b", false, 10, 2},
		{"gamma", "middle", "claim-c", "", "repost-c", "purchase-c", nil, 20, 1},
	}
	for _, fixture := range fixtures {
		addOutputQueryTestTransaction(t, database, fixture.txid, fixture.height, 0, true)
		addOutputQueryTestOutput(t, database, fixture.txid, 0, "mine-address", fixture.amount, 1, false)
		mustExec(t, database.sql, `UPDATE txo SET claim_name=?, claim_id=?, channel_id=?,
			reposted_claim_id=?, has_source=? WHERE txoid=?`, fixture.name, fixture.claimID,
			nullableOutputQueryTestString(fixture.channelID), fixture.repostID,
			fixture.hasSource, fixture.txid+":0")
		mustExec(t, database.sql, "UPDATE tx SET purchased_claim_id=? WHERE txid=?",
			fixture.purchaseID, fixture.txid)
	}

	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{"mine"}, ClaimNames: []string{"alpha"},
	}, []string{"beta:0"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{"mine"}, ChannelIDs: []string{"channel-a"},
		RepostedClaimIDs: []string{"repost-a"}, PurchasedClaimIDs: []string{"purchase-a"},
	}, []string{"alpha:0"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{"mine"}, NotChannelIDs: []string{"channel-a", "channel-b"},
	}, []string{"gamma:0"})
	yes, no := true, false
	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{"mine"}, HasSource: &yes,
	}, []string{"alpha:0"})
	assertOutputQueryTestIDs(t, database, OutputQuery{
		AccountIDs: []string{"mine"}, HasSource: &no,
	}, []string{"beta:0"})

	orders := []struct {
		order OutputOrder
		want  []string
	}{
		{OutputOrderName, []string{"beta:0", "gamma:0", "alpha:0"}},
		{OutputOrderAmount, []string{"beta:0", "gamma:0", "alpha:0"}},
		{OutputOrderHeight, []string{"alpha:0", "beta:0", "gamma:0"}},
	}
	for _, test := range orders {
		rows, err := database.ListOutputs(context.Background(), OutputQuery{
			AccountIDs: []string{"mine"}, Order: test.order,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := outputQueryTestIDs(rows); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("order %d = %v, want %v", test.order, got, test.want)
		}
	}
}

func nullableOutputQueryTestString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
