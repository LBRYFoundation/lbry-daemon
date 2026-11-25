package wallet

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// A second extended public key on the curve (the key is 2G, the chain code the bytes 0..31).
const publicKeyTwoReal = "xpub661MyMwAqRbcEYSGagKuFUqExQV8d2eizDP5SamP9TcLeqAk9JsrNexcG7vovorzyLCiPg9afre6trjvEKGpwr9v4S2SwD4SsLW7HWiE5yG"

func p2pkh(hash []byte) []byte {
	return append(append([]byte{0x76, 0xa9, 0x14}, hash...), 0x88, 0xac)
}

func claimScript(hash []byte) []byte {
	return append([]byte{opClaimName, 4, 'n', 'a', 'm', 'e', 2, 'c', 'd', 0x6d, 0x75}, p2pkh(hash)...)
}

func supportScript(hash []byte) []byte {
	return append(append([]byte{opSupportClaim, 4, 'n', 'a', 'm', 'e', 20}, append(make([]byte, 20), 0x6d, 0x75)...), p2pkh(hash)...)
}

// rawTransaction builds a raw transaction in hex.
func rawTransaction(inputs []outPoint, outputs []txOutput) string {
	var raw []byte
	raw = append(raw, 1, 0, 0, 0)
	raw = append(raw, byte(len(inputs)))
	for _, input := range inputs {
		hash, _ := hex.DecodeString(input.txid)
		for i := len(hash) - 1; i >= 0; i-- {
			raw = append(raw, hash[i])
		}
		raw = binary.LittleEndian.AppendUint32(raw, input.index)
		raw = append(raw, 0, 0xff, 0xff, 0xff, 0xff)
	}
	raw = append(raw, byte(len(outputs)))
	for _, output := range outputs {
		raw = binary.LittleEndian.AppendUint64(raw, uint64(output.amount))
		raw = append(raw, byte(len(output.script)))
		raw = append(raw, output.script...)
	}
	raw = append(raw, 0, 0, 0, 0)
	return hex.EncodeToString(raw)
}

// fakeHub is the wallet-server view of some addresses and transactions.
type fakeHub struct {
	mutex     sync.Mutex
	history   map[string]bool             // script hash -> has history
	unspent   map[string][]map[string]any // script hash -> listunspent entries
	txs       map[string]string           // txid -> raw transaction
	next      int
	failOn    string // the whole batch fails when it holds this method
	errorOn   string // this method is answered with an error, the rest of the batch with results
	badFormat bool
	calls     map[string]int
	batches   int
}

// CallMany answers the requests one by one. A failing method fails the whole batch, as a hub that is down.
func (hub *fakeHub) CallMany(requests []HubRequest) ([]HubResponse, error) {
	responses := make([]HubResponse, len(requests))
	for i, request := range requests {
		result, err := hub.call(request.Method, request.Params)
		if err != nil && request.Method == hub.failOn {
			return nil, err
		}
		responses[i] = HubResponse{result, err}
	}
	hub.mutex.Lock()
	hub.batches++
	hub.mutex.Unlock()
	return responses, nil
}

func (hub *fakeHub) call(method string, params any) (any, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	if hub.calls == nil {
		hub.calls = map[string]int{}
	}
	hub.calls[method]++
	if method == hub.failOn || method == hub.errorOn {
		return nil, errors.New("hub down")
	}
	if method == "blockchain.transaction.get_batch" {
		reply := map[string]any{}
		for _, txid := range params.([]any) {
			if raw, has := hub.txs[txid.(string)]; has {
				reply[txid.(string)] = []any{raw, map[string]any{}}
			}
		}
		return reply, nil
	}
	scriptHash := params.([]any)[0].(string)
	switch method {
	case "blockchain.scripthash.get_history":
		if hub.badFormat {
			return "nonsense", nil
		}
		if hub.history[scriptHash] {
			return []any{map[string]any{"tx_hash": "00", "height": 1.0}}, nil
		}
		return []any{}, nil
	case "blockchain.scripthash.listunspent":
		entries := []any{}
		for _, entry := range hub.unspent[scriptHash] {
			entries = append(entries, entry)
		}
		return entries, nil
	}
	return nil, errors.New("unexpected method " + method)
}

func (hub *fakeHub) newTxID() string {
	hub.next++
	return fmt.Sprintf("%064x", hub.next)
}

// fund gives the key an unspent output made by the script, in a transaction with the given inputs, at the
// height (0 is unconfirmed). It returns the outpoint.
func (hub *fakeHub) fund(key *ExtendedPublicKey, script []byte, value int64, height float64, inputs ...outPoint) outPoint {
	if hub.history == nil {
		hub.history, hub.unspent, hub.txs = map[string]bool{}, map[string][]map[string]any{}, map[string]string{}
	}
	txid := hub.newTxID()
	hub.txs[txid] = rawTransaction(inputs, []txOutput{{amount: value, script: script}})
	hub.history[key.ScriptHash()] = true
	hub.unspent[key.ScriptHash()] = append(hub.unspent[key.ScriptHash()], map[string]any{
		"tx_hash": txid, "tx_pos": 0.0, "height": height, "value": float64(value),
	})
	return outPoint{txid, 0}
}

func keyAt(t *testing.T, xpub string, chain uint32, index uint32) *ExtendedPublicKey {
	t.Helper()
	keys, err := DeriveKeys(xpub, chain, index, 1)
	if err != nil {
		t.Fatal(err)
	}
	return keys[0]
}

// use puts a plain payment of value on the address at index of a chain, confirmed.
func (hub *fakeHub) use(t *testing.T, xpub string, chain uint32, index uint32, value int64, height float64) {
	t.Helper()
	key := keyAt(t, xpub, chain, index)
	hub.fund(key, p2pkh(key.Hash160()), value, height)
}

// Computed with an independent implementation: the first receiving address of publicKeyG.
func TestScriptHash(t *testing.T) {
	key := keyAt(t, publicKeyG, ReceivingChain, 0)
	if got, want := key.ScriptHash(), "a17cec7ec1c7c9622f0480dcd333bcb072bc62e11bb7475eb6b87d3558463814"; got != want {
		t.Errorf("script hash %s, want %s", got, want)
	}
	if got, want := key.Address(), "bPG2gs7buobagNmQi2kmMNy3D7TjrwqbAG"; got != want {
		t.Errorf("address %s, want %s", got, want)
	}
}

func TestWalletBalanceAddsUpBothChains(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 0, 100_000_000, 5)
	hub.use(t, publicKeyG, ReceivingChain, 3, 250_000_000, 6)
	hub.use(t, publicKeyG, ReceivingChain, 3, 50_000_000, 0) // unconfirmed
	hub.use(t, publicKeyG, ChangeChain, 1, 5, 7)
	accounts := []*Account{{PublicKey: publicKeyG}}

	all, err := WalletBalance(hub, accounts, 0)
	if err != nil || all.Total != 100_000_000+250_000_000+50_000_000+5 || all.Available != all.Total || all.Reserved != 0 {
		t.Errorf("unconfirmed included: %+v %v", all, err)
	}
	confirmed, err := WalletBalance(hub, accounts, 1)
	if err != nil || confirmed.Total != 100_000_000+250_000_000+5 {
		t.Errorf("confirmed only: %+v %v", confirmed, err)
	}
}

func TestWalletBalanceReservesClaimsSupportsAndTips(t *testing.T) {
	hub := &fakeHub{}
	mine := keyAt(t, publicKeyG, ReceivingChain, 0)
	change := keyAt(t, publicKeyG, ChangeChain, 0)
	stranger := keyAt(t, publicKeyTwoReal, ReceivingChain, 0)

	hub.fund(keyAt(t, publicKeyG, ReceivingChain, 1), p2pkh(keyAt(t, publicKeyG, ReceivingChain, 1).Hash160()), 1_000, 5) // plain
	hub.fund(keyAt(t, publicKeyG, ReceivingChain, 2), claimScript(keyAt(t, publicKeyG, ReceivingChain, 2).Hash160()), 20_000, 5)
	// A support the wallet sent: its transaction spends an output paid to the wallet.
	funding := hub.fund(mine, p2pkh(mine.Hash160()), 7, 4)
	hub.fund(change, supportScript(change.Hash160()), 300_000, 5, funding)
	// A tip: the transaction spends an output of somebody else.
	elsewhere := hub.fund(stranger, p2pkh(stranger.Hash160()), 9, 4)
	tipKey := keyAt(t, publicKeyG, ReceivingChain, 4)
	hub.fund(tipKey, supportScript(tipKey.Hash160()), 4_000_000, 5, elsewhere)

	got, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := Balance{
		Total: 1_000 + 20_000 + 7 + 300_000 + 4_000_000, Reserved: 20_000 + 300_000 + 4_000_000,
		Claims: 20_000, Supports: 300_000, Tips: 4_000_000,
	}
	want.Available = want.Total - want.Reserved
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestWalletBalanceSumsAccountsAndCountsNothingForAnEmptyWallet(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 0, 10, 1)
	hub.use(t, publicKeyTwoReal, ReceivingChain, 0, 20, 1)
	got, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}, {PublicKey: publicKeyTwoReal}}, 0)
	if err != nil || got.Total != 30 {
		t.Errorf("two accounts: %+v %v", got, err)
	}
	if got, err := WalletBalance(&fakeHub{}, nil, 0); err != nil || got != (Balance{}) {
		t.Errorf("no accounts: %+v %v", got, err)
	}
	empty := &fakeHub{}
	if got, err := WalletBalance(empty, []*Account{{PublicKey: publicKeyG}}, 0); err != nil || got != (Balance{}) {
		t.Errorf("unused account: %+v %v", got, err)
	}
	if empty.calls["blockchain.transaction.get_batch"] != 0 {
		t.Error("no transactions are needed when nothing is unspent")
	}
}

// Addresses past the gap are found when the ones before them are used: the scan goes on until a whole gap
// has no history.
func TestWalletBalanceFollowsUsedAddressesPastTheGap(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 19, 1, 1) // last address of the first gap window of 20
	hub.use(t, publicKeyG, ReceivingChain, 35, 2, 1) // reachable only because 19 was used
	hub.use(t, publicKeyG, ReceivingChain, 80, 4, 1) // beyond a full empty gap: not found, as in lbry-sdk
	got, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}}, 0)
	if err != nil || got.Total != 3 {
		t.Errorf("got %+v %v, want 3", got, err)
	}
}

func TestWalletBalanceUsesTheAccountsGap(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 2, 3, 1)    // inside the first window of 3
	hub.use(t, publicKeyG, ReceivingChain, 4, 4, 1)    // found because 2 was used
	hub.use(t, publicKeyG, ReceivingChain, 12, 100, 1) // past an empty gap of 3: not found
	small := &Account{PublicKey: publicKeyG, AddressGenerator: AddressGenerator{Name: ADDRESS_GENERATOR_NAME_DETERMINISTIC_CHAIN, Receiving: AddressGeneratorChain{Gap: 3, MaximumUsesPerAddress: 1}, Change: AddressGeneratorChain{Gap: 3, MaximumUsesPerAddress: 1}}}
	got, err := WalletBalance(hub, []*Account{small}, 0)
	if err != nil || got.Total != 7 {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestWalletBalanceSingleAddress(t *testing.T) {
	hub := &fakeHub{}
	key, _ := ParseExtendedPublicKey(publicKeyG)
	hub.fund(key, p2pkh(key.Hash160()), 42, 1)
	account := &Account{PublicKey: publicKeyG, AddressGenerator: AddressGenerator{Name: ADDRESS_GENERATOR_NAME_SINGLE_ADDRESS}}
	if got, err := WalletBalance(hub, []*Account{account}, 0); err != nil || got.Total != 42 {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestWalletBalanceHubFailuresAreErrorsNotZero(t *testing.T) {
	accounts := []*Account{{PublicKey: publicKeyG}}
	for _, hub := range []*fakeHub{{failOn: "blockchain.scripthash.get_history"}, {badFormat: true}} {
		if got, err := WalletBalance(hub, accounts, 0); err == nil {
			t.Errorf("expected an error, got %+v", got)
		}
	}
	for _, hub := range []*fakeHub{{errorOn: "blockchain.scripthash.listunspent"}, {errorOn: "blockchain.transaction.get_batch"}} {
		hub.use(t, publicKeyG, ReceivingChain, 0, 1, 1)
		if got, err := WalletBalance(hub, accounts, 0); err == nil {
			t.Errorf("an error answer must fail the request, got %+v", got)
		}
	}
	for _, method := range []string{"blockchain.scripthash.listunspent", "blockchain.transaction.get_batch"} {
		hub := &fakeHub{failOn: method}
		hub.use(t, publicKeyG, ReceivingChain, 0, 1, 1)
		if got, err := WalletBalance(hub, accounts, 0); err == nil {
			t.Errorf("a failing %s must fail the request, got %+v", method, got)
		}
	}
	if _, err := WalletBalance(&fakeHub{}, []*Account{{PublicKey: "not a key"}}, 0); err == nil {
		t.Error("an unreadable public key must be an error")
	}
}

func TestWalletBalanceRejectsBadTransactionsFromTheHub(t *testing.T) {
	accounts := []*Account{{PublicKey: publicKeyG}}
	key := keyAt(t, publicKeyG, ReceivingChain, 0)

	missing := &fakeHub{}
	missing.fund(key, p2pkh(key.Hash160()), 1, 1)
	missing.txs = map[string]string{} // the hub does not know the transaction it listed
	if _, err := WalletBalance(missing, accounts, 0); err == nil {
		t.Error("a missing transaction must be an error")
	}

	garbage := &fakeHub{}
	txid := garbage.fund(key, p2pkh(key.Hash160()), 1, 1).txid
	garbage.txs[txid] = "00ff"
	if _, err := WalletBalance(garbage, accounts, 0); err == nil {
		t.Error("an unreadable transaction must be an error")
	}

	short := &fakeHub{}
	txid = short.fund(key, p2pkh(key.Hash160()), 1, 1).txid
	short.unspent[key.ScriptHash()][0]["tx_pos"] = 3.0 // an output the transaction does not have
	if _, err := WalletBalance(short, accounts, 0); err == nil {
		t.Error("an output the transaction does not have must be an error")
	}
}

func TestWalletBalanceConfirmationsAboveOneAreRefused(t *testing.T) {
	for _, confirmations := range []int{-1, 2, 6} {
		if _, err := WalletBalance(&fakeHub{}, nil, confirmations); !errors.Is(err, ErrUnsupportedConfirmations) {
			t.Errorf("confirmations %d: %v", confirmations, err)
		}
	}
}

func TestParseTransaction(t *testing.T) {
	hash := make([]byte, 20)
	prev := outPoint{txid: fmt.Sprintf("%064x", 0xabcdef), index: 2}
	tx, err := parseTransaction(rawTransaction([]outPoint{prev}, []txOutput{{amount: 5, script: p2pkh(hash)}, {amount: 6, script: claimScript(hash)}}))
	if err != nil || len(tx.inputs) != 1 || tx.inputs[0] != prev || len(tx.outputs) != 2 {
		t.Fatalf("unexpected %+v %v", tx, err)
	}
	if tx.outputs[0].amount != 5 || tx.outputs[0].isClaim() || tx.outputs[1].amount != 6 || !tx.outputs[1].isClaim() {
		t.Errorf("outputs: %+v", tx.outputs)
	}
	if string(tx.outputs[1].hash160()) != string(hash) || (txOutput{script: []byte{0x6a}}).hash160() != nil {
		t.Error("hash160 of claim and data scripts")
	}
	raw := rawTransaction([]outPoint{prev}, []txOutput{{amount: 5, script: p2pkh(hash)}})
	for _, bad := range []string{"", "zz", raw[:len(raw)-2], raw[:20]} {
		if _, err := parseTransaction(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestFormatLBC(t *testing.T) {
	for dewies, want := range map[int64]string{
		0: "0.0", 1: "0.00000001", 100_000_000: "1.0", 150_000_000: "1.5", 123_456_789: "1.23456789",
		1_006_399_000_000: "10063.99", -250_000_000: "-2.5",
	} {
		if got := FormatLBC(dewies); got != want {
			t.Errorf("FormatLBC(%d) = %s, want %s", dewies, got, want)
		}
	}
}

// A window of addresses is one batch, and the chains are scanned side by side, so a wallet costs a few round
// trips however many addresses it has.
func TestWalletBalanceBatchesEachWindow(t *testing.T) {
	hub := &fakeHub{}
	if _, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}}, 0); err != nil {
		t.Fatal(err)
	}
	// An unused account: one window of 20 receiving addresses and one of 6 change addresses.
	if hub.batches != 2 {
		t.Errorf("%d batches, want 2", hub.batches)
	}
	if hub.calls["blockchain.scripthash.get_history"] != 26 || hub.calls["blockchain.scripthash.listunspent"] != 26 {
		t.Errorf("calls: %v", hub.calls)
	}
}
