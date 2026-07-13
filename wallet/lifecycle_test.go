package wallet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestLedgerPersistenceCreatesOneLevelDirectoryAndClosesInOrder(t *testing.T) {
	directory := t.TempDir()
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": directory})
	if err != nil {
		t.Fatal(err)
	}

	result, err := ledger.OpenPersistence(context.Background())
	if err != nil || result.Err() != nil {
		t.Fatalf("open persistence = %#v, %v", result, err)
	}
	ledgerPath, _ := ledger.Path()
	if info, statErr := os.Stat(ledgerPath); statErr != nil || !info.IsDir() {
		t.Fatalf("ledger directory = %v, %v", info, statErr)
	}
	if !ledger.Database.IsOpen() || !ledger.Headers.opened {
		t.Fatalf("open state = database %t, headers %t", ledger.Database.IsOpen(), ledger.Headers.opened)
	}

	if err := ledger.ClosePersistence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ledger.Database.IsOpen() || ledger.Headers.opened {
		t.Fatalf("closed state = database %t, headers %t", ledger.Database.IsOpen(), ledger.Headers.opened)
	}
	for _, path := range []string{ledger.Database.Path(), ledger.Headers.path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("persistence artifact %s: %v", path, err)
		}
	}
}

func TestLedgerPersistenceRetainsSuccessfulDatabaseWhenHeadersFail(t *testing.T) {
	directory := t.TempDir()
	ledger, err := newLedger(keys.TestNet, LedgerConfig{"data_path": directory})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, _ := ledger.Path()
	if err := os.Mkdir(ledgerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger.Headers.path, 0o755); err != nil {
		t.Fatal(err)
	}

	result, orchestrationErr := ledger.OpenPersistence(context.Background())
	if orchestrationErr != nil || result.DatabaseErr != nil || result.HeadersErr == nil {
		t.Fatalf("open result = %#v, orchestration error = %v", result, orchestrationErr)
	}
	if !ledger.Database.IsOpen() || ledger.Headers.opened {
		t.Fatalf("partial state = database %t, headers %t", ledger.Database.IsOpen(), ledger.Headers.opened)
	}
	if err := ledger.ClosePersistence(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerPersistenceRetainsSuccessfulHeadersWhenDatabaseFails(t *testing.T) {
	directory := t.TempDir()
	ledger, err := newLedger(keys.RegTest, LedgerConfig{"data_path": directory})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, _ := ledger.Path()
	if err := os.Mkdir(ledgerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger.Database.Path(), 0o755); err != nil {
		t.Fatal(err)
	}

	result, orchestrationErr := ledger.OpenPersistence(context.Background())
	if orchestrationErr != nil || result.DatabaseErr == nil || result.HeadersErr != nil {
		t.Fatalf("open result = %#v, orchestration error = %v", result, orchestrationErr)
	}
	if ledger.Database.IsOpen() || !ledger.Headers.opened {
		t.Fatalf("partial state = database %t, headers %t", ledger.Database.IsOpen(), ledger.Headers.opened)
	}
	if err := ledger.ClosePersistence(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerPersistenceReportsPathFailureBeforeChildOpens(t *testing.T) {
	missingParent := filepath.Join(t.TempDir(), "missing", "parent")
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": missingParent})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ledger.OpenPersistence(context.Background())
	if err == nil || result.Err() != nil {
		t.Fatalf("path failure = %#v, %v", result, err)
	}
	if ledger.Database.IsOpen() || ledger.Headers.opened {
		t.Fatal("child persistence opened after path failure")
	}
}

func TestLedgerPersistenceRepeatedOpenKeepsSeparateChildOutcomes(t *testing.T) {
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := ledger.OpenPersistence(context.Background())
	if err != nil || first.Err() != nil {
		t.Fatalf("first open = %#v, %v", first, err)
	}
	second, err := ledger.OpenPersistence(context.Background())
	if err != nil || !errors.Is(second.DatabaseErr, ledgerdb.ErrAlreadyOpen) || second.HeadersErr != nil {
		t.Fatalf("second open = %#v, %v", second, err)
	}
	if err := ledger.ClosePersistence(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWalletManagerPersistencePreservesLedgerOrderAndRunningFlag(t *testing.T) {
	directory := t.TempDir()
	manager, err := WalletManagerFromConfig(ManagerConfig{Ledgers: []LedgerSpec{
		{ID: keys.RegTest.ID(), Config: LedgerConfig{"data_path": directory}},
		{ID: keys.MainNet.ID(), Config: LedgerConfig{"data_path": directory}},
		{ID: keys.TestNet.ID(), Config: LedgerConfig{"data_path": directory}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	testnet := manager.Ledgers[keys.TestNet]
	testnetPath, _ := testnet.Path()
	if err := os.Mkdir(testnetPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(testnet.Headers.path, 0o755); err != nil {
		t.Fatal(err)
	}

	results := manager.OpenLedgersPersistence(context.Background())
	gotOrder := make([]string, len(results))
	for index, result := range results {
		gotOrder[index] = result.LedgerID
	}
	wantOrder := []string{keys.RegTest.ID(), keys.MainNet.ID(), keys.TestNet.ID()}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("result order = %v, want %v", gotOrder, wantOrder)
	}
	if manager.Running {
		t.Fatal("persistence prefix set the full-lifecycle Running flag")
	}
	openErr := LedgerPersistenceOpenError(results)
	if openErr == nil || !strings.Contains(openErr.Error(), keys.TestNet.ID()) ||
		!strings.Contains(openErr.Error(), "open headers") {
		t.Fatalf("aggregate open error = %v", openErr)
	}
	if err := manager.CloseLedgersPersistence(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, ledger := range manager.OrderedLedgers() {
		if ledger.Database.IsOpen() || ledger.Headers.opened {
			t.Fatalf("ledger %s remained open", ledger.ID())
		}
	}
}
