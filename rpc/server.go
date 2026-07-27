package rpc

import "bufio"
import "encoding/base64"
import "encoding/hex"
import "encoding/json"
import "fmt"
import "lbry/daemon/auth"
import "lbry/daemon/blob"
import "lbry/daemon/dht"
import "lbry/daemon/settings"
import "lbry/daemon/wallet"
import "math"
import "math/rand"
import "net"
import "net/http"
import "os"
import "os/exec"
import "regexp"
import "runtime/debug"
import "slices"
import "strconv"
import "strings"

import "google.golang.org/protobuf/encoding/protowire"

// const TMP_HUB_HOSTNAME = "s1.lbry.network"
const TMP_HUB_HOSTNAME = "hub.lbry.grin.io"

type RPCServer struct {
	authManager   *auth.AuthManager
	blobManager   *blob.BlobManager
	configuration settings.Configuration
	dhtNode       *dht.Node
	httpServer    http.Server
	walletManager *wallet.WalletManager
}

func CreateServer(configuration settings.Configuration, authManager *auth.AuthManager, blobManager *blob.BlobManager, dhtNode *dht.Node, walletManager *wallet.WalletManager) *RPCServer {
	rpcServeMux := http.NewServeMux()

	server := &RPCServer{
		authManager:   authManager,
		blobManager:   blobManager,
		configuration: configuration,
		dhtNode:       dhtNode,
		httpServer:    http.Server{Handler: rpcServeMux},
		walletManager: walletManager,
	}

	rpcServeMux.HandleFunc("/", server.handleJSONRPC)

	return server
}

func (rpcServer RPCServer) StartServer(listener net.Listener) {
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

func (rpcServer RPCServer) handleJSONRPC(w http.ResponseWriter, req *http.Request) {
	info, _ := debug.ReadBuildInfo()

	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

var handlers = map[string]func(RPCServer, http.ResponseWriter, *http.Request, any){
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

func (rpcServer RPCServer) handleJSONRPCMessage(w http.ResponseWriter, req *http.Request, message map[string]any) {
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

func handleJSONRPCMessageAccountAdd(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountBalance(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountDeposit(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountFund(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountMaxAddressGap(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountRemove(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountSend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAccountSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressIsMine(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageAddressUnused(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobAnnounce(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req) // Relaxed // TODO: Maybe allow guests too?
	if authenticated {
		_ = user

		sendErrorResponse(w, 501, "NOT IMPLEMENTED")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobClean(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageBlobDelete(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobReflect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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
		blobHashes, hasBlobHashes := paramsMap["blob_hashes"]
		if !hasBlobHashes {
			sendErrorResponse(w, 400, "Missing parameter 'blob_hashes'.")
			return
		}
		blobHashesStringArray, blobHashesIsStringArray := blobHashes.([]string)
		if !blobHashesIsStringArray {
			sendErrorResponse(w, 400, "Parameter 'key' not of type string array.")
			return
		}

		resp := rpcServer.blobManager.Reflect(blobHashesStringArray)

		sendResultResponse(w, resp)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageBlobReflectAll(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageChannelAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelSign(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageChannelUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageClaimList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func SendJSON(host string, port int, req any) (map[string]any, error) {
	conn, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	data, _ := json.Marshal(req)

	conn.Write(append(data, '\n'))

	line, _ := bufio.NewReader(conn).ReadString('\n')
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}

	var resp map[string]any
	json.Unmarshal([]byte(line), &resp)
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

func handleJSONRPCMessageClaimSearch(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		searchResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.claimtrie.search",
			"params":  params,
		})

		decodedBase64, _ := base64.StdEncoding.DecodeString(searchResp["result"].(string))
		decodedProtobuf, _ := DecodeRawProto(decodedBase64)

		claims, ok := decodedProtobuf[1].([]any)

		txIDs := []string{}

		if ok {
			for _, claim := range claims {
				claimMap := claim.(map[int]any)
				txidValue, txidOk := claimMap[1]
				if txidOk {
					txidBytes := txidValue.([]byte)
					slices.Reverse(txidBytes)
					txID := hex.EncodeToString(txidBytes)
					txIDs = append(txIDs, txID)
				}
			}
		}

		transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.get_batch",
			"params":  txIDs,
		})

		transactionData := transactionResp["result"].(map[string]any)

		var items []map[string]any = []map[string]any{}

		if ok {
			for _, claim := range claims {
				claimMap := claim.(map[int]any)

				item := convertProtobufToClaim(claimMap, transactionData)

				items = append(items, item)
			}
		}

		totalItems, okTotal := (decodedProtobuf[3]).(uint64)

		var pageSize float64 = 20
		var totalItemsFloat float64 = 0
		if okTotal {
			totalItemsFloat = float64(totalItems)
		}

		sendResultResponse(w, map[string]any{
			"items":       items,
			"page":        1,
			"page_size":   pageSize,
			"total_items": totalItems,
			"total_pages": math.Ceil(totalItemsFloat / pageSize),
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionResolve(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		sendErrorResponse(w, 501, "NOT IMPLEMENTED")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageCollectionUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFfmpegFind(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageFileDelete(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileReflect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileSave(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageFileSetStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user

		sendErrorResponse(w, 401, "Not exposed for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		var paramsMap map[string]any = params.(map[string]any)

		uri, _ := paramsMap["uri"].(string)

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
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePeerList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessagePeerPing(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessagePreferenceGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePreferenceSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePublish(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePurchaseCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessagePurchaseList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageResolve(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		var paramsMap map[string]any = params.(map[string]any)

		_, ok := paramsMap["urls"].([]any)

		var urls []any = []any{}

		if ok {
			urls = paramsMap["urls"].([]any)
		}

		resolveResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.claimtrie.resolve",
			"params":  urls,
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

					resolutionKey := urls[index].(string)
					resolutions[resolutionKey] = item
				}

			}
		}

		sendResultResponse(w, resolutions)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageRoutingTableGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageSettingsClear(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		key, hasKey := paramsMap["key"]
		if !hasKey {
			sendErrorResponse(w, 400, "Missing parameter 'key'.")
			return
		}
		keyString, keyIsString := key.(string)
		if !keyIsString {
			sendErrorResponse(w, 400, "Parameter 'key' not of type string.")
			return
		}
		newValue, err := rpcServer.configuration.Clear(keyString)
		if err == nil {
			sendResultResponse(w, map[string]any{
				keyString: newValue,
			})
			return
		}
		sendErrorResponse(w, 500, err.Error())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageSettingsGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		sendResultResponse(w, rpcServer.configuration.All())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageSettingsSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateAdminUser(req)
	if authenticated {
		_ = user
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		key, hasKey := paramsMap["key"]
		if !hasKey {
			sendErrorResponse(w, 400, "Missing parameter 'key'.")
			return
		}
		keyString, keyIsString := key.(string)
		if !keyIsString {
			sendErrorResponse(w, 400, "Parameter 'key' not of type string.")
			return
		}
		value, hasValue := paramsMap["value"]
		if !hasValue {
			sendErrorResponse(w, 400, "Missing parameter 'value'.")
			return
		}
		newValue, err := rpcServer.configuration.Set(keyString, value)
		if err == nil {
			sendResultResponse(w, map[string]any{
				keyString: newValue,
			})
			return
		}
		sendErrorResponse(w, 500, err.Error())
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		sendResultResponse(w, map[string]any{
			"is_running": true,
		})
		return
	}

	handleUnauthorized(w)
}

func handleJSONRPCMessageStop(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageStreamAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamCostEstimate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		var paramsMap map[string]any = params.(map[string]any)

		uri, _ := paramsMap["uri"].(string)

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
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamRepost(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageStreamUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSupportSum(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSyncApply(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageSyncHash(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTracemallocDisable(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTracemallocEnable(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTracemallocTop(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 404, "Tracemalloc has been deprecated.")
}

func handleJSONRPCMessageTransactionList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTransactionShow(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		transactionID, hasTransactionID := paramsMap["txid"]
		if !hasTransactionID {
			sendErrorResponse(w, 400, "Missing parameter 'txid'.")
			return
		}
		transactionIDString, transactionIDIsString := transactionID.(string)
		if !transactionIDIsString {
			sendErrorResponse(w, 400, "Parameter 'txid' not of type string.")
			return
		}

		transactionResp, _ := SendJSON(TMP_HUB_HOSTNAME, 50001, map[string]any{
			"jsonrpc": "2.0",
			"id":      rand.Int() + 1,
			"method":  "blockchain.transaction.info",
			"params":  []string{transactionIDString},
		})

		errorObj, hasError := transactionResp["error"].(map[string]any)

		if !hasError {
			sendResultResponse(w, nil) // TODO: Decode transaction and return
			return
		}

		sendErrorResponse(w, int(errorObj["code"].(float64)), errorObj["message"].(string))
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoPlot(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoSpend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageTxoSum(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageUtxoList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageUtxoRelease(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageVersion(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req) // Relaxed
	if authenticated {
		_ = user

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
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletAdd(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletBalance(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletDecrypt(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletEncrypt(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletExport(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletImport(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		wallets := rpcServer.walletManager.List(user)

		walletMaps := []any{}

		for _, wallet := range wallets {
			walletMaps = append(walletMaps, map[string]any{
				"id":   wallet.ID,
				"name": wallet.Name,
			})
		}

		paginator := NewPaginator(walletMaps, 0, 0, 0, 0)

		sendResultResponse(w, paginator.ToMap())
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletLock(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			paramsMap = map[string]any{}
		}
		walletID, hasWalletID := paramsMap["wallet_id"]
		if !hasWalletID {
			walletID = "default_wallet"
		}
		walletIDString, walletIDIsString := walletID.(string)
		if !walletIDIsString {
			sendErrorResponse(w, 400, "Parameter 'wallet_id' not of type string.")
			return
		}

		wallet := rpcServer.walletManager.Get(user, walletIDString)
		if wallet == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		ok := rpcServer.walletManager.Lock(user, walletIDString)

		sendResultResponse(w, ok)
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletReconnect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletRemove(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		walletID, hasWalletID := paramsMap["wallet_id"]
		if !hasWalletID {
			sendErrorResponse(w, 400, "Missing parameter 'wallet_id'.")
			return
		}
		walletIDString, walletIDIsString := walletID.(string)
		if !walletIDIsString {
			sendErrorResponse(w, 400, "Parameter 'wallet_id' not of type string.")
			return
		}

		wallet := rpcServer.walletManager.Get(user, walletIDString)
		if wallet == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		rpcServer.walletManager.Remove(user, walletIDString)

		sendResultResponse(w, map[string]any{
			"id":   wallet.ID,
			"name": wallet.Name,
		})
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletSend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		_ = user
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateGuest(req)
	if authenticated {
		_ = user

		if regexp.MustCompile(`(?:^|\s)LBRY/(\d+)\.(\d+)\.(\d+)(?:\s|$)`).MatchString(req.UserAgent()) {
			// Backward compatibility
			sendResultResponse(w, map[string]any{})
			return
		}
		sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
		return
	}
	handleUnauthorized(w)
}

func handleJSONRPCMessageWalletUnlock(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	user, authenticated := rpcServer.authManager.ValidateUser(req)
	if authenticated {
		paramsMap, paramsMapOk := params.(map[string]any)
		if !paramsMapOk {
			sendErrorResponse(w, 400, "Parameters not present.")
			return
		}
		password, hasPassword := paramsMap["password"]
		if !hasPassword {
			sendErrorResponse(w, 400, "Missing parameter 'password'.")
			return
		}
		passwordString, passwordIsString := password.(string)
		if !passwordIsString {
			sendErrorResponse(w, 400, "Parameter 'password' not of type string.")
			return
		}
		walletID, hasWalletID := paramsMap["wallet_id"]
		if !hasWalletID {
			walletID = "default_wallet"
		}
		walletIDString, walletIDIsString := walletID.(string)
		if !walletIDIsString {
			sendErrorResponse(w, 400, "Parameter 'wallet_id' not of type string.")
			return
		}

		wallet := rpcServer.walletManager.Get(user, walletIDString)
		if wallet == nil {
			sendErrorResponse(w, 400, "Wallet does not exist.")
			return
		}
		ok := rpcServer.walletManager.Unlock(user, walletIDString, passwordString)

		sendResultResponse(w, ok)
		return
	}
	handleUnauthorized(w)
}

func handleUnauthorized(w http.ResponseWriter) {
	sendErrorResponse(w, 401, "Not permitted to use this method.")
}
