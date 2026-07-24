package rpc

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/blob"
	daemonconfig "lbry/daemon/config"
	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestGetResolvesFetchesPersistsAndSavesFreeStream(t *testing.T) {
	fixture := newTXOHandlerOracleFixture(t)
	ledger := fixture.manager.DefaultLedger()
	if ledger == nil {
		t.Fatal("fixture has no default ledger")
	}
	key, iv := bytes.Repeat([]byte{0x31}, 16), bytes.Repeat([]byte{0x32}, 16)
	plaintext := []byte("managed download")
	encrypted := encryptGetTestBlob(t, plaintext, key, iv)
	contentHash := getTestHash(encrypted)
	descriptor := blob.StreamDescriptor{
		StreamName: hex.EncodeToString([]byte("download")), Key: hex.EncodeToString(key),
		SuggestedFileName: hex.EncodeToString([]byte("download.mp4")),
		StreamHash:        getTestHash([]byte("stream")), StreamType: "lbryfile",
		Blobs: []blob.BlobInfo{
			{BlobHash: contentHash, BlobNum: 0, IV: hex.EncodeToString(iv), Length: len(encrypted)},
			{BlobNum: 1, IV: hex.EncodeToString(iv), Length: 0},
		},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(&descriptor)
	descriptorBytes, err := blob.MarshalDescriptor(&descriptor)
	if err != nil {
		t.Fatal(err)
	}
	sdHash := getTestHash(descriptorBytes)
	claimPayload := getTestStreamClaim(sdHash)
	transaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{1},
	}}).AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewClaimNameOutput(
			100_000_000, "download", claimPayload, bytes.Repeat([]byte{0x41}, 20),
		),
	})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	network := &resolvePublicNetwork{
		resolveResult:  resolvePublicResultBase64(transaction.Hash[:]),
		transactionRaw: map[string]string{transaction.ID: hex.EncodeToString(transaction.Raw)},
	}
	ledger.SPVNetwork = network

	manager := blob.NewManager()
	fetched := map[string][]byte{sdHash: descriptorBytes, contentHash: encrypted}
	fetchCalls := []string{}
	manager.SetFetcher(func(_ context.Context, blobHash string) ([]byte, error) {
		fetchCalls = append(fetchCalls, blobHash)
		return append([]byte(nil), fetched[blobHash]...), nil
	})
	store := databasepkg.NewResolvedClaimStore(t.TempDir())
	if err := store.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := daemonconfig.NewMemory()
	directory := t.TempDir()
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return fixture.manager }),
		WithManagedFileLister(store), WithResolvedClaimSaver(store), WithBlobManager(manager),
		WithSettingsStore(settings),
	)
	result := fileMutationRPCResult(t, server, "get", map[string]any{
		"uri": "download", "save_file": true, "download_directory": directory,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["sd_hash"] != sdHash || encoded["status"] != "finished" ||
		encoded["claim_name"] != "download" || encoded["file_name"] != "download.mp4" {
		t.Fatalf("get result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(directory, "download.mp4"))
	if err != nil || !bytes.Equal(data, plaintext) {
		t.Fatalf("downloaded file = %q, %v", data, err)
	}
	if len(fetchCalls) != 2 || fetchCalls[0] != sdHash || fetchCalls[1] != contentHash {
		t.Fatalf("blob fetch calls = %v", fetchCalls)
	}
	files, err := store.ListManagedFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].ClaimOutpoint != transaction.Outputs[0].ID() ||
		files[0].BlobsCompleted != 1 || !files[0].SavedFile {
		t.Fatalf("persisted get files = %#v, %v", files, err)
	}
}

func TestManagedClaimPurchaseFeeLBCAndMaximum(t *testing.T) {
	claim := &walletpkg.ClaimValue{Value: map[string]any{"fee": map[string]any{
		"currency": "LBC", "amount": "1.25000000", "address": "merchant",
	}}}
	settings := daemonconfig.NewMemory()
	if _, err := settings.Set("max_key_fee", map[string]any{"currency": "LBC", "amount": 2.0}); err != nil {
		t.Fatal(err)
	}
	amount, address, err := managedClaimPurchaseFee(claim, settings, nil)
	if err != nil || amount != 125_000_000 || address != "merchant" {
		t.Fatalf("purchase fee = %d, %q, %v", amount, address, err)
	}
	if _, err := settings.Set("max_key_fee", map[string]any{"currency": "LBC", "amount": 1.0}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedClaimPurchaseFee(claim, settings, nil); err == nil ||
		!strings.Contains(err.Error(), "exceeds maximum configured price") {
		t.Fatalf("maximum fee error = %v", err)
	}
	if _, err := settings.Set("max_key_fee", nil); err != nil {
		t.Fatal(err)
	}
	if amount, _, err := managedClaimPurchaseFee(claim, settings, nil); err != nil || amount != 125_000_000 {
		t.Fatalf("unbounded purchase fee = %d, %v", amount, err)
	}
}

func TestManagedClaimPurchaseFeeRejectsUnavailableConversion(t *testing.T) {
	claim := &walletpkg.ClaimValue{Value: map[string]any{"fee": map[string]any{
		"currency": "USD", "amount": "0.25",
	}}}
	settings := daemonconfig.NewMemory()
	if _, err := settings.Set("max_key_fee", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedClaimPurchaseFee(claim, settings, nil); err == nil ||
		!strings.Contains(err.Error(), "exchange rate manager is unavailable") {
		t.Fatalf("conversion error = %v", err)
	}
	for _, amount := range []string{"-1", "0.000000001", "invalid"} {
		claim.Value["fee"].(map[string]any)["currency"] = "LBC"
		claim.Value["fee"].(map[string]any)["amount"] = amount
		if _, _, err := managedClaimPurchaseFee(claim, settings, nil); err == nil {
			t.Fatalf("amount %q unexpectedly accepted", amount)
		}
	}
}

type getTestExchangeRates map[string]uint64

func (rates getTestExchangeRates) ToDewies(currency, amount string) (uint64, error) {
	value, ok := rates[currency+":"+amount]
	if !ok {
		return 0, errors.New("missing test rate")
	}
	return value, nil
}

func TestManagedClaimPurchaseFeeConvertsClaimAndMaximum(t *testing.T) {
	claim := &walletpkg.ClaimValue{Value: map[string]any{"fee": map[string]any{
		"currency": "USD", "amount": "2.50", "address": "merchant",
	}}}
	settings := daemonconfig.NewMemory()
	if _, err := settings.Set("max_key_fee", map[string]any{"currency": "BTC", "amount": 0.001}); err != nil {
		t.Fatal(err)
	}
	rates := getTestExchangeRates{"USD:2.50": 100_000_000, "BTC:0.001": 200_000_000}
	amount, address, err := managedClaimPurchaseFee(claim, settings, rates)
	if err != nil || amount != 100_000_000 || address != "merchant" {
		t.Fatalf("converted purchase fee = %d, %q, %v", amount, address, err)
	}
}

type paidGetNetwork struct {
	*resolvePublicNetwork
	mu                sync.Mutex
	broadcasts        []string
	broadcastErr      error
	downloadComplete  func() bool
	claimSearchResult any
}

func (network *paidGetNetwork) BroadcastTransaction(_ context.Context, raw string) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	if network.downloadComplete != nil && !network.downloadComplete() {
		return nil, errors.New("purchase broadcast before stream startup")
	}
	network.broadcasts = append(network.broadcasts, raw)
	return "broadcast", network.broadcastErr
}

func (network *paidGetNetwork) OneShotNamedValue(
	context.Context, string, map[string]any, bool,
) (any, error) {
	return network.claimSearchResult, nil
}

const paidGetAccountXPrv = "xprv9s21ZrQH143K42ovpZygnjfHdAqSd9jo7zceDfPRogM7bkkoNVv7DRNLEoB8HoirMgH969NrgL8jNzLEegqFzPRWM37GXd4uE8uuRkx4LAe"

func TestGetPaidStreamFundsStartsBroadcastsAndPersistsPurchase(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "get", map[string]any{
		"uri": "paid", "save_file": false,
	})
	encoded, ok := result.(map[string]any)
	if !ok || encoded["sd_hash"] != fixture.sdHash {
		t.Fatalf("paid get result = %#v", result)
	}
	fixture.network.mu.Lock()
	broadcasts := append([]string(nil), fixture.network.broadcasts...)
	fixture.network.mu.Unlock()
	if len(broadcasts) != 1 {
		t.Fatalf("purchase broadcasts = %v", broadcasts)
	}
	transaction, err := walletpkg.ParseTransaction(mustDecodeHex(t, broadcasts[0]))
	if err != nil || len(transaction.Outputs) < 2 || transaction.Outputs[0].Amount != 100_000_000 {
		t.Fatalf("purchase transaction = %#v, %v", transaction, err)
	}
	rows, err := fixture.store.ListManagedFiles(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ContentFeeHex == nil || *rows[0].ContentFeeHex == "" {
		t.Fatalf("managed paid rows = %#v, %v", rows, err)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(context.Background(), []string{fixture.account.ID})
	if err != nil || len(spendable) != 0 {
		t.Fatalf("broadcast purchase spendables = %#v, %v", spendable, err)
	}
}

func TestStreamingGetUsesManagedPaidDownloadPipeline(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	sdHash, err := fixture.server.StreamingGet(context.Background(), "lbry://paid")
	if err != nil || sdHash != fixture.sdHash {
		t.Fatalf("StreamingGet() = %q, %v", sdHash, err)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 1 {
		t.Fatalf("purchase broadcasts = %d", broadcasts)
	}
	rows, err := fixture.store.ListManagedFiles(context.Background())
	if err != nil || len(rows) != 1 || rows[0].FileName != nil || rows[0].DownloadDirectory != nil {
		t.Fatalf("streaming managed rows = %#v, %v", rows, err)
	}
}

func TestConcurrentStreamingGetSharesPaidDownloadAndSurvivesOneCanceledWaiter(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.network.downloadComplete = func() bool { return true }
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	fixture.blobs.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-fetchStarted:
		default:
			close(fetchStarted)
		}
		select {
		case <-releaseFetch:
			return fixture.descriptorBytes, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := fixture.server.StreamingGet(firstCtx, "lbry://paid")
		firstResult <- err
	}()
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("first paid download did not reach descriptor acquisition")
	}
	secondResult := make(chan struct {
		hash string
		err  error
	}, 1)
	go func() {
		hash, err := fixture.server.StreamingGet(context.Background(), "lbry://paid")
		secondResult <- struct {
			hash string
			err  error
		}{hash: hash, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		fixture.server.getMu.Lock()
		waiters := 0
		for _, flight := range fixture.server.getFlights {
			waiters = flight.waiters
		}
		fixture.server.getMu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second paid request did not join the in-flight download")
		}
		time.Sleep(time.Millisecond)
	}
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	close(releaseFetch)
	second := <-secondResult
	if second.err != nil || second.hash != fixture.sdHash {
		t.Fatalf("remaining waiter = %q, %v", second.hash, second.err)
	}
	fixture.network.mu.Lock()
	broadcasts := len(fixture.network.broadcasts)
	fixture.network.mu.Unlock()
	if broadcasts != 1 {
		t.Fatalf("purchase broadcasts = %d, want 1", broadcasts)
	}
}

func TestCanceledLastManagedGetWaiterRemovesJoinableFlight(t *testing.T) {
	server := &RPCServer{getFlights: make(map[string]*managedGetFlight)}
	flightCtx, cancelFlight := context.WithCancel(context.Background())
	flight := &managedGetFlight{done: make(chan struct{}), cancel: cancelFlight, waiters: 1}
	server.getFlights["key"] = flight
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.waitManagedGet(ctx, "key", flight); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	server.getMu.Lock()
	_, exists := server.getFlights["key"]
	server.getMu.Unlock()
	if exists {
		t.Fatal("zero-waiter canceled flight remained joinable")
	}
	select {
	case <-flightCtx.Done():
	default:
		t.Fatal("zero-waiter flight context was not canceled")
	}
}

func TestGetPaidStreamReleasesFundingWhenAcquisitionFails(t *testing.T) {
	fixture := newPaidGetFixture(t, true)
	result := fileMutationRPCResult(t, fixture.server, "get", map[string]any{
		"uri": "paid", "save_file": false,
	})
	encoded, ok := result.(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(encoded["error"]), "acquisition failed") {
		t.Fatalf("failed paid get result = %#v", result)
	}
	spendable, err := fixture.ledger.Database.ListSpendableOutputs(context.Background(), []string{fixture.account.ID})
	if err != nil || len(spendable) != 1 {
		t.Fatalf("released purchase spendables = %#v, %v", spendable, err)
	}
}

type paidGetFixture struct {
	server          *RPCServer
	store           *databasepkg.ResolvedClaimStore
	blobs           *blob.BlobManager
	descriptorBytes []byte
	ledger          *walletpkg.Ledger
	account         *walletpkg.Account
	network         *paidGetNetwork
	sdHash          string
	claimID         string
}

func newPaidGetFixture(t *testing.T, failAcquisition bool) paidGetFixture {
	t.Helper()
	ctx := context.Background()
	manager := walletpkg.NewWalletManager()
	ledger := txoHandlerOracleLedger(t, manager, keys.MainNet, t.TempDir())
	account, err := walletpkg.NewAccount(keys.MainNet, walletpkg.NewObject(
		walletpkg.Member{Key: "private_key", Value: paidGetAccountXPrv},
	))
	if err != nil {
		t.Fatal(err)
	}
	wallet := walletpkg.NewWallet(
		walletpkg.WithWalletName("paid-wallet"),
		walletpkg.WithWalletAccounts([]*walletpkg.Account{account}),
	)
	manager.Wallets = []*walletpkg.Wallet{wallet}
	if err := manager.RegisterAccount(keys.MainNet.ID(), account); err != nil {
		t.Fatal(err)
	}
	if _, err := account.EnsureAddressGap(ctx); err != nil {
		t.Fatal(err)
	}
	addresses, err := account.Receiving.GetAddresses(ctx, false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("funding addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	decodedAddress, err := keys.DecodeBase58(address)
	if err != nil || len(decodedAddress) < 21 {
		t.Fatalf("decode funding address = %x, %v", decodedAddress, err)
	}
	funding := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{1},
	}}).AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewPayPubKeyHashOutput(500_000_000, decodedAddress[1:21]),
	})
	if err := funding.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	funding.Height, funding.Position, funding.IsVerified = 1, 1, true
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: funding.ID, Raw: funding.Raw, Height: 1, Position: 1, IsVerified: true,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: funding.Outputs[0].ID(), Address: &address, Position: 0,
			Amount: 500_000_000, Script: funding.Outputs[0].Script.Source,
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}

	descriptor := blob.StreamDescriptor{
		StreamName: "70616964", SuggestedFileName: "706169642e6d7034",
		StreamHash: getTestHash([]byte("paid-stream")), StreamType: "lbryfile",
		Key:   "00000000000000000000000000000000",
		Blobs: []blob.BlobInfo{{BlobNum: 0, IV: "00000000000000000000000000000000"}},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(&descriptor)
	descriptorBytes, _ := blob.MarshalDescriptor(&descriptor)
	sdHash := getTestHash(descriptorBytes)
	claimPayload := getTestPaidStreamClaim(sdHash, 100_000_000)
	claimTransaction := walletpkg.NewTransaction().AddInputs([]walletpkg.TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0), Coinbase: []byte{2},
	}}).AddOutputs([]walletpkg.TransactionOutput{
		walletpkg.NewClaimNameOutput(1, "paid", claimPayload, bytes.Repeat([]byte{0x91}, 20)),
	})
	if err := claimTransaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	baseNetwork := &resolvePublicNetwork{
		resolveResult: resolvePublicResultBase64(claimTransaction.Hash[:]),
		transactionRaw: map[string]string{
			claimTransaction.ID: hex.EncodeToString(claimTransaction.Raw),
		},
	}
	downloaded := false
	network := &paidGetNetwork{
		resolvePublicNetwork: baseNetwork,
		downloadComplete:     func() bool { return downloaded },
		claimSearchResult:    claimSearchPublicResultBase64(claimTransaction.Hash[:], 0, 1),
	}
	ledger.SPVNetwork = network
	blobManager := blob.NewManager()
	blobManager.SetFetcher(func(context.Context, string) ([]byte, error) {
		if failAcquisition {
			return nil, errors.New("acquisition failed")
		}
		downloaded = true
		return descriptorBytes, nil
	})
	store := databasepkg.NewResolvedClaimStore(t.TempDir())
	if err := store.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	settings := daemonconfig.NewMemory()
	if _, err := settings.Set("max_key_fee", nil); err != nil {
		t.Fatal(err)
	}
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }),
		WithManagedFileLister(store), WithResolvedClaimSaver(store),
		WithBlobManager(blobManager), WithSettingsStore(settings),
	)
	claimID, err := claimTransaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	return paidGetFixture{
		server: server, store: store, ledger: ledger, account: account,
		network: network, blobs: blobManager, descriptorBytes: descriptorBytes,
		sdHash: sdHash, claimID: claimID,
	}
}

func getTestPaidStreamClaim(sdHash string, feeAmount uint64) []byte {
	sourceHash, _ := hex.DecodeString(sdHash)
	source := protowire.AppendTag(nil, 6, protowire.BytesType)
	source = protowire.AppendBytes(source, sourceHash)
	fee := protowire.AppendTag(nil, 1, protowire.VarintType)
	fee = protowire.AppendVarint(fee, 1)
	fee = protowire.AppendTag(fee, 3, protowire.VarintType)
	fee = protowire.AppendVarint(fee, feeAmount)
	stream := protowire.AppendTag(nil, 1, protowire.BytesType)
	stream = protowire.AppendBytes(stream, source)
	stream = protowire.AppendTag(stream, 6, protowire.BytesType)
	stream = protowire.AppendBytes(stream, fee)
	claim := protowire.AppendTag(nil, 1, protowire.BytesType)
	claim = protowire.AppendBytes(claim, stream)
	return append([]byte{0}, claim...)
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func getTestStreamClaim(sdHash string) []byte {
	sourceHash, _ := hex.DecodeString(sdHash)
	source := protowire.AppendTag(nil, 6, protowire.BytesType)
	source = protowire.AppendBytes(source, sourceHash)
	stream := protowire.AppendTag(nil, 1, protowire.BytesType)
	stream = protowire.AppendBytes(stream, source)
	claim := protowire.AppendTag(nil, 1, protowire.BytesType)
	claim = protowire.AppendBytes(claim, stream)
	return append([]byte{0}, claim...)
}

func getTestHash(data []byte) string {
	digest := sha512.Sum384(data)
	return hex.EncodeToString(digest[:])
}

func encryptGetTestBlob(t *testing.T, plaintext, key, iv []byte) []byte {
	t.Helper()
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)
	return encrypted
}
