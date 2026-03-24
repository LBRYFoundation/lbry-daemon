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
	"status":  handleJSONRPCMessageStatus,
	"version": handleJSONRPCMessageVersion,
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

func handleJSONRPCMessageStatus(w http.ResponseWriter, params any) {
	sendResultResponse(w, map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{},
	})
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
