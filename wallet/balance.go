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

// Balance is an amount in dewies (1 LBC = 100,000,000 dewies).
//
// Reserved (value locked in claims, supports and tips) is not worked out yet, so it is always zero and
// Available equals Total. That overstates what can be spent by the amount held in claims.
type Balance struct {
	Total     int64
	Available int64
	Reserved  int64
}

const (
	// maxAddressesPerChain bounds the gap scan, so a hub that reports every address as used cannot make
	// one request run forever.
	maxAddressesPerChain = 2000
	// hubParallelism limits concurrent hub calls from one balance request.
	hubParallelism = 8
)

// WalletBalance adds up the balance of the accounts' addresses with the hub. confirmations is 0 to include
// unconfirmed transactions or 1 for confirmed ones only (lbry-sdk's wallet_balance default is 0). It only
// needs public keys, so it works on a locked wallet. A hub failure is an error, never a zero balance.
func WalletBalance(hub Hub, accounts []*Account, confirmations int) (Balance, error) {
	if confirmations < 0 || confirmations > 1 {
		return Balance{}, ErrUnsupportedConfirmations
	}
	var total int64
	for i := range accounts {
		for _, chain := range []uint32{ReceivingChain, ChangeChain} {
			amount, err := scanChain(hub, accounts[i], chain, confirmations)
			if err != nil {
				return Balance{}, err
			}
			total += amount
		}
	}
	return Balance{Total: total, Available: total}, nil
}

// scanChain sums a chain's balance, extending the scan until `gap` addresses in a row have no history.
// An account with the single-address generator has just its own key.
func scanChain(hub Hub, account *Account, chain uint32, confirmations int) (int64, error) {
	if account.UsesSingleAddress() {
		key, err := ParseExtendedPublicKey(account.PublicKey)
		if err != nil {
			return 0, err
		}
		if chain == ChangeChain {
			return 0, nil // the one address is counted with the receiving chain
		}
		results, err := queryKeys(hub, []*ExtendedPublicKey{key}, confirmations)
		if err != nil {
			return 0, err
		}
		return results[0].amount, nil
	}

	gap := account.Gap(chain)
	if gap < 1 {
		return 0, nil
	}
	var sum int64
	lastUsed := -1
	for start := 0; start < maxAddressesPerChain; start += gap {
		keys, err := DeriveKeys(account.PublicKey, chain, uint32(start), gap)
		if err != nil {
			return 0, err
		}
		results, err := queryKeys(hub, keys, confirmations)
		if err != nil {
			return 0, err
		}
		for i, result := range results {
			sum += result.amount
			if result.used {
				lastUsed = start + i
			}
		}
		if start+gap-1-lastUsed >= gap {
			break
		}
	}
	return sum, nil
}

type addressResult struct {
	amount int64
	used   bool
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
		return result, nil // no history, so no balance
	}

	balance, err := hub.Call("blockchain.scripthash.get_balance", []any{scriptHash})
	if err != nil {
		return addressResult{}, err
	}
	fields, isMap := balance.(map[string]any)
	confirmed, confirmedOK := fields["confirmed"].(float64)
	unconfirmed, unconfirmedOK := fields["unconfirmed"].(float64)
	if !isMap || !confirmedOK || !unconfirmedOK {
		return addressResult{}, fmt.Errorf("the wallet server sent an unexpected balance for %s", key.Address())
	}
	result.amount = int64(confirmed)
	if confirmations == 0 {
		result.amount += int64(unconfirmed)
	}
	return result, nil
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
