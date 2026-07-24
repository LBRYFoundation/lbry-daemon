package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"lbry/daemon/blob"
	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

var fileListFilterFields = map[string]struct{}{
	"rowid": {}, "status": {}, "file_name": {}, "added_on": {},
	"download_path": {}, "claim_name": {}, "claim_height": {},
	"claim_id": {}, "outpoint": {}, "txid": {}, "nout": {},
	"channel_claim_id": {}, "channel_name": {}, "completed": {},
	"sd_hash": {}, "stream_hash": {}, "blobs_remaining": {},
	"blobs_in_stream": {}, "uploading_to_reflector": {},
	"is_fully_reflected": {},
}

var fileListFixedParameters = map[string]struct{}{
	"sort": {}, "reverse": {}, "comparison": {}, "wallet_id": {},
	"page": {}, "page_size": {},
}

func (rpcServer *RPCServer) handleFileList(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(errors.New("'NoneType' object has no attribute 'get_wallet_or_default'"))
	}
	var walletID *string
	if value := normalized.named["wallet_id"]; value != nil {
		text, ok := value.(string)
		if !ok {
			panic(fmt.Errorf("wallet id has type %T", value))
		}
		walletID = &text
	}
	wallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		panic(err)
	}
	if wallet == nil {
		panic(errors.New("'NoneType' object has no attribute 'accounts'"))
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		panic(errors.New("'NoneType' object has no attribute 'db'"))
	}
	rows, err := rpcServer.managedFileLister.ListManagedFiles(normalized.ctx)
	if err != nil {
		panic(err)
	}
	sortBy := fileListOptionalText(normalized.named["sort"])
	if sortBy == "" {
		sortBy = "rowid"
	}
	comparison := fileListOptionalText(normalized.named["comparison"])
	if comparison == "" {
		comparison = "eq"
	}
	rows, err = filterManagedFiles(rows, sortBy, comparison, normalized)
	if err != nil {
		panic(err)
	}
	if transactionListTruthy(normalized.named["reverse"]) {
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
	}
	pagination, err := transactionListPaginationParameters(normalized.named)
	if err != nil {
		panic(err)
	}
	totalItems := len(rows)
	start := pagination.offset
	if start > totalItems {
		start = totalItems
	}
	end := start + pagination.pageSize
	if end > totalItems {
		end = totalItems
	}
	items := make([]any, end-start)
	receipts := make(map[string]any)
	pageRows := rows[start:end]
	if len(pageRows) > 0 {
		claimIDs := make([]string, len(pageRows))
		for index := range pageRows {
			claimIDs[index] = pageRows[index].ClaimID
		}
		purchases, purchaseErr := ledger.GetPurchases(normalized.ctx, walletpkg.PurchaseListOptions{
			Query:    ledgerdb.TransactionQuery{PurchasedClaimIDs: claimIDs},
			Accounts: wallet.Accounts, Wallet: wallet,
		})
		if purchaseErr != nil {
			panic(purchaseErr)
		}
		for _, purchase := range purchases {
			claimID, claimErr := walletpkg.TransactionPurchasedClaimID(purchase)
			if claimErr != nil {
				panic(claimErr)
			}
			encoded, encodeErr := ledger.LegacyTransactionOutputJSONWithOptions(
				purchase, walletpkg.LegacyTransactionJSONOptions{},
			)
			if encodeErr != nil {
				panic(encodeErr)
			}
			receipts[claimID] = encoded
		}
	}
	for index, row := range pageRows {
		encoded, encodeErr := rpcServer.encodeManagedFile(ledger, row, receipts[row.ClaimID])
		if encodeErr != nil {
			panic(encodeErr)
		}
		items[index] = encoded
	}
	sendResultResponse(w, map[string]any{
		"items": items, "total_items": totalItems,
		"total_pages": transactionListPythonTotalPages(
			int64(totalItems), pagination.pageSizeNumber,
		),
		"page": pagination.wirePage, "page_size": pagination.wirePageSize,
	})
}

func filterManagedFiles(
	rows []databasepkg.ManagedFileRow,
	sortBy, comparison string,
	params normalizedRPCParams,
) ([]databasepkg.ManagedFileRow, error) {
	if _, valid := fileListFilterFields[sortBy]; !valid {
		return nil, fmt.Errorf("'%s' is not a valid field to sort by", sortBy)
	}
	if comparison != "eq" && comparison != "ne" && comparison != "g" &&
		comparison != "l" && comparison != "ge" && comparison != "le" {
		return nil, fmt.Errorf("'%s' is not a valid comparison", comparison)
	}
	filters := make(map[string]any)
	for name, value := range params.named {
		if _, fixed := fileListFixedParameters[name]; fixed || name == "full_status" {
			continue
		}
		if value == nil {
			continue
		}
		if _, valid := fileListFilterFields[name]; !valid {
			return nil, fmt.Errorf("'%s' is not a valid search operation", name)
		}
		filters[name] = value
	}
	filtered := make([]databasepkg.ManagedFileRow, 0, len(rows))
	for _, row := range rows {
		matches := true
		for name, wanted := range filters {
			actual := managedFileFilterValue(row, name)
			if list, ok := wanted.([]any); ok &&
				(name == "claim_id" || name == "outpoint" || name == "channel_claim_id") {
				matches = fileListContains(list, actual)
			} else {
				matches, _ = compareManagedFileValues(actual, wanted, comparison)
			}
			if !matches {
				break
			}
		}
		if matches {
			filtered = append(filtered, row)
		}
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		less, _ := compareManagedFileValues(
			managedFileFilterValue(filtered[left], sortBy),
			managedFileFilterValue(filtered[right], sortBy), "l",
		)
		return less
	})
	return filtered, nil
}

func managedFileFilterValue(row databasepkg.ManagedFileRow, name string) any {
	fileName := row.SuggestedFileName
	if row.FileName != nil {
		fileName = *row.FileName
	}
	var downloadPath any
	if row.FileName != nil && row.DownloadDirectory != nil {
		downloadPath = *row.DownloadDirectory + "/" + *row.FileName
	}
	txid, nout := managedFileOutpoint(row.ClaimOutpoint)
	switch name {
	case "rowid":
		return row.RowID
	case "status":
		return row.Status
	case "file_name":
		return fileName
	case "added_on":
		return row.AddedOn
	case "download_path":
		return downloadPath
	case "claim_name":
		return row.ClaimName
	case "claim_height":
		return row.ClaimHeight
	case "claim_id":
		return row.ClaimID
	case "outpoint":
		return row.ClaimOutpoint
	case "txid":
		return txid
	case "nout":
		return nout
	case "channel_claim_id":
		return managedFileOptional(row.ChannelClaimID)
	case "channel_name":
		return managedFileOptional(row.ChannelName)
	case "completed":
		return managedFileWrittenBytes(row) >= row.TotalBytesLowerBound
	case "sd_hash":
		return row.SDHash
	case "stream_hash":
		return row.StreamHash
	case "blobs_remaining":
		return row.BlobsInStream - row.BlobsCompleted
	case "blobs_in_stream":
		return row.BlobsInStream
	case "uploading_to_reflector":
		return false
	case "is_fully_reflected":
		return row.FullyReflected
	default:
		return nil
	}
}

func compareManagedFileValues(left, right any, comparison string) (bool, error) {
	if leftNumber, ok := fileListNumber(left); ok {
		if rightNumber, rightOK := fileListNumber(right); rightOK {
			order := leftNumber.Cmp(rightNumber)
			return fileListCompareOrder(order, comparison), nil
		}
	}
	if comparison == "eq" || comparison == "ne" {
		equal := fmt.Sprint(left) == fmt.Sprint(right)
		if comparison == "ne" {
			return !equal, nil
		}
		return equal, nil
	}
	leftText, rightText := fmt.Sprint(left), fmt.Sprint(right)
	return fileListCompareOrder(strings.Compare(leftText, rightText), comparison), nil
}

func fileListCompareOrder(order int, comparison string) bool {
	switch comparison {
	case "eq":
		return order == 0
	case "ne":
		return order != 0
	case "g":
		return order > 0
	case "l":
		return order < 0
	case "ge":
		return order >= 0
	case "le":
		return order <= 0
	default:
		return false
	}
}

func fileListNumber(value any) (*big.Rat, bool) {
	switch typed := value.(type) {
	case int:
		return new(big.Rat).SetInt64(int64(typed)), true
	case int64:
		return new(big.Rat).SetInt64(typed), true
	case json.Number:
		number, ok := new(big.Rat).SetString(typed.String())
		return number, ok
	case float64:
		number := new(big.Rat)
		number.SetFloat64(typed)
		return number, true
	default:
		return nil, false
	}
}

func (rpcServer *RPCServer) encodeManagedFile(
	ledger *walletpkg.Ledger, row databasepkg.ManagedFileRow, purchaseReceipt any,
) (map[string]any, error) {
	written := managedFileWrittenBytes(row)
	outputExists := written >= 0
	if written < 0 {
		written = 0
	}
	txid, nout := managedFileOutpoint(row.ClaimOutpoint)
	metadata, err := managedFileMetadata(row.SerializedMetadataHex)
	if err != nil {
		return nil, err
	}
	var contentFee any
	if row.ContentFeeHex != nil {
		raw, decodeErr := hex.DecodeString(*row.ContentFeeHex)
		if decodeErr != nil {
			return nil, decodeErr
		}
		transaction, parseErr := walletpkg.ParseTransaction(raw)
		if parseErr != nil {
			return nil, parseErr
		}
		contentFee, parseErr = ledger.LegacyTransactionJSON(transaction)
		if parseErr != nil {
			return nil, parseErr
		}
	}
	streamingServer := "localhost:5280"
	if value, exists := rpcServer.settings.Get("streaming_server"); exists {
		if configured, ok := value.(string); ok {
			streamingServer = configured
		}
	}
	streamingHost, streamingPort, splitErr := net.SplitHostPort(streamingServer)
	if splitErr != nil {
		return nil, splitErr
	}
	streamingURL := "http://" + net.JoinHostPort(streamingHost, streamingPort) + "/stream/" + row.SDHash
	confirmations := row.ClaimHeight
	if row.ClaimHeight > 0 {
		confirmations = int64(ledger.Headers.Height()+1) - row.ClaimHeight
	}
	var timestamp any
	if estimated, ok := ledger.Headers.EstimatedTimestamp(int(row.ClaimHeight), true); ok {
		timestamp = estimated
	}
	encoded := map[string]any{
		"streaming_url": streamingURL, "completed": written >= row.TotalBytesLowerBound,
		"file_name": nil, "download_directory": nil, "download_path": nil,
		"points_paid": 0.0, "stopped": row.Status != "running",
		"stream_hash": row.StreamHash, "stream_name": row.StreamName,
		"suggested_file_name": row.SuggestedFileName, "sd_hash": row.SDHash,
		"mime_type": blob.GuessMIME(row.SuggestedFileName, row.StreamName), "key": row.Key,
		"total_bytes_lower_bound": row.TotalBytesLowerBound,
		"total_bytes":             row.TotalBytesUpperBound, "written_bytes": written,
		"blobs_completed": row.BlobsCompleted, "blobs_in_stream": row.BlobsInStream,
		"blobs_remaining": row.BlobsInStream - row.BlobsCompleted, "status": row.Status,
		"claim_id": row.ClaimID, "txid": txid, "nout": nout, "outpoint": row.ClaimOutpoint,
		"metadata": metadata, "protobuf": row.SerializedMetadataHex,
		"channel_claim_id": managedFileOptional(row.ChannelClaimID),
		"channel_name":     managedFileOptional(row.ChannelName), "claim_name": row.ClaimName,
		"content_fee": contentFee, "purchase_receipt": purchaseReceipt, "added_on": row.AddedOn,
		"height": row.ClaimHeight, "confirmations": confirmations, "timestamp": timestamp,
		"is_fully_reflected": row.FullyReflected, "reflector_progress": 0,
		"uploading_to_reflector": false,
	}
	if outputExists && row.FileName != nil && row.DownloadDirectory != nil {
		encoded["file_name"] = *row.FileName
		encoded["download_directory"] = *row.DownloadDirectory
		encoded["download_path"] = *row.DownloadDirectory + "/" + *row.FileName
	}
	return encoded, nil
}

func managedFileWrittenBytes(row databasepkg.ManagedFileRow) int64 {
	if row.FileName == nil || row.DownloadDirectory == nil {
		return -1
	}
	info, err := os.Stat(*row.DownloadDirectory + "/" + *row.FileName)
	if err != nil || info.IsDir() {
		return -1
	}
	return info.Size()
}

func managedFileMetadata(serialized string) (any, error) {
	decoded, err := hex.DecodeString(serialized)
	if err != nil {
		return nil, err
	}
	value, err := walletpkg.DecodeClaimValue(decoded)
	if err != nil {
		value, err = walletpkg.DecodeClaimValue(append([]byte{0}, decoded...))
	}
	if err != nil {
		return nil, err
	}
	return value.Value, nil
}

func managedFileOutpoint(outpoint string) (string, int64) {
	txid, position, found := strings.Cut(outpoint, ":")
	if !found {
		return outpoint, 0
	}
	nout, _ := strconv.ParseInt(position, 10, 64)
	return txid, nout
}

func managedFileOptional(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func fileListContains(values []any, wanted any) bool {
	for _, value := range values {
		if equal, _ := compareManagedFileValues(wanted, value, "eq"); equal {
			return true
		}
	}
	return false
}

func fileListOptionalText(value any) string {
	text, _ := value.(string)
	return text
}
