package rpc

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
)

func CreateServer() http.Server {
	rpcServeMux := http.NewServeMux()
	rpcServeMux.HandleFunc("/", handleJSONRPC)

	return http.Server{Handler: rpcServeMux}
}

func StartServer(rpcServer http.Server, port int) {
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil && err != http.ErrServerClosed {
		fmt.Println("Error when starting listening.")
	}

	err = rpcServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		fmt.Println("Error when starting RPC server.")
	}
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
		var message any

		err := json.NewDecoder(req.Body).Decode(&message)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			sendErrorResponse(w, -32700, "Cannot parse invalid JSON data.")
			return
		}

		_, okBatch := message.([]map[string]any)
		if okBatch {
			w.WriteHeader(http.StatusBadRequest)
			sendErrorResponse(w, -32700, "Batches are not supported")
			return
		}

		_, ok := message.(map[string]any)
		if ok {
			handleJSONRPCMessage(w, message.(map[string]any))
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		sendErrorResponse(w, -32700, "JSON must have an array or object as root.")
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
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountBalance(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountDeposit(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountFund(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountMaxAddressGap(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountRemove(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressIsMine(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressUnused(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageBlobAnnounce(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageBlobClean(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobDelete(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflectAll(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageChannelAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelSign(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageClaimList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageClaimSearch(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageCollectionAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionResolve(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageCollectionUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageFfmpegFind(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileDelete(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileReflect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSave(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSetStatus(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageGet(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessagePeerList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessagePeerPing(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessagePreferenceGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePreferenceSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePublish(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageResolve(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageRoutingTableGet(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageSettingsClear(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsGet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsSet(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageStatus(w http.ResponseWriter, params any) {
	// Relaxed
	sendResultResponse(w, map[string]any{})
}

func handleJSONRPCMessageStop(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageStreamAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamCostEstimate(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageStreamCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamRepost(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamUpdate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportSum(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncApply(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncHash(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTracemallocDisable(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTracemallocEnable(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTracemallocTop(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageTransactionList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTransactionShow(w http.ResponseWriter, params any) {
	// Relaxed
}

func handleJSONRPCMessageTxoList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoPlot(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSpend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSum(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoRelease(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageVersion(w http.ResponseWriter, params any) {
	// Relaxed
	info, _ := debug.ReadBuildInfo()

	sendResultResponse(w, map[string]any{
		"build":           nil,
		"lbrynet_version": nil,
		"os_release":      nil,
		"os_system":       nil,
		"platform":        nil,
		"processor":       nil,
		"python_version":  nil,
		"version":         info.Main.Version,
	})
}

func handleJSONRPCMessageWalletAdd(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletBalance(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletCreate(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletDecrypt(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletEncrypt(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletExport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletImport(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletLock(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletReconnect(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletRemove(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletSend(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletStatus(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletUnlock(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}
