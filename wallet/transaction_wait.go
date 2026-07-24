package wallet

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lbry/daemon/wallet/ledgerdb"
)

var (
	ErrTransactionWaitUnavailable = errors.New("transaction wait is unavailable")
	ErrTransactionWaitTimeout     = errors.New("transaction wait timed out")
	ErrTransactionWaitInputScript = errors.New("resolved transaction input is not pay-to-public-key-hash")
	ErrTransactionWaitNoRecords   = errors.New("Set of Tasks/Futures is empty.")
)

const (
	transactionWaitBlockingSeconds = 600
	transactionWaitRoundDuration   = time.Second
)

var transactionWaitClockOrigin = time.Now()

type TransactionWaitTimeoutError struct {
	TransactionID string
}

func (err *TransactionWaitTimeoutError) Error() string {
	if err == nil {
		return ErrTransactionWaitTimeout.Error()
	}
	return fmt.Sprintf("Timed out waiting for transaction. %s", err.TransactionID)
}

func (*TransactionWaitTimeoutError) Unwrap() error { return ErrTransactionWaitTimeout }

type transactionWaitOptions struct {
	roundDuration time.Duration
	now           func() time.Duration
}

func defaultTransactionWaitOptions() transactionWaitOptions {
	return transactionWaitOptions{
		roundDuration: transactionWaitRoundDuration,
		now:           func() time.Duration { return time.Since(transactionWaitClockOrigin) },
	}
}

// WaitTransaction waits until every affected owned address reports the
// transaction in one event round, or any affected history contains it.
// timeoutSeconds follows the Python API: zero means 600 seconds, while a
// negative value times out before starting a round.
func (ledger *Ledger) WaitTransaction(
	ctx context.Context, transaction *Transaction, height int64, timeoutSeconds float64,
) error {
	return ledger.waitTransaction(
		ctx, transaction, height, timeoutSeconds, defaultTransactionWaitOptions(),
	)
}

func (ledger *Ledger) waitTransaction(
	ctx context.Context,
	transaction *Transaction,
	height int64,
	timeoutSeconds float64,
	options transactionWaitOptions,
) error {
	if ledger == nil || ledger.Database == nil {
		return fmt.Errorf("%w: wallet ledger database is unavailable", ErrTransactionWaitUnavailable)
	}
	if transaction == nil {
		return fmt.Errorf("%w: transaction is nil", ErrTransactionWaitUnavailable)
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrTransactionWaitUnavailable)
	}
	if transaction.ID == "" {
		if err := transaction.RebuildDerived(); err != nil {
			return err
		}
	}
	addresses, err := ledger.transactionWaitAddresses(transaction)
	if err != nil {
		return err
	}
	if timeoutSeconds == 0 {
		timeoutSeconds = transactionWaitBlockingSeconds
	}
	if options.now == nil {
		options.now = defaultTransactionWaitOptions().now
	}
	if options.roundDuration <= 0 {
		options.roundDuration = transactionWaitRoundDuration
	}
	start := int64(options.now() / time.Second)
	for timeoutSeconds != 0 &&
		float64(int64(options.now()/time.Second)-start) <= timeoutSeconds {
		matched, err := ledger.waitTransactionRound(
			ctx, transaction.ID, height, addresses, options.roundDuration,
		)
		if err != nil {
			return err
		}
		if matched {
			return nil
		}
	}
	return &TransactionWaitTimeoutError{TransactionID: transaction.ID}
}

func (ledger *Ledger) transactionWaitAddresses(transaction *Transaction) ([]string, error) {
	addresses := make([]string, 0, len(transaction.Inputs)+len(transaction.Outputs))
	seen := make(map[string]struct{}, cap(addresses))
	appendAddress := func(address string) {
		if _, exists := seen[address]; exists {
			return
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	for index := range transaction.Inputs {
		resolved := transaction.Inputs[index].ResolvedOutput
		if resolved == nil {
			continue
		}
		output := currentTransactionOutput(resolved)
		if output.Script.Err != nil {
			return nil, output.Script.Err
		}
		if !output.Script.HasPubKeyHash {
			return nil, fmt.Errorf("%w: input %d", ErrTransactionWaitInputScript, index)
		}
		address, err := output.Address(ledger.Network)
		if err != nil {
			return nil, err
		}
		appendAddress(address)
	}
	for index := range transaction.Outputs {
		output := currentTransactionOutput(&transaction.Outputs[index])
		if !output.Script.HasPubKeyHash && !output.Script.HasScriptHash {
			continue
		}
		address, err := output.Address(ledger.Network)
		if err != nil {
			return nil, err
		}
		appendAddress(address)
	}
	return addresses, nil
}

func (ledger *Ledger) waitTransactionRound(
	ctx context.Context,
	transactionID string,
	height int64,
	addresses []string,
	roundDuration time.Duration,
) (bool, error) {
	records, err := ledger.Database.GetAddresses(ctx, ledgerdb.AddressQuery{Addresses: addresses})
	if err != nil {
		return false, err
	}
	if len(records) == 0 {
		return false, ErrTransactionWaitNoRecords
	}

	pending := make(map[string]struct{}, len(records))
	for _, record := range records {
		pending[record.Address] = struct{}{}
	}
	var pendingMu sync.Mutex
	allObserved := make(chan struct{}, 1)
	cancel := ledger.SubscribeTransactions(func(event TransactionEvent) error {
		if event.Transaction == nil || event.Transaction.ID != transactionID ||
			event.Transaction.Height < height {
			return nil
		}
		pendingMu.Lock()
		if _, exists := pending[event.Address]; exists {
			delete(pending, event.Address)
			if len(pending) == 0 {
				select {
				case allObserved <- struct{}{}:
				default:
				}
			}
		}
		pendingMu.Unlock()
		return nil
	})
	timer := time.NewTimer(roundDuration)
	select {
	case <-ctx.Done():
		cancel()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return false, ctx.Err()
	case <-allObserved:
		cancel()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return true, nil
	case <-timer.C:
		cancel()
	}

	pendingMu.Lock()
	eventsComplete := len(pending) == 0
	pendingMu.Unlock()
	if eventsComplete {
		return true, nil
	}
	records, err = ledger.Database.GetAddresses(ctx, ledgerdb.AddressQuery{Addresses: addresses})
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.History == nil || *record.History == "" {
			continue
		}
		_, history, err := LocalAddressStatusAndHistory(*record.History)
		if err != nil {
			return false, err
		}
		for _, entry := range history {
			if entry.TxHash == transactionID {
				if entry.Height >= height || (entry.Height == 0 && height > entry.Height) {
					return true, nil
				}
				return false, nil
			}
		}
	}
	return false, nil
}
