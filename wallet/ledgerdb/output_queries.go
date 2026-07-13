package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrOutputAnnotationAccountsRequired = errors.New(
	"wallet accounts are required for transaction output ownership annotations",
)

type OutputOrder uint8

const (
	OutputOrderDefault OutputOrder = iota
	OutputOrderNone
	OutputOrderTransactionID
	OutputOrderOutputID
	OutputOrderName
	OutputOrderHeight
	OutputOrderAmount
)

// OutputQuery is the typed subset of Database.select_txos needed by public
// UTXO, aggregate, and balance calls. Empty IN slices deliberately omit their
// constraint, matching constraints_to_sql in the pinned SDK.
type OutputQuery struct {
	// NoTransaction omits parent raw transaction bytes and materializes outputs
	// from indexed txo columns, matching Database.get_txos(no_tx=True).
	NoTransaction        bool
	AccountIDs           []string
	AnnotationAccountIDs []string
	// SkipAccountOutputConstraint is used by the public is_my_input_or_output
	// union. AccountIDs otherwise imply the SDK's default is_my_output=True.
	SkipAccountOutputConstraint bool
	TXID                        string
	TXIDs                       []string
	TXOID                       string
	TXOIDs                      []string
	ClaimIDs                    []string
	ClaimNames                  []string
	ChannelIDs                  []string
	NotChannelIDs               []string
	RepostedClaimIDs            []string
	PurchasedClaimIDs           []string
	Types                       []int64
	HasSource                   *bool
	HeightLTE                   *int64
	HeightGT                    *int64
	DayGTE                      *float64
	DayLTE                      *float64
	IsSpent                     *bool
	IsMyInput                   *bool
	IsMyOutput                  *bool
	IsMyInputOrOutput           bool
	ExcludeInternalTransfers    bool
	IncludeIsSpent              bool
	IncludeIsMyInput            bool
	IncludeIsMyOutput           bool
	IncludeReceivedTips         bool
	Limit                       *int
	Offset                      *int
	Order                       OutputOrder
}

// OutputRow retains both stored output columns and parent transaction bytes.
// Wallet hydration normally takes amount and script from Raw, as Python does.
type OutputRow struct {
	TXID           string
	TXOID          string
	Address        *string
	Raw            []byte
	Height         int64
	TXPosition     int64
	OutputPosition int64
	IsVerified     bool
	Amount         int64
	Script         []byte
	IsReserved     bool
	TXOType        int64
	IsSpent        *bool
	IsMyInput      *bool
	IsMyOutput     *bool
	ReceivedTips   *int64
}

func (database *DB) ListOutputs(
	ctx context.Context, query OutputQuery,
) ([]OutputRow, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}
	columns, annotationArguments, err := outputListSelection(query)
	if err != nil {
		return nil, err
	}
	statement, arguments, err := buildOutputQuery(columns, query, false)
	if err != nil {
		return nil, err
	}
	arguments = append(annotationArguments, arguments...)
	rows, err := database.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outputs := make([]OutputRow, 0)
	for rows.Next() {
		output, err := scanOutputRow(rows, query)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, rows.Err()
}

func (database *DB) CountOutputs(ctx context.Context, query OutputQuery) (int64, error) {
	return database.aggregateOutputs(ctx, "COUNT(*)", query)
}

func (database *DB) SumOutputs(ctx context.Context, query OutputQuery) (int64, error) {
	return database.aggregateOutputs(ctx, "SUM(txo.amount)", query)
}

func (database *DB) aggregateOutputs(
	ctx context.Context, expression string, query OutputQuery,
) (int64, error) {
	if database == nil {
		return 0, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return 0, ErrNotOpen
	}
	statement, arguments, err := buildOutputQuery(expression, query, true)
	if err != nil {
		return 0, err
	}
	var result sql.NullInt64
	if err := database.sql.QueryRowContext(ctx, statement, arguments...).Scan(&result); err != nil {
		return 0, err
	}
	if !result.Valid {
		return 0, nil
	}
	return result.Int64, nil
}

// ReleaseAllOutputs clears stale reservations globally or for one account's
// address inventory. Unknown accounts simply update zero rows.
func (database *DB) ReleaseAllOutputs(ctx context.Context, accountID *string) error {
	if database == nil {
		return ErrNotOpen
	}
	return database.transaction(ctx, func(transaction *sql.Tx) error {
		if accountID == nil {
			_, err := transaction.ExecContext(
				ctx, "UPDATE txo SET is_reserved = 0 WHERE is_reserved = 1",
			)
			return err
		}
		_, err := transaction.ExecContext(ctx, `UPDATE txo SET is_reserved = 0 WHERE
            is_reserved = 1 AND txo.address IN (
                SELECT address FROM account_address WHERE account = ?
            )`, *accountID)
		return err
	})
}

const outputListColumns = `tx.txid, txo.txoid, txo.address,
    tx.raw, typeof(tx.raw), tx.height, tx.position, txo.position,
    tx.is_verified, txo.amount, txo.script, typeof(txo.script),
    txo.is_reserved, txo.txo_type`

const outputListColumnsWithoutTransaction = `tx.txid, txo.txoid, txo.address,
    tx.height, tx.position, txo.position, tx.is_verified,
    txo.amount, txo.script, typeof(txo.script), txo.is_reserved, txo.txo_type`

func outputListSelection(query OutputQuery) (string, []any, error) {
	columns := outputListColumns
	if query.NoTransaction {
		columns = outputListColumnsWithoutTransaction
	}
	arguments := make([]any, 0, len(query.AnnotationAccountIDs))
	if query.IncludeIsSpent {
		columns += ", spent.txoid IS NOT NULL"
	}
	needsAnnotationAccounts :=
		(query.IncludeIsMyOutput && query.IsMyOutput == nil) ||
			(query.IncludeIsMyInput && query.IsMyInput == nil) ||
			query.IncludeReceivedTips
	if needsAnnotationAccounts {
		if len(query.AnnotationAccountIDs) == 0 {
			return "", nil, ErrOutputAnnotationAccountsRequired
		}
		arguments = appendOutputStrings(arguments, query.AnnotationAccountIDs)
	}
	if query.IncludeIsMyOutput {
		if query.IsMyOutput != nil {
			columns += ", " + outputQuerySQLBool(*query.IsMyOutput)
		} else {
			columns += ", txo.address IN (SELECT address FROM account_address WHERE account IN (" +
				outputQueryNumberedPlaceholders(len(query.AnnotationAccountIDs)) + "))"
		}
	}
	if query.IncludeIsMyInput {
		if query.IsMyInput != nil {
			columns += ", " + outputQuerySQLBool(*query.IsMyInput)
		} else {
			columns += `, created_by.address IS NOT NULL AND created_by.address IN (
            SELECT address FROM account_address WHERE account IN (` +
				outputQueryNumberedPlaceholders(len(query.AnnotationAccountIDs)) + "))"
		}
	}
	if query.IncludeReceivedTips {
		columns += `, (
            SELECT COALESCE(SUM(support.amount), 0) FROM txo AS support WHERE
                support.claim_id = txo.claim_id AND
                support.txo_type = 3 AND
                support.address IN (
                    SELECT address FROM account_address WHERE account IN (` +
			outputQueryNumberedPlaceholders(len(query.AnnotationAccountIDs)) + `)
                ) AND
                support.txoid NOT IN (SELECT txoid FROM txi)
        )`
	}
	return columns, arguments, nil
}

func buildOutputQuery(
	columns string, query OutputQuery, aggregate bool,
) (string, []any, error) {
	statement := "SELECT " + columns + ` FROM txo
        JOIN tx ON (tx.txid = txo.txid)`
	if query.IsSpent != nil || (!aggregate && query.IncludeIsSpent) {
		statement += " LEFT JOIN txi AS spent ON (spent.txoid = txo.txoid)"
	}
	needsCreatedBy := (!aggregate && query.IncludeIsMyInput) ||
		(len(query.AccountIDs) > 0 && (query.IsMyInput != nil ||
			query.IsMyInputOrOutput || query.ExcludeInternalTransfers))
	if needsCreatedBy {
		statement += " LEFT JOIN txi AS created_by ON (created_by.position = 0 AND created_by.txid = txo.txid)"
	}
	where := make([]string, 0, 20)
	arguments := make(
		[]any, 0,
		len(query.AccountIDs)*5+len(query.TXIDs)+len(query.TXOIDs)+len(query.ClaimIDs)+
			len(query.ClaimNames)+len(query.ChannelIDs)+len(query.NotChannelIDs)+
			len(query.RepostedClaimIDs)+len(query.PurchasedClaimIDs)+len(query.Types)+12,
	)
	if len(query.AccountIDs) > 0 {
		ownedOutput := "txo.address IN (SELECT address FROM account_address WHERE account IN (" +
			outputQueryPlaceholders(len(query.AccountIDs)) + "))"
		createdByWallet := "created_by.address IN (SELECT address FROM account_address WHERE account IN (" +
			outputQueryPlaceholders(len(query.AccountIDs)) + "))"
		if query.IsMyInputOrOutput {
			where = append(where, "("+ownedOutput+" OR (created_by.address IS NOT NULL AND "+createdByWallet+"))")
			arguments = appendOutputStrings(arguments, query.AccountIDs)
			arguments = appendOutputStrings(arguments, query.AccountIDs)
		} else {
			switch {
			case query.IsMyOutput != nil && *query.IsMyOutput:
				where = append(where, ownedOutput)
				arguments = appendOutputStrings(arguments, query.AccountIDs)
			case query.IsMyOutput != nil && !*query.IsMyOutput:
				where = append(where, "txo.address NOT IN (SELECT address FROM account_address WHERE account IN ("+
					outputQueryPlaceholders(len(query.AccountIDs))+"))")
				arguments = appendOutputStrings(arguments, query.AccountIDs)
			case !query.SkipAccountOutputConstraint:
				where = append(where, ownedOutput)
				arguments = appendOutputStrings(arguments, query.AccountIDs)
			}
			if query.IsMyInput != nil {
				if *query.IsMyInput {
					where = append(where, "created_by.address IS NOT NULL", createdByWallet)
				} else {
					where = append(where, "(created_by.address IS NULL OR created_by.address NOT IN (SELECT address FROM account_address WHERE account IN ("+
						outputQueryPlaceholders(len(query.AccountIDs))+")))")
				}
				arguments = appendOutputStrings(arguments, query.AccountIDs)
			}
		}
		if query.ExcludeInternalTransfers {
			where = append(where, "(txo.txo_type != 0 OR txo.address NOT IN (SELECT address FROM account_address WHERE account IN ("+
				outputQueryPlaceholders(len(query.AccountIDs))+
				")) OR created_by.address IS NULL OR created_by.address NOT IN (SELECT address FROM account_address WHERE account IN ("+
				outputQueryPlaceholders(len(query.AccountIDs))+")))")
			arguments = appendOutputStrings(arguments, query.AccountIDs)
			arguments = appendOutputStrings(arguments, query.AccountIDs)
		}
	}
	if query.TXID != "" {
		where = append(where, "txo.txid = ?")
		arguments = append(arguments, query.TXID)
	}
	if len(query.TXIDs) > 0 {
		where = append(where, "txo.txid IN ("+outputQueryPlaceholders(len(query.TXIDs))+")")
		arguments = appendOutputStrings(arguments, query.TXIDs)
	}
	if query.TXOID != "" {
		where = append(where, "txo.txoid = ?")
		arguments = append(arguments, query.TXOID)
	}
	if len(query.TXOIDs) > 0 {
		where = append(where, "txo.txoid IN ("+outputQueryPlaceholders(len(query.TXOIDs))+")")
		arguments = appendOutputStrings(arguments, query.TXOIDs)
	}
	if len(query.ClaimIDs) > 0 {
		where = append(where, "txo.claim_id IN ("+outputQueryPlaceholders(len(query.ClaimIDs))+")")
		arguments = appendOutputStrings(arguments, query.ClaimIDs)
	}
	if len(query.ClaimNames) > 0 {
		where = append(where, "txo.claim_name IN ("+outputQueryPlaceholders(len(query.ClaimNames))+")")
		arguments = appendOutputStrings(arguments, query.ClaimNames)
	}
	if len(query.ChannelIDs) > 0 {
		where = append(where, "txo.channel_id IN ("+outputQueryPlaceholders(len(query.ChannelIDs))+")")
		arguments = appendOutputStrings(arguments, query.ChannelIDs)
	}
	if len(query.NotChannelIDs) > 0 {
		where = append(where, "(txo.channel_id IS NULL OR txo.channel_id NOT IN ("+
			outputQueryPlaceholders(len(query.NotChannelIDs))+"))")
		arguments = appendOutputStrings(arguments, query.NotChannelIDs)
	}
	if len(query.RepostedClaimIDs) > 0 {
		where = append(where, "txo.reposted_claim_id IN ("+
			outputQueryPlaceholders(len(query.RepostedClaimIDs))+")")
		arguments = appendOutputStrings(arguments, query.RepostedClaimIDs)
	}
	if len(query.PurchasedClaimIDs) > 0 {
		where = append(where, "tx.purchased_claim_id IN ("+
			outputQueryPlaceholders(len(query.PurchasedClaimIDs))+")")
		arguments = appendOutputStrings(arguments, query.PurchasedClaimIDs)
	}
	if len(query.Types) > 0 {
		where = append(where, "txo.txo_type IN ("+outputQueryPlaceholders(len(query.Types))+")")
		for _, outputType := range query.Types {
			arguments = append(arguments, outputType)
		}
	}
	if query.HasSource != nil {
		where = append(where, "txo.has_source = ?")
		arguments = append(arguments, *query.HasSource)
	}
	if query.HeightLTE != nil {
		where = append(where, "tx.height <= ?")
		arguments = append(arguments, *query.HeightLTE)
	}
	if query.HeightGT != nil {
		where = append(where, "tx.height > ?")
		arguments = append(arguments, *query.HeightGT)
	}
	if query.DayGTE != nil {
		where = append(where, "tx.day >= ?")
		arguments = append(arguments, *query.DayGTE)
	}
	if query.DayLTE != nil {
		where = append(where, "tx.day <= ?")
		arguments = append(arguments, *query.DayLTE)
	}
	if query.IsSpent != nil {
		if *query.IsSpent {
			where = append(where, "spent.txoid IS NOT NULL")
		} else {
			where = append(where, "txo.is_reserved = 0", "spent.txoid IS NULL")
		}
	}
	if len(where) > 0 {
		statement += " WHERE " + strings.Join(where, " AND ")
	}
	if aggregate {
		return statement, arguments, nil
	}
	switch query.Order {
	case OutputOrderDefault:
		statement += ` ORDER BY tx.height IN (0, -1) DESC, tx.height DESC,
            tx.position DESC, txo.position`
	case OutputOrderNone:
	case OutputOrderTransactionID:
		statement += " ORDER BY txo.txid"
	case OutputOrderOutputID:
		statement += " ORDER BY txo.txoid"
	case OutputOrderName:
		statement += " ORDER BY txo.claim_name"
	case OutputOrderHeight:
		statement += ` ORDER BY tx.height IN (0, -1) DESC, tx.height DESC,
            tx.position DESC, txo.position`
	case OutputOrderAmount:
		statement += " ORDER BY txo.amount"
	default:
		return "", nil, fmt.Errorf("unknown wallet output order %d", query.Order)
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

func scanOutputRow(rows *sql.Rows, query OutputQuery) (OutputRow, error) {
	var output OutputRow
	var address sql.NullString
	var rawStorageClass, scriptStorageClass string
	var isVerified, isReserved sql.NullInt64
	destinations := []any{
		&output.TXID, &output.TXOID, &address,
	}
	if !query.NoTransaction {
		destinations = append(destinations, &output.Raw, &rawStorageClass)
	}
	destinations = append(destinations,
		&output.Height, &output.TXPosition,
		&output.OutputPosition, &isVerified, &output.Amount,
		&output.Script, &scriptStorageClass, &isReserved, &output.TXOType,
	)
	var isSpent, isMyOutput, isMyInput, receivedTips sql.NullInt64
	if query.IncludeIsSpent {
		destinations = append(destinations, &isSpent)
	}
	if query.IncludeIsMyOutput {
		destinations = append(destinations, &isMyOutput)
	}
	if query.IncludeIsMyInput {
		destinations = append(destinations, &isMyInput)
	}
	if query.IncludeReceivedTips {
		destinations = append(destinations, &receivedTips)
	}
	if err := rows.Scan(destinations...); err != nil {
		return OutputRow{}, err
	}
	output.Address = nullableString(address)
	if !query.NoTransaction {
		output.Raw = cloneStoredBytes(output.Raw, rawStorageClass)
	}
	output.Script = cloneStoredBytes(output.Script, scriptStorageClass)
	output.IsVerified = isVerified.Valid && isVerified.Int64 != 0
	output.IsReserved = isReserved.Valid && isReserved.Int64 != 0
	if query.IncludeIsSpent {
		output.IsSpent = outputQueryBool(isSpent.Valid && isSpent.Int64 != 0)
	}
	if query.IncludeIsMyOutput {
		output.IsMyOutput = outputQueryBool(isMyOutput.Valid && isMyOutput.Int64 != 0)
	}
	if query.IncludeIsMyInput {
		output.IsMyInput = outputQueryBool(isMyInput.Valid && isMyInput.Int64 != 0)
	}
	if query.IncludeReceivedTips {
		output.ReceivedTips = outputQueryInt64(receivedTips.Int64)
	}
	return output, nil
}

func outputQueryBool(value bool) *bool {
	return &value
}

func outputQueryInt64(value int64) *int64 {
	return &value
}

func outputQuerySQLBool(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func outputQueryPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func outputQueryNumberedPlaceholders(count int) string {
	placeholders := make([]string, count)
	for index := range placeholders {
		placeholders[index] = fmt.Sprintf("?%d", index+1)
	}
	return strings.Join(placeholders, ",")
}

func appendOutputStrings(arguments []any, values []string) []any {
	for _, value := range values {
		arguments = append(arguments, value)
	}
	return arguments
}
