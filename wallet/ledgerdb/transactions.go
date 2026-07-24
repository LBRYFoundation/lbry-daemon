package ledgerdb

import (
	"context"
	"database/sql"
	"strings"
)

// TransactionIORow is the already-projected portion of one wallet transaction
// that belongs in the ledger database for a watched address.
type TransactionIORow struct {
	Transaction TransactionRow
	Inputs      []TransactionInputRow
	Outputs     []TransactionOutputRow
}

// TransactionRow contains the columns projected for the tx table.
type TransactionRow struct {
	TXID             string
	Raw              []byte
	Height           int64
	Position         int64
	IsVerified       bool
	PurchasedClaimID *string
	Day              *float64
}

// TransactionInputRow omits TXID and Address because the pinned SDK always
// takes those values from the containing transaction and watched address.
type TransactionInputRow struct {
	TXOID    string
	Position int64
}

// TransactionOutputRow contains projected claim metadata. The caller owns
// script decoding; this database boundary does not decode claim payloads.
type TransactionOutputRow struct {
	TXOID           string
	Address         *string
	Position        int64
	Amount          int64
	Script          []byte
	TXOType         int64
	ClaimID         *string
	ClaimName       *string
	HasSource       bool
	ChannelID       *string
	RepostedClaimID *string
}

// SaveTransactionIOBatch mirrors Database.save_transaction_io_batch from the
// pinned Python SDK. Every transaction, input, output, and the final address
// history update either commits together or rolls back together.
func (database *DB) SaveTransactionIOBatch(
	ctx context.Context, rows []TransactionIORow, address, history string,
) error {
	usedTimes := strings.Count(history, ":") / 2
	return database.transaction(ctx, func(transaction *sql.Tx) error {
		var insertTransaction, insertInput, insertOutput *sql.Stmt
		defer func() {
			if insertTransaction != nil {
				_ = insertTransaction.Close()
			}
			if insertInput != nil {
				_ = insertInput.Close()
			}
			if insertOutput != nil {
				_ = insertOutput.Close()
			}
		}()

		for _, row := range rows {
			if insertTransaction == nil {
				var err error
				insertTransaction, err = transaction.PrepareContext(ctx, `
                    INSERT OR REPLACE INTO tx
                    (txid, raw, height, position, is_verified,
                     purchased_claim_id, day)
                    VALUES (?, ?, ?, ?, ?, ?, ?)`)
				if err != nil {
					return err
				}
			}
			stored := row.Transaction
			if _, err := insertTransaction.ExecContext(
				ctx, stored.TXID, stored.Raw, stored.Height, stored.Position,
				stored.IsVerified, stored.PurchasedClaimID, stored.Day,
			); err != nil {
				return err
			}
			for _, input := range row.Inputs {
				if insertInput == nil {
					var err error
					insertInput, err = transaction.PrepareContext(ctx, `
                        INSERT OR IGNORE INTO txi
                        (txid, txoid, address, position) VALUES (?, ?, ?, ?)`)
					if err != nil {
						return err
					}
				}
				if _, err := insertInput.ExecContext(
					ctx, stored.TXID, input.TXOID, address, input.Position,
				); err != nil {
					return err
				}
			}
			for _, output := range row.Outputs {
				if insertOutput == nil {
					var err error
					insertOutput, err = transaction.PrepareContext(ctx, `
                        INSERT OR IGNORE INTO txo
                        (txid, txoid, address, position, amount, script,
                         txo_type, claim_id, claim_name, has_source,
                         channel_id, reposted_claim_id)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
					if err != nil {
						return err
					}
				}
				if _, err := insertOutput.ExecContext(
					ctx, stored.TXID, output.TXOID, output.Address,
					output.Position, output.Amount, output.Script, output.TXOType,
					output.ClaimID, output.ClaimName, output.HasSource,
					output.ChannelID, output.RepostedClaimID,
				); err != nil {
					return err
				}
			}
		}

		_, err := transaction.ExecContext(ctx, `
            UPDATE pubkey_address SET history = ?, used_times = ?
            WHERE address = ?`, history, usedTimes, address)
		return err
	})
}
