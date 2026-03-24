package rpc

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"runtime/debug"
	"strings"
)

func StartServer() {
	http.HandleFunc("/", handleJSONRPC)
	http.ListenAndServe(":5279", nil)
}

func sendResultResponse(w http.ResponseWriter, result any) {
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"result":  result,
	})
}

func sendErrorResponse(w http.ResponseWriter, code int, message string) {
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func handleJSONRPC(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if strings.EqualFold(req.Method, "POST") {
		var messageBatch []map[string]any
		var message map[string]any

		body, _ := ioutil.ReadAll(req.Body)

		errBatch := json.Unmarshal(body, &messageBatch)
		err := json.Unmarshal(body, &message)

		if errBatch != nil && err != nil {
			w.WriteHeader(http.StatusBadRequest)
			sendErrorResponse(w, -32700, "Cannot parse invalid JSON data.")
			return
		}
		if messageBatch != nil {
			w.WriteHeader(http.StatusBadRequest)
			sendErrorResponse(w, -32700, "Batches are not supported")
			return
		}

		handleJSONRPCMessage(w, message)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	sendErrorResponse(w, -32700, "HTTP method not allowed.")
}

var handlers = map[string]func(http.ResponseWriter, any){
	"account_add":             handleJSONRPCMessageAccountAdd,
	"account_balance":         handleJSONRPCMessageAccountBalance,
	"account_create":          handleJSONRPCMessageAccountCreate,
	"account_deposit":         handleJSONRPCMessageAccountDeposit,
	"account_fund":            handleJSONRPCMessageAccountFund,
	"account_list":            handleJSONRPCMessageAccountList,
	"account_max_address_gap": handleJSONRPCMessageAccountMaxAddressGap,
	"account_remove":          handleJSONRPCMessageAccountRemove,
	"account_send":            handleJSONRPCMessageAccountSend,
	"account_set":             handleJSONRPCMessageAccountSet,
	"address_is_mine":         handleJSONRPCMessageAddressIsMine,
	"address_list":            handleJSONRPCMessageAddressList,
	"address_unused":          handleJSONRPCMessageAddressUnused,
	"blob_announce":           handleJSONRPCMessageBlobAnnounce,
	"blob_clean":              handleJSONRPCMessageBlobClean,
	"blob_delete":             handleJSONRPCMessageBlobDelete,
	"blob_get":                handleJSONRPCMessageBlobGet,
	"blob_list":               handleJSONRPCMessageBlobList,
	"blob_reflect":            handleJSONRPCMessageBlobReflect,
	"blob_reflect_all":        handleJSONRPCMessageBlobReflectAll,
	"channel_abandon":         handleJSONRPCMessageChannelAbandon,
	"channel_create":          handleJSONRPCMessageChannelCreate,
	"channel_list":            handleJSONRPCMessageChannelList,
	"channel_sign":            handleJSONRPCMessageChannelSign,
	"channel_update":          handleJSONRPCMessageChannelUpdate,
	"claim_list":              handleJSONRPCMessageClaimList,
	"claim_search":            handleJSONRPCMessageClaimSearch,
	"collection_abandon":      handleJSONRPCMessageCollectionAbandon,
	"collection_create":       handleJSONRPCMessageCollectionCreate,
	"collection_list":         handleJSONRPCMessageCollectionList,
	"collection_resolve":      handleJSONRPCMessageCollectionResolve,
	"collection_update":       handleJSONRPCMessageCollectionUpdate,
	"ffmpeg_find":             handleJSONRPCMessageFfmpegFind,
	"file_delete":             handleJSONRPCMessageFileDelete,
	"file_list":               handleJSONRPCMessageFileList,
	"file_reflect":            handleJSONRPCMessageFileReflect,
	"file_save":               handleJSONRPCMessageFileSave,
	"file_set_status":         handleJSONRPCMessageFileSetStatus,
	"get":                     handleJSONRPCMessageGet,
	"peer_list":               handleJSONRPCMessagePeerList,
	"peer_ping":               handleJSONRPCMessagePeerPing,
	"preference_get":          handleJSONRPCMessagePreferenceGet,
	"preference_set":          handleJSONRPCMessagePreferenceSet,
	"publish":                 handleJSONRPCMessagePublish,
	"purchase_create":         handleJSONRPCMessagePurchaseCreate,
	"purchase_list":           handleJSONRPCMessagePurchaseList,
	"resolve":                 handleJSONRPCMessageResolve,
	"routing_table_get":       handleJSONRPCMessageRoutingTableGet,
	"settings_clear":          handleJSONRPCMessageSettingsClear,
	"settings_get":            handleJSONRPCMessageSettingsGet,
	"settings_set":            handleJSONRPCMessageSettingsSet,
	"status":                  handleJSONRPCMessageStatus,
	"stop":                    handleJSONRPCMessageStop,
	"stream_abandon":          handleJSONRPCMessageStreamAbandon,
	"stream_cost_estimate":    handleJSONRPCMessageStreamCostEstimate,
	"stream_create":           handleJSONRPCMessageStreamCreate,
	"stream_list":             handleJSONRPCMessageStreamList,
	"stream_repost":           handleJSONRPCMessageStreamRepost,
	"stream_update":           handleJSONRPCMessageStreamUpdate,
	"support_abandon":         handleJSONRPCMessageSupportAbandon,
	"support_create":          handleJSONRPCMessageSupportCreate,
	"support_list":            handleJSONRPCMessageSupportList,
	"support_sum":             handleJSONRPCMessageSupportSum,
	"sync_apply":              handleJSONRPCMessageSyncApply,
	"sync_hash":               handleJSONRPCMessageSyncHash,
	"tracemalloc_disable":     handleJSONRPCMessageTracemallocDisable,
	"tracemalloc_enable":      handleJSONRPCMessageTracemallocEnable,
	"tracemalloc_top":         handleJSONRPCMessageTracemallocTop,
	"transaction_list":        handleJSONRPCMessageTransactionList,
	"transaction_show":        handleJSONRPCMessageTransactionShow,
	"txo_list":                handleJSONRPCMessageTxoList,
	"txo_plot":                handleJSONRPCMessageTxoPlot,
	"txo_spend":               handleJSONRPCMessageTxoSpend,
	"txo_sum":                 handleJSONRPCMessageTxoSum,
	"utxo_list":               handleJSONRPCMessageUtxoList,
	"utxo_release":            handleJSONRPCMessageUtxoRelease,
	"version":                 handleJSONRPCMessageVersion,
	"wallet_add":              handleJSONRPCMessageWalletAdd,
	"wallet_balance":          handleJSONRPCMessageWalletBalance,
	"wallet_create":           handleJSONRPCMessageWalletCreate,
	"wallet_decrypt":          handleJSONRPCMessageWalletDecrypt,
	"wallet_encrypt":          handleJSONRPCMessageWalletEncrypt,
	"wallet_export":           handleJSONRPCMessageWalletExport,
	"wallet_import":           handleJSONRPCMessageWalletImport,
	"wallet_list":             handleJSONRPCMessageWalletList,
	"wallet_lock":             handleJSONRPCMessageWalletLock,
	"wallet_reconnect":        handleJSONRPCMessageWalletReconnect,
	"wallet_remove":           handleJSONRPCMessageWalletRemove,
	"wallet_send":             handleJSONRPCMessageWalletSend,
	"wallet_status":           handleJSONRPCMessageWalletStatus,
	"wallet_unlock":           handleJSONRPCMessageWalletUnlock,
}

func handleJSONRPCMessage(w http.ResponseWriter, message map[string]any) {
	method, existsMethod := message["method"].(string)
	params, existsParams := message["params"]

	if !existsMethod {
		w.WriteHeader(http.StatusBadRequest)
		sendErrorResponse(w, -32600, "Method property is missing.")
		return
	}

	handler, exists := handlers[method]
	if exists {
		if existsParams {
			handler(w, params)
			return
		}
		handler(w, nil)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	sendErrorResponse(w, -32601, "Unknown JSON-RPC method.")
}

func handleJSONRPCMessageAccountAdd(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "account_add not implemented"})
}

func handleJSONRPCMessageAccountBalance(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"available": "0.0",
		"reserved":  "0.0",
		"reserved_subtotals": map[string]any{
			"claims":   "0.0",
			"supports": "0.0",
			"tips":     "0.0",
		},
		"total": "0.0",
	})
}

func handleJSONRPCMessageAccountCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "account_create not implemented"})
}

func handleJSONRPCMessageAccountDeposit(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "account_deposit not implemented"})
}

func handleJSONRPCMessageAccountFund(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "account_fund not implemented"})
}

func handleJSONRPCMessageAccountList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageAccountMaxAddressGap(w http.ResponseWriter, params any) {
	sendResultResponse(w, 1)
}

func handleJSONRPCMessageAccountRemove(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageAccountSend(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "account_send not implemented"})
}

func handleJSONRPCMessageAccountSet(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageAddressIsMine(w http.ResponseWriter, params any) {
	sendResultResponse(w, false)
}

func handleJSONRPCMessageAddressList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageAddressUnused(w http.ResponseWriter, params any) {
	sendResultResponse(w, "")
}

func handleJSONRPCMessageBlobAnnounce(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageBlobClean(w http.ResponseWriter, params any) {
	sendResultResponse(w, 0)
}

func handleJSONRPCMessageBlobDelete(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageBlobGet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "blob_get not implemented"})
}

func handleJSONRPCMessageBlobList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageBlobReflect(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageBlobReflectAll(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageChannelAbandon(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "channel_abandon not implemented"})
}

func handleJSONRPCMessageChannelCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "channel_create not implemented"})
}

func handleJSONRPCMessageChannelList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageChannelSign(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "channel_sign not implemented"})
}

func handleJSONRPCMessageChannelUpdate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "channel_update not implemented"})
}

func handleJSONRPCMessageClaimList(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"page":        1,
		"page_size":   20,
		"total_pages": 0,
		"total_items": 0,
		"items":       []any{},
	})
}

func handleJSONRPCMessageClaimSearch(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"page":        1,
		"page_size":   20,
		"total_pages": 0,
		"total_items": 0,
		"items":       []any{},
	})
}

func handleJSONRPCMessageCollectionAbandon(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "collection_abandon not implemented"})
}

func handleJSONRPCMessageCollectionCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "collection_create not implemented"})
}

func handleJSONRPCMessageCollectionList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageCollectionResolve(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "collection_resolve not implemented"})
}

func handleJSONRPCMessageCollectionUpdate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "collection_update not implemented"})
}

func handleJSONRPCMessageFfmpegFind(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"available":            false,
		"which":                "",
		"analyze_audio_volume": false,
	})
}

func handleJSONRPCMessageFileDelete(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "file_delete not implemented"})
}

func handleJSONRPCMessageFileList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageFileReflect(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "file_reflect not implemented"})
}

func handleJSONRPCMessageFileSave(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageFileSetStatus(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageGet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "get not implemented"})
}

func handleJSONRPCMessagePeerList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessagePeerPing(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "peer_ping not implemented"})
}

func handleJSONRPCMessagePreferenceGet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{})
}

func handleJSONRPCMessagePreferenceSet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "preference_set not implemented"})
}

func handleJSONRPCMessagePublish(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "publish not implemented"})
}

func handleJSONRPCMessagePurchaseCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "purchase_create not implemented"})
}

func handleJSONRPCMessagePurchaseList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageResolve(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "resolve not implemented"})
}

func handleJSONRPCMessageRoutingTableGet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "routing_table_get not implemented"})
}

func handleJSONRPCMessageSettingsClear(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "settings_clear not implemented"})
}

func handleJSONRPCMessageSettingsGet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"share_usage_data":     true,
		"download_timeout":     60,
		"max_key_fee":          map[string]any{},
		"search_timeout":       10,
		"max_search_results":   25,
		"filter_bluray":        false,
		"currency":             "USD",
		"exchange_rate_fiat":   "1.0",
		"run_reflector_server": false,
		"audio_bitrate":        128,
		"video_bitrate":        1000,
		"audio_normalize":      false,
		"data_rate":            0.5,
		"max_upload_rate":      0,
		"max_download_rate":    0,
		"upload_log":           true,
		"upload_log_filename":  "",
		"peer_port":            3333,
		"dht_node_port":        4444,
		"downloader_limit":     32,
		"uploader_limit":       64,
		"ui_theme":             "light",
		"language":             "en",
		"background_va":        false,
		"ffmpeg_path":          "",
		"bid_influence":        1.0,
		"cache_time":           60,
		"romfs_root":           "",
		"romfs_level":          0,
		"prometheus_port":      0,
		"allowed_origin":       "",
		"streaming_get":        true,
		"streaming_port":       0,
		"streaming_hosts":      []any{},
	})
}

func handleJSONRPCMessageSettingsSet(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "settings_set not implemented"})
}

func handleJSONRPCMessageStatus(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{},
	})
}

func handleJSONRPCMessageStop(w http.ResponseWriter, params any) {
	sendResultResponse(w, "Shutting down")
}

func handleJSONRPCMessageStreamAbandon(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "stream_abandon not implemented"})
}

func handleJSONRPCMessageStreamCostEstimate(w http.ResponseWriter, params any) {
	sendResultResponse(w, 0.0)
}

func handleJSONRPCMessageStreamCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "stream_create not implemented"})
}

func handleJSONRPCMessageStreamList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageStreamRepost(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "stream_repost not implemented"})
}

func handleJSONRPCMessageStreamUpdate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "stream_update not implemented"})
}

func handleJSONRPCMessageSupportAbandon(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "support_abandon not implemented"})
}

func handleJSONRPCMessageSupportCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "support_create not implemented"})
}

func handleJSONRPCMessageSupportList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageSupportSum(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"support_amount": "0.0"})
}

func handleJSONRPCMessageSyncApply(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "sync_apply not implemented"})
}

func handleJSONRPCMessageSyncHash(w http.ResponseWriter, params any) {
	sendResultResponse(w, "")
}

func handleJSONRPCMessageTracemallocDisable(w http.ResponseWriter, params any) {
	sendResultResponse(w, nil)
}

func handleJSONRPCMessageTracemallocEnable(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"tracing": true})
}

func handleJSONRPCMessageTracemallocTop(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageTransactionList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageTransactionShow(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "transaction_show not implemented"})
}

func handleJSONRPCMessageTxoList(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"page":        1,
		"page_size":   20,
		"total_pages": 0,
		"total_items": 0,
		"items":       []any{},
	})
}

func handleJSONRPCMessageTxoPlot(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "txo_plot not implemented"})
}

func handleJSONRPCMessageTxoSpend(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "txo_spend not implemented"})
}

func handleJSONRPCMessageTxoSum(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"amount":   "0.0",
		"currency": "LBC",
	})
}

func handleJSONRPCMessageUtxoList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageUtxoRelease(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageVersion(w http.ResponseWriter, params any) {
	info, _ := debug.ReadBuildInfo()

	sendResultResponse(w, map[string]any{
		"jsonrpc": "2.0",
		"result": map[string]any{
			"build":           nil,
			"lbrynet_version": nil,
			"os_release":      nil,
			"os_system":       nil,
			"platform":        nil,
			"processor":       nil,
			"python_version":  nil,
			"version":         info.Main.Version,
		},
	})
}

func handleJSONRPCMessageWalletAdd(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "wallet_add not implemented"})
}

func handleJSONRPCMessageWalletBalance(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"available": "0.0",
		"reserved":  "0.0",
		"reserved_subtotals": map[string]any{
			"claims":   "0.0",
			"supports": "0.0",
			"tips":     "0.0",
		},
		"total": "0.0",
	})
}

func handleJSONRPCMessageWalletCreate(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "wallet_create not implemented"})
}

func handleJSONRPCMessageWalletDecrypt(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletEncrypt(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletExport(w http.ResponseWriter, params any) {
	sendResultResponse(w, "")
}

func handleJSONRPCMessageWalletImport(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletList(w http.ResponseWriter, params any) {
	sendResultResponse(w, []any{})
}

func handleJSONRPCMessageWalletLock(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletReconnect(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletRemove(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}

func handleJSONRPCMessageWalletSend(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{"error": "wallet_send not implemented"})
}

func handleJSONRPCMessageWalletStatus(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"connected":         false,
		"encrypted":         false,
		"locked":            true,
		"items":             0,
		"keys":              0,
		"height":            0,
		"wallet_db_version": 0,
	})
}

func handleJSONRPCMessageWalletUnlock(w http.ResponseWriter, params any) {
	sendResultResponse(w, true)
}
