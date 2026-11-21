package wallet

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Hub is a wallet-server connection: one JSON-RPC call, returning the decoded "result".
type Hub interface {
	Call(method string, params any) (any, error)
}

// ErrUnsupportedConfirmations is returned for a confirmations value the hub cannot answer.
var ErrUnsupportedConfirmations = errors.New("confirmations above 1 are not supported yet")

// Balance is an amount in dewies (1 LBC = 100,000,000 dewies), split like lbry-sdk's wallet_balance:
// Total is every unspent output of the wallet, Reserved the part locked in claims and supports, and
// Available the rest.
type Balance struct {
	Total     int64
	Available int64
	Reserved  int64
	Claims    int64 // unspent claim outputs: channels, streams, reposts, collections
	Supports  int64 // supports sent from this wallet
	Tips      int64 // supports at this wallet's addresses sent from somewhere else
}

const (
	// maxAddressesPerChain bounds the gap scan, so a hub that reports every address as used cannot make
	// one request run forever.
	maxAddressesPerChain = 2000
	// hubParallelism limits concurrent hub calls from one balance request.
	hubParallelism = 8
	// transactionBatch is how many transactions are asked of the hub in one call.
	transactionBatch = 50
)

// WalletBalance works out the balance of the accounts' addresses with the hub. confirmations is 0 to include
// unconfirmed transactions or 1 for confirmed ones only (lbry-sdk's wallet_balance default is 0). It only
// needs public keys, so it works on a locked wallet. A hub failure is an error, never a zero balance.
func WalletBalance(hub Hub, accounts []*Account, confirmations int) (Balance, error) {
	if confirmations < 0 || confirmations > 1 {
		return Balance{}, ErrUnsupportedConfirmations
	}
	var unspent []unspentOutput
	owned := map[string]bool{} // public key hashes of the wallet's addresses
	for i := range accounts {
		for _, chain := range []uint32{ReceivingChain, ChangeChain} {
			found, hashes, err := scanChain(hub, accounts[i], chain, confirmations)
			if err != nil {
				return Balance{}, err
			}
			unspent = append(unspent, found...)
			for _, hash := range hashes {
				owned[string(hash)] = true
			}
		}
	}
	return classify(hub, unspent, owned)
}

type unspentOutput struct {
	outPoint
	value int64
}

// scanChain lists a chain's unspent outputs, extending the scan until `gap` addresses in a row have no
// history. An account with the single-address generator has just its own key.
func scanChain(hub Hub, account *Account, chain uint32, confirmations int) ([]unspentOutput, [][]byte, error) {
	if account.UsesSingleAddress() {
		if chain == ChangeChain {
			return nil, nil, nil // the one address is counted with the receiving chain
		}
		key, err := ParseExtendedPublicKey(account.PublicKey)
		if err != nil {
			return nil, nil, err
		}
		results, err := queryKeys(hub, []*ExtendedPublicKey{key}, confirmations)
		if err != nil {
			return nil, nil, err
		}
		return results[0].unspent, [][]byte{key.Hash160()}, nil
	}

	gap := account.Gap(chain)
	if gap < 1 {
		return nil, nil, nil
	}
	var unspent []unspentOutput
	var hashes [][]byte
	lastUsed := -1
	for start := 0; start < maxAddressesPerChain; start += gap {
		keys, err := DeriveKeys(account.PublicKey, chain, uint32(start), gap)
		if err != nil {
			return nil, nil, err
		}
		results, err := queryKeys(hub, keys, confirmations)
		if err != nil {
			return nil, nil, err
		}
		for i, result := range results {
			unspent = append(unspent, result.unspent...)
			hashes = append(hashes, keys[i].Hash160())
			if result.used {
				lastUsed = start + i
			}
		}
		if start+gap-1-lastUsed >= gap {
			break
		}
	}
	return unspent, hashes, nil
}

type addressResult struct {
	unspent []unspentOutput
	used    bool
}

func queryKeys(hub Hub, keys []*ExtendedPublicKey, confirmations int) ([]addressResult, error) {
	results := make([]addressResult, len(keys))
	errs := make([]error, len(keys))
	var wg sync.WaitGroup
	slots := make(chan struct{}, hubParallelism)
	for i := range keys {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			results[i], errs[i] = queryKey(hub, keys[i], confirmations)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func queryKey(hub Hub, key *ExtendedPublicKey, confirmations int) (addressResult, error) {
	scriptHash := key.ScriptHash()
	history, err := hub.Call("blockchain.scripthash.get_history", []any{scriptHash})
	if err != nil {
		return addressResult{}, err
	}
	items, isList := history.([]any)
	if !isList {
		return addressResult{}, fmt.Errorf("the wallet server sent an unexpected history for %s", key.Address())
	}
	result := addressResult{used: len(items) > 0}
	if !result.used {
		return result, nil // no history, so nothing unspent
	}

	listed, err := hub.Call("blockchain.scripthash.listunspent", []any{scriptHash})
	if err != nil {
		return addressResult{}, err
	}
	entries, isList := listed.([]any)
	if !isList {
		return addressResult{}, fmt.Errorf("the wallet server sent an unexpected unspent list for %s", key.Address())
	}
	for _, entry := range entries {
		fields, isMap := entry.(map[string]any)
		txid, txidOK := fields["tx_hash"].(string)
		position, positionOK := fields["tx_pos"].(float64)
		height, heightOK := fields["height"].(float64)
		value, valueOK := fields["value"].(float64)
		if !isMap || !txidOK || !positionOK || !heightOK || !valueOK {
			return addressResult{}, fmt.Errorf("the wallet server sent an unexpected unspent output for %s", key.Address())
		}
		if confirmations > 0 && height <= 0 {
			continue // not in a block yet
		}
		result.unspent = append(result.unspent, unspentOutput{outPoint{txid, uint32(position)}, int64(value)})
	}
	return result, nil
}

// classify splits the unspent outputs like lbry-sdk's get_detailed_balance: claims and supports are
// reserved, and a support is a tip unless one of the inputs of its transaction is the wallet's own.
func classify(hub Hub, unspent []unspentOutput, owned map[string]bool) (Balance, error) {
	var balance Balance
	if len(unspent) == 0 {
		return balance, nil
	}
	txids := []string{}
	seen := map[string]bool{}
	for _, output := range unspent {
		if !seen[output.txid] {
			seen[output.txid] = true
			txids = append(txids, output.txid)
		}
	}
	transactions, err := fetchTransactions(hub, txids)
	if err != nil {
		return Balance{}, err
	}

	type support struct {
		value int64
		tx    *transaction
	}
	var supports []support
	for _, output := range unspent {
		tx := transactions[output.txid]
		if int(output.index) >= len(tx.outputs) {
			return Balance{}, fmt.Errorf("the wallet server sent a transaction without the output %s:%d", output.txid, output.index)
		}
		balance.Total += output.value
		switch spent := tx.outputs[output.index]; {
		case spent.isClaim():
			balance.Claims += output.value
		case spent.isSupport():
			supports = append(supports, support{output.value, tx})
		}
	}

	// A support is the wallet's own when one of the outputs its transaction spends was paid to the wallet.
	var previous []string
	seenPrevious := map[string]bool{}
	for _, item := range supports {
		for _, input := range item.tx.inputs {
			if _, have := transactions[input.txid]; !have && !seenPrevious[input.txid] {
				seenPrevious[input.txid] = true
				previous = append(previous, input.txid)
			}
		}
	}
	if len(previous) > 0 {
		more, err := fetchTransactions(hub, previous)
		if err != nil {
			return Balance{}, err
		}
		for txid, tx := range more {
			transactions[txid] = tx
		}
	}
	for _, item := range supports {
		mine := false
		for _, input := range item.tx.inputs {
			spent := transactions[input.txid]
			if int(input.index) < len(spent.outputs) && owned[string(spent.outputs[input.index].hash160())] {
				mine = true
				break
			}
		}
		if mine {
			balance.Supports += item.value
		} else {
			balance.Tips += item.value
		}
	}
	balance.Reserved = balance.Claims + balance.Supports + balance.Tips
	balance.Available = balance.Total - balance.Reserved
	return balance, nil
}

// fetchTransactions gets and parses transactions from the hub, in batches.
func fetchTransactions(hub Hub, txids []string) (map[string]*transaction, error) {
	transactions := make(map[string]*transaction, len(txids))
	for start := 0; start < len(txids); start += transactionBatch {
		batch := txids[start:min(start+transactionBatch, len(txids))]
		params := make([]any, len(batch))
		for i, txid := range batch {
			params[i] = txid
		}
		result, err := hub.Call("blockchain.transaction.get_batch", params)
		if err != nil {
			return nil, err
		}
		reply, isMap := result.(map[string]any)
		if !isMap {
			return nil, errors.New("the wallet server sent an unexpected transaction batch")
		}
		for _, txid := range batch {
			entry, isList := reply[txid].([]any)
			if !isList || len(entry) == 0 {
				return nil, fmt.Errorf("the wallet server did not send the transaction %s", txid)
			}
			raw, isString := entry[0].(string)
			if !isString {
				return nil, fmt.Errorf("the wallet server sent an unexpected transaction %s", txid)
			}
			tx, err := parseTransaction(raw)
			if err != nil {
				return nil, fmt.Errorf("transaction %s: %w", txid, err)
			}
			transactions[txid] = tx
		}
	}
	return transactions, nil
}

// FormatLBC writes dewies as lbry-sdk does (satoshis_to_coins): at least one decimal, no trailing zeros.
func FormatLBC(dewies int64) string {
	sign := ""
	if dewies < 0 {
		sign, dewies = "-", -dewies
	}
	fraction := strings.TrimRight(fmt.Sprintf("%08d", dewies%100000000), "0")
	if fraction == "" {
		fraction = "0"
	}
	return sign + strconv.FormatInt(dewies/100000000, 10) + "." + fraction
}
