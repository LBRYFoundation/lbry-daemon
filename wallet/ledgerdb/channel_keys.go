package ledgerdb

import (
	"bytes"
	"context"
)

const ChannelTXOType = 2

type DecodeChannelPublicKey func(script []byte) (key []byte, isChannel bool, err error)

func (database *DB) IsChannelKeyUsed(
	ctx context.Context, accountAddress string, candidate []byte,
	decode DecodeChannelPublicKey,
) (bool, error) {
	candidate = append([]byte(nil), candidate...)
	scripts, err := database.channelScripts(ctx, accountAddress)
	if err != nil {
		return false, err
	}
	for _, script := range scripts {
		if decode == nil {
			return false, ErrChannelDecoderRequired
		}
		publicKey, isChannel, err := decode(script)
		if err != nil {
			return false, err
		}
		if isChannel && bytes.Equal(publicKey, candidate) {
			return true, nil
		}
	}
	return false, nil
}

func (database *DB) channelScripts(
	ctx context.Context, accountAddress string,
) ([][]byte, error) {
	if database == nil {
		return nil, ErrNotOpen
	}
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.sql == nil {
		return nil, ErrNotOpen
	}
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rows, err := transaction.QueryContext(ctx, `
        SELECT txo.script
        FROM txo JOIN tx ON (tx.txid=txo.txid)
        WHERE txo.address IN (
            SELECT address FROM account_address WHERE account = ?
        ) AND txo.txo_type = ?
        ORDER BY tx.height in (0, -1) DESC,
                 tx.height DESC, tx.position DESC, txo.position`,
		accountAddress, ChannelTXOType,
	)
	if err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	var scripts [][]byte
	for rows.Next() {
		var script []byte
		if err := rows.Scan(&script); err != nil {
			_ = rows.Close()
			_ = transaction.Rollback()
			return nil, err
		}
		scripts = append(scripts, append([]byte(nil), script...))
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		_ = transaction.Rollback()
		return nil, rowsErr
	}
	if closeErr != nil {
		_ = transaction.Rollback()
		return nil, closeErr
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return scripts, nil
}
