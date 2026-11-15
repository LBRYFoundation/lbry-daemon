package wallet

import (
	"errors"
	"sync"
	"testing"
)

// A second extended public key on the curve (the key is 2G, the chain code the bytes 0..31).
const publicKeyTwoReal = "xpub661MyMwAqRbcEYSGagKuFUqExQV8d2eizDP5SamP9TcLeqAk9JsrNexcG7vovorzyLCiPg9afre6trjvEKGpwr9v4S2SwD4SsLW7HWiE5yG"

// fakeHub answers the scripthash calls from a table: the wallet-server view of some addresses.
type fakeHub struct {
	mutex     sync.Mutex
	history   map[string]bool       // script hash -> has history
	balances  map[string][2]float64 // script hash -> confirmed, unconfirmed
	failOn    string                // fail calls of this method
	calls     map[string]int
	badFormat bool
}

func (hub *fakeHub) Call(method string, params any) (any, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	if hub.calls == nil {
		hub.calls = map[string]int{}
	}
	hub.calls[method]++
	if method == hub.failOn {
		return nil, errors.New("hub down")
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
	case "blockchain.scripthash.get_balance":
		amounts := hub.balances[scriptHash]
		return map[string]any{"confirmed": amounts[0], "unconfirmed": amounts[1]}, nil
	}
	return nil, errors.New("unexpected method " + method)
}

// use marks the receiving (chain 0) or change (chain 1) address at index as having history and a balance.
func (hub *fakeHub) use(t *testing.T, xpub string, chain uint32, index uint32, confirmed, unconfirmed float64) {
	t.Helper()
	keys, err := DeriveKeys(xpub, chain, index, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hub.history == nil {
		hub.history, hub.balances = map[string]bool{}, map[string][2]float64{}
	}
	hub.history[keys[0].ScriptHash()] = true
	hub.balances[keys[0].ScriptHash()] = [2]float64{confirmed, unconfirmed}
}

// Computed with an independent implementation: the first receiving address of publicKeyG.
func TestScriptHash(t *testing.T) {
	keys, err := DeriveKeys(publicKeyG, ReceivingChain, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := keys[0].ScriptHash(), "a17cec7ec1c7c9622f0480dcd333bcb072bc62e11bb7475eb6b87d3558463814"; got != want {
		t.Errorf("script hash %s, want %s", got, want)
	}
	if got, want := keys[0].Address(), "bPG2gs7buobagNmQi2kmMNy3D7TjrwqbAG"; got != want {
		t.Errorf("address %s, want %s", got, want)
	}
}

func TestWalletBalanceAddsUpBothChains(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 0, 100_000_000, 0)
	hub.use(t, publicKeyG, ReceivingChain, 3, 250_000_000, 50_000_000)
	hub.use(t, publicKeyG, ChangeChain, 1, 5, 0)
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

func TestWalletBalanceSumsAccountsAndCountsNothingForAnEmptyWallet(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 0, 10, 0)
	hub.use(t, publicKeyTwoReal, ReceivingChain, 0, 20, 0)
	got, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}, {PublicKey: publicKeyTwoReal}}, 0)
	if err != nil || got.Total != 30 {
		t.Errorf("two accounts: %+v %v", got, err)
	}
	if got, err := WalletBalance(&fakeHub{}, nil, 0); err != nil || got.Total != 0 {
		t.Errorf("no accounts: %+v %v", got, err)
	}
	if got, err := WalletBalance(&fakeHub{}, []*Account{{PublicKey: publicKeyG}}, 0); err != nil || got.Total != 0 {
		t.Errorf("unused account: %+v %v", got, err)
	}
}

// Addresses past the gap are found when the ones before them are used: the scan goes on until a whole gap
// has no history.
func TestWalletBalanceFollowsUsedAddressesPastTheGap(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 19, 1, 0) // last address of the first gap window of 20
	hub.use(t, publicKeyG, ReceivingChain, 35, 2, 0) // reachable only because 19 was used
	hub.use(t, publicKeyG, ReceivingChain, 80, 4, 0) // beyond a full empty gap: not found, as in lbry-sdk
	got, err := WalletBalance(hub, []*Account{{PublicKey: publicKeyG}}, 0)
	if err != nil || got.Total != 3 {
		t.Errorf("got %+v %v, want 3", got, err)
	}
}

func TestWalletBalanceUsesTheAccountsGap(t *testing.T) {
	hub := &fakeHub{}
	hub.use(t, publicKeyG, ReceivingChain, 2, 3, 0)    // inside the first window of 3
	hub.use(t, publicKeyG, ReceivingChain, 4, 4, 0)    // found because 2 was used
	hub.use(t, publicKeyG, ReceivingChain, 12, 100, 0) // past an empty gap of 3: not found
	small := &Account{PublicKey: publicKeyG, AddressGenerator: AddressGenerator{Name: ADDRESS_GENERATOR_NAME_DETERMINISTIC_CHAIN, Receiving: AddressGeneratorChain{Gap: 3, MaximumUsesPerAddress: 1}, Change: AddressGeneratorChain{Gap: 3, MaximumUsesPerAddress: 1}}}
	got, err := WalletBalance(hub, []*Account{small}, 0)
	if err != nil || got.Total != 7 {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestWalletBalanceSingleAddress(t *testing.T) {
	hub := &fakeHub{}
	key, _ := ParseExtendedPublicKey(publicKeyG)
	hub.history = map[string]bool{key.ScriptHash(): true}
	hub.balances = map[string][2]float64{key.ScriptHash(): {42, 0}}
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
	hub := &fakeHub{failOn: "blockchain.scripthash.get_balance"}
	hub.use(t, publicKeyG, ReceivingChain, 0, 1, 0)
	if got, err := WalletBalance(hub, accounts, 0); err == nil {
		t.Errorf("a failing balance call must fail the request, got %+v", got)
	}
	if _, err := WalletBalance(&fakeHub{}, []*Account{{PublicKey: "not a key"}}, 0); err == nil {
		t.Error("an unreadable public key must be an error")
	}
}

func TestWalletBalanceConfirmationsAboveOneAreRefused(t *testing.T) {
	for _, confirmations := range []int{-1, 2, 6} {
		if _, err := WalletBalance(&fakeHub{}, nil, confirmations); !errors.Is(err, ErrUnsupportedConfirmations) {
			t.Errorf("confirmations %d: %v", confirmations, err)
		}
	}
}

func TestFormatLBC(t *testing.T) {
	for dewies, want := range map[int64]string{
		0: "0.0", 1: "0.00000001", 100_000_000: "1.0", 150_000_000: "1.5", 123_456_789: "1.23456789",
		10_063_99_000_000: "10063.99", -250_000_000: "-2.5",
	} {
		if got := FormatLBC(dewies); got != want {
			t.Errorf("FormatLBC(%d) = %s, want %s", dewies, got, want)
		}
	}
}
