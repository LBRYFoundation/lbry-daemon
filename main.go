package main

import "fmt"
import "lbry/daemon/dht"
import "lbry/daemon/stream"
import "lbry/daemon/rpc"
import "net"
import "net/http"
import "strconv"
import "sync"

var wg sync.WaitGroup

func main() {
	node, _ := dht.NewNode(4444)
	rpcServer := rpc.CreateServer()
	contentServer := stream.CreateServer(stream.NewManager(node))

	wg.Go(func() {
		fmt.Println("Starting DHT server on port 4444.")
		// node.TCPPort = 5567
		node.Start()
	})

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
