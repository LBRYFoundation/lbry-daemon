package wallet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	walletspv "lbry/daemon/wallet/spv"
)

func TestLiveSPVNetworkFillsVerifiedCheckpoint(t *testing.T) {
	chunk := checkpointFetchFixture(89)
	headers := NewHeaders(
		":memory:",
		WithHeaderValidation(false),
		withHeaderCheckpoints(checkpointTableFromHashes(t, string(HashHeader(chunk)))),
	)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	dialer := &liveCheckpointDialer{encoded: checkpointFetchEncoded(t, chunk, nil)}
	network, err := walletspv.NewNetwork(walletspv.NetworkConfig{
		Servers: []walletspv.Server{{Host: "checkpoint-hub", Port: 50001}},
		Client: walletspv.ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 250 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
		KeepaliveIdle:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForLedgerSPVFill(t, ledger)
	if snapshot := ledger.SPVSnapshot(); snapshot.FillErr != nil || !snapshot.InitialTipDone || snapshot.TipErr != nil {
		t.Fatalf("live checkpoint fill failed: %#v", snapshot)
	}
	if missing := headers.MissingCheckpointedChunks(); len(missing) != 0 {
		t.Fatalf("live checkpoint remained missing: %v", missing)
	}
	raw, err := headers.GetRaw(777)
	if err != nil || string(raw) != string(chunk[777*HeaderSize:778*HeaderSize]) {
		t.Fatalf("live fetched header = %x, %v", raw, err)
	}
	dialer.mu.Lock()
	starts := append([]int(nil), dialer.starts...)
	tipStarts := append([]int(nil), dialer.tipStarts...)
	dialer.mu.Unlock()
	if !reflect.DeepEqual(starts, []int{0}) || !reflect.DeepEqual(tipStarts, []int{1_000}) {
		t.Fatalf("live checkpoint/tip starts = %v / %v", starts, tipStarts)
	}
	next, err := SerializeHeader(BlockHeader{
		Version:       1,
		PreviousHash:  HashHeader(chunk[999*HeaderSize : 1_000*HeaderSize]),
		MerkleRoot:    bytes.Repeat([]byte{'1'}, 64),
		ClaimTrieRoot: bytes.Repeat([]byte{'2'}, 64),
		Timestamp:     1_700_000_000,
		Bits:          0x207fffff,
		Nonce:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer.notify(t, []any{map[string]any{"height": 1_000, "hex": fmt.Sprintf("%x", next)}})
	waitForSPVTip(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.TipHeight == 1_000 && snapshot.TipChange == 1
	})
	if stored, err := headers.GetRaw(1_000); err != nil || !bytes.Equal(stored, next) {
		t.Fatalf("live notification header = %x, %v", stored, err)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveSPVNetworkSynchronizesAddressInventoryAndStatusNotification(t *testing.T) {
	ledger, account := newAddressTestLedger(t, DeterministicChainGenerator)
	dialer := &liveCheckpointDialer{
		histories:    make(map[string][]any),
		transactions: make(map[string][]byte),
	}
	network, err := walletspv.NewNetwork(walletspv.NetworkConfig{
		Servers: []walletspv.Server{{Host: "address-hub", Port: 50001}},
		Client: walletspv.ClientConfig{
			Dialer: dialer, RequestTimeout: 250 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
		KeepaliveIdle:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForAddressSnapshot(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.AddressCycles == 1 && snapshot.HistoryUpdates == 26 && !snapshot.AddressSyncing
	})
	dialer.mu.Lock()
	batches := make([][]string, len(dialer.addressBatches))
	for index, batch := range dialer.addressBatches {
		batches[index] = append([]string(nil), batch...)
	}
	dialer.mu.Unlock()
	if len(batches) != 2 || len(batches[0]) != 20 || len(batches[1]) != 6 {
		t.Fatalf("live address batches = %v", addressSubscriptionLengths(batches))
	}
	records, err := account.Receiving.GetAddressRecords(context.Background(), false)
	if err != nil || len(records) != 20 {
		t.Fatalf("live receiving inventory = %d, %v", len(records), err)
	}
	address := records[0].Address
	addressHash, err := ledger.addressHash160(address)
	if err != nil {
		t.Fatal(err)
	}
	transaction, raw := addressSyncTransaction(t, 7_000, addressHash, "", nil)
	history := transaction.ID + ":0:"
	status, _, err := LocalAddressStatusAndHistory(history)
	if err != nil || status == nil {
		t.Fatalf("live remote status = %#v, %v", status, err)
	}
	dialer.mu.Lock()
	dialer.histories[address] = []any{
		map[string]any{"tx_hash": transaction.ID, "height": 0},
	}
	dialer.transactions[transaction.ID] = raw
	dialer.mu.Unlock()
	dialer.notifyMethod(t, SPVAddressSubscribeMethod, []any{address, *status})
	waitForStoredAddressHistory(t, ledger, address, history)
	if snapshot := ledger.SPVSnapshot(); snapshot.OutOfSyncAddresses != 0 || snapshot.AddressErr != nil {
		t.Fatalf("live address transaction snapshot = %#v", snapshot)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type liveCheckpointDialer struct {
	mu             sync.Mutex
	writeMu        sync.Mutex
	encoded        string
	starts         []int
	tipStarts      []int
	addressBatches [][]string
	histories      map[string][]any
	transactions   map[string][]byte
	connection     net.Conn
}

func (dialer *liveCheckpointDialer) DialContext(
	ctx context.Context, _, _ string,
) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	client, server := net.Pipe()
	go dialer.serve(server)
	return client, nil
}

func (dialer *liveCheckpointDialer) serve(connection net.Conn) {
	dialer.mu.Lock()
	dialer.connection = connection
	dialer.mu.Unlock()
	defer connection.Close()
	defer func() {
		dialer.mu.Lock()
		if dialer.connection == connection {
			dialer.connection = nil
		}
		dialer.mu.Unlock()
	}()
	reader := bufio.NewReader(connection)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request struct {
			Method string          `json:"method"`
			ID     json.Number     `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&request); err != nil {
			return
		}
		id, err := request.ID.Int64()
		if err != nil {
			return
		}
		var result any
		switch request.Method {
		case "server.version":
			result = []any{"checkpoint hub", "0.65.0"}
		case "server.features":
			result = map[string]any{"server_version": "checkpoint-test"}
		case "server.peers.subscribe":
			result = []any{}
		case "blockchain.headers.subscribe":
			result = map[string]any{"height": 999}
		case "blockchain.block.headers":
			var params []any
			paramsDecoder := json.NewDecoder(bytes.NewReader(request.Params))
			paramsDecoder.UseNumber()
			if err := paramsDecoder.Decode(&params); err != nil || len(params) < 4 {
				return
			}
			start, err := params[0].(json.Number).Int64()
			if err != nil {
				return
			}
			b64, _ := params[3].(bool)
			dialer.mu.Lock()
			if b64 {
				dialer.starts = append(dialer.starts, int(start))
				result = map[string]any{"base64": dialer.encoded}
			} else {
				dialer.tipStarts = append(dialer.tipStarts, int(start))
				result = map[string]any{"hex": ""}
			}
			dialer.mu.Unlock()
		case "blockchain.address.subscribe":
			var addresses []string
			if err := json.Unmarshal(request.Params, &addresses); err != nil {
				return
			}
			dialer.mu.Lock()
			dialer.addressBatches = append(
				dialer.addressBatches, append([]string(nil), addresses...),
			)
			dialer.mu.Unlock()
			result = make([]any, len(addresses))
		case "blockchain.address.get_history":
			var addresses []string
			if err := json.Unmarshal(request.Params, &addresses); err != nil || len(addresses) != 1 {
				return
			}
			dialer.mu.Lock()
			result = append([]any(nil), dialer.histories[addresses[0]]...)
			dialer.mu.Unlock()
		case SPVTransactionBatchMethod:
			var transactionIDs []string
			if err := json.Unmarshal(request.Params, &transactionIDs); err != nil {
				return
			}
			transactions := make(map[string]any, len(transactionIDs))
			dialer.mu.Lock()
			for _, transactionID := range transactionIDs {
				raw, ok := dialer.transactions[transactionID]
				if !ok {
					dialer.mu.Unlock()
					return
				}
				transactions[transactionID] = []any{hex.EncodeToString(raw), map[string]any{}}
			}
			dialer.mu.Unlock()
			result = transactions
		case "server.ping":
			result = nil
		default:
			return
		}
		response, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "result": result, "id": id,
		})
		if err != nil {
			return
		}
		dialer.writeMu.Lock()
		_, err = connection.Write(append(response, '\n'))
		dialer.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (dialer *liveCheckpointDialer) notify(t *testing.T, params any) {
	dialer.notifyMethod(t, "blockchain.headers.subscribe", params)
}

func (dialer *liveCheckpointDialer) notifyMethod(t *testing.T, method string, params any) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var connection net.Conn
	for connection == nil {
		dialer.mu.Lock()
		connection = dialer.connection
		dialer.mu.Unlock()
		if connection == nil {
			if time.Now().After(deadline) {
				t.Fatal("live checkpoint connection was unavailable")
			}
			time.Sleep(time.Millisecond)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer.writeMu.Lock()
	_, err = connection.Write(append(payload, '\n'))
	dialer.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

var _ walletspv.Dialer = (*liveCheckpointDialer)(nil)
