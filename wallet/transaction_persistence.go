package wallet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionPersistenceUnavailable = errors.New("wallet transaction persistence is unavailable")
	ErrTransactionAmountOverflow         = errors.New("transaction output amount exceeds SQLite integer range")
)

// ProjectTransactionIOBatch applies the watched-address filtering performed by
// Python's Database._transaction_io. Input references must be resolved before
// projection; unresolved inputs are deliberately ignored.
func (ledger *Ledger) ProjectTransactionIOBatch(
	transactions []*Transaction, address string, targetHash [20]byte,
) ([]ledgerdb.TransactionIORow, error) {
	if ledger == nil || ledger.Database == nil {
		return nil, ErrTransactionPersistenceUnavailable
	}
	rows := make([]ledgerdb.TransactionIORow, 0, len(transactions))
	for _, transaction := range transactions {
		if transaction == nil {
			return nil, fmt.Errorf("%w: transaction is nil", ErrInvalidWalletTransaction)
		}
		metadata := ProjectTransactionMetadata(transaction)
		day, err := ledger.transactionJulianDay(transaction)
		if err != nil {
			return nil, err
		}
		row := ledgerdb.TransactionIORow{Transaction: ledgerdb.TransactionRow{
			TXID:             transaction.ID,
			Raw:              append([]byte(nil), transaction.Raw...),
			Height:           transaction.Height,
			Position:         transaction.Position,
			IsVerified:       transaction.IsVerified,
			PurchasedClaimID: cloneTransactionString(metadata.PurchasedClaimID),
			Day:              day,
		}}

		isMyInput := false
		for _, input := range transaction.Inputs {
			resolved := input.ResolvedOutput
			if resolved == nil {
				continue
			}
			if resolved.Script.Err != nil {
				return nil, fmt.Errorf("resolve input %d script: %w", input.Position, resolved.Script.Err)
			}
			if !resolved.Script.HasAddress() {
				continue
			}
			resolvedAddress, err := resolved.Address(ledger.Network)
			if err != nil {
				return nil, fmt.Errorf("resolve input %d address: %w", input.Position, err)
			}
			if resolvedAddress == address {
				isMyInput = true
				row.Inputs = append(row.Inputs, ledgerdb.TransactionInputRow{
					TXOID: resolved.ID(), Position: int64(input.Position),
				})
			}
		}

		for index := range transaction.Outputs {
			output := &transaction.Outputs[index]
			if output.Script.Err != nil {
				return nil, fmt.Errorf("transaction %s output %d script: %w", transaction.ID, output.Position, output.Script.Err)
			}
			selected := output.Script.IsPayPubKeyHash() &&
				(bytes.Equal(output.Script.PubKeyHash, targetHash[:]) || isMyInput)
			if !selected {
				selected = output.Script.IsPayScriptHash() && isMyInput
			}
			if !selected {
				continue
			}
			if output.Amount > math.MaxInt64 {
				return nil, fmt.Errorf("%w at %s", ErrTransactionAmountOverflow, output.ID())
			}
			projected := metadata.Outputs[index]
			if projected.Err != nil {
				return nil, fmt.Errorf("project transaction output %s: %w", output.ID(), projected.Err)
			}
			outputAddress, err := output.Address(ledger.Network)
			if err != nil {
				return nil, fmt.Errorf("project transaction output %s address: %w", output.ID(), err)
			}
			row.Outputs = append(row.Outputs, ledgerdb.TransactionOutputRow{
				TXOID:           output.ID(),
				Address:         &outputAddress,
				Position:        int64(output.Position),
				Amount:          int64(output.Amount),
				Script:          append([]byte(nil), output.Script.Source...),
				TXOType:         projected.TXOType,
				ClaimID:         cloneTransactionString(projected.ClaimID),
				ClaimName:       cloneTransactionString(projected.ClaimName),
				HasSource:       projected.HasSource,
				ChannelID:       cloneTransactionString(projected.ChannelID),
				RepostedClaimID: cloneTransactionString(projected.RepostedClaimID),
			})
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (ledger *Ledger) SaveTransactionIOBatch(
	ctx context.Context,
	transactions []*Transaction,
	address string,
	targetHash [20]byte,
	history string,
) error {
	rows, err := ledger.ProjectTransactionIOBatch(transactions, address, targetHash)
	if err != nil {
		return err
	}
	return ledger.Database.SaveTransactionIOBatch(ctx, rows, address, history)
}

func (ledger *Ledger) transactionJulianDay(transaction *Transaction) (*float64, error) {
	if transaction.JulianDay != nil {
		day := *transaction.JulianDay
		return &day, nil
	}
	if transaction.Height <= 0 {
		return nil, nil
	}
	if ledger.Headers == nil {
		return nil, fmt.Errorf("%w: ledger headers are nil", ErrTransactionPersistenceUnavailable)
	}
	height := int(transaction.Height)
	if int64(height) != transaction.Height {
		return nil, fmt.Errorf("%w: transaction height is outside int range", ErrTransactionPersistenceUnavailable)
	}
	timestamp, ok := ledger.Headers.EstimatedTimestamp(height, false)
	if !ok {
		return nil, fmt.Errorf("%w: no timestamp for height %d", ErrTransactionPersistenceUnavailable, height)
	}
	date := time.Unix(timestamp, 0).In(time.Local)
	if date.Year() < 1 || date.Year() > 9999 {
		return nil, fmt.Errorf("%w: timestamp is outside Python date range", ErrTransactionPersistenceUnavailable)
	}
	day := float64(gregorianOrdinal(date.Year(), int(date.Month()), date.Day())) + 1721424.5
	transaction.JulianDay = &day
	return &day, nil
}

func gregorianOrdinal(year, month, day int) int64 {
	priorYears := int64(year - 1)
	ordinal := 365*priorYears + priorYears/4 - priorYears/100 + priorYears/400
	monthOffsets := [...]int64{0, 0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	ordinal += monthOffsets[month] + int64(day)
	if month > 2 && (year%4 == 0 && (year%100 != 0 || year%400 == 0)) {
		ordinal++
	}
	return ordinal
}

func cloneTransactionString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
