package wallet

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// HubRequest is one JSON-RPC call to the wallet server.
type HubRequest struct {
	Method string
	Params any
}

// HubResponse is the decoded "result" of a reply, or the error the hub answered it with.
type HubResponse struct {
	Result any
	Err    error
}

// Hub is a wallet-server connection. CallMany sends the requests together, on one connection, and returns
// the responses in the same order; the error is for the hub as a whole not answering.
type Hub interface {
	CallMany(requests []HubRequest) ([]HubResponse, error)
}

func callOne(hub Hub, method string, params any) (any, error) {
	responses, err := hub.CallMany([]HubRequest{{method, params}})
	if err != nil {
		return nil, err
	}
	return responses[0].Result, responses[0].Err
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
	// laterWindowGaps sizes the windows after the first, in gaps: the first window is one gap, which is all
	// an unused account needs, and once an address is used the scan reads further ahead in one round trip.
	// The stop rule does not depend on the window: it is a whole gap of addresses without history.
	laterWindowGaps = 3
	// transactionBatch is how many transactions are asked of the hub in one call.
	transactionBatch = 50
)

// WalletBalance works out the balance of the accounts' addresses with the hub. confirmations is 0 to include
// unconfirmed transactions or 1 for confirmed ones only (lbry-sdk's wallet_balance default is 0). It only
// needs public keys, so it works on a locked wallet. A hub failure is an error, never a zero balance.
func WalletBalance(hub Hub, accounts []*Account, confirmations int) (Balance, error) {
	results, err := scanAccounts(hub, accounts, confirmations)
	if err != nil {
		return Balance{}, err
	}
	var unspent []unspentOutput
	owned := map[string]bool{} // public key hashes of the wallet's addresses
	for _, result := range results {
		unspent = append(unspent, result.unspent...)
		for _, hash := range result.hashes {
			owned[string(hash)] = true
		}
	}
	return classify(hub, unspent, owned)
}

// scanned is what one chain of one account holds.
type scanned struct {
	unspent []unspentOutput
	hashes  [][]byte
	err     error
}

// scanAccounts scans every chain of every account at once (a scan is a few round trips in a row). The result
// has two entries per account, its receiving chain and then its change chain. confirmations is checked here.
func scanAccounts(hub Hub, accounts []*Account, confirmations int) ([]scanned, error) {
	if confirmations < 0 || confirmations > 1 {
		return nil, ErrUnsupportedConfirmations
	}
	results := make([]scanned, len(accounts)*2)
	var wg sync.WaitGroup
	for i := range accounts {
		for chain := uint32(0); chain < 2; chain++ {
			wg.Add(1)
			go func(slot int, account *Account, chain uint32) {
				defer wg.Done()
				results[slot].unspent, results[slot].hashes, results[slot].err = scanChain(hub, account, chain, confirmations)
			}(i*2+int(chain), accounts[i], chain)
		}
	}
	wg.Wait()
	for _, result := range results {
		if result.err != nil {
			return nil, result.err
		}
	}
	return results, nil
}

type unspentOutput struct {
	outPoint
	value int64
}

// scanChain lists a chain's unspent outputs, reading windows of addresses until `gap` addresses in a row have
// no history. An account with the single-address generator has just its own key.
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
	for start, window := 0, gap; start < maxAddressesPerChain; start, window = start+window, gap*laterWindowGaps {
		keys, err := DeriveKeys(account.PublicKey, chain, uint32(start), window)
		if err != nil {
			return nil, nil, err
		}
		results, err := queryKeys(hub, keys, confirmations)
		if err != nil {
			return nil, nil, err
		}
		for i, result := range results {
			index := start + i
			if index-lastUsed > gap {
				return unspent, hashes, nil // a whole gap after the last used address has no history
			}
			unspent = append(unspent, result.unspent...)
			hashes = append(hashes, keys[i].Hash160())
			if result.used {
				lastUsed = index
			}
		}
		if start+len(results)-1-lastUsed >= gap {
			break // the window ends with a whole gap without history, so there is nothing further to read
		}
	}
	return unspent, hashes, nil
}

type addressResult struct {
	unspent []unspentOutput
	used    bool
}

// queryKeys asks the hub about the keys' addresses with one batch: the history of each, to tell whether it is
// used, and its unspent outputs, which are only read for a used one. Asking for both at once costs one round
// trip for the whole window instead of two.
func queryKeys(hub Hub, keys []*ExtendedPublicKey, confirmations int) ([]addressResult, error) {
	requests := make([]HubRequest, 0, 2*len(keys))
	for _, key := range keys {
		requests = append(requests, HubRequest{"blockchain.scripthash.get_history", []any{key.ScriptHash()}})
	}
	for _, key := range keys {
		requests = append(requests, HubRequest{"blockchain.scripthash.listunspent", []any{key.ScriptHash()}})
	}
	responses, err := hub.CallMany(requests)
	if err != nil {
		return nil, err
	}
	if len(responses) != len(requests) {
		return nil, errors.New("the wallet server answered the wrong number of requests")
	}

	results := make([]addressResult, len(keys))
	for i, key := range keys {
		history, listed := responses[i], responses[len(keys)+i]
		if history.Err != nil {
			return nil, history.Err
		}
		items, isList := history.Result.([]any)
		if !isList {
			return nil, fmt.Errorf("the wallet server sent an unexpected history for %s", key.Address())
		}
		results[i].used = len(items) > 0
		if !results[i].used {
			continue // no history, so nothing unspent
		}
		if listed.Err != nil {
			return nil, listed.Err
		}
		entries, isList := listed.Result.([]any)
		if !isList {
			return nil, fmt.Errorf("the wallet server sent an unexpected unspent list for %s", key.Address())
		}
		for _, entry := range entries {
			fields, isMap := entry.(map[string]any)
			txid, txidOK := fields["tx_hash"].(string)
			position, positionOK := fields["tx_pos"].(float64)
			height, heightOK := fields["height"].(float64)
			value, valueOK := fields["value"].(float64)
			if !isMap || !txidOK || !positionOK || !heightOK || !valueOK {
				return nil, fmt.Errorf("the wallet server sent an unexpected unspent output for %s", key.Address())
			}
			if confirmations > 0 && height <= 0 {
				continue // not in a block yet
			}
			results[i].unspent = append(results[i].unspent, unspentOutput{outPoint{txid, uint32(position)}, int64(value)})
		}
	}
	return results, nil
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
		result, err := callOne(hub, "blockchain.transaction.get_batch", params)
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
