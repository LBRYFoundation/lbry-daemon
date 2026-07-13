package main

import (
	"path/filepath"
	"sync"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestDaemonAppWalletManagerForRPCLifecycle(t *testing.T) {
	app := newDaemonApp(nil)
	provider := app.walletManagerForRPC

	if manager := provider(); manager != nil {
		t.Fatalf("manager before wallet startup = %p, want nil", manager)
	}

	started := walletpkg.NewWalletManager()
	app.walletMu.Lock()
	app.walletManager = started
	app.walletMu.Unlock()
	if manager := provider(); manager != started {
		t.Fatalf("manager after wallet startup = %p, want %p", manager, started)
	}

	app.walletMu.Lock()
	app.walletShuttingDown = true
	app.walletMu.Unlock()
	if manager := provider(); manager != nil {
		t.Fatalf("manager during wallet shutdown = %p, want nil", manager)
	}
}

func TestDaemonAppWalletManagerForRPCConcurrentSnapshots(t *testing.T) {
	app := newDaemonApp(nil)
	managers := []*walletpkg.WalletManager{
		walletpkg.NewWalletManager(),
		walletpkg.NewWalletManager(),
	}

	const (
		iterations = 10_000
		readers    = 8
	)
	start := make(chan struct{})
	failures := make(chan *walletpkg.WalletManager, 1)
	var workers sync.WaitGroup

	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for index := range iterations {
			app.walletMu.Lock()
			app.walletManager = managers[index%len(managers)]
			app.walletShuttingDown = index%3 == 0
			app.walletMu.Unlock()
		}
	}()

	workers.Add(readers)
	for range readers {
		go func() {
			defer workers.Done()
			<-start
			for range iterations {
				manager := app.walletManagerForRPC()
				if manager != nil && manager != managers[0] && manager != managers[1] {
					select {
					case failures <- manager:
					default:
					}
					return
				}
			}
		}()
	}

	close(start)
	workers.Wait()
	select {
	case manager := <-failures:
		t.Fatalf("provider returned an unknown manager snapshot: %p", manager)
	default:
	}

	app.walletMu.Lock()
	app.walletShuttingDown = true
	app.walletMu.Unlock()
	if manager := app.walletManagerForRPC(); manager != nil {
		t.Fatalf("final manager snapshot = %p, want nil", manager)
	}
}

func TestNewProductionAppWalletRPCUsesRequestTimeManager(t *testing.T) {
	root := t.TempDir()
	arguments := map[string]any{
		"api":                "127.0.0.1:0",
		"components_to_skip": []string{"dht", "hash_announcer", "peer_protocol_server"},
		"config":             filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":           filepath.Join(root, "data"),
		"download_dir":       filepath.Join(root, "downloads"),
		"network_interface":  "127.0.0.1",
		"streaming_server":   "127.0.0.1:0",
		"wallet_dir":         filepath.Join(root, "wallet"),
	}
	app, err := newProductionApp(arguments, "")
	if err != nil {
		t.Fatal(err)
	}

	var rpcService *managedService
	for index := range app.services {
		if app.services[index].name == "rpc" {
			rpcService = &app.services[index]
			break
		}
	}
	if rpcService == nil {
		_ = app.Shutdown()
		t.Fatal("production app has no RPC service")
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- rpcService.serve(rpcService.listener) }()
	t.Cleanup(func() {
		if err := app.Shutdown(); err != nil {
			t.Errorf("shutdown production app: %v", err)
		}
		if err := <-serveResult; !expectedServiceClose(err) {
			t.Errorf("RPC serve result = %v", err)
		}
	})

	endpoint := "http://" + rpcService.listener.Addr().String() + "/"
	assertTransactionListRPCErrorCode(t, endpoint, -32500)

	app.walletMu.Lock()
	app.walletManager = walletpkg.NewWalletManager()
	app.walletMu.Unlock()
	assertTransactionListRPCErrorCode(t, endpoint, -32500)

	app.walletMu.Lock()
	app.walletShuttingDown = true
	app.walletMu.Unlock()
	assertTransactionListRPCErrorCode(t, endpoint, -32500)
}

func assertTransactionListRPCErrorCode(t *testing.T, endpoint string, want float64) {
	t.Helper()
	payload := postRPC(t, endpoint, `{"method":"transaction_list"}`)
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("transaction_list response = %#v, want error", payload)
	}
	if code := errorPayload["code"]; code != want {
		t.Fatalf("transaction_list error code = %#v, want %.0f", code, want)
	}
}
