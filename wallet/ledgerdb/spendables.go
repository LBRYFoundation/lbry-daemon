package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
)

const estimatedPubKeyHashSpendSize int64 = 148

var ErrSpendableAmountOverflow = errors.New("spendable effective amount overflows int64")

// SpendableOutputRow is the storage projection used by the two legacy coin
// selection paths. Raw is retained because the SQLite selector reconstructs
// whole parent transactions, while the in-memory selectors use the remaining
// columns as lightweight immutable output references.
type SpendableOutputRow struct {
	TXID           string
	TXOID          string
	Raw            []byte
	Height         int64
	TXPosition     int64
	OutputPosition int64
	IsVerified     bool
	Amount         int64
	Script         []byte
}

// SpendableEffectiveAmountFunc lets the wallet package decode the stored raw
// transaction and construct Input.spend while the selection transaction is
// still open. That preserves rollback when a nominal type-0 row is not a
// supported P2PKH output without creating a ledgerdb -> wallet import cycle.
type SpendableEffectiveAmountFunc func(SpendableOutputRow) (int64, error)

// SpendableInspectFunc decodes the parent transaction as soon as SQLite's
// cursor reaches a row. Unverified rows are inspected immediately but their
// spend-input assertion remains deferred until they are actually needed.
type SpendableInspectFunc func(SpendableOutputRow) error

// ListSpendableOutputs matches Account.get_utxos(no_tx=True): owned,
// unreserved outputs with no spending txi, in Database.get_txos' default
// order. Account IDs are the account public-key addresses stored by Python.
func (database *DB) ListSpendableOutputs(
	ctx context.Context, accountIDs []string,
) ([]SpendableOutputRow, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}
	statement, arguments := spendableOutputQuery(accountIDs, false)
	rows, err := database.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSpendableOutputs(rows)
}

// ReserveOutputs applies Database.reserve_outputs' ordered UPDATE behavior.
// Missing output IDs are ignored by SQLite, just as they are in the SDK.
func (database *DB) ReserveOutputs(
	ctx context.Context, outputIDs []string, isReserved bool,
) error {
	return database.transaction(ctx, func(transaction *sql.Tx) error {
		statement, err := transaction.PrepareContext(
			ctx, "UPDATE txo SET is_reserved = ? WHERE txoid = ?",
		)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, outputID := range outputIDs {
			if _, err := statement.ExecContext(ctx, isReserved, outputID); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetAndReserveSpendableOutputs ports get_and_reserve_spendable_utxos. It
// prefers verified rows, keeps unverified rows as ordered fallbacks, widens
// the amount window with the SDK's unusual multiplier schedule, and reserves
// only when the accumulated effective amount reaches the requested target.
func (database *DB) GetAndReserveSpendableOutputs(
	ctx context.Context,
	accountIDs []string,
	amountToReserve int64,
	floor int64,
	feePerByte int64,
	setReserved bool,
	returnInsufficientFunds bool,
	effectiveAmount SpendableEffectiveAmountFunc,
	inspect SpendableInspectFunc,
) ([]SpendableOutputRow, error) {
	var selected []SpendableOutputRow
	err := database.transaction(ctx, func(transaction *sql.Tx) error {
		var err error
		selected, err = getAndReserveSpendableOutputs(
			ctx, transaction, accountIDs, amountToReserve, floor, feePerByte,
			setReserved, returnInsufficientFunds, effectiveAmount, inspect,
		)
		return err
	})
	return selected, err
}

func getAndReserveSpendableOutputs(
	ctx context.Context,
	transaction *sql.Tx,
	accountIDs []string,
	amountToReserve int64,
	floor int64,
	feePerByte int64,
	setReserved bool,
	returnInsufficientFunds bool,
	effectiveAmount SpendableEffectiveAmountFunc,
	inspect SpendableInspectFunc,
) ([]SpendableOutputRow, error) {
	selected := make([]SpendableOutputRow, 0)
	selectedIDs := make([]string, 0)
	reservedAmount := int64(0)
	multiplier := int64(100)
	gapCount := 0

	for reservedAmount < amountToReserve && gapCount < 5 {
		ceiling, ok := checkedPositiveProduct(floor, multiplier)
		if !ok || ceiling >= math.MaxInt64 {
			break
		}
		previousReservedAmount := reservedAmount
		var err error
		reservedAmount, err = selectSQLiteSpendableWindow(
			ctx, transaction, accountIDs, floor, ceiling, amountToReserve,
			reservedAmount, feePerByte, effectiveAmount, inspect,
			&selected, &selectedIDs,
		)
		if err != nil {
			return nil, err
		}

		floor = ceiling
		if previousReservedAmount == reservedAmount {
			gapCount++
			if multiplier > math.MaxInt64/multiplier {
				break
			}
			multiplier *= multiplier
		} else {
			gapCount = 0
			multiplier = 100
		}
	}

	if reservedAmount < amountToReserve && !returnInsufficientFunds {
		return []SpendableOutputRow{}, nil
	}
	if reservedAmount >= amountToReserve && setReserved {
		statement, err := transaction.PrepareContext(
			ctx, "UPDATE txo SET is_reserved = ? WHERE txoid = ?",
		)
		if err != nil {
			return nil, err
		}
		defer statement.Close()
		for _, outputID := range selectedIDs {
			if _, err := statement.ExecContext(ctx, true, outputID); err != nil {
				return nil, err
			}
		}
	}
	return selected, nil
}

func sqliteSpendableEffectiveAmount(
	row SpendableOutputRow,
	feePerByte int64,
	effectiveAmount SpendableEffectiveAmountFunc,
) (int64, error) {
	if effectiveAmount != nil {
		return effectiveAmount(row)
	}
	return row.Amount - estimatedPubKeyHashSpendSize*feePerByte, nil
}

func selectSQLiteSpendableWindow(
	ctx context.Context,
	transaction *sql.Tx,
	accountIDs []string,
	floor int64,
	ceiling int64,
	amountToReserve int64,
	reservedAmount int64,
	feePerByte int64,
	effectiveAmount SpendableEffectiveAmountFunc,
	inspect SpendableInspectFunc,
	selected *[]SpendableOutputRow,
	selectedIDs *[]string,
) (int64, error) {
	statement, arguments := spendableOutputQuery(accountIDs, true)
	arguments = append([]any{floor, ceiling}, arguments...)
	rows, err := transaction.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return reservedAmount, err
	}
	defer rows.Close()
	unconfirmed := make([]SpendableOutputRow, 0)
	for rows.Next() {
		row, err := scanSpendableOutput(rows)
		if err != nil {
			return reservedAmount, err
		}
		if inspect != nil {
			if err := inspect(row); err != nil {
				return reservedAmount, err
			}
		}
		if row.IsVerified {
			contribution, err := sqliteSpendableEffectiveAmount(row, feePerByte, effectiveAmount)
			if err != nil {
				return reservedAmount, err
			}
			*selected = append(*selected, row)
			*selectedIDs = append(*selectedIDs, row.TXOID)
			reservedAmount, err = addSpendableEffectiveAmount(reservedAmount, contribution)
			if err != nil {
				return reservedAmount, err
			}
			if reservedAmount >= amountToReserve {
				return reservedAmount, nil
			}
			continue
		}
		unconfirmed = append(unconfirmed, row)
	}
	if err := rows.Err(); err != nil {
		return reservedAmount, err
	}
	for _, row := range unconfirmed {
		if reservedAmount >= amountToReserve {
			break
		}
		contribution, err := sqliteSpendableEffectiveAmount(row, feePerByte, effectiveAmount)
		if err != nil {
			return reservedAmount, err
		}
		*selected = append(*selected, row)
		*selectedIDs = append(*selectedIDs, row.TXOID)
		reservedAmount, err = addSpendableEffectiveAmount(reservedAmount, contribution)
		if err != nil {
			return reservedAmount, err
		}
	}
	return reservedAmount, nil
}

func spendableOutputQuery(accountIDs []string, sqliteSelector bool) (string, []any) {
	statement := `SELECT tx.txid, txo.txoid, tx.raw, typeof(tx.raw),
        tx.height, tx.position, txo.position, tx.is_verified, txo.amount,
        txo.script, typeof(txo.script)
        FROM txo
        INNER JOIN account_address USING (address)
        LEFT JOIN txi AS spent USING (txoid)
        INNER JOIN tx USING (txid)
        WHERE spent.txoid IS NULL AND tx.txid IS NOT NULL
          AND NOT txo.is_reserved`
	arguments := make([]any, 0, len(accountIDs)+2)
	if sqliteSelector {
		statement += " AND txo.txo_type = 0 AND txo.amount >= ? AND txo.amount < ?"
	} else {
		statement += " AND txo.txo_type IN (0, 4)"
	}
	if len(accountIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(accountIDs)), ",")
		statement += " AND account_address.account IN (" + placeholders + ")"
		for _, accountID := range accountIDs {
			arguments = append(arguments, accountID)
		}
	}
	if sqliteSelector {
		statement += " ORDER BY txo.amount ASC, tx.height DESC"
	} else {
		statement += ` ORDER BY tx.height IN (0, -1) DESC, tx.height DESC,
            tx.position DESC, txo.position`
	}
	return statement, arguments
}

func scanSpendableOutputs(rows *sql.Rows) ([]SpendableOutputRow, error) {
	outputs := make([]SpendableOutputRow, 0)
	for rows.Next() {
		output, err := scanSpendableOutput(rows)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return outputs, nil
}

func scanSpendableOutput(rows *sql.Rows) (SpendableOutputRow, error) {
	var output SpendableOutputRow
	var rawStorageClass, scriptStorageClass string
	var isVerified sql.NullInt64
	if err := rows.Scan(
		&output.TXID, &output.TXOID, &output.Raw, &rawStorageClass,
		&output.Height, &output.TXPosition, &output.OutputPosition,
		&isVerified, &output.Amount, &output.Script, &scriptStorageClass,
	); err != nil {
		return SpendableOutputRow{}, err
	}
	output.IsVerified = isVerified.Valid && isVerified.Int64 != 0
	output.Raw = cloneStoredBytes(output.Raw, rawStorageClass)
	output.Script = cloneStoredBytes(output.Script, scriptStorageClass)
	return output, nil
}

func checkedPositiveProduct(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	if left < 0 || right < 0 || left > math.MaxInt64/right {
		return 0, false
	}
	return left * right, true
}

func addSpendableEffectiveAmount(left, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) ||
		(right < 0 && left < math.MinInt64-right) {
		return 0, ErrSpendableAmountOverflow
	}
	return left + right, nil
}
