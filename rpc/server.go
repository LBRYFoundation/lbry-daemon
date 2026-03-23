package rpc

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
)

func StartServer() {
	http.HandleFunc("/", handleJSONRPC)

	http.ListenAndServe(":5279", nil)
}

func handleJSONRPC(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if strings.EqualFold(req.Method, "POST") {
		body, _ := ioutil.ReadAll(req.Body)
		var message map[string]any
		err := json.Unmarshal(body, &message)
		if err != nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
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
	fmt.Fprintf(w, "MSG = %v", message)
}
