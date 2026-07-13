package wallet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

// PersistenceOpenResult retains the two child-task outcomes from the
// database/header prefix of Python Ledger.start. Python awaits those tasks
// with asyncio.wait and does not rethrow either exception.
type PersistenceOpenResult struct {
	DatabaseErr error
	HeadersErr  error
}

func (result PersistenceOpenResult) Err() error {
	return errors.Join(result.DatabaseErr, result.HeadersErr)
}

// LedgerPersistenceOpenResult associates one persistence-prefix result with
// its manager ledger. Err is reserved for path creation/orchestration errors.
type LedgerPersistenceOpenResult struct {
	LedgerID string
	Open     PersistenceOpenResult
	Err      error
}

func (result LedgerPersistenceOpenResult) CombinedError() error {
	var openErr error
	if result.Open.DatabaseErr != nil {
		openErr = errors.Join(openErr, fmt.Errorf("open database: %w", result.Open.DatabaseErr))
	}
	if result.Open.HeadersErr != nil {
		openErr = errors.Join(openErr, fmt.Errorf("open headers: %w", result.Open.HeadersErr))
	}
	return errors.Join(result.Err, openErr)
}

// OpenPersistence implements only the directory/database/header prefix of
// Python Ledger.start. It deliberately reports child outcomes separately and
// leaves a successful sibling open when the other one fails, matching the
// observable partial state produced by asyncio.wait.
func (ledger *Ledger) OpenPersistence(ctx context.Context) (PersistenceOpenResult, error) {
	var result PersistenceOpenResult
	if ledger == nil {
		return result, errors.New("ledger is nil")
	}
	if ctx == nil {
		return result, errors.New("persistence context is nil")
	}
	path, err := ledger.Path()
	if err != nil {
		return result, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o777); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		result.DatabaseErr = ledger.Database.Open(ctx)
	}()
	go func() {
		defer wait.Done()
		result.HeadersErr = ledger.Headers.Open()
	}()
	wait.Wait()
	return result, nil
}

// ClosePersistence mirrors the database-before-headers portion of
// Ledger.stop. A database close failure intentionally prevents header close.
func (ledger *Ledger) ClosePersistence(ctx context.Context) error {
	if ledger == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("persistence context is nil")
	}
	if err := ledger.Database.Close(ctx); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	if err := ledger.Headers.Close(); err != nil {
		return fmt.Errorf("close headers: %w", err)
	}
	return nil
}

// OpenLedgersPersistence runs the persistence prefix for every ledger in
// first-construction order. It does not mutate WalletManager.Running.
func (manager *WalletManager) OpenLedgersPersistence(ctx context.Context) []LedgerPersistenceOpenResult {
	ledgers := manager.OrderedLedgers()
	results := make([]LedgerPersistenceOpenResult, len(ledgers))
	var wait sync.WaitGroup
	wait.Add(len(ledgers))
	for index, ledger := range ledgers {
		index, ledger := index, ledger
		go func() {
			defer wait.Done()
			result, err := ledger.OpenPersistence(ctx)
			results[index] = LedgerPersistenceOpenResult{
				LedgerID: ledger.ID(),
				Open:     result,
				Err:      err,
			}
		}()
	}
	wait.Wait()
	return results
}

// CloseLedgersPersistence closes all ledgers concurrently, as the Python
// manager does, while returning every completed close error deterministically.
func (manager *WalletManager) CloseLedgersPersistence(ctx context.Context) error {
	ledgers := manager.OrderedLedgers()
	errorsByLedger := make([]error, len(ledgers))
	var wait sync.WaitGroup
	wait.Add(len(ledgers))
	for index, ledger := range ledgers {
		index, ledger := index, ledger
		go func() {
			defer wait.Done()
			if err := ledger.ClosePersistence(ctx); err != nil {
				errorsByLedger[index] = fmt.Errorf("close ledger %s persistence: %w", ledger.ID(), err)
			}
		}()
	}
	wait.Wait()
	return errors.Join(errorsByLedger...)
}

func LedgerPersistenceOpenError(results []LedgerPersistenceOpenResult) error {
	errorsByLedger := make([]error, 0, len(results))
	for _, result := range results {
		if err := result.CombinedError(); err != nil {
			errorsByLedger = append(errorsByLedger,
				fmt.Errorf("open ledger %s persistence: %w", result.LedgerID, err))
		}
	}
	return errors.Join(errorsByLedger...)
}
