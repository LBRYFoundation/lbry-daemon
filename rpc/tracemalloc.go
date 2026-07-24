package rpc

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
)

type traceAllocation struct {
	Line  string `json:"line"`
	Code  string `json:"code"`
	Size  int64  `json:"size"`
	Count int64  `json:"count"`
}

func (rpcServer *RPCServer) handleTracemallocEnable(response http.ResponseWriter, _ any) {
	rpcServer.traceMu.Lock()
	rpcServer.traceEnabled = true
	rpcServer.traceMu.Unlock()
	sendResultResponse(response, true)
}

func (rpcServer *RPCServer) handleTracemallocDisable(response http.ResponseWriter, _ any) {
	rpcServer.traceMu.Lock()
	rpcServer.traceEnabled = false
	rpcServer.traceMu.Unlock()
	sendResultResponse(response, false)
}

func (rpcServer *RPCServer) handleTracemallocTop(response http.ResponseWriter, params any) {
	rpcServer.traceMu.Lock()
	enabled := rpcServer.traceEnabled
	rpcServer.traceMu.Unlock()
	if !enabled {
		panic(errors.New("Enable tracemalloc first! See 'tracemalloc set' command."))
	}

	normalized := params.(normalizedRPCParams)
	limit := walletListPositiveInteger(normalized.named["items"], 10)
	sendResultResponse(response, topHeapAllocations(limit))
}

func topHeapAllocations(limit int) []traceAllocation {
	n, _ := runtime.MemProfile(nil, true)
	var records []runtime.MemProfileRecord
	for {
		records = make([]runtime.MemProfileRecord, n)
		var ok bool
		n, ok = runtime.MemProfile(records, true)
		if ok && n <= len(records) {
			records = records[:n]
			break
		}
	}

	results := make([]traceAllocation, 0, len(records))
	for index := range records {
		pcs := records[index].Stack()
		if len(pcs) == 0 {
			continue
		}
		frame, _ := runtime.CallersFrames(pcs).Next()
		directory, file := filepath.Split(frame.File)
		parent := filepath.Base(filepath.Clean(directory))
		results = append(results, traceAllocation{
			Line:  fmt.Sprintf("%s:%d", filepath.Join(parent, file), frame.Line),
			Code:  frame.Function,
			Size:  records[index].InUseBytes(),
			Count: records[index].InUseObjects(),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Size == results[j].Size {
			return results[i].Line < results[j].Line
		}
		return results[i].Size > results[j].Size
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
