package rpc

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	blobpkg "lbry/daemon/blob"
	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleStreamCreate(response http.ResponseWriter, params any) {
	result, err := rpcServer.streamMutation(params.(normalizedRPCParams), false)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) handleStreamUpdate(response http.ResponseWriter, params any) {
	result, err := rpcServer.streamMutation(params.(normalizedRPCParams), true)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) handlePublish(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	result, err := rpcServer.publish(normalized)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) publish(normalized normalizedRPCParams) (map[string]any, error) {
	name, _ := normalized.named["name"].(string)
	if err := validateStreamName(name); err != nil {
		return nil, err
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
	if err != nil || selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	accounts := selectedWallet.Accounts
	if accountID != nil {
		account, accountErr := selectedWallet.AccountOrDefault(accountID)
		if accountErr != nil {
			return nil, accountErr
		}
		accounts = []*walletpkg.Account{account}
	}
	ids := make([]string, len(accounts))
	for index, account := range accounts {
		ids[index] = account.ID
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	claims, err := ledger.GetClaims(normalized.ctx, walletpkg.ClaimListOptions{
		Query: ledgerdb.OutputQuery{AccountIDs: ids, ClaimNames: []string{name}}, Wallet: selectedWallet,
	})
	if err != nil {
		return nil, err
	}
	switch len(claims) {
	case 0:
		if _, exists := normalized.named["bid"]; !exists {
			return nil, errors.New("'bid' is a required argument for new publishes.")
		}
		return rpcServer.streamMutation(normalized, false)
	case 1:
		value, err := walletpkg.DecodeClaimValue(claims[0].Script.Claim)
		if err != nil {
			return nil, err
		}
		if value.Type != "stream" {
			return nil, fmt.Errorf("Claim at name '%s' is not a stream claim.", name)
		}
		claimID, err := claims[0].ClaimID()
		if err != nil {
			return nil, err
		}
		normalized.named["claim_id"] = claimID
		normalized.named["replace"] = true
		normalized.kwargs["replace"] = true
		return rpcServer.streamMutation(normalized, true)
	default:
		return nil, fmt.Errorf(
			"There are %d claims for '%s', please use 'stream update' command to update a specific stream claim.",
			len(claims), name,
		)
	}
}

func (rpcServer *RPCServer) streamMutation(normalized normalizedRPCParams, update bool) (map[string]any, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil || selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	if selectedWallet.IsLocked() {
		return nil, errors.New("Cannot spend funds with locked wallet, unlock first.")
	}
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		return nil, err
	}
	funding, err := selectedWallet.AccountsOrAll(fundingIDs)
	if err != nil || len(funding) == 0 {
		return nil, walletpkg.ErrPurchaseFundingAccount
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	account, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		return nil, err
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	channel, err := selectSigningChannel(normalized, ledger, selectedWallet)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]any, len(normalized.kwargs)+4)
	for key, value := range normalized.kwargs {
		fields[key] = value
	}
	filePath := fileMutationOptionalString(normalized.named["file_path"])
	preview := transactionListTruthy(normalized.named["preview"])
	var created *blobpkg.CreatedStream
	var managedStore ManagedDownloadStore
	if filePath != nil {
		validateFile := transactionListTruthy(normalized.named["validate_file"])
		optimizeFile := transactionListTruthy(normalized.named["optimize_file"])
		if validateFile || optimizeFile {
			if rpcServer.fileAnalyzer == nil {
				return nil, errors.New("Unable to locate or run ffmpeg or ffprobe. Please install FFmpeg and ensure that it is callable via PATH or conf.ffmpeg_path")
			}
			analyzedPath, spec, analyzeErr := rpcServer.fileAnalyzer.VerifyOrRepair(
				normalized.ctx, validateFile, optimizeFile, *filePath, true,
			)
			if analyzeErr != nil {
				return nil, analyzeErr
			}
			filePath = &analyzedPath
			for name, value := range spec {
				fields[name] = value
			}
		}
		if err := addStreamFileMetadata(fields, *filePath); err != nil {
			return nil, err
		}
		if preview {
			fields["sd_hash"] = strings.Repeat("0", 96)
		} else {
			if rpcServer.blobManager == nil {
				return nil, errors.New("blob manager is unavailable")
			}
			var ok bool
			managedStore, ok = rpcServer.managedFileLister.(ManagedDownloadStore)
			if !ok {
				return nil, errors.New("managed stream store is unavailable")
			}
			created, err = blobpkg.CreateStreamDescriptor(*filePath, nil)
			if err != nil {
				return nil, err
			}
			if err := persistCreatedStream(normalized.ctx, rpcServer.blobManager, managedStore, *filePath, created); err != nil {
				return nil, err
			}
			if registrar, ok := rpcServer.managedFileController.(ManagedFileRegistrar); ok {
				if err := registrar.RegisterManagedFile(
					context.WithoutCancel(normalized.ctx),
					databasepkg.ManagedFileRow{
						StreamHash: created.Descriptor.StreamHash,
						SDHash:     created.SDHash,
					},
				); err != nil {
					return nil, err
				}
			}
			fields["sd_hash"] = created.SDHash
		}
	}
	var transaction *walletpkg.Transaction
	if !update {
		name, _ := normalized.named["name"].(string)
		if err := validateStreamName(name); err != nil {
			return nil, err
		}
		existing, err := ledger.GetStreams(normalized.ctx, walletpkg.ClaimListOptions{
			Query:  ledgerdb.OutputQuery{AccountIDs: []string{account.ID}, ClaimNames: []string{name}},
			Wallet: selectedWallet,
		})
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 && !transactionListTruthy(normalized.named["allow_duplicate_name"]) {
			return nil, fmt.Errorf(
				"You already have a stream claim published under the name '%s'. Use --allow-duplicate-name flag to override.", name,
			)
		}
		amount, err := managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
		}
		address, err := streamClaimAddress(normalized, account)
		if err != nil {
			return nil, err
		}
		defaultStreamFeeAddress(fields, address)
		transaction, err = walletpkg.CreateStreamTransaction(
			normalized.ctx, name, amount, address, funding, fields, channel,
		)
		if err != nil {
			return nil, err
		}
	} else {
		claimID, _ := normalized.named["claim_id"].(string)
		accounts := selectedWallet.Accounts
		if accountID != nil {
			accounts = []*walletpkg.Account{account}
		}
		ids := make([]string, len(accounts))
		for index, item := range accounts {
			ids[index] = item.ID
		}
		items, err := ledger.GetStreams(normalized.ctx, walletpkg.ClaimListOptions{
			Query: ledgerdb.OutputQuery{AccountIDs: ids, ClaimIDs: []string{claimID}}, Wallet: selectedWallet,
		})
		if err != nil || len(items) != 1 {
			return nil, fmt.Errorf("Can't find the stream '%s'.", claimID)
		}
		old := items[0]
		if channel == nil && old.Channel != nil && !transactionListTruthy(normalized.named["clear_channel"]) &&
			!transactionListTruthy(normalized.named["replace"]) {
			channel = old.Channel
			if channel.PrivateKey == nil {
				return nil, errors.New("Could not find private key for signing channel.")
			}
		}
		amount := old.Amount
		if normalized.named["bid"] != nil {
			amount, err = managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
		}
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
		}
		address, err := old.Address(ledger.Network)
		if err != nil {
			return nil, err
		}
		if supplied := fileMutationOptionalString(normalized.named["claim_address"]); supplied != nil {
			address = *supplied
		}
		defaultStreamFeeAddress(fields, address)
		transaction, err = walletpkg.CreateStreamUpdateTransaction(
			normalized.ctx, old, amount, address, funding, fields,
			transactionListTruthy(normalized.named["replace"]), channel,
		)
		if err != nil {
			return nil, err
		}
	}
	if preview {
		if err := ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction); err != nil {
			return nil, err
		}
	} else if err := ledger.BroadcastOrRelease(
		normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]),
	); err != nil {
		return nil, err
	}
	if created != nil {
		if rpcServer.resolvedClaimSaver == nil {
			return nil, errors.New("resolved claim saver is unavailable")
		}
		if err := rpcServer.resolvedClaimSaver.SaveResolvedClaims(
			context.WithoutCancel(normalized.ctx), ledger, []*walletpkg.TransactionOutput{&transaction.Outputs[0]},
		); err != nil {
			return nil, err
		}
		if err := managedStore.LinkManagedStreamClaim(
			context.WithoutCancel(normalized.ctx), created.Descriptor.StreamHash, transaction.Outputs[0].ID(),
		); err != nil {
			return nil, err
		}
	}
	return ledger.LegacyTransactionJSON(transaction)
}

func persistCreatedStream(
	ctx context.Context, manager *blobpkg.BlobManager, store ManagedDownloadStore,
	filePath string, created *blobpkg.CreatedStream,
) error {
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		return err
	}
	hashes := make([]string, 0, len(created.Blobs)+1)
	for hash, data := range created.Blobs {
		if err := manager.Set(hash, data, false); err != nil {
			return err
		}
		hashes = append(hashes, hash)
	}
	hashes = append(hashes, created.SDHash)
	addedOn := time.Now().Unix()
	if err := store.SaveStreamDescriptor(
		ctx, created.SDHash, len(created.DescriptorBytes), created.Descriptor, addedOn, true,
	); err != nil {
		return err
	}
	directory, name := filepath.Dir(filePath), filepath.Base(filePath)
	if _, err := store.SaveManagedFile(
		ctx, created.Descriptor.StreamHash, &name, &directory, 0, "finished", nil, addedOn,
	); err != nil {
		return err
	}
	return store.MarkManagedBlobsFinished(ctx, hashes)
}

func streamClaimAddress(normalized normalizedRPCParams, account *walletpkg.Account) (string, error) {
	if supplied := fileMutationOptionalString(normalized.named["claim_address"]); supplied != nil {
		return *supplied, nil
	}
	if account.Receiving == nil {
		return "", errors.New("stream holding account is unavailable")
	}
	return account.Receiving.GetOrCreateUsableAddress(normalized.ctx)
}

func defaultStreamFeeAddress(fields map[string]any, claimAddress string) {
	if _, exists := fields["fee_address"]; exists {
		return
	}
	if _, currency := fields["fee_currency"]; currency {
		fields["fee_address"] = claimAddress
		return
	}
	if _, amount := fields["fee_amount"]; amount {
		fields["fee_address"] = claimAddress
	}
}

func addStreamFileMetadata(fields map[string]any, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("Cannot publish empty file: %s", path)
	}
	digest := sha512.Sum384(data)
	name := filepath.Base(path)
	fields["file_name"] = name
	fields["file_size"] = strconv.FormatInt(int64(len(data)), 10)
	fields["file_hash"] = hex.EncodeToString(digest[:])
	mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if separator := strings.IndexByte(mediaType, ';'); separator >= 0 {
		mediaType = mediaType[:separator]
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	fields["media_type"] = mediaType
	return nil
}

func validateStreamName(name string) error {
	if name == "" {
		return errors.New("Stream name cannot be blank.")
	}
	if strings.HasPrefix(name, "@") {
		return errors.New("Stream names cannot start with '@' symbol. This is reserved for channels claims.")
	}
	if strings.ContainsAny(name, "/:#$") {
		return errors.New("Stream name has invalid characters.")
	}
	return nil
}
