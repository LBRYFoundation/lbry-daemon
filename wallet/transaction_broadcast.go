package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
)

var ErrTransactionBroadcastUnavailable = errors.New("transaction broadcast RPC is unavailable")

// LedgerSPVTransactionBroadcaster is optional so header-only and test network
// implementations do not need to expose transaction submission.
type LedgerSPVTransactionBroadcaster interface {
	BroadcastTransaction(context.Context, string) (any, error)
}

// BroadcastTransaction sends the transaction through the active SPV session.
// The network boundary deliberately performs no inline retry.
func (ledger *Ledger) BroadcastTransaction(
	ctx context.Context, transaction *Transaction,
) (any, error) {
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger is nil", ErrTransactionBroadcastUnavailable)
	}
	if transaction == nil {
		return nil, fmt.Errorf("%w: transaction is nil", ErrTransactionBroadcastUnavailable)
	}
	broadcaster, ok := ledger.SPVNetwork.(LedgerSPVTransactionBroadcaster)
	if !ok || isNilTransactionBroadcaster(broadcaster) {
		return nil, fmt.Errorf(
			"%w: ledger SPV network does not support transaction broadcast",
			ErrTransactionBroadcastUnavailable,
		)
	}
	if transaction.Raw == nil {
		if err := transaction.RebuildDerived(); err != nil {
			return nil, err
		}
	}
	return broadcaster.BroadcastTransaction(ctx, hex.EncodeToString(transaction.Raw))
}

func isNilTransactionBroadcaster(broadcaster LedgerSPVTransactionBroadcaster) bool {
	if broadcaster == nil {
		return true
	}
	value := reflect.ValueOf(broadcaster)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// BroadcastOrRelease mirrors Ledger.broadcast_or_release. Only a broadcast
// failure releases inputs; a later blocking-wait failure leaves them reserved.
func (ledger *Ledger) BroadcastOrRelease(
	ctx context.Context, transaction *Transaction, blocking bool,
) error {
	if _, err := ledger.BroadcastTransaction(ctx, transaction); err != nil {
		cleanupContext := context.Background()
		if ctx != nil {
			cleanupContext = context.WithoutCancel(ctx)
		}
		if releaseErr := ledger.ReleaseTransaction(cleanupContext, transaction); releaseErr != nil {
			return releaseErr
		}
		return err
	}
	if blocking {
		return ledger.WaitTransaction(ctx, transaction, -1, 0)
	}
	return nil
}
