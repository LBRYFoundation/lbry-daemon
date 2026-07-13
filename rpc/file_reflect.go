package rpc

import (
	"fmt"
	"net"
	"net/http"
	"strconv"

	blobpkg "lbry/daemon/blob"
	daemonconfig "lbry/daemon/config"
	reflectorpkg "lbry/daemon/reflector"
)

func (rpcServer *RPCServer) handleFileReflect(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	rows, err := rpcServer.managedFileLister.ListManagedFiles(normalized.ctx)
	if err != nil {
		panic(err)
	}
	filters := make(map[string]any)
	for _, name := range []string{"sd_hash", "file_name", "stream_hash", "rowid"} {
		if value := normalized.named[name]; value != nil {
			filters[name] = value
		}
	}
	rows, err = filterManagedFiles(rows, "rowid", "eq", normalizedRPCParams{named: filters})
	if err != nil {
		panic(err)
	}
	address, err := rpcServer.reflectorAddress(normalized)
	if err != nil {
		panic(err)
	}
	reflected := make([]string, 0)
	for _, row := range rows {
		hashes, reflectErr := reflectorpkg.ReflectStream(normalized.ctx, address, rpcServer.blobManager, row.SDHash)
		reflected = append(reflected, hashes...)
		if reflectErr == nil && rpcServer.reflectionComplete(row.SDHash, hashes) {
			if store, ok := rpcServer.managedFileLister.(ManagedReflectorStore); ok {
				if err := store.MarkStreamReflected(normalized.ctx, row.SDHash, address); err != nil {
					panic(err)
				}
			}
		}
	}
	sendResultResponse(response, reflected)
}

func (rpcServer *RPCServer) reflectionComplete(sdHash string, reflected []string) bool {
	if len(reflected) == 0 {
		return true
	}
	data, ok := rpcServer.blobManager.Get(sdHash)
	if !ok {
		return false
	}
	descriptor, err := blobpkg.DecodeDescriptor(sdHash, data)
	return err == nil && len(reflected) == len(descriptor.ContentBlobs())+1
}

func (rpcServer *RPCServer) reflectorAddress(normalized normalizedRPCParams) (string, error) {
	host, _ := normalized.named["server"].(string)
	port := reflectorPort(normalized.named["port"])
	if host != "" && port > 0 {
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	value, _ := rpcServer.settings.Get("reflector_servers")
	servers, _ := value.([]daemonconfig.Server)
	if len(servers) == 0 {
		return "", fmt.Errorf("no reflector servers are configured")
	}
	port = reflectorPort(servers[0].Port)
	if servers[0].Host == "" || port <= 0 {
		return "", fmt.Errorf("invalid reflector server configuration")
	}
	return net.JoinHostPort(servers[0].Host, strconv.Itoa(port)), nil
}

func reflectorPort(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		port, _ := strconv.Atoi(typed)
		return port
	default:
		return 0
	}
}
