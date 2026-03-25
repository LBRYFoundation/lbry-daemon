package rpc

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

const preferencesFile = "preferences.json"

func CreateServer() http.Server {
	rpcServeMux := http.NewServeMux()
	rpcServeMux.HandleFunc("/", handleJSONRPC)

	return http.Server{Handler: rpcServeMux}
}

func StartServer(rpcServer http.Server, port int) {
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		fmt.Printf("Error starting listener on port %d: %v\n", port, err)
		return
	}

	fmt.Printf("lbryd v0.113.0 listening on port %d\n", port)

	err = rpcServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		fmt.Printf("Error starting RPC server: %v\n", err)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if req.Method == "OPTIONS" {
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
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
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

// --- Hub Outputs protobuf types ---

type HubOutput struct {
	TxHash []byte
	Nout   uint32
	Height uint32
	Meta   *HubClaimMeta
}

type HubClaimMeta struct {
	CanonicalURL    string
	ShortURL        string
	IsControlling   bool
	CreationHeight  uint32
	EffectiveAmount uint64
	SupportAmount   uint64
	ClaimsInChannel uint32
	Reposted        uint32
	ChannelTxHash   []byte
	ChannelNout     uint32
	HasChannel      bool
}

type HubOutputs struct {
	Txos      []HubOutput
	ExtraTxos []HubOutput
	Total     uint32
}

func decodeOutputs(data []byte) (*HubOutputs, error) {
	result := &HubOutputs{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // txos
			val, _ := protowire.ConsumeBytes(b)
			result.Txos = append(result.Txos, decodeHubOutput(val))
		case typ == protowire.BytesType && num == 2: // extra_txos
			val, _ := protowire.ConsumeBytes(b)
			result.ExtraTxos = append(result.ExtraTxos, decodeHubOutput(val))
		case typ == protowire.VarintType && num == 3: // total
			val, _ := protowire.ConsumeVarint(b)
			result.Total = uint32(val)
		}
		b = b[fieldLen:]
	}
	return result, nil
}

func decodeHubOutput(data []byte) HubOutput {
	out := HubOutput{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // tx_hash
			val, _ := protowire.ConsumeBytes(b)
			out.TxHash = make([]byte, len(val))
			copy(out.TxHash, val)
		case typ == protowire.VarintType && num == 2: // nout
			val, _ := protowire.ConsumeVarint(b)
			out.Nout = uint32(val)
		case typ == protowire.VarintType && num == 3: // height
			val, _ := protowire.ConsumeVarint(b)
			out.Height = uint32(val)
		case typ == protowire.BytesType && num == 7: // ClaimMeta
			val, _ := protowire.ConsumeBytes(b)
			out.Meta = decodeClaimMeta(val)
		}
		b = b[fieldLen:]
	}
	return out
}

func decodeClaimMeta(data []byte) *HubClaimMeta {
	meta := &HubClaimMeta{}
	for b := data; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		fieldLen := protowire.ConsumeFieldValue(num, typ, b)
		if fieldLen < 0 {
			break
		}
		switch {
		case typ == protowire.BytesType && num == 1: // channel reference
			val, _ := protowire.ConsumeBytes(b)
			ch := decodeHubOutput(val)
			if len(ch.TxHash) > 0 {
				meta.ChannelTxHash = ch.TxHash
				meta.ChannelNout = ch.Nout
				meta.HasChannel = true
			}
		case typ == protowire.BytesType && num == 3: // short_url
			val, _ := protowire.ConsumeBytes(b)
			meta.ShortURL = string(val)
		case typ == protowire.BytesType && num == 4: // canonical_url
			val, _ := protowire.ConsumeBytes(b)
			meta.CanonicalURL = string(val)
		case typ == protowire.VarintType && num == 5: // is_controlling
			val, _ := protowire.ConsumeVarint(b)
			meta.IsControlling = val != 0
		case typ == protowire.VarintType && num == 7: // creation_height
			val, _ := protowire.ConsumeVarint(b)
			meta.CreationHeight = uint32(val)
		case typ == protowire.VarintType && num == 10: // claims_in_channel
			val, _ := protowire.ConsumeVarint(b)
			meta.ClaimsInChannel = uint32(val)
		case typ == protowire.VarintType && num == 11: // reposted
			val, _ := protowire.ConsumeVarint(b)
			meta.Reposted = uint32(val)
		case typ == protowire.VarintType && num == 20: // effective_amount
			val, _ := protowire.ConsumeVarint(b)
			meta.EffectiveAmount = val
		case typ == protowire.VarintType && num == 21: // support_amount
			val, _ := protowire.ConsumeVarint(b)
			meta.SupportAmount = val
		}
		b = b[fieldLen:]
	}
	return meta
}

// --- Helpers ---

// SendJSONBatch sends multiple JSON-RPC requests over a single TCP connection.
func SendJSONBatch(host string, port int, requests []map[string]any) []map[string]any {
	responses := make([]map[string]any, len(requests))
	if len(requests) == 0 {
		return responses
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return responses
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for i, req := range requests {
		data, _ := json.Marshal(req)
		conn.Write(append(data, '\n'))

		line, err := reader.ReadString('\n')
		if err != nil {
			continue
		}
		line = strings.TrimRight(line, "\r\n")

		var resp map[string]any
		json.Unmarshal([]byte(line), &resp)
		responses[i] = resp
	}

	return responses
}

// extractPagination converts page/page_size to offset/limit for the hub.
func extractPagination(params any) (int, int, map[string]any) {
	page := 1
	pageSize := 20

	paramsMap, ok := params.(map[string]any)
	if !ok {
		return page, pageSize, map[string]any{}
	}

	hubParams := make(map[string]any)
	for k, v := range paramsMap {
		hubParams[k] = v
	}

	if p, ok := hubParams["page"]; ok {
		if pf, ok := p.(float64); ok {
			page = int(pf)
		}
		delete(hubParams, "page")
	}
	if ps, ok := hubParams["page_size"]; ok {
		if psf, ok := ps.(float64); ok {
			pageSize = int(psf)
		}
		delete(hubParams, "page_size")
	}
	if pageSize > 50 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}

	hubParams["offset"] = pageSize * (page - 1)
	hubParams["limit"] = pageSize
	return page, pageSize, hubParams
}

// fetchTransactions fetches raw transaction hex for all given txids over one connection.
func fetchTransactions(host string, port int, txids []string) map[string]string {
	txMap := make(map[string]string, len(txids))
	if len(txids) == 0 {
		return txMap
	}

	requests := make([]map[string]any, len(txids))
	for i, txid := range txids {
		requests[i] = map[string]any{
			"jsonrpc": "2.0",
			"id":      i,
			"method":  "blockchain.transaction.get",
			"params":  []any{txid},
		}
	}

	responses := SendJSONBatch(host, port, requests)
	for i, resp := range responses {
		if resp == nil {
			continue
		}
		if rawHex, ok := resp["result"].(string); ok {
			txMap[txids[i]] = rawHex
		}
	}
	return txMap
}

// collectTxIDs gathers unique txids from hub outputs.
func collectTxIDs(txos []HubOutput, extraTxos []HubOutput) []string {
	seen := make(map[string]bool)
	for _, txo := range txos {
		if len(txo.TxHash) > 0 {
			seen[txHashToTxID(txo.TxHash)] = true
		}
	}
	for _, txo := range extraTxos {
		if len(txo.TxHash) > 0 {
			seen[txHashToTxID(txo.TxHash)] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// inflateOutput builds a full claim object from a hub output + fetched transaction data.
func inflateOutput(txo HubOutput, txMap map[string]string, extraTxos []HubOutput) map[string]any {
	txid := txHashToTxID(txo.TxHash)

	item := map[string]any{
		"txid":   txid,
		"nout":   txo.Nout,
		"height": txo.Height,
	}

	if txo.Meta != nil {
		if txo.Meta.CanonicalURL != "" {
			item["canonical_url"] = "lbry://" + txo.Meta.CanonicalURL
		}
		if txo.Meta.ShortURL != "" {
			item["short_url"] = "lbry://" + txo.Meta.ShortURL
		}
		item["meta"] = map[string]any{
			"effective_amount":  fmt.Sprintf("%d", txo.Meta.EffectiveAmount),
			"support_amount":    fmt.Sprintf("%d", txo.Meta.SupportAmount),
			"claims_in_channel": txo.Meta.ClaimsInChannel,
			"is_controlling":    txo.Meta.IsControlling,
			"creation_height":   txo.Meta.CreationHeight,
			"reposted":          txo.Meta.Reposted,
		}
	}

	rawHex := txMap[txid]
	if rawHex == "" {
		return item
	}

	txOutputs, err := parseTxOutputs(rawHex)
	if err != nil || int(txo.Nout) >= len(txOutputs) {
		return item
	}

	cs, err := parseClaimScript(txOutputs[txo.Nout].Script)
	if err != nil {
		return item
	}

	item["name"] = cs.Name
	item["type"] = "claim"

	if cs.IsUpdate && len(cs.ClaimID) == 20 {
		item["claim_id"] = claimIDFromBytes(cs.ClaimID)
	} else {
		item["claim_id"] = computeClaimID(txo.TxHash, txo.Nout)
	}

	if claimID, ok := item["claim_id"].(string); ok {
		item["permanent_url"] = fmt.Sprintf("lbry://%s#%s", cs.Name, claimID)
	}

	value, valueType, err := decodeClaim(cs.ClaimData)
	if err == nil {
		item["value_type"] = valueType
		item["value"] = value
	}

	// Resolve signing channel from extra_txos
	if txo.Meta != nil && txo.Meta.HasChannel && extraTxos != nil {
		chTxID := txHashToTxID(txo.Meta.ChannelTxHash)
		chNout := txo.Meta.ChannelNout
		for _, extra := range extraTxos {
			if txHashToTxID(extra.TxHash) == chTxID && extra.Nout == chNout {
				item["signing_channel"] = inflateOutput(extra, txMap, nil)
				break
			}
		}
	}

	return item
}

// decodeHubResponse decodes base64 hub response into HubOutputs and fetches transactions.
func decodeHubResponse(resultStr string) (*HubOutputs, map[string]string, error) {
	decodedBase64, err := base64.StdEncoding.DecodeString(resultStr)
	if err != nil {
		return nil, nil, err
	}

	outputs, err := decodeOutputs(decodedBase64)
	if err != nil {
		return nil, nil, err
	}

	txids := collectTxIDs(outputs.Txos, outputs.ExtraTxos)
	txMap := fetchTransactions("s1.lbry.network", 50001, txids)

	return outputs, txMap, nil
}

// --- Handlers ---

func handleJSONRPCMessageClaimSearch(w http.ResponseWriter, params any) {
	page, pageSize, hubParams := extractPagination(params)

	searchResp, err := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"method":  "blockchain.claimtrie.search",
		"params":  hubParams,
	})
	if err != nil {
		sendErrorResponse(w, -32000, "Hub connection error")
		return
	}

	resultStr, ok := searchResp["result"].(string)
	if !ok {
		sendErrorResponse(w, -32000, "Invalid hub response")
		return
	}

	outputs, txMap, err := decodeHubResponse(resultStr)
	if err != nil {
		sendErrorResponse(w, -32000, "Decode error: "+err.Error())
		return
	}

	items := make([]map[string]any, 0, len(outputs.Txos))
	for _, txo := range outputs.Txos {
		items = append(items, inflateOutput(txo, txMap, outputs.ExtraTxos))
	}

	totalItems := float64(outputs.Total)
	pageSizeF := float64(pageSize)

	sendResultResponse(w, map[string]any{
		"items":       items,
		"page":        page,
		"page_size":   pageSize,
		"total_items": outputs.Total,
		"total_pages": math.Ceil(totalItems / pageSizeF),
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
	paramsMap, ok := params.(map[string]any)
	if !ok {
		sendErrorResponse(w, -32600, "Invalid params")
		return
	}

	uri, _ := paramsMap["uri"].(string)
	if uri == "" {
		sendErrorResponse(w, -32600, "Missing 'uri' parameter")
		return
	}

	// Resolve the claim
	resolveResp, err := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"method":  "blockchain.claimtrie.resolve",
		"params":  []any{uri},
	})
	if err != nil {
		sendErrorResponse(w, -32000, "Hub connection error")
		return
	}

	resultStr, ok := resolveResp["result"].(string)
	if !ok {
		sendErrorResponse(w, -32000, "Invalid hub response")
		return
	}

	outputs, txMap, err := decodeHubResponse(resultStr)
	if err != nil || len(outputs.Txos) == 0 {
		sendErrorResponse(w, -32000, "Claim not found")
		return
	}

	claim := inflateOutput(outputs.Txos[0], txMap, outputs.ExtraTxos)

	value, _ := claim["value"].(map[string]any)
	source, _ := value["source"].(map[string]any)
	sdHash, _ := source["sd_hash"].(string)
	name, _ := claim["name"].(string)
	claimID, _ := claim["claim_id"].(string)

	// Construct streaming URL via CDN (v3 API, first 6 chars of sd_hash)
	streamingURL := ""
	if sdHash != "" && name != "" && claimID != "" {
		sdHashPrefix := sdHash
		if len(sdHashPrefix) > 6 {
			sdHashPrefix = sdHashPrefix[:6]
		}
		streamingURL = fmt.Sprintf(
			"https://player.odycdn.com/api/v3/streams/free/%s/%s/%s",
			name, claimID, sdHashPrefix,
		)
	}

	sendResultResponse(w, map[string]any{
		"streaming_url": streamingURL,
		"stream_hash":   sdHash,
		"sd_hash":       sdHash,
		"completed":     true,
		"claim_id":      claimID,
		"claim_name":    name,
		"mime_type":     source["media_type"],
		"metadata":      value,
	})
}

func handleJSONRPCMessagePeerList(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func handleJSONRPCMessagePeerPing(w http.ResponseWriter, params any) {
	sendErrorResponse(w, 401, "Not exposed for now.")
}

func loadPreferences() map[string]any {
	data, err := os.ReadFile(preferencesFile)
	if err != nil {
		return map[string]any{
			"local": map[string]any{
				"subscriptions": []any{},
				"tags":          []any{},
			},
		}
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return map[string]any{
			"local": map[string]any{
				"subscriptions": []any{},
				"tags":          []any{},
			},
		}
	}
	return prefs
}

func savePreferences(prefs map[string]any) error {
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(preferencesFile, data, 0644)
}

func handleJSONRPCMessagePreferenceGet(w http.ResponseWriter, params any) {
	prefs := loadPreferences()

	paramsMap, _ := params.(map[string]any)
	key, _ := paramsMap["key"].(string)

	if key != "" {
		val, ok := prefs[key]
		if !ok {
			val = map[string]any{}
		}
		sendResultResponse(w, map[string]any{key: val})
	} else {
		sendResultResponse(w, prefs)
	}
}

func handleJSONRPCMessagePreferenceSet(w http.ResponseWriter, params any) {
	paramsMap, ok := params.(map[string]any)
	if !ok {
		sendErrorResponse(w, -32600, "Invalid params")
		return
	}

	prefs := loadPreferences()

	key, _ := paramsMap["key"].(string)
	value := paramsMap["value"]

	if key != "" && value != nil {
		prefs[key] = value
	}

	if err := savePreferences(prefs); err != nil {
		sendErrorResponse(w, -32000, "Failed to save preferences")
		return
	}

	sendResultResponse(w, prefs)
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
	paramsMap, ok := params.(map[string]any)
	if !ok {
		sendResultResponse(w, map[string]any{})
		return
	}

	urls, ok := paramsMap["urls"].([]any)
	if !ok || len(urls) == 0 {
		sendResultResponse(w, map[string]any{})
		return
	}

	resolveResp, err := SendJSON("s1.lbry.network", 50001, map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"method":  "blockchain.claimtrie.resolve",
		"params":  urls,
	})
	if err != nil {
		sendErrorResponse(w, -32000, "Hub connection error")
		return
	}

	resultStr, ok := resolveResp["result"].(string)
	if !ok {
		sendResultResponse(w, map[string]any{})
		return
	}

	outputs, txMap, err := decodeHubResponse(resultStr)
	if err != nil {
		sendResultResponse(w, map[string]any{})
		return
	}

	resolutions := make(map[string]any)
	for i, txo := range outputs.Txos {
		if i < len(urls) {
			key, _ := urls[i].(string)
			if key != "" {
				resolutions[key] = inflateOutput(txo, txMap, outputs.ExtraTxos)
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
	sendResultResponse(w, map[string]any{
		"streaming_server": "localhost:5280",
		"save_files":       false,
		"share_usage_data": false,
		"download_dir":     "",
	})
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
