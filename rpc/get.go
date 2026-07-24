package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"time"

	"lbry/daemon/blob"
	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
)

// StreamingGet runs the same managed get pipeline used by JSON-RPC and
// returns the descriptor hash needed by the media server redirect.
func (rpcServer *RPCServer) StreamingGet(ctx context.Context, uri string) (string, error) {
	if rpcServer == nil || rpcServer.walletManagerProvider == nil || rpcServer.blobManager == nil {
		return "", errors.New("managed get is unavailable")
	}
	if ctx == nil {
		return "", errors.New("managed get context is nil")
	}
	result, err := rpcServer.downloadFromURI(normalizedRPCParams{
		ctx: ctx, named: map[string]any{"uri": uri},
	}, uri, nil, nil)
	if err != nil {
		return "", err
	}
	sdHash, _ := result["sd_hash"].(string)
	if sdHash == "" {
		return "", errors.New("managed get returned no stream descriptor")
	}
	return sdHash, nil
}

func (rpcServer *RPCServer) handleGet(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	uri, _ := normalized.named["uri"].(string)
	fileName := fileMutationOptionalString(normalized.named["file_name"])
	downloadDirectory := fileMutationOptionalString(normalized.named["download_directory"])
	if downloadDirectory != nil {
		if info, err := os.Stat(*downloadDirectory); err != nil || !info.IsDir() {
			sendResultResponse(w, map[string]any{
				"error": fmt.Sprintf("specified download directory %q does not exist", *downloadDirectory),
			})
			return
		}
	}
	result, err := rpcServer.downloadFromURI(normalized, uri, fileName, downloadDirectory)
	if err != nil {
		sendResultResponse(w, map[string]any{"error": err.Error()})
		return
	}
	sendResultResponse(w, result)
}

func (rpcServer *RPCServer) downloadFromURI(
	normalized normalizedRPCParams,
	uri string,
	fileName, downloadDirectory *string,
) (map[string]any, error) {
	ctx := normalized.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	key := rpcServer.managedGetFlightKey(normalized, uri, fileName, downloadDirectory)
	rpcServer.getMu.Lock()
	if rpcServer.getFlights == nil {
		rpcServer.getFlights = make(map[string]*managedGetFlight)
	}
	if flight := rpcServer.getFlights[key]; flight != nil {
		flight.waiters++
		rpcServer.getMu.Unlock()
		return rpcServer.waitManagedGet(ctx, key, flight)
	}
	flightCtx, cancel := context.WithCancel(context.Background())
	flight := &managedGetFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	rpcServer.getFlights[key] = flight
	rpcServer.getMu.Unlock()
	workerParams := normalized
	workerParams.ctx = flightCtx
	workerParams.named = copyMap(normalized.named)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				flight.err = fmt.Errorf("managed get panic: %v", recovered)
			}
			rpcServer.getMu.Lock()
			if rpcServer.getFlights[key] == flight {
				delete(rpcServer.getFlights, key)
			}
			flight.cancel()
			close(flight.done)
			rpcServer.getMu.Unlock()
		}()
		flight.result, flight.err = rpcServer.downloadFromURIOnce(
			workerParams, uri, fileName, downloadDirectory,
		)
	}()
	return rpcServer.waitManagedGet(ctx, key, flight)
}

func (rpcServer *RPCServer) waitManagedGet(
	ctx context.Context, key string, flight *managedGetFlight,
) (map[string]any, error) {
	select {
	case <-flight.done:
		return copyMap(flight.result), flight.err
	case <-ctx.Done():
		rpcServer.getMu.Lock()
		if rpcServer.getFlights[key] == flight {
			flight.waiters--
			if flight.waiters == 0 {
				delete(rpcServer.getFlights, key)
				flight.cancel()
			}
		}
		rpcServer.getMu.Unlock()
		return nil, ctx.Err()
	}
}

func (rpcServer *RPCServer) managedGetFlightKey(
	normalized normalizedRPCParams, uri string, fileName, downloadDirectory *string,
) string {
	text := func(value *string) any {
		if value == nil {
			return nil
		}
		return *value
	}
	saveFile := normalized.named["save_file"]
	if saveFile == nil {
		saveFile, _ = rpcServer.settings.Get("save_files")
	}
	timeout := normalized.named["timeout"]
	if timeout == nil {
		timeout, _ = rpcServer.settings.Get("download_timeout")
	}
	defaultDirectory, _ := rpcServer.settings.Get("download_dir")
	encoded, err := json.Marshal([]any{
		uri, text(fileName), text(downloadDirectory), timeout, saveFile,
		normalized.named["wallet_id"], defaultDirectory,
	})
	if err != nil {
		return fmt.Sprintf("%q|%v|%v|%#v", uri, text(fileName), text(downloadDirectory), normalized.named)
	}
	return string(encoded)
}

func (rpcServer *RPCServer) downloadFromURIOnce(
	normalized normalizedRPCParams,
	uri string,
	fileName, downloadDirectory *string,
) (map[string]any, error) {
	parsed, err := walletpkg.ParseLBRYURL(uri)
	if err != nil || !parsed.HasStream {
		return nil, fmt.Errorf("%s is not a valid stream url", uri)
	}
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		return nil, err
	}
	if selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	store, ok := rpcServer.managedFileLister.(ManagedDownloadStore)
	if !ok {
		return nil, errors.New("managed download store is unavailable")
	}
	timeout, err := getTimeout(normalized.named["timeout"], rpcServer.settings)
	if err != nil {
		return nil, err
	}
	requestCtx := normalized.ctx
	resolveCtx, cancelResolve := context.WithTimeout(requestCtx, 3*time.Second)
	ctx := resolveCtx
	var resolvedOutput *walletpkg.TransactionOutput
	encoded, err := ledger.ResolveAndSnapshot(
		ctx, []walletpkg.ResolveRequest{{URL: uri}},
		walletpkg.ResolvedTransactionOutputAnnotationOptions{
			Accounts: selectedWallet.Accounts, Wallet: selectedWallet,
			PurchaseReceiptRequested: true, IncludeIsMyOutput: true,
		},
		walletpkg.LegacyTransactionJSONOptions{},
		func(outputs []*walletpkg.TransactionOutput) error {
			if len(outputs) != 1 {
				return errors.New("resolved stream output is unavailable")
			}
			resolvedOutput = outputs[0]
			return store.SaveResolvedClaims(ctx, ledger, outputs)
		},
	)
	cancelResolve()
	if err != nil {
		return nil, err
	}
	ctx = requestCtx
	if timeout > 0 {
		var cancelDownload context.CancelFunc
		ctx, cancelDownload = context.WithTimeout(requestCtx, timeout)
		defer cancelDownload()
	}
	if len(encoded) != 1 || resolvedOutput == nil {
		return nil, fmt.Errorf("Failed to resolve stream at %q", uri)
	}
	if resolution, ok := encoded[0].(map[string]any); ok {
		if resolutionError, failed := resolution["error"]; failed {
			return nil, fmt.Errorf("Failed to resolve stream at %q: %v", uri, resolutionError)
		}
	}
	sdHash, claimValue, err := walletpkg.TransactionOutputStreamSource(resolvedOutput)
	if err != nil {
		return nil, fmt.Errorf("There is nothing to download at %s - Source is unknown or unset", uri)
	}
	claimID, err := resolvedOutput.ClaimID()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListManagedFiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SDHash != sdHash {
			continue
		}
		if row.ClaimID != claimID {
			return nil, fmt.Errorf(
				"stream for %s collides with existing download %s", row.ClaimID, claimID,
			)
		}
		return rpcServer.finishExistingDownload(
			ctx, store, ledger, row, normalized, fileName, downloadDirectory,
		)
	}
	var payment *walletpkg.Transaction
	paymentFinalized := false
	if managedClaimHasPrice(claimValue) &&
		(resolvedOutput.IsMyOutput == nil || !*resolvedOutput.IsMyOutput) &&
		resolvedOutput.PurchaseReceipt == nil {
		amount, address, feeErr := managedClaimPurchaseFee(
			claimValue, rpcServer.settings, rpcServer.exchangeRates,
		)
		if feeErr != nil {
			return nil, feeErr
		}
		payment, err = walletpkg.CreatePurchaseTransaction(
			ctx, selectedWallet.Accounts, resolvedOutput, amount, address,
		)
		if err != nil {
			return nil, err
		}
		defer func() {
			if !paymentFinalized {
				_ = ledger.ReleaseTransaction(context.WithoutCancel(ctx), payment)
			}
		}()
	}
	if err := rpcServer.blobManager.Ensure(ctx, sdHash); err != nil {
		return nil, fmt.Errorf("Failed to download sd blob %s: %w", sdHash, err)
	}
	sdBytes, ok := rpcServer.blobManager.Get(sdHash)
	if !ok {
		return nil, fmt.Errorf("Failed to download sd blob %s", sdHash)
	}
	descriptor, err := blob.DecodeDescriptor(sdHash, sdBytes)
	if err != nil {
		return nil, err
	}
	if err := store.SaveStreamDescriptor(
		ctx, sdHash, len(sdBytes), descriptor, time.Now().Unix(), false,
	); err != nil {
		return nil, err
	}
	if err := store.MarkManagedBlobsFinished(ctx, []string{sdHash}); err != nil {
		return nil, err
	}
	saveFile := getSaveFile(normalized.named["save_file"], rpcServer.settings)
	if fileName != nil {
		saveFile = true
	}
	var storedName, storedDirectory *string
	if saveFile {
		storedDirectory = downloadDirectory
		if storedDirectory == nil {
			storedDirectory = getSettingStringPointer(rpcServer.settings, "download_dir")
		}
		storedName = fileName
		if storedName == nil {
			name := descriptor.SuggestedFileName
			if decoded, decodeErr := hex.DecodeString(name); decodeErr == nil {
				name = string(decoded)
			}
			storedName = &name
		}
	}
	var rawContentFee []byte
	if payment != nil {
		rawContentFee = append([]byte(nil), payment.Raw...)
	}
	if _, err := store.SaveManagedFile(
		ctx, descriptor.StreamHash, storedName, storedDirectory,
		0, "running", rawContentFee, time.Now().Unix(),
	); err != nil {
		return nil, err
	}
	if err := store.LinkManagedStreamClaim(ctx, descriptor.StreamHash, resolvedOutput.ID()); err != nil {
		return nil, err
	}
	rows, err = store.ListManagedFiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.StreamHash == descriptor.StreamHash {
			result, finishErr := rpcServer.finishExistingDownload(
				ctx, store, ledger, row, normalized, fileName, downloadDirectory,
			)
			if finishErr != nil {
				return nil, finishErr
			}
			if payment != nil {
				paymentFinalized = true
				if err := ledger.BroadcastOrRelease(ctx, payment, false); err != nil {
					return nil, err
				}
			}
			return result, nil
		}
	}
	return nil, errors.New("downloaded stream was not persisted")
}

func (rpcServer *RPCServer) finishExistingDownload(
	ctx context.Context, store ManagedDownloadStore, ledger *walletpkg.Ledger,
	row databasepkg.ManagedFileRow, normalized normalizedRPCParams,
	fileName, downloadDirectory *string,
) (map[string]any, error) {
	if err := rpcServer.blobManager.Ensure(ctx, row.SDHash); err != nil {
		return nil, err
	}
	if registrar, ok := rpcServer.managedFileController.(ManagedFileRegistrar); ok {
		if err := registrar.RegisterManagedFile(ctx, row); err != nil {
			return nil, err
		}
	}
	saveFile := getSaveFile(normalized.named["save_file"], rpcServer.settings)
	if fileName != nil {
		saveFile = true
	}
	if saveFile && managedFileWrittenBytes(row) >= 0 {
		saveFile = false
	}
	if saveFile {
		if rpcServer.managedFileController == nil {
			descriptorBytes, ok := rpcServer.blobManager.Get(row.SDHash)
			if !ok {
				return nil, fmt.Errorf("stream descriptor %s is unavailable", row.SDHash)
			}
			descriptor, err := blob.DecodeDescriptor(row.SDHash, descriptorBytes)
			if err != nil {
				return nil, err
			}
			completed := []string{row.SDHash}
			for _, blobInfo := range descriptor.ContentBlobs() {
				if err := rpcServer.blobManager.Ensure(ctx, blobInfo.BlobHash); err != nil {
					return nil, err
				}
				completed = append(completed, blobInfo.BlobHash)
			}
			if err := store.MarkManagedBlobsFinished(ctx, completed); err != nil {
				return nil, err
			}
		}
		updated, err := rpcServer.saveManagedFile(ctx, store, row, fileName, downloadDirectory)
		if err != nil {
			return nil, err
		}
		row = updated
	}
	return rpcServer.encodeManagedFile(ledger, row, nil)
}

func managedClaimHasPrice(value *walletpkg.ClaimValue) bool {
	if value == nil {
		return false
	}
	fee, ok := value.Value["fee"].(map[string]any)
	if !ok {
		return false
	}
	amount, _ := fee["amount"].(string)
	parsed, ok := new(big.Rat).SetString(amount)
	return ok && parsed.Sign() > 0
}

func managedClaimPurchaseFee(
	value *walletpkg.ClaimValue, settings SettingsStore, exchangeRates ExchangeRateConverter,
) (uint64, string, error) {
	fee, ok := value.Value["fee"].(map[string]any)
	if !ok {
		return 0, "", errors.New("claim does not have a purchase price")
	}
	currency, _ := fee["currency"].(string)
	amountText, _ := fee["amount"].(string)
	amount, err := managedFeeToDewies(currency, amountText, exchangeRates)
	if err != nil {
		return 0, "", err
	}
	if configured, exists := settings.Get("max_key_fee"); exists && configured != nil {
		maximum, ok := configured.(map[string]any)
		if !ok {
			return 0, "", errors.New("invalid max_key_fee setting")
		}
		maximumCurrency, _ := maximum["currency"].(string)
		maximumText := fmt.Sprint(maximum["amount"])
		maximumAmount, maximumErr := managedFeeToDewies(
			maximumCurrency, maximumText, exchangeRates,
		)
		if maximumErr != nil {
			return 0, "", maximumErr
		}
		if maximumAmount > 0 && amount > maximumAmount {
			return 0, "", fmt.Errorf(
				"Purchase price of %s LBC exceeds maximum configured price of %s LBC.",
				amountText, maximumText,
			)
		}
	}
	address, _ := fee["address"].(string)
	return amount, address, nil
}

func managedFeeToDewies(currency, amount string, exchangeRates ExchangeRateConverter) (uint64, error) {
	if exchangeRates != nil {
		return exchangeRates.ToDewies(currency, amount)
	}
	if currency != "LBC" {
		return 0, fmt.Errorf(
			"Unable to convert %s from %s to LBC: exchange rate manager is unavailable",
			amount, currency,
		)
	}
	return managedLBCToDewies(amount)
}

func managedLBCToDewies(amount string) (uint64, error) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok || value.Sign() < 0 {
		return 0, fmt.Errorf("invalid LBC amount %q", amount)
	}
	value.Mul(value, big.NewRat(walletpkg.TransactionCoin, 1))
	if !value.IsInt() || !value.Num().IsUint64() {
		return 0, fmt.Errorf("invalid LBC amount %q", amount)
	}
	return value.Num().Uint64(), nil
}

func getTimeout(value any, settings SettingsStore) (time.Duration, error) {
	seconds := 0.0
	if value != nil {
		switch typed := value.(type) {
		case json.Number:
			parsed, err := strconv.ParseFloat(typed.String(), 64)
			if err != nil {
				return 0, err
			}
			seconds = parsed
		case float64:
			seconds = typed
		default:
			return 0, fmt.Errorf("timeout has type %T", value)
		}
	}
	if seconds == 0 {
		if configured, exists := settings.Get("download_timeout"); exists {
			seconds, _ = configured.(float64)
		}
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, errors.New("invalid timeout")
	}
	if seconds <= 0 {
		return time.Nanosecond, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func getSaveFile(value any, settings SettingsStore) bool {
	if value != nil {
		return transactionListTruthy(value)
	}
	configured, _ := settings.Get("save_files")
	return transactionListTruthy(configured)
}

func getSettingStringPointer(settings SettingsStore, name string) *string {
	value, _ := settings.Get(name)
	text, _ := value.(string)
	if text == "" {
		return nil
	}
	return &text
}
