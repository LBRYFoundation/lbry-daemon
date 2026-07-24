package wallet

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

var transactionMerkleBranchesFixture = []string{
	"000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
	"fffefdfcfbfaf9f8f7f6f5f4f3f2f1f0efeeedecebeae9e8e7e6e5e4e3e2e1e0",
}

func TestTransactionMerkleRootDisplayOrderAndPositionBits(t *testing.T) {
	transaction := mustFetchTransaction(t)
	if root, err := TransactionMerkleRoot(nil, 0, transaction.Hash); err != nil || root != transaction.ID {
		t.Fatalf("empty merkle root = %q, %v; want %q", root, err, transaction.ID)
	}
	tests := []struct {
		position int64
		root     string
	}{
		{0, "128f1c08468f617fca9731ccce0e3f3404516ffddd72bda756e3958b9300f559"},
		{1, "680c4ae1d429542cc9d09a5bf9a96557a91c4c21e629112e40f9cf9dea0999a9"},
		{2, "63ceaaee01c4caf45d88e44dd6a604ec4c28c9abffdb2ef7a758a215bba5b1e0"},
		{3, "fa28f7cf99e1423e0244e18124a8e036106ab240e3f65eccb404ea7e5fbc1117"},
		{-1, "fa28f7cf99e1423e0244e18124a8e036106ab240e3f65eccb404ea7e5fbc1117"},
	}
	for _, fixture := range tests {
		root, err := TransactionMerkleRoot(transactionMerkleBranchesFixture, fixture.position, transaction.Hash)
		if err != nil || root != fixture.root {
			t.Errorf("position %d root = %q, %v; want %q", fixture.position, root, err, fixture.root)
		}
	}
	if _, err := TransactionMerkleRoot([]string{"0"}, 0, transaction.Hash); !errors.Is(err, ErrMalformedTransactionMerkle) {
		t.Fatalf("invalid branch error = %v", err)
	}
}

func TestApplyTransactionMerkleVerificationHeightAndMissingProofStates(t *testing.T) {
	for _, remoteHeight := range []int64{-1, 0, 10, 11} {
		transaction := mustFetchTransaction(t)
		transaction.Position = 7
		transaction.IsVerified = true
		status, err := ApplyTransactionMerkleVerification(
			transaction, remoteHeight, 10, []byte(transaction.ID), map[string]any{"merkle": []any{}, "pos": json.Number("0")},
		)
		if err != nil || status != TransactionMerkleHeightGated || transaction.Height != remoteHeight ||
			transaction.Position != 7 || !transaction.IsVerified {
			t.Fatalf("height %d state = %q, %#v, %v", remoteHeight, status, transaction, err)
		}
	}

	for _, merkle := range []map[string]any{nil, {}} {
		transaction := mustFetchTransaction(t)
		transaction.Position = 7
		transaction.IsVerified = true
		status, err := ApplyTransactionMerkleVerification(transaction, 1, 10, nil, merkle)
		if err != nil || status != TransactionMerkleProofRequired ||
			transaction.Position != 7 || !transaction.IsVerified {
			t.Fatalf("falsey proof state = %q, %#v, %v", status, transaction, err)
		}
	}

	transaction := mustFetchTransaction(t)
	transaction.Position = 7
	transaction.IsVerified = true
	status, err := ApplyTransactionMerkleVerification(
		transaction, 1, 10, nil, map[string]any{"block_height": json.Number("1")},
	)
	if err != nil || status != TransactionMerkleProofMissing ||
		transaction.Position != 7 || !transaction.IsVerified {
		t.Fatalf("truthy missing proof state = %q, %#v, %v", status, transaction, err)
	}
}

func TestApplyTransactionMerkleVerificationMatchAndMismatch(t *testing.T) {
	transaction := mustFetchTransaction(t)
	proof := map[string]any{
		"merkle": []any{transactionMerkleBranchesFixture[0], transactionMerkleBranchesFixture[1]},
		"pos":    json.Number("1"),
	}
	root := []byte("680c4ae1d429542cc9d09a5bf9a96557a91c4c21e629112e40f9cf9dea0999a9")
	status, err := ApplyTransactionMerkleVerification(transaction, 4, 10, root, proof)
	if err != nil || status != TransactionMerkleMatched || transaction.Height != 4 ||
		transaction.Position != 1 || !transaction.IsVerified {
		t.Fatalf("matched state = %q, %#v, %v", status, transaction, err)
	}

	transaction.IsVerified = true
	status, err = ApplyTransactionMerkleVerification(transaction, 4, 10, []byte("different"), proof)
	if err != nil || status != TransactionMerkleMismatched || transaction.Position != 1 || transaction.IsVerified {
		t.Fatalf("mismatched state = %q, %#v, %v", status, transaction, err)
	}

	emptyBranch := mustFetchTransaction(t)
	status, err = ApplyTransactionMerkleVerification(emptyBranch, 1, 2, []byte(emptyBranch.ID), map[string]any{
		"merkle": []any{}, "pos": json.Number("0"),
	})
	if err != nil || status != TransactionMerkleMatched || !emptyBranch.IsVerified || emptyBranch.Position != 0 {
		t.Fatalf("empty branch state = %q, %#v, %v", status, emptyBranch, err)
	}
}

func TestApplyTransactionMerkleVerificationTypedMalformedProofs(t *testing.T) {
	transaction := mustFetchTransaction(t)
	tests := []struct {
		name   string
		tx     *Transaction
		merkle map[string]any
		field  string
	}{
		{"nil transaction", nil, map[string]any{"merkle": []any{}, "pos": json.Number("0")}, "transaction"},
		{"branches type", transaction, map[string]any{"merkle": "bad", "pos": json.Number("0")}, "merkle"},
		{"branch type", transaction, map[string]any{"merkle": []any{1}, "pos": json.Number("0")}, "merkle[0]"},
		{"branch hexadecimal", transaction, map[string]any{"merkle": []any{"zz"}, "pos": json.Number("0")}, "merkle[0]"},
		{"position missing", transaction, map[string]any{"merkle": []any{}}, "pos"},
		{"position fraction", transaction, map[string]any{"merkle": []any{}, "pos": json.Number("1.5")}, "pos"},
		{"position overflow", transaction, map[string]any{"merkle": []any{}, "pos": json.Number("9223372036854775808")}, "pos"},
		{"position type", transaction, map[string]any{"merkle": []any{}, "pos": "1"}, "pos"},
	}
	for _, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			_, err := ApplyTransactionMerkleVerification(fixture.tx, 1, 10, nil, fixture.merkle)
			if !errors.Is(err, ErrTransactionMerkle) || !errors.Is(err, ErrMalformedTransactionMerkle) {
				t.Fatalf("error = %v", err)
			}
			var typed *TransactionMerkleError
			if !errors.As(err, &typed) || typed.Field != fixture.field {
				t.Fatalf("typed error = %#v, want field %q", typed, fixture.field)
			}
		})
	}
}

func mustFetchTransaction(t *testing.T) *Transaction {
	t.Helper()
	raw, err := hex.DecodeString(transactionFetchFixtureHex)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := ParseTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}
