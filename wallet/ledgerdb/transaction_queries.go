package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrTransactionAccountsRequired = errors.New(
	"'accounts' argument required when no 'txid' constraint is present",
)

type TransactionOrder uint8

const (
	TransactionOrderDefault TransactionOrder = iota
	TransactionOrderNone
)

// TransactionQuery is the typed transaction-selection subset used by wallet
// history hydration. A non-nil TXIDs slice records that the constraint was
// supplied; an empty supplied slice bypasses account scope but emits no IN
// predicate, matching the pinned query helper.
type TransactionQuery struct {
	AccountIDs      []string
	InputAccountIDs []string
	TXID            *string
	// TXIDValue preserves transaction_show's unvalidated Python argument. The
	// companion flag distinguishes an explicit nil constraint from no filter.
	TXIDValue               any
	HasTXIDValue            bool
	TXIDs                   []string
	PurchasedClaimID        *string
	PurchasedClaimIDs       []string
	RequirePurchasedClaimID bool
	HeightLTE               *int64
	HeightGT                *int64
	Limit                   *int
	Offset                  *int
	Order                   TransactionOrder
}

func (database *DB) ListTransactions(
	ctx context.Context, query TransactionQuery,
) ([]TransactionRow, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}
	statement, arguments, err := buildTransactionQuery(
		"tx.txid, tx.raw, typeof(tx.raw), tx.height, tx.position, tx.is_verified",
		query, false,
	)
	if err != nil {
		return nil, err
	}
	rows, err := database.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	transactions := make([]TransactionRow, 0)
	for rows.Next() {
		var transaction TransactionRow
		var rawStorageClass string
		var isVerified sql.NullInt64
		if err := rows.Scan(
			&transaction.TXID, &transaction.Raw, &rawStorageClass,
			&transaction.Height, &transaction.Position, &isVerified,
		); err != nil {
			return nil, err
		}
		transaction.Raw = cloneStoredBytes(transaction.Raw, rawStorageClass)
		transaction.IsVerified = isVerified.Valid && isVerified.Int64 != 0
		transactions = append(transactions, transaction)
	}
	return transactions, rows.Err()
}

func (database *DB) CountTransactions(
	ctx context.Context, query TransactionQuery,
) (int64, error) {
	if database == nil {
		return 0, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return 0, ErrNotOpen
	}
	statement, arguments, err := buildTransactionQuery("COUNT(*)", query, true)
	if err != nil {
		return 0, err
	}
	var count sql.NullInt64
	if err := database.sql.QueryRowContext(ctx, statement, arguments...).Scan(&count); err != nil {
		return 0, err
	}
	if !count.Valid {
		return 0, nil
	}
	return count.Int64, nil
}

func buildTransactionQuery(
	columns string, query TransactionQuery, aggregate bool,
) (string, []any, error) {
	statement := "SELECT " + columns + " FROM tx"
	where := make([]string, 0, 5)
	arguments := make(
		[]any, 0,
		len(query.AccountIDs)*2+len(query.InputAccountIDs)+len(query.TXIDs)+
			len(query.PurchasedClaimIDs)+6,
	)
	hasTXIDConstraint := query.TXID != nil || query.HasTXIDValue ||
		query.TXIDs != nil || query.InputAccountIDs != nil
	if hasTXIDConstraint {
		if query.TXID != nil {
			where = append(where, "tx.txid = ?")
			arguments = append(arguments, *query.TXID)
		}
		if query.HasTXIDValue {
			where = append(where, "tx.txid = ?")
			arguments = append(arguments, query.TXIDValue)
		}
		if len(query.TXIDs) > 0 {
			where = append(where, "tx.txid IN ("+transactionQueryPlaceholders(len(query.TXIDs))+")")
			arguments = appendTransactionQueryStrings(arguments, query.TXIDs)
		}
		if len(query.InputAccountIDs) > 0 {
			where = append(where, `tx.txid IN (
                SELECT txid FROM txi JOIN account_address USING (address)
                WHERE account_address.account IN (`+
				transactionQueryPlaceholders(len(query.InputAccountIDs))+`)
            )`)
			arguments = appendTransactionQueryStrings(arguments, query.InputAccountIDs)
		}
	} else {
		if len(query.AccountIDs) == 0 {
			return "", nil, ErrTransactionAccountsRequired
		}
		placeholders := transactionQueryNumberedPlaceholders(len(query.AccountIDs))
		where = append(where, `tx.txid IN (
            SELECT txo.txid FROM txo JOIN account_address USING (address)
                WHERE account_address.account IN (`+placeholders+`)
            UNION
            SELECT txi.txid FROM txi JOIN account_address USING (address)
                WHERE account_address.account IN (`+placeholders+`)
        )`)
		arguments = appendTransactionQueryStrings(arguments, query.AccountIDs)
	}
	if query.PurchasedClaimID != nil {
		where = append(where, "tx.purchased_claim_id = ?")
		arguments = append(arguments, *query.PurchasedClaimID)
	}
	if len(query.PurchasedClaimIDs) > 0 {
		where = append(where, "tx.purchased_claim_id IN ("+
			transactionQueryPlaceholders(len(query.PurchasedClaimIDs))+")")
		arguments = appendTransactionQueryStrings(arguments, query.PurchasedClaimIDs)
	}
	if query.RequirePurchasedClaimID {
		where = append(where, "tx.purchased_claim_id IS NOT NULL")
	}
	if query.HeightLTE != nil {
		where = append(where, "tx.height <= ?")
		arguments = append(arguments, *query.HeightLTE)
	}
	if query.HeightGT != nil {
		where = append(where, "tx.height > ?")
		arguments = append(arguments, *query.HeightGT)
	}
	if len(where) > 0 {
		statement += " WHERE " + strings.Join(where, " AND ")
	}
	if aggregate {
		return statement, arguments, nil
	}
	switch query.Order {
	case TransactionOrderDefault:
		statement += " ORDER BY tx.height = 0 DESC, tx.height DESC, tx.position DESC"
	case TransactionOrderNone:
	default:
		return "", nil, fmt.Errorf("unknown wallet transaction order %d", query.Order)
	}
	if query.Limit != nil {
		statement += " LIMIT ?"
		arguments = append(arguments, *query.Limit)
	}
	if query.Offset != nil {
		statement += " OFFSET ?"
		arguments = append(arguments, *query.Offset)
	}
	return statement, arguments, nil
}

func transactionQueryPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func transactionQueryNumberedPlaceholders(count int) string {
	placeholders := make([]string, count)
	for index := range placeholders {
		placeholders[index] = fmt.Sprintf("?%d", index+1)
	}
	return strings.Join(placeholders, ",")
}

func appendTransactionQueryStrings(arguments []any, values []string) []any {
	for _, value := range values {
		arguments = append(arguments, value)
	}
	return arguments
}
