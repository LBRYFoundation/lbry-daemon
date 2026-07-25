package rpc

import "bufio"
import "encoding/base64"
import "encoding/hex"
import "encoding/json"
import "fmt"
import "lbry/daemon/dht"
import "math"
import "math/rand"
import "net"
import "net/http"
import "os"
import "os/exec"
import "runtime/debug"
import "slices"
import "strconv"
import "strings"

import "google.golang.org/protobuf/encoding/protowire"

type RPCServer struct {
	dhtNode    *dht.Node
	httpServer http.Server
}

func CreateServer(dhtNode *dht.Node) RPCServer {
	rpcServeMux := http.NewServeMux()

	server := RPCServer{
		dhtNode:    dhtNode,
		httpServer: http.Server{Handler: rpcServeMux},
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
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountBalance(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountDeposit(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountFund(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountMaxAddressGap(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountRemove(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAccountSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressIsMine(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageAddressUnused(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageBlobAnnounce(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageBlobClean(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobDelete(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageBlobReflectAll(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageChannelAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelSign(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageChannelUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageClaimList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
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
	// Relaxed
	searchResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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

	transactionResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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
}

func handleJSONRPCMessageCollectionAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageCollectionResolve(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageCollectionUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageFfmpegFind(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	mayUse := !PROTECT_MODE_ADMIN

	if PROTECT_MODE_ADMIN {
		username, password, ok := req.BasicAuth()
		if ok {
			if username == os.Getenv("ADMIN_USERNAME") && password == os.Getenv("ADMIN_PASSWORD") {
				mayUse = true
			}
		}
	}

	if mayUse {
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
	sendErrorResponse(w, 401, "Not permitted to use this method.")
}

func handleJSONRPCMessageFileDelete(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileReflect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSave(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageFileSetStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	var paramsMap map[string]any = params.(map[string]any)

	uri, _ := paramsMap["uri"].(string)

	resolveResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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

		transactionResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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

var PROTECT_MODE_ADMIN = true

func handleJSONRPCMessagePeerList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	mayUse := !PROTECT_MODE_ADMIN

	if PROTECT_MODE_ADMIN {
		username, password, ok := req.BasicAuth()
		if ok {
			if username == os.Getenv("ADMIN_USERNAME") && password == os.Getenv("ADMIN_PASSWORD") {
				mayUse = true
			}
		}
	}

	if mayUse {
		if rpcServer.dhtNode == nil {
			sendErrorResponse(w, 401, "DHT component is not enabled.")
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
	sendErrorResponse(w, 401, "Not permitted to use this method.")
}

func handleJSONRPCMessagePeerPing(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	mayUse := !PROTECT_MODE_ADMIN

	if PROTECT_MODE_ADMIN {
		username, password, ok := req.BasicAuth()
		if ok {
			if username == os.Getenv("ADMIN_USERNAME") && password == os.Getenv("ADMIN_PASSWORD") {
				mayUse = true
			}
		}
	}

	if mayUse {
		if rpcServer.dhtNode == nil {
			sendErrorResponse(w, 401, "DHT component is not enabled.")
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

	sendErrorResponse(w, 401, "Not permitted to use this method.")
}

func handleJSONRPCMessagePreferenceGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePreferenceSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePublish(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessagePurchaseList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageResolve(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	var paramsMap map[string]any = params.(map[string]any)

	_, ok := paramsMap["urls"].([]any)

	var urls []any = []any{}

	if ok {
		urls = paramsMap["urls"].([]any)
	}

	resolveResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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

		transactionResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
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
}

func handleJSONRPCMessageRoutingTableGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageSettingsClear(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsGet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageSettingsSet(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendResultResponse(w, map[string]any{
		"is_running": true,
	})
}

func handleJSONRPCMessageStop(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	mayUse := !PROTECT_MODE_ADMIN

	if PROTECT_MODE_ADMIN {
		username, password, ok := req.BasicAuth()
		if ok {
			if username == os.Getenv("ADMIN_USERNAME") && password == os.Getenv("ADMIN_PASSWORD") {
				mayUse = true
			}
		}
	}

	if mayUse {
		sendResultResponse(w, "Shutting down")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// TODO: Improve graceful shutdown

		defer os.Exit(0)
		return
	}

	sendErrorResponse(w, 401, "Not permitted to use this method.")
}

func handleJSONRPCMessageStreamAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamCostEstimate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageStreamCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamRepost(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamUpdate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportAbandon(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSupportSum(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncApply(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageSyncHash(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
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
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTransactionShow(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
}

func handleJSONRPCMessageTxoList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoPlot(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSpend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageTxoSum(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageUtxoRelease(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageVersion(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
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

func handleJSONRPCMessageWalletAdd(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletBalance(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletCreate(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletDecrypt(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletEncrypt(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletExport(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletImport(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletList(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletLock(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletReconnect(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletRemove(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletSend(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}

func handleJSONRPCMessageWalletStatus(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	//sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
	sendResultResponse(w, map[string]any{})
}

func handleJSONRPCMessageWalletUnlock(rpcServer RPCServer, w http.ResponseWriter, req *http.Request, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}
