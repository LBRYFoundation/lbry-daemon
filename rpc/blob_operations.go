package rpc

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"unicode/utf8"

	blobpkg "lbry/daemon/blob"
	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleBlobGet(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	blobHash, _ := normalized.named["blob_hash"].(string)
	if err := rpcServer.blobManager.Ensure(normalized.ctx, blobHash); err != nil {
		panic(err)
	}
	if transactionListTruthy(normalized.named["read"]) {
		data, exists := rpcServer.blobManager.Get(blobHash)
		if !exists {
			panic(fmt.Errorf("blob %s is unavailable", blobHash))
		}
		if !utf8.Valid(data) {
			panic(errors.New("'utf-8' codec can't decode blob data"))
		}
		sendResultResponse(response, string(data))
		return
	}
	sendResultResponse(response, "Downloaded blob "+blobHash)
}

func (rpcServer *RPCServer) handleBlobAnnounce(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	blobHash, _ := normalized.named["blob_hash"].(string)
	streamHash, _ := normalized.named["stream_hash"].(string)
	sdHash, _ := normalized.named["sd_hash"].(string)

	if blobHash == "" && streamHash == "" && sdHash == "" {
		panic(errors.New("single argument must be specified"))
	}
	if sdHash != "" && streamHash != "" {
		panic(errors.New("either the sd hash or the stream hash should be provided, not both"))
	}
	hashes := []string{blobHash}
	if blobHash == "" {
		var err error
		hashes, err = rpcServer.selectedBlobHashes(normalized)
		if err != nil {
			panic(err)
		}
	}
	node := rpcServer.dhtNodeProvider()
	queued := false
	if store, ok := rpcServer.managedFileLister.(ManagedAnnouncementStore); ok {
		if err := store.QueueBlobAnnouncements(normalized.ctx, hashes, true); err != nil {
			panic(err)
		}
		queued = true
	}
	for _, hash := range hashes {
		if queued || hash == "" || !rpcServer.blobManager.Has(hash) || node == nil {
			continue
		}
		go func() {
			_, _ = node.AnnounceBlob(hash)
		}()
	}
	sendResultResponse(response, true)
}

func (rpcServer *RPCServer) handleBlobClean(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	cleaner := rpcServer.managedBlobCleaner
	if cleaner == nil {
		cleaner = rpcServer.managedFileLister.(ManagedBlobCleaner)
	}
	contentLimit := settingInteger(rpcServer.settings, "blob_storage_limit")
	networkLimit := settingInteger(rpcServer.settings, "network_storage_limit")
	if _, err := cleaner.CleanManagedBlobs(
		normalized.ctx, rpcServer.blobManager, contentLimit, networkLimit,
	); err != nil {
		panic(err)
	}
	sendResultResponse(response, nil)
}

func settingInteger(settings SettingsStore, name string) int64 {
	value, _ := settings.Get(name)
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func (rpcServer *RPCServer) handleBlobDelete(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	blobHash, _ := normalized.named["blob_hash"].(string)
	if blobHash == "" || !blobpkg.ValidHash(blobHash) {
		sendResultResponse(response, fmt.Sprintf("Invalid blob hash to delete '%s'", blobHash))
		return
	}
	if store, ok := rpcServer.managedFileLister.(ManagedFileStore); ok {
		rows, err := store.ListManagedFiles(normalized.ctx)
		if err != nil {
			panic(err)
		}
		for _, row := range rows {
			if row.SDHash != blobHash {
				continue
			}
			hashes, err := store.DeleteManagedStream(normalized.ctx, row.StreamHash)
			if err != nil {
				panic(err)
			}
			if err := rpcServer.blobManager.Delete(hashes...); err != nil {
				panic(err)
			}
			sendResultResponse(response, "Deleted "+blobHash)
			return
		}
	}
	if err := rpcServer.blobManager.Delete(blobHash); err != nil {
		panic(err)
	}
	sendResultResponse(response, "Deleted "+blobHash)
}

func (rpcServer *RPCServer) handleBlobList(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	if transactionListTruthy(normalized.named["uri"]) {
		sdHash, err := rpcServer.resolveBlobListSDHash(normalized)
		if err != nil {
			panic(err)
		}
		named := make(map[string]any, len(normalized.named))
		for name, value := range normalized.named {
			named[name] = value
		}
		named["sd_hash"] = sdHash
		normalized.named = named
	}
	hashes, err := rpcServer.selectedBlobHashes(normalized)
	if err != nil {
		panic(err)
	}
	if transactionListTruthy(normalized.named["needed"]) {
		filtered := hashes[:0]
		for _, hash := range hashes {
			if !rpcServer.blobManager.Has(hash) {
				filtered = append(filtered, hash)
			}
		}
		hashes = filtered
	}
	if transactionListTruthy(normalized.named["finished"]) {
		filtered := hashes[:0]
		for _, hash := range hashes {
			if rpcServer.blobManager.Has(hash) {
				filtered = append(filtered, hash)
			}
		}
		hashes = filtered
	}
	page := walletListPositiveInteger(normalized.named["page"], 1)
	pageSize := walletListPositiveInteger(normalized.named["page_size"], 20)
	total, start := len(hashes), pageSize*(page-1)
	end := min(start+pageSize, total)
	items := make([]string, 0)
	if start <= total {
		items = append(items, hashes[start:end]...)
	}
	sendResultResponse(response, map[string]any{
		"items": items, "total_pages": (total + pageSize - 1) / pageSize,
		"total_items": total, "page": page, "page_size": pageSize,
	})
}

func (rpcServer *RPCServer) resolveBlobListSDHash(normalized normalizedRPCParams) (string, error) {
	if rpcServer.walletManagerProvider == nil || rpcServer.walletManagerProvider() == nil {
		return "", resolveComponentsNotStartedError()
	}
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		return "", err
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if ledger == nil {
		return "", errors.New("default ledger is unavailable")
	}
	uri, _ := normalized.named["uri"].(string)
	var resolved *walletpkg.TransactionOutput
	encoded, err := ledger.ResolveAndSnapshot(
		normalized.ctx, []walletpkg.ResolveRequest{{URL: uri}},
		walletpkg.ResolvedTransactionOutputAnnotationOptions{
			Accounts: selectedWallet.Accounts, Wallet: selectedWallet,
		},
		walletpkg.LegacyTransactionJSONOptions{},
		func(outputs []*walletpkg.TransactionOutput) error {
			if len(outputs) == 1 {
				resolved = outputs[0]
			}
			return nil
		},
	)
	if err != nil {
		return "", err
	}
	if len(encoded) == 0 || resolved == nil {
		return "", nil
	}
	if value, ok := encoded[0].(map[string]any); ok && value["error"] != nil {
		return "", nil
	}
	sdHash, _, err := walletpkg.TransactionOutputStreamSource(resolved)
	if err != nil {
		return "", nil
	}
	return sdHash, nil
}

func (rpcServer *RPCServer) selectedBlobHashes(normalized normalizedRPCParams) ([]string, error) {
	sdHash, _ := normalized.named["sd_hash"].(string)
	streamHash, _ := normalized.named["stream_hash"].(string)
	if sdHash == "" && streamHash == "" {
		if transactionListTruthy(normalized.named["uri"]) {
			return []string{}, nil
		}
		hashes := rpcServer.blobManager.CompletedBlobHashes()
		sort.Strings(hashes)
		return hashes, nil
	}
	if store, ok := rpcServer.managedFileLister.(ManagedFileStore); ok {
		rows, err := store.ListManagedFiles(normalized.ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if (sdHash != "" && row.SDHash == sdHash) || (streamHash != "" && row.StreamHash == streamHash) {
				sdHash = row.SDHash
				break
			}
		}
	}
	if sdHash == "" {
		return []string{}, nil
	}
	hashes := []string{sdHash}
	if !rpcServer.blobManager.Has(sdHash) {
		return hashes, nil
	}
	data, exists := rpcServer.blobManager.Get(sdHash)
	if !exists {
		return hashes, nil
	}
	descriptor, err := blobpkg.DecodeDescriptor(sdHash, data)
	if err != nil {
		return nil, err
	}
	for _, info := range descriptor.ContentBlobs() {
		if info.BlobHash != "" {
			hashes = append(hashes, info.BlobHash)
		}
	}
	return hashes, nil
}
