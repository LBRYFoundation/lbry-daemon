package wallet

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type managerResetNetwork struct {
	ledger   *Ledger
	events   *[]string
	stopErr  error
	startErr error
}

func (network *managerResetNetwork) Start(context.Context) error {
	*network.events = append(*network.events, "start")
	return network.startErr
}

func (network *managerResetNetwork) Stop(context.Context) error {
	*network.events = append(*network.events, "stop")
	if network.ledger.Config["auto_connect"] != true {
		return errors.New("config was not replaced before stop")
	}
	return network.stopErr
}

func (*managerResetNetwork) RemoteHeight() int { return 0 }

func (*managerResetNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, nil
}

func TestWalletManagerResetPromotesLiveExplicitServersAndRestartsInOrder(t *testing.T) {
	manager, ledger := managerResetFixture()
	var events []string
	ledger.SPVNetwork = &managerResetNetwork{ledger: ledger, events: &events}
	manager.lbryNetConfig = func() (LBRYNetConfig, error) {
		return LBRYNetConfig{
			WalletDir: "/live", HubTimeout: 17.5,
			DefaultServers:        []any{"explicit:50001"},
			DefaultServerDefaults: []any{"default:50001"},
			LBryumServersSet:      true,
			KnownHubs:             "known", Jurisdiction: "US",
			ConcurrentHubRequests: 9,
		}, nil
	}
	manager.reconfigureSPV = func(_ context.Context, ledger *Ledger, _ LBRYNetConfig) error {
		events = append(events, "reconfigure")
		ledger.SPVNetwork = &managerResetNetwork{ledger: ledger, events: &events}
		return nil
	}

	if err := manager.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"stop", "reconfigure", "start"}) {
		t.Fatalf("reset events = %v", events)
	}
	want := LedgerConfig{
		"auto_connect": true, "explicit_servers": []any{"explicit:50001"},
		"default_servers": []any{"default:50001"}, "known_hubs": "known",
		"jurisdiction": "US", "hub_timeout": 17.5,
		"concurrent_hub_requests": 9, "data_path": "/live",
	}
	if !reflect.DeepEqual(ledger.Config, want) {
		t.Fatalf("reset ledger config = %#v, want %#v", ledger.Config, want)
	}
}

func TestWalletManagerResetUsesEmptyExplicitServersWhenSettingIsUnset(t *testing.T) {
	manager, ledger := managerResetFixture()
	var events []string
	ledger.SPVNetwork = &managerResetNetwork{ledger: ledger, events: &events}
	manager.lbryNetConfig = func() (LBRYNetConfig, error) {
		return LBRYNetConfig{
			DefaultServers:        []any{"effective-default:50001"},
			DefaultServerDefaults: []any{"sdk-default:50001"},
		}, nil
	}
	if err := manager.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Config["explicit_servers"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("explicit servers = %#v", got)
	}
	if got := ledger.Config["default_servers"]; !reflect.DeepEqual(got, []any{"sdk-default:50001"}) {
		t.Fatalf("default servers = %#v", got)
	}
}

func TestWalletManagerResetFailureOrder(t *testing.T) {
	loadErr, stopErr, reconfigureErr := errors.New("load"), errors.New("stop"), errors.New("reconfigure")
	for _, test := range []struct {
		name                             string
		loadErr, stopErr, reconfigureErr error
		wantEvents                       []string
	}{
		{name: "reload", loadErr: loadErr},
		{name: "stop", stopErr: stopErr, wantEvents: []string{"stop"}},
		{name: "reconfigure", reconfigureErr: reconfigureErr, wantEvents: []string{"stop", "reconfigure"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, ledger := managerResetFixture()
			var events []string
			ledger.SPVNetwork = &managerResetNetwork{
				ledger: ledger, events: &events, stopErr: test.stopErr,
			}
			manager.lbryNetConfig = func() (LBRYNetConfig, error) {
				return LBRYNetConfig{}, test.loadErr
			}
			manager.reconfigureSPV = func(context.Context, *Ledger, LBRYNetConfig) error {
				events = append(events, "reconfigure")
				return test.reconfigureErr
			}
			err := manager.Reset(context.Background())
			wantErr := test.loadErr
			if wantErr == nil {
				wantErr = test.stopErr
			}
			if wantErr == nil {
				wantErr = test.reconfigureErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("reset error = %v, want %v", err, wantErr)
			}
			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("events = %v, want %v", events, test.wantEvents)
			}
		})
	}
}

func managerResetFixture() (*WalletManager, *Ledger) {
	ledger := &Ledger{Config: LedgerConfig{"old": true}}
	account := &Account{ledger: ledger}
	wallet := &Wallet{Accounts: []*Account{account}}
	return &WalletManager{Wallets: []*Wallet{wallet}}, ledger
}
