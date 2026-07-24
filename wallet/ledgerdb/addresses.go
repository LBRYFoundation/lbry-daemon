package ledgerdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type AddressKey struct {
	Address   string
	Chain     int64
	PublicKey []byte
	ChainCode []byte
	N         int64
	Depth     int64
}

type AddressRecord struct {
	Address   string
	Account   string
	Chain     int64
	History   *string
	UsedTimes int64
	PublicKey []byte
	ChainCode []byte
	N         int64
	Depth     int64
}

type AddressOrder uint8

const (
	AddressOrderUnspecified AddressOrder = iota
	AddressOrderUsedTimesAscending
	AddressOrderIndexAscending
	AddressOrderIndexDescending
)

type AddressQuery struct {
	Account string
	Address *string
	// Addresses mirrors the SDK's __in constraint: a nonempty slice filters
	// with IN, while nil or empty omits the constraint.
	Addresses   []string
	Chain       *int64
	UsedTimesLT *int64
	Order       AddressOrder
	Limit       *int
}

// GetAddresses is the typed subset of Database.get_addresses used by address
// managers, status synchronization, and transaction waiting. Order clauses
// are closed constants so caller-controlled data is never interpolated into SQL.
func (database *DB) GetAddresses(
	ctx context.Context, query AddressQuery,
) ([]AddressRecord, error) {
	if database == nil {
		return nil, errors.New("wallet ledger database is nil")
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}

	statement := `SELECT address, account, chain, history, used_times,
        pubkey, chain_code, n, depth
        FROM pubkey_address JOIN account_address USING (address)`
	where := make([]string, 0, 4)
	arguments := make([]any, 0, 5)
	if query.Account != "" {
		where = append(where, "account = ?")
		arguments = append(arguments, query.Account)
	}
	if query.Address != nil {
		where = append(where, "address = ?")
		arguments = append(arguments, *query.Address)
	}
	if len(query.Addresses) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(query.Addresses)), ",")
		where = append(where, "address IN ("+placeholders+")")
		for _, address := range query.Addresses {
			arguments = append(arguments, address)
		}
	}
	if query.Chain != nil {
		where = append(where, "chain = ?")
		arguments = append(arguments, *query.Chain)
	}
	if query.UsedTimesLT != nil {
		where = append(where, "used_times < ?")
		arguments = append(arguments, *query.UsedTimesLT)
	}
	if len(where) > 0 {
		statement += " WHERE " + strings.Join(where, " AND ")
	}
	switch query.Order {
	case AddressOrderUnspecified:
	case AddressOrderUsedTimesAscending:
		statement += " ORDER BY used_times ASC, n ASC"
	case AddressOrderIndexAscending:
		statement += " ORDER BY n ASC"
	case AddressOrderIndexDescending:
		statement += " ORDER BY n DESC"
	default:
		return nil, fmt.Errorf("unknown wallet address order %d", query.Order)
	}
	if query.Limit != nil {
		statement += " LIMIT ?"
		arguments = append(arguments, *query.Limit)
	}

	rows, err := database.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]AddressRecord, 0)
	for rows.Next() {
		var record AddressRecord
		var history sql.NullString
		if err := rows.Scan(
			&record.Address, &record.Account, &record.Chain, &history,
			&record.UsedTimes, &record.PublicKey, &record.ChainCode,
			&record.N, &record.Depth,
		); err != nil {
			return nil, err
		}
		if history.Valid {
			value := history.String
			record.History = &value
		}
		record.PublicKey = append([]byte(nil), record.PublicKey...)
		record.ChainCode = append([]byte(nil), record.ChainCode...)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (database *DB) GetAddress(
	ctx context.Context, address string,
) (*AddressRecord, error) {
	limit := 1
	records, err := database.GetAddresses(ctx, AddressQuery{Address: &address, Limit: &limit})
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return &records[0], nil
}

// AddKeys intentionally uses two transactions. The pinned SDK first persists
// account_address rows and only then pubkey_address rows, so a second-phase
// failure leaves the first phase committed.
func (database *DB) AddKeys(
	ctx context.Context, accountID string, addressKeys []AddressKey,
) error {
	owned := make([]AddressKey, len(addressKeys))
	for index, addressKey := range addressKeys {
		owned[index] = addressKey
		owned[index].PublicKey = append([]byte(nil), addressKey.PublicKey...)
		owned[index].ChainCode = append([]byte(nil), addressKey.ChainCode...)
	}
	if err := database.transaction(ctx, func(transaction *sql.Tx) error {
		statement, err := transaction.PrepareContext(ctx, `
            insert or ignore into account_address
            (account, address, chain, pubkey, chain_code, n, depth) values
            (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, addressKey := range owned {
			if _, err := statement.ExecContext(
				ctx, accountID, addressKey.Address, addressKey.Chain,
				addressKey.PublicKey, addressKey.ChainCode, addressKey.N, addressKey.Depth,
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return database.transaction(ctx, func(transaction *sql.Tx) error {
		statement, err := transaction.PrepareContext(
			ctx, "insert or ignore into pubkey_address (address) values (?)",
		)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, addressKey := range owned {
			if _, err := statement.ExecContext(ctx, addressKey.Address); err != nil {
				return err
			}
		}
		return nil
	})
}

func (database *DB) SetAddressHistory(
	ctx context.Context, address, history string,
) error {
	usedTimes := strings.Count(history, ":") / 2
	return database.transaction(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(
			ctx,
			"UPDATE pubkey_address SET history = ?, used_times = ? WHERE address = ?",
			history, usedTimes, address,
		)
		return err
	})
}
