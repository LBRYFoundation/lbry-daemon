package rpc

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"runtime/debug"
	"strings"
)

func StartServer() {
	http.HandleFunc("/", handleJSONRPC)

	http.ListenAndServe(":5279", nil)
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
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"error": map[string]any{
					"code":    -32700,
					"message": "Cannot parse invalid JSON data.",
				},
			})
			fmt.Fprint(w, string(resp))
			return
		}
		if messageBatch != nil {
			w.WriteHeader(http.StatusBadRequest)
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"error": map[string]any{
					"code":    -32700,
					"message": "Batches are not supported",
				},
			})
			fmt.Fprint(w, string(resp))
			return
		}

		handleJSONRPCMessage(w, message)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32700,
			"message": "HTTP method not allowed.",
		},
	})
	fmt.Fprint(w, string(resp))
}

func handleJSONRPCMessage(w http.ResponseWriter, message map[string]any) {
	if message["method"] == "status" {
		handleJSONRPCMessageStatus(w)
		return
	}
	if message["method"] == "version" {
		handleJSONRPCMessageVersion(w)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32601,
			"message": "Unknown JSON-RPC method.",
		},
	})
	fmt.Fprint(w, string(resp))
}

func handleJSONRPCMessageStatus(w http.ResponseWriter) {
	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{},
	})
	fmt.Fprint(w, string(resp))
}

func handleJSONRPCMessageVersion(w http.ResponseWriter) {
	info, _ := debug.ReadBuildInfo()

	resp, _ := json.Marshal(map[string]any{
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
	fmt.Fprint(w, string(resp))
}
