package rpc

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"lbry/daemon/auth"
	"lbry/daemon/blob"
	"lbry/daemon/dht"
	"lbry/daemon/settings"
	"lbry/daemon/wallet"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// const TMP_HUB_HOSTNAME = "s1.lbry.network"
const TMP_HUB_HOSTNAME = "hub.lbry.grin.io"

type RPCServer struct {
	authManager   auth.AuthManager
	blobManager   *blob.BlobManager
	configuration settings.Configuration
	dhtNode       *dht.Node
	httpServer    http.Server
	walletManager wallet.WalletManager
	hub           wallet.Hub
}

func NewRPCServer(configuration settings.Configuration, authManager auth.AuthManager, blobManager *blob.BlobManager, dhtNode *dht.Node, walletManager wallet.WalletManager) *RPCServer {
	rpcServeMux := http.NewServeMux()

	server := &RPCServer{
		authManager:   authManager,
		blobManager:   blobManager,
		configuration: configuration,
		dhtNode:       dhtNode,
		httpServer:    http.Server{Handler: rpcServeMux},
		walletManager: walletManager,
		hub:           hubClient{},
	}

	rpcServeMux.HandleFunc("/", server.handleJSONRPC)

	return server
}

func (rpcServer *RPCServer) Start(listener net.Listener) {
	err := rpcServer.httpServer.Serve(listener)
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

func (rpcServer *RPCServer) handleJSONRPC(w http.ResponseWriter, req *http.Request) {
	info, _ := debug.ReadBuildInfo()

	// CORS is open, as in lbry-sdk. Every client, local or remote, has to authenticate: the protection is
	// the credentials on the request, not where it came from.
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server", "LBRYd/"+info.Main.Version)

	if strings.EqualFold(req.Method, "OPTIONS") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if strings.EqualFold(req.Method, "POST") {
		var message any

		jsonDecoder := json.NewDecoder(req.Body)
		jsonDecoder.UseNumber()
		err := jsonDecoder.Decode(&message)
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
			rpcServer.handleJSONRPCMessage(w, req, message.(map[string]any))
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		sendErrorResponse(w, -32700, "JSON must have an array or object as root.")
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	sendErrorResponse(w, -32700, "HTTP method not allowed.")
}

var handlers = map[string]func(*RPCServer, http.ResponseWriter, *http.Request, any){
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
	"channel_export":          handleJSONRPCMessageChannelExport,
	"channel_import":          handleJSONRPCMessageChannelImport,
	"channel_list":            handleJSONRPCMessageChannelList,
	"channel_new":             handleJSONRPCMessageChannelNew,
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

	// Deprecated RPCs
	"tracemalloc_disable": handleJSONRPCMessageTracemallocDisable,
	"tracemalloc_enable":  handleJSONRPCMessageTracemallocEnable,
	"tracemalloc_top":     handleJSONRPCMessageTracemallocTop,
}

func (rpcServer *RPCServer) handleJSONRPCMessage(w http.ResponseWriter, req *http.Request, message map[string]any) {
	method, existsMethod := message["method"].(string)
	params, existsParams := message["params"]

	if !existsMethod {
		w.WriteHeader(http.StatusBadRequest)
		sendErrorResponse(w, -32600, "Method property is missing.")
		return
	}

	handler, exists := handlers[method]
	if exists {
		fmt.Printf("Receiving '%s' method\n", method)
		if existsParams {
			handler(rpcServer, w, req, params)
			return
		}
		handler(rpcServer, w, req, nil)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	sendErrorResponse(w, -32601, "Unknown JSON-RPC method.")
}

func handleJSONRPCMessageAccountAdd(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountBalance(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountDeposit(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountFund(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}

		showSeed, err := GetBooleanParameter(paramsMap, "show_seed", false, false)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		if HasParameter(paramsMap, "account_id") {
			accountID, err := GetStringParameter(paramsMap, "account_id", true, "")
			if err != nil {
				sendErrorResponse(w, 400, err.Error())
				return
			}

			accountObj := walletObj.GetAccountByID(accountID)
			if accountObj == nil {
				sendErrorResponse(w, 400, "Account does not exist.")
				return
			}

			sendResultResponse(w, Paginate([]any{accountDetails(accountObj, showSeed)}, 1, 1).ToMap())
			return
		}

		page, err := GetIntegerParameter(paramsMap, "page", false, 1)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		pageSize, err := GetIntegerParameter(paramsMap, "page_size", false, 20)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		accountList := []any{}
		for i, account := range walletObj.Accounts {
			details := accountDetails(account, showSeed)
			details["is_default"] = i == 0
			accountList = append(accountList, details)
		}

		sendResultResponse(w, Paginate(accountList, uint(max(page, 0)), uint(max(pageSize, 0))).ToMap())
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountMaxAddressGap(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountRemove(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountSend(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountSet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}

		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		accountID, err := GetStringParameter(paramsMap, "account_id", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}

		accountObj := walletObj.GetAccountByID(accountID)
		if accountObj == nil {
			sendErrorResponse(w, 400, "Account does not exist.")
			return
		}

		madeChanges := false

		if accountObj.AddressGenerator.Name == wallet.ADDRESS_GENERATOR_NAME_DETERMINISTIC_CHAIN {
			if HasParameter(paramsMap, "change_gap") {
				changeGap, err := GetIntegerParameter(paramsMap, "change_gap", true, -1)
				if err != nil {
					sendErrorResponse(w, 400, err.Error())
					return
				}
				accountObj.AddressGenerator.Change.Gap = uint64(changeGap)
				madeChanges = true
			}

			if HasParameter(paramsMap, "change_max_uses") {
				changeMaxUses, err := GetIntegerParameter(paramsMap, "change_max_uses", true, -1)
				if err != nil {
					sendErrorResponse(w, 400, err.Error())
					return
				}
				accountObj.AddressGenerator.Change.MaximumUsesPerAddress = uint64(changeMaxUses)
				madeChanges = true
			}

			if HasParameter(paramsMap, "receiving_gap") {
				receivingGap, err := GetIntegerParameter(paramsMap, "receiving_gap", true, -1)
				if err != nil {
					sendErrorResponse(w, 400, err.Error())
					return
				}
				accountObj.AddressGenerator.Receiving.Gap = uint64(receivingGap)
				madeChanges = true
			}

			if HasParameter(paramsMap, "receiving_max_uses") {
				receivingMaxUses, err := GetIntegerParameter(paramsMap, "receiving_max_uses", true, -1)
				if err != nil {
					sendErrorResponse(w, 400, err.Error())
					return
				}
				accountObj.AddressGenerator.Receiving.MaximumUsesPerAddress = uint64(receivingMaxUses)
				madeChanges = true
			}
		}

		if HasParameter(paramsMap, "new_name") {
			newName, err := GetStringParameter(paramsMap, "new_name", true, "")
			if err != nil {
				sendErrorResponse(w, 400, err.Error())
				return
			}
			if accountObj.Name != newName {
				accountObj.Name = newName
				madeChanges = true
			}
		}

		defaultBool, err := GetBooleanParameter(paramsMap, "default", false, false)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		if defaultBool && accountObj != walletObj.GetDefaultAccount() {
			for index, account := range walletObj.Accounts {
				if account != accountObj {
					continue

				}
				copy(walletObj.Accounts[1:index+1], walletObj.Accounts[:index])
				walletObj.Accounts[0] = accountObj
				madeChanges = true
				break
			}

		}

		if madeChanges {
			accountObj.ModifiedOn = time.Now().Unix()

			updateOk := rpcServer.walletManager.Update(user, walletObj)
			if !updateOk {
				sendErrorResponse(w, 400, "Failed updating wallet.")
				return
			}
		}

		details := accountDetails(accountObj, true)

		result := map[string]any{
			"id":                details["id"],
			"name":              details["name"],
			"ledger":            details["ledger"],
			"seed":              accountObj.Seed,
			"encrypted":         accountObj.Locked,
			"private_key":       accountObj.PrivateKey,
			"public_key":        accountObj.PublicKey,
			"address_generator": details["address_generator"],
			"modified_on":       accountObj.ModifiedOn,
			"is_default":        accountObj == walletObj.GetDefaultAccount(),
		}
		sendResultResponse(w, result)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressIsMine(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressUnused(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobAnnounce(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req) // Relaxed // TODO: Maybe allow guests too?
	if authenticated {
		_ = user

		sendErrorResponse(w, 501, "NOT IMPLEMENTED")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobClean(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		if rpcServer.blobManager == nil {
			sendErrorResponse(w, 500, "Blob Manager component is not running.")
			return
		}

		resp := rpcServer.blobManager.Clean()

		sendResultResponse(w, resp)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobDelete(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobGet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobReflect(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		if rpcServer.blobManager == nil {
			sendErrorResponse(w, 500, "Blob Manager component is not running.")
			return
		}
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		blobHashes, err := GetStringArrayParameter(paramsMap, "blob_hashes", true, []string{})
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		resp := rpcServer.blobManager.Reflect(blobHashes)

		sendResultResponse(w, resp)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobReflectAll(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		if rpcServer.blobManager == nil {
			sendErrorResponse(w, 500, "Blob Manager component is not running.")
			return
		}

		resp := rpcServer.blobManager.ReflectAll()

		sendResultResponse(w, resp)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelAbandon(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelExport(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelImport(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelNew(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Command 'channel_new' has been deprecated. Use 'channel_create' for this.")
}

func handleJSONRPCMessageChannelSign(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelUpdate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageClaimList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

// Hub connection timeouts. Variables so that tests can shorten them.
var (
	HubDialTimeout  = 3 * time.Second
	HubReplyTimeout = 15 * time.Second
)

// SendJSON sends one JSON-RPC request to a hub and returns its reply. The host is resolved and every
// address is tried in turn, so one address that is dead or silent does not stop the request. It returns an
// error when no address answers with a JSON object.
func SendJSON(host string, port int, req any) (map[string]any, error) {
	addresses, err := net.DefaultResolver.LookupHost(context.Background(), host)
	if err != nil {
		return nil, err
	}
	for i, address := range addresses {
		addresses[i] = net.JoinHostPort(address, strconv.Itoa(port))
	}
	return SendJSONToAddresses(addresses, req)
}

// SendJSONToAddresses tries each address ("host:port") and returns the first reply. An address that failed
// recently is tried last (see hubHealth), so a dead one costs one timeout, not one per call.
func SendJSONToAddresses(addresses []string, req any) (map[string]any, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	errs := []error{}
	for _, address := range hubAddressHealth.order(addresses) {
		resp, err := sendJSONToAddress(address, data)
		if err == nil {
			hubAddressHealth.succeeded(address)
			return resp, nil
		}
		hubAddressHealth.fail(address)
		errs = append(errs, fmt.Errorf("%s: %w", address, err))
	}
	if len(errs) == 0 {
		return nil, errors.New("no hub address to try")
	}
	return nil, fmt.Errorf("no hub address answered: %w", errors.Join(errs...))
}

func sendJSONToAddress(address string, data []byte) (map[string]any, error) {
	conn, err := net.DialTimeout("tcp", address, HubDialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(HubReplyTimeout)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return nil, err
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		return nil, fmt.Errorf("unexpected reply: %w", err)
	}
	return resp, nil
}

func DecodeRawProto(b []byte) (map[int]any, error) {
	m := make(map[int]any)

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b) // num is protowire.Number
		if n < 0 {
			return nil, fmt.Errorf("invalid tag")
		}
		b = b[n:]

		var val any
		var consumed int

		switch typ {
		case protowire.VarintType:
			val, consumed = protowire.ConsumeVarint(b)
		case protowire.Fixed32Type:
			val, consumed = protowire.ConsumeFixed32(b)
		case protowire.Fixed64Type:
			val, consumed = protowire.ConsumeFixed64(b)
		case protowire.BytesType:
			val, consumed = protowire.ConsumeBytes(b)
			// Recursive decode for nested messages
			if sub, err := DecodeRawProto(val.([]byte)); err == nil && len(sub) > 0 {
				val = sub
			}
		default:
			return nil, fmt.Errorf("unsupported wire type %d for field %d", typ, num)
		}
		if consumed < 0 {
			return nil, fmt.Errorf("consume failed for field %d", num)
		}
		b = b[consumed:]

		// Handle repeating fields (multiple same field number)
		key := int(num) // <-- this fixes the compile error
		if existing, ok := m[key]; ok {
			if slice, ok := existing.([]any); ok {
				m[key] = append(slice, val)
			} else {
				m[key] = []any{existing, val}
			}
		} else {
			m[key] = val
		}
	}
	return m, nil
}

// claimSearchPaging returns the requested page and page size, defaulting to the SDK's 1 and 10.
func claimSearchPaging(params any) (int64, int64) {
	page, pageSize := int64(1), int64(10)
	if paramsMap, ok := params.(map[string]any); ok {
		if value, err := GetIntegerParameter(paramsMap, "page", false, 1); err == nil && value >= 1 {
			page = value
		}
		if value, err := GetIntegerParameter(paramsMap, "page_size", false, 10); err == nil && value >= 1 {
			pageSize = value
		}
	}
	return page, pageSize
}

func handleJSONRPCMessageClaimSearch(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	items, totalItems, okTotal, err := hubClaimSearch(params)
	if err != nil {
		sendErrorResponse(w, 502, err.Error())
		return
	}

	page, pageSize := claimSearchPaging(params)
	var totalItemsFloat float64 = 0
	if okTotal {
		totalItemsFloat = float64(totalItems)
	}

	sendResultResponse(w, map[string]any{
		"items":       items,
		"page":        page,
		"page_size":   pageSize,
		"total_items": totalItems,
		"total_pages": math.Ceil(totalItemsFloat / float64(pageSize)),
	})
}

func handleJSONRPCMessageCollectionAbandon(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionResolve(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageCollectionUpdate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFfmpegFind(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		analyzeAudioVolume := false // TODO: Get from configuration
		available := false
		var which string // TODO: Get from configuration

		if !available {
			path, err := exec.LookPath("ffmpeg")
			if err == nil {
				available = true
				which = path
			}
		}

		if available {
			sendResultResponse(w, map[string]any{
				"available":            available,
				"which":                which,
				"analyze_audio_volume": analyzeAudioVolume,
			})
			return
		}

		sendResultResponse(w, map[string]any{
			"available":            available,
			"which":                nil,
			"analyze_audio_volume": analyzeAudioVolume,
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileDelete(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileReflect(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileSave(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileSetStatus(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageGet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	paramsMap, paramsMapOk := params.(map[string]any)
	if !paramsMapOk {
		sendErrorResponse(w, 400, "Parameters not present.")
		return
	}
	uri, err := GetStringParameter(paramsMap, "uri", true, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}

	resolveResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.claimtrie.resolve",
		"params":  []string{uri},
	})

	var resolutions map[string]any = map[string]any{}

	_, resultIsString := resolveResp["result"].(string)
	if resultIsString {
		decodedBase64, _ := base64.StdEncoding.DecodeString(resolveResp["result"].(string))

		decodedProtobuf, _ := DecodeRawProto(decodedBase64)

		var resolutionData []any

		_, okResolution := decodedProtobuf[1].([]any)
		if okResolution {
			resolutionData = decodedProtobuf[1].([]any)
		} else {
			resolutionData = []any{decodedProtobuf[1]}
		}

		txIDs := []string{}

		for _, claim := range resolutionData {
			claimMap := claim.(map[int]any)
			txidValue, txidOk := claimMap[1]
			if txidOk {
				txidBytes := txidValue.([]byte)
				slices.Reverse(txidBytes)
				txID := hex.EncodeToString(txidBytes)
				txIDs = append(txIDs, txID)
			}
		}

		transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})

		transactionData := transactionResp["result"].(map[string]any)

		for _, claim := range resolutionData {
			claimMap, ok := claim.(map[int]any)
			if ok {
				item := convertProtobufToClaim(claimMap, transactionData)

				resolutionKey := uri
				resolutions[resolutionKey] = item
			}

		}
	}

	sdHash := resolutions[uri].(map[string]any)["value"].(map[string]any)["source"].(map[string]any)["sd_hash"].(string)

	streamingURL := "http://localhost:5280/stream/" + sdHash

	sendResultResponse(w, map[string]any{
		"streaming_url": streamingURL,
	})
}

func handleJSONRPCMessagePeerList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		if rpcServer.dhtNode == nil {
			sendErrorResponse(w, 500, "DHT component is not running.")
			return
		}
		addr, _ := net.ResolveUDPAddr("udp", "s1.lbry.network:4444") // TODO Remove hardcoded server
		payload, err := rpcServer.dhtNode.Ping(addr)
		if err == nil {
			sendResultResponse(w, string(payload))
			return
		}
		sendResultResponse(w, map[string]any{
			"error": err.Error(),
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePeerPing(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		if rpcServer.dhtNode == nil {
			sendErrorResponse(w, 500, "DHT component is not running.")
			return
		}
		addr, _ := net.ResolveUDPAddr("udp", "s1.lbry.network:4444") // TODO Remove hardcoded server
		payload, err := rpcServer.dhtNode.Ping(addr)
		if err == nil {
			sendResultResponse(w, string(payload))
			return
		}
		sendResultResponse(w, map[string]any{
			"error": err.Error(),
		})
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessagePreferenceGet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}

		if HasParameter(paramsMap, "key") {
			key, err := GetStringParameter(paramsMap, "key", true, "")
			if err != nil {
				sendErrorResponse(w, 400, err.Error())
				return
			}

			preference, hasPreference := walletObj.Preferences[key]

			if hasPreference {
				sendResultResponse(w, map[string]any{
					key: preference.Value,
				})
				return
			}
			sendResultResponse(w, nil)
			return
		}

		allPreferences := map[string]any{}

		for preferenceKey, preference := range walletObj.Preferences {
			allPreferences[preferenceKey] = preference.Value
		}

		sendResultResponse(w, allPreferences)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePreferenceSet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		key, err := GetStringParameter(paramsMap, "key", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		value, err := GetParameter(paramsMap, "value")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}

		valueString, valueIsString := value.(string)
		if valueIsString && (strings.HasPrefix(valueString, "{") || strings.HasPrefix(valueString, "[")) {
			var decodedValue any

			err := json.Unmarshal([]byte(valueString), &decodedValue)
			if err != nil {
				sendErrorResponse(w, 400, "Failed to decode JSON object or array.")
				return
			}

			value = decodedValue
		}

		preference, hasPreference := walletObj.Preferences[key]
		if !hasPreference || preference == nil {
			preference = &wallet.Preference{}
			walletObj.Preferences[key] = preference
		}
		preference.SetNewValue(value)

		updateOk := rpcServer.walletManager.Update(user, walletObj)
		if !updateOk {
			sendErrorResponse(w, 400, "Failed updating wallet.")
			return
		}

		sendResultResponse(w, map[string]any{
			key: value,
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePublish(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePurchaseCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePurchaseList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageResolve(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	paramsMap, paramsMapOk := params.(map[string]any)
	if !paramsMapOk {
		sendErrorResponse(w, 400, "Parameters not present.")
		return
	}
	urls, err := GetParameter(paramsMap, "urls")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}

	urlArray := []string{}

	urlsString, urlsIsString := urls.(string)
	urlsArray, urlsIsArray := urls.([]any)
	if !urlsIsString && !urlsIsArray {
		sendErrorResponse(w, 400, fmt.Sprintf("Parameter '%s' not of type string or string array.", "urls"))
		return
	}
	if urlsIsString {
		urlArray = append(urlArray, urlsString)
	}
	if urlsIsArray {
		for _, url := range urlsArray {
			urlString, urlsIsString := url.(string)
			if !urlsIsString {
				sendErrorResponse(w, 400, fmt.Sprintf("Parameter '%s' not of type string or string array.", "urls"))
				return
			}
			urlArray = append(urlArray, urlString)
		}
	}

	resolveResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.claimtrie.resolve",
		"params":  urlArray,
	})

	var resolutions map[string]any = map[string]any{}

	_, resultIsString := resolveResp["result"].(string)
	if resultIsString {
		decodedBase64, _ := base64.StdEncoding.DecodeString(resolveResp["result"].(string))

		decodedProtobuf, _ := DecodeRawProto(decodedBase64)

		var resolutionData []any

		_, okResolution := decodedProtobuf[1].([]any)
		if okResolution {
			resolutionData = decodedProtobuf[1].([]any)
		} else {
			resolutionData = []any{decodedProtobuf[1]}
		}

		txIDs := []string{}

		for _, claim := range resolutionData {
			if claim == nil {
				continue
			}
			claimMap := claim.(map[int]any)
			txidValue, txidOk := claimMap[1]
			if txidOk {
				txidBytes := txidValue.([]byte)
				slices.Reverse(txidBytes)
				txID := hex.EncodeToString(txidBytes)
				txIDs = append(txIDs, txID)
			}
		}

		transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})

		transactionData := transactionResp["result"].(map[string]any)

		for index, claim := range resolutionData {
			claimMap, ok := claim.(map[int]any)
			if ok {
				item := convertProtobufToClaim(claimMap, transactionData)

				resolutionKey := urlArray[index]
				resolutions[resolutionKey] = item
			}

		}
	}

	sendResultResponse(w, resolutions)
}

func handleJSONRPCMessageRoutingTableGet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req) // Relaxed
	if authenticated {
		_ = user
		if rpcServer.dhtNode == nil {
			sendErrorResponse(w, 500, "DHT component is not running.")
			return
		}

		buckets := map[string]any{}
		nodeID := hex.EncodeToString(rpcServer.dhtNode.ID[:])
		prefixNeighborsCount := 0

		routingTable := rpcServer.dhtNode.Routing

		for i, bucket := range routingTable.Buckets {
			bucketItem := []any{}
			for _, peer := range bucket.Peers {
				bucketItem = append(bucketItem, map[string]any{
					"address":  peer.IP.String(),
					"udp_port": peer.UDPPort,
					"tcp_port": peer.TCPPort,
					"node_id":  hex.EncodeToString(peer.ID[:]),
				})

				if rpcServer.dhtNode.ID[0] == peer.ID[0] {
					prefixNeighborsCount++
				}
			}
			if len(bucketItem) > 0 {
				buckets[strconv.Itoa(i)] = bucketItem
			}
		}

		sendResultResponse(w, map[string]any{
			"buckets":                buckets,
			"node_id":                nodeID,
			"prefix_neighbors_count": prefixNeighborsCount,
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSettingsClear(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		key, err := GetStringParameter(paramsMap, "key", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		newValue, err := rpcServer.configuration.Clear(key)
		if err == nil {
			sendResultResponse(w, map[string]any{
				key: newValue,
			})
			return
		}
		sendErrorResponse(w, 500, err.Error())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageSettingsGet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		sendResultResponse(w, rpcServer.configuration.All())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageSettingsSet(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		key, err := GetStringParameter(paramsMap, "key", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		value, err := GetParameter(paramsMap, "value")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		valueString, valueIsString := value.(string)
		if valueIsString && (strings.HasPrefix(valueString, "{") || strings.HasPrefix(valueString, "[")) {
			var decodedValue any

			err := json.Unmarshal([]byte(valueString), &decodedValue)
			if err != nil {
				sendErrorResponse(w, 400, "Failed to decode JSON object or array.")
				return
			}

			value = decodedValue
		}

		newValue, err := rpcServer.configuration.Set(key, value)
		if err == nil {
			sendResultResponse(w, map[string]any{
				key: newValue,
			})
			return
		}
		sendErrorResponse(w, 500, err.Error())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageStatus(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendResultResponse(w, map[string]any{
		"disk_space": map[string]any{
			"content_blobs_storage_used_mb": 123,
		},
		"ffmpeg_status": map[string]any{
			"available":            false,
			"which":                nil,
			"analyze_audio_volume": nil,
		},
		"is_running": true,
	})
}

func handleJSONRPCMessageStop(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		sendResultResponse(w, "Shutting down")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// TODO: Improve graceful shutdown

		defer os.Exit(0)
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamAbandon(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamCostEstimate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	paramsMap, paramsMapOk := params.(map[string]any)
	if !paramsMapOk {
		sendErrorResponse(w, 400, "Parameters not present.")
		return
	}
	uri, err := GetStringParameter(paramsMap, "uri", true, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}

	resolveResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.claimtrie.resolve",
		"params":  []string{uri},
	})

	var resolutions map[string]any = map[string]any{}

	_, resultIsString := resolveResp["result"].(string)
	if resultIsString {
		decodedBase64, _ := base64.StdEncoding.DecodeString(resolveResp["result"].(string))

		decodedProtobuf, _ := DecodeRawProto(decodedBase64)

		var resolutionData []any

		_, okResolution := decodedProtobuf[1].([]any)
		if okResolution {
			resolutionData = decodedProtobuf[1].([]any)
		} else {
			resolutionData = []any{decodedProtobuf[1]}
		}

		txIDs := []string{}

		for _, claim := range resolutionData {
			claimMap := claim.(map[int]any)
			txidValue, txidOk := claimMap[1]
			if txidOk {
				txidBytes := txidValue.([]byte)
				slices.Reverse(txidBytes)
				txID := hex.EncodeToString(txidBytes)
				txIDs = append(txIDs, txID)
			}
		}

		transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})

		transactionData := transactionResp["result"].(map[string]any)

		for _, claim := range resolutionData {
			claimMap, ok := claim.(map[int]any)
			if ok {
				item := convertProtobufToClaim(claimMap, transactionData)

				resolutionKey := uri
				resolutions[resolutionKey] = item
			}

		}

		finalClaim := resolutions[uri]
		fee, hasFee := finalClaim.(map[string]any)["value"].(map[string]any)["fee"]

		if hasFee {
			if fee.(map[string]any)["currency"] == "LBC" {
				sendResultResponse(w, float64(fee.(map[string]any)["amount"].(uint64))/100_000_000.0)
				return
			}

			sendErrorResponse(w, 500, "Cannot convert rate at this moment") // TODO: Convert BTC and USD
			return
		}

		sendResultResponse(w, 0)
		return
	}

	sendErrorResponse(w, 501, "Failed to get result")
}

func handleJSONRPCMessageStreamCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamRepost(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamUpdate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportAbandon(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportSum(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSyncApply(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSyncHash(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTracemallocDisable(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTracemallocEnable(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTracemallocTop(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTransactionList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTransactionShow(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	paramsMap, paramsMapOk := params.(map[string]any)
	if !paramsMapOk {
		sendErrorResponse(w, 400, "Parameters not present.")
		return
	}
	txid, err := GetStringParameter(paramsMap, "txid", true, "")
	if err != nil {
		sendErrorResponse(w, 400, err.Error())
		return
	}

	transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      rand.Int() + 1,
		"method":  "blockchain.transaction.info",
		"params":  []string{txid},
	})

	errorObj, hasError := transactionResp["error"].(map[string]any)

	if !hasError {
		sendResultResponse(w, nil) // TODO: Decode transaction and return
		return
	}

	sendErrorResponse(w, int(errorObj["code"].(float64)), errorObj["message"].(string))
}

func handleJSONRPCMessageTxoList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoPlot(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoSpend(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoSum(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageUtxoList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageUtxoRelease(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageVersion(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	_, _ = debug.ReadBuildInfo()

	sendResultResponse(w, map[string]any{
		"build":           nil,
		"lbrynet_version": "0.113.0",
		"os_release":      nil,
		"os_system":       nil,
		"platform":        nil,
		"processor":       nil,
		"python_version":  nil,
		"version":         "0.113.0",
	})
}

func handleJSONRPCMessageWalletAdd(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletBalance(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	authenticated = true // Temporary
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		confirmations, err := GetIntegerParameter(paramsMap, "confirmations", false, 0)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		balance, err := wallet.WalletBalance(rpcServer.hub, walletObj.Accounts, int(confirmations))
		switch {
		case errors.Is(err, wallet.ErrUnsupportedConfirmations):
			sendErrorResponse(w, 400, err.Error())
		case err != nil:
			sendErrorResponse(w, 502, "Could not work out the balance with the wallet server: "+err.Error())
		default:
			sendResultResponse(w, map[string]any{
				"total":     wallet.FormatLBC(balance.Total),
				"available": wallet.FormatLBC(balance.Available),
				"reserved":  wallet.FormatLBC(balance.Reserved),
				"reserved_subtotals": map[string]any{
					"claims":   wallet.FormatLBC(balance.Claims),
					"supports": wallet.FormatLBC(balance.Supports),
					"tips":     wallet.FormatLBC(balance.Tips),
				},
			})
		}
		return
	}
	handleUnauthorized(w)
}

// hubClient calls the wallet server and returns the "result" of its reply.
type hubClient struct{}

func hubResult(method string, resp map[string]any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	result, has := resp["result"]
	if !has {
		return nil, fmt.Errorf("%s: %v", method, resp["error"])
	}
	return result, nil
}

func handleJSONRPCMessageWalletCreate(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletDecrypt(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletEncrypt(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletExport(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletImport(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletList(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}

		if HasParameter(paramsMap, "wallet_id") {
			walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
			if err != nil {
				sendErrorResponse(w, 400, err.Error())
				return
			}

			walletObj := rpcServer.walletManager.Read(user, walletID)
			if walletObj == nil {
				sendErrorResponse(w, 400, "Wallet does not exist.")
				return
			}

			sendResultResponse(w, Paginate([]*wallet.Wallet{walletObj}, 1, 1).ToMap())
			return
		}

		page, err := GetIntegerParameter(paramsMap, "page", false, 1)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		pageSize, err := GetIntegerParameter(paramsMap, "page_size", false, 20)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		wallets := rpcServer.walletManager.ReadList(user)

		walletList := []any{}
		for _, item := range wallets {
			walletList = append(walletList, map[string]any{
				"id":   item.ID,
				"name": item.Name,
			})
		}

		sendResultResponse(w, Paginate(walletList, uint(max(page, 0)), uint(max(pageSize, 0))).ToMap())
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletLock(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		ok := walletObj.Lock()

		sendResultResponse(w, ok)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletReconnect(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletRemove(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		rpcServer.walletManager.Delete(user, walletID)

		sendResultResponse(w, map[string]any{
			"id":   walletObj.ID,
			"name": walletObj.Name,
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletSend(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletStatus(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req) // TODO Was guest, maybe fix
	if authenticated {
		_ = user

		if regexp.MustCompile(`(?:^|\s)LBRY/(\d+)\.(\d+)\.(\d+)(?:\s|$)`).MatchString(req.UserAgent()) {
			// Backward compatibility
			sendResultResponse(w, map[string]any{})
			return
		}

		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}

		sendResultResponse(w, map[string]any{
			"is_encrypted": walletObj.IsEncrypted(),
			"is_syncing":   walletObj.IsSyncing(),
			"is_locked":    walletObj.IsLocked(),
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletUnlock(rpcServer *RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		walletID, err := GetStringParameter(paramsMap, "wallet_id", false, wallet.DEFAULT_WALLET)
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}
		password, err := GetStringParameter(paramsMap, "password", true, "")
		if err != nil {
			sendErrorResponse(w, 400, err.Error())
			return
		}

		walletObj := rpcServer.walletManager.Read(user, walletID)
		if walletObj == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		ok := walletObj.Unlock(password)

		sendResultResponse(w, ok)
		return
	}
	handleUnauthorized(w)
}

func handleUnauthorized(w http.ResponseWriter) {
	sendErrorResponse(w, 401, "Not permitted to use this method.")
}

// accountDetails is lbry-sdk's Account.get_details. The balance needs the wallet-server client, so it
// is reported as 0 until that exists.
func accountDetails(account *wallet.Account, showSeed bool) map[string]any {
	ledger := account.Ledger
	if ledger == "" {
		ledger = "lbc_mainnet"
	}
	details := map[string]any{
		"id":                account.ID,
		"name":              account.Name,
		"ledger":            ledger,
		"coins":             0,
		"satoshis":          0,
		"encrypted":         account.Locked,
		"public_key":        account.PublicKey,
		"address_generator": account.AddressGenerator,
	}
	if showSeed {
		details["seed"] = account.Seed
	}
	details["certificates"] = len(account.Certificates)
	return details
}
