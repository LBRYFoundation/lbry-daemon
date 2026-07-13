package ledgerdb

import (
	"context"
	"errors"
	"testing"
)

func TestIsChannelKeyUsedFiltersAndDecodesInPinnedOrder(t *testing.T) {
	t.Parallel()

	candidate := []byte{2, 3, 4}
	database := newChannelTestDatabase(t)
	insertChannelTestTX(t, database, "confirmed", 5, 0)
	insertChannelTestTX(t, database, "unconfirmed", 0, 0)
	insertChannelTestTXO(t, database, "confirmed", "confirmed:0", "owned", 0, []byte("decoder-error"), ChannelTXOType, false)
	insertChannelTestTXO(t, database, "unconfirmed", "unconfirmed:0", "owned", 0, []byte("match"), ChannelTXOType, true)
	insertChannelTestTXO(t, database, "confirmed", "confirmed:1", "other", 1, []byte("decoder-error"), ChannelTXOType, false)
	insertChannelTestTXO(t, database, "confirmed", "confirmed:2", "owned", 2, []byte("decoder-error"), 1, false)
	insertChannelTestTXO(t, database, "missing", "missing:0", "owned", 0, []byte("decoder-error"), ChannelTXOType, false)

	var decoded []string
	used, err := database.IsChannelKeyUsed(
		context.Background(), "account", candidate,
		func(script []byte) ([]byte, bool, error) {
			decoded = append(decoded, string(script))
			switch string(script) {
			case "match":
				return candidate, true, nil
			case "decoder-error":
				return nil, false, errors.New("decode failure")
			default:
				return nil, false, nil
			}
		},
	)
	if err != nil || !used {
		t.Fatalf("usage = %t, %v", used, err)
	}
	if len(decoded) != 1 || decoded[0] != "match" {
		t.Fatalf("decode order = %v, want [match]", decoded)
	}
}

func TestIsChannelKeyUsedPropagatesEarlierDecodeError(t *testing.T) {
	t.Parallel()

	database := newChannelTestDatabase(t)
	insertChannelTestTX(t, database, "confirmed", 5, 0)
	insertChannelTestTX(t, database, "unconfirmed", -1, 1)
	insertChannelTestTXO(t, database, "confirmed", "confirmed:0", "owned", 0, []byte("match"), ChannelTXOType, false)
	insertChannelTestTXO(t, database, "unconfirmed", "unconfirmed:0", "owned", 0, []byte("bad"), ChannelTXOType, false)
	wantErr := errors.New("decoded channel key is invalid")
	used, err := database.IsChannelKeyUsed(
		context.Background(), "account", []byte("candidate"),
		func(script []byte) ([]byte, bool, error) {
			if string(script) == "bad" {
				return nil, false, wantErr
			}
			return []byte("candidate"), true, nil
		},
	)
	if used || !errors.Is(err, wantErr) {
		t.Fatalf("usage = %t, error %v", used, err)
	}
}

func TestIsChannelKeyUsedCommitsBeforeDecoderAndHandlesEmptyRows(t *testing.T) {
	t.Parallel()

	database := newChannelTestDatabase(t)
	if used, err := database.IsChannelKeyUsed(context.Background(), "account", nil, nil); err != nil || used {
		t.Fatalf("empty usage = %t, %v", used, err)
	}
	insertChannelTestTX(t, database, "tx", 1, 0)
	insertChannelTestTXO(t, database, "tx", "tx:0", "owned", 0, []byte("malformed"), ChannelTXOType, false)
	called := false
	used, err := database.IsChannelKeyUsed(
		context.Background(), "account", []byte("candidate"),
		func(script []byte) ([]byte, bool, error) {
			called = true
			// Re-entering a writer operation proves the SELECT transaction and DB
			// mutex were released before claim decoding, as in Python.
			if err := database.SetAddressHistory(context.Background(), "owned", "tx:0:"); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		},
	)
	if err != nil || used || !called {
		t.Fatalf("reentrant decode = used %t called %t err %v", used, called, err)
	}
	if _, err := database.IsChannelKeyUsed(context.Background(), "account", nil, nil); !errors.Is(err, ErrChannelDecoderRequired) {
		t.Fatalf("missing decoder error = %v", err)
	}
}

func newChannelTestDatabase(t *testing.T) *DB {
	t.Helper()
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := database.AddKeys(context.Background(), "account", []AddressKey{{
		Address: "owned", PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddKeys(context.Background(), "other-account", []AddressKey{{
		Address: "other", PublicKey: []byte{3}, ChainCode: []byte{4},
	}}); err != nil {
		t.Fatal(err)
	}
	return database
}

func insertChannelTestTX(t *testing.T, database *DB, txid string, height, position int) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO tx
        (txid, raw, height, position, is_verified) VALUES (?, x'00', ?, ?, 0)`,
		txid, height, position,
	)
}

func insertChannelTestTXO(
	t *testing.T, database *DB, txid, txoid, address string, position int,
	script []byte, txoType int, reserved bool,
) {
	t.Helper()
	mustExec(t, database.sql, `INSERT INTO txo
        (txid, txoid, address, position, amount, script, is_reserved, txo_type)
        VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		txid, txoid, address, position, script, reserved, txoType,
	)
}
