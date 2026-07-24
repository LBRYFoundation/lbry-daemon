package ledgerdb

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

const transactionReadVariableLimit = 900

// StoredTransactionOutput is the exact txo-table projection needed to resolve
// a transaction input. Nullable columns remain pointers so a legacy or
// externally-created current-version database can be read without inventing
// metadata values.
type StoredTransactionOutput struct {
	TXID            string
	TXOID           string
	Address         *string
	Position        int64
	Amount          int64
	Script          []byte
	IsReserved      bool
	TXOType         int64
	ClaimID         *string
	ClaimName       *string
	HasSource       *bool
	ChannelID       *string
	RepostedClaimID *string
}

// GetTransaction returns the stored transaction row for txid. A missing
// transaction is reported as (nil, nil), matching the pinned SDK's
// Database.get_transaction behavior.
func (database *DB) GetTransaction(
	ctx context.Context, txid string,
) (*TransactionRow, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}

	var row TransactionRow
	var purchasedClaimID sql.NullString
	var day sql.NullFloat64
	var rawStorageClass string
	err := database.sql.QueryRowContext(ctx, `
        SELECT txid, raw, typeof(raw), height, position, is_verified,
               purchased_claim_id, day
        FROM tx WHERE txid = ? LIMIT 1`, txid).Scan(
		&row.TXID, &row.Raw, &rawStorageClass, &row.Height, &row.Position,
		&row.IsVerified, &purchasedClaimID, &day,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Raw = cloneStoredBytes(row.Raw, rawStorageClass)
	row.PurchasedClaimID = nullableString(purchasedClaimID)
	row.Day = nullableFloat64(day)
	return &row, nil
}

// GetOutputsByID returns one entry for every requested txoid present in the
// database. Duplicate IDs collapse and missing IDs are absent, as in the
// dictionary built by the pinned SDK while resolving transaction inputs.
func (database *DB) GetOutputsByID(
	ctx context.Context, txoids []string,
) (map[string]StoredTransactionOutput, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}

	outputs := make(map[string]StoredTransactionOutput)
	if len(txoids) == 0 {
		return outputs, nil
	}

	unique := make(map[string]struct{}, len(txoids))
	for _, txoid := range txoids {
		unique[txoid] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for txoid := range unique {
		ordered = append(ordered, txoid)
	}
	sort.Strings(ordered)

	for offset := 0; offset < len(ordered); offset += transactionReadVariableLimit {
		end := offset + transactionReadVariableLimit
		if end > len(ordered) {
			end = len(ordered)
		}
		batch := ordered[offset:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		arguments := make([]any, len(batch))
		for index, txoid := range batch {
			arguments[index] = txoid
		}
		rows, err := database.sql.QueryContext(ctx, `
            SELECT txid, txoid, address, position, amount, script, typeof(script),
                   is_reserved, txo_type, claim_id, claim_name, has_source,
                   channel_id, reposted_claim_id
            FROM txo WHERE txoid IN (`+placeholders+`)
            ORDER BY txoid`, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var output StoredTransactionOutput
			var address, claimID, claimName, channelID, repostedClaimID sql.NullString
			var hasSource sql.NullBool
			var scriptStorageClass string
			if err := rows.Scan(
				&output.TXID, &output.TXOID, &address, &output.Position,
				&output.Amount, &output.Script, &scriptStorageClass,
				&output.IsReserved, &output.TXOType, &claimID, &claimName,
				&hasSource, &channelID, &repostedClaimID,
			); err != nil {
				_ = rows.Close()
				return nil, err
			}
			output.Script = cloneStoredBytes(output.Script, scriptStorageClass)
			output.Address = nullableString(address)
			output.ClaimID = nullableString(claimID)
			output.ClaimName = nullableString(claimName)
			output.HasSource = nullableBool(hasSource)
			output.ChannelID = nullableString(channelID)
			output.RepostedClaimID = nullableString(repostedClaimID)
			outputs[output.TXOID] = output
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if rowsErr != nil {
			return nil, rowsErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return outputs, nil
}

func cloneStoredBytes(value []byte, storageClass string) []byte {
	if storageClass == "null" {
		return nil
	}
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableFloat64(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullableBool(value sql.NullBool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}
