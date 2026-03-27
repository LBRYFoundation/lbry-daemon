package main

import "fmt"
import "net"
import "net/http"
import "strconv"
import "sync"
import "lbry/daemon/stream"
import "lbry/daemon/rpc"

var wg sync.WaitGroup

func main() {
	rpcServer := rpc.CreateServer()
	contentServer := stream.CreateServer(nil)

	wg.Go(func() {
		fmt.Println("Starting RPC server on port 5279.")
		startHTTPServer(rpcServer, 5279)
	})

	wg.Go(func() {
		fmt.Println("Starting content server on port 5280.")
		startHTTPServer(contentServer, 5280)
	})

	wg.Wait()

	fmt.Println("All servers have stopped.")
}

func startHTTPServer(rpcServer *http.Server, port int) {
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil && err != http.ErrServerClosed {
		fmt.Println("Error when starting listening.")
	}

	err = rpcServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		fmt.Println("Error when starting HTTP server.")
	}
}
