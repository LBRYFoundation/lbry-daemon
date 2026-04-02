package rpc

import "bufio"
import "encoding/base64"
import "encoding/json"
import "fmt"
import "math"
import "net"
import "net/http"
import "runtime/debug"
import "strconv"
import "strings"

import "google.golang.org/protobuf/encoding/protowire"

type RPCServer struct {
	httpServer http.Server
}

func CreateServer() RPCServer {
	rpcServeMux := http.NewServeMux()

	server := RPCServer{
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
		fmt.Printf("Receiving '%s' method\n", method)
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
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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

func handleJSONRPCMessageClaimSearch(w http.ResponseWriter, params any) {
	// Relaxed
	searchResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      "123",
		"method":  "blockchain.claimtrie.search",
		"params":  params,
	})

	decodedBase64, _ := base64.StdEncoding.DecodeString(searchResp["result"].(string))
	decodedProtobuf, _ := DecodeRawProto(decodedBase64)

	var items []map[string]any = []map[string]any{}

	claims, ok := decodedProtobuf[1].([]any)
	if ok {
		for _, claim := range claims {
			claimMap := claim.(map[int]any)
			claimMap7 := (claimMap[7]).(map[int]any)

			uriBase64 := claimMap7[4].([]uint8)
			uri := string(uriBase64)

			item := map[string]any{
				"canonical_url": "lbry://" + uri,
			}

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
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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
	var paramsMap map[string]any = params.(map[string]any)

	_, ok := paramsMap["urls"].([]any)

	var urls []any = []any{}

	if ok {
		urls = paramsMap["urls"].([]any)
	}

	resolveResp, _ := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      "123",
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

		for index, claim := range resolutionData {
			var item map[string]any

			claimMap, ok := claim.(map[int]any)
			if ok {
				claimMap7 := (claimMap[7]).(map[int]any)

				uriBase64 := claimMap7[4].([]uint8)
				uri := string(uriBase64)

				item = map[string]any{
					"canonical_url": "lbry://" + uri,
					"claim_id":      "e0e99956966e1ac7b468bc2bb5430a1841b048e1",
					"name":          "Some Claim: " + uri,
					"value": map[string]any{
						"thumbnail": map[string]any{
							"url": "https://spee.ch/d/f3b724e6ff579f07.png",
						},
					},
					"_": claimMap,
				}
			}

			if ok {
				resolutionKey := urls[index].(string)
				resolutions[resolutionKey] = item
			}

		}
	}

	sendResultResponse(w, resolutions)
}

func handleJSONRPCMessageRoutingTableGet(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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
	sendResultResponse(w, map[string]any{
		"is_running": true,
	})
}

func handleJSONRPCMessageStop(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessageStreamAbandon(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Commands that require having a wallet are not implemented for now.")
}

func handleJSONRPCMessageStreamCostEstimate(w http.ResponseWriter, params any) {
	// Relaxed
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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
	sendErrorResponse(w, 501, "NOT IMPLEMENTED")
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
	//sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
	sendResultResponse(w, map[string]any{})
}

func handleJSONRPCMessageWalletUnlock(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 501, "Wallet commands are not implemented for now.")
}
