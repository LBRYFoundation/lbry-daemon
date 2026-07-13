package wallet

import (
	"sync"
	"sync/atomic"
)

// TransactionEvent is emitted after a fetched transaction batch has been
// committed for an address and before that address's final history is stored.
type TransactionEvent struct {
	Address     string
	Transaction *Transaction
}

// TransactionHandler receives transaction events synchronously. Returning an
// error stops the current batch before any later handlers or events run.
type TransactionHandler func(TransactionEvent) error

type transactionEventListener struct {
	handler TransactionHandler
	active  atomic.Bool
}

type transactionEventListeners struct {
	mu        sync.Mutex
	emission  sync.Mutex
	listeners []*transactionEventListener
}

// SubscribeTransactions registers a handler in call order and returns an
// idempotent cancellation function. Cancellation does not interrupt a handler
// already running, but the handler will not receive later events.
func (ledger *Ledger) SubscribeTransactions(handler TransactionHandler) func() {
	if ledger == nil || handler == nil {
		return func() {}
	}
	listener := &transactionEventListener{handler: handler}
	listener.active.Store(true)
	events := &ledger.transactionEvents
	events.mu.Lock()
	events.listeners = append(events.listeners, listener)
	events.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			listener.active.Store(false)
			events.mu.Lock()
			for index, registered := range events.listeners {
				if registered == listener {
					copy(events.listeners[index:], events.listeners[index+1:])
					events.listeners[len(events.listeners)-1] = nil
					events.listeners = events.listeners[:len(events.listeners)-1]
					break
				}
			}
			events.mu.Unlock()
		})
	}
}

func (ledger *Ledger) publishTransactionBatch(
	address string, transactions []*Transaction,
) error {
	if ledger == nil || len(transactions) == 0 {
		return nil
	}
	events := &ledger.transactionEvents
	// Python drains each ordered batch with dict.popitem(). Keep a complete
	// batch contiguous even when separate addresses synchronize concurrently.
	events.emission.Lock()
	defer events.emission.Unlock()
	for index := len(transactions) - 1; index >= 0; index-- {
		event := TransactionEvent{Address: address, Transaction: transactions[index]}
		for _, listener := range events.snapshot() {
			if !listener.active.Load() {
				continue
			}
			if err := listener.handler(event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (events *transactionEventListeners) snapshot() []*transactionEventListener {
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]*transactionEventListener(nil), events.listeners...)
}
