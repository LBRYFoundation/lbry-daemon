package main

import "fmt"
import "lbry/daemon/blob"
import "lbry/daemon/dht"
import "lbry/daemon/peer"
import "lbry/daemon/stream"
import "lbry/daemon/reflector"
import "lbry/daemon/rpc"
import "net"
import "strconv"
import "sync"

var wg sync.WaitGroup

func main() {
	blobManager := blob.BlobManager{}
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
		listener, err := getTCPListener("", 5279)
		if err != nil {
			fmt.Println("Error when getting TCP listener.")
		}
		defer listener.Close()
		rpc.StartServer(rpcServer, listener)
	})

	wg.Go(func() {
		fmt.Println("Starting content server on port 5280.")
		listener, err := getTCPListener("", 5280)
		if err != nil {
			fmt.Println("Error when getting TCP listener.")
		}
		defer listener.Close()
		stream.StartServer(contentServer, listener)
	})

	wg.Go(func() {
		fmt.Println("Starting reflector server on port 5566.")
		listener, err := getTCPListener("", 5566)
		if err != nil {
			fmt.Println("Error when getting TCP listener.")
		}
		defer listener.Close()
		reflector.StartServer(blobManager, listener)
	})

	wg.Go(func() {
		fmt.Println("Starting peer server on port 5567.")
		listener, err := getTCPListener("", 5567)
		if err != nil {
			fmt.Println("Error when getting TCP listener.")
		}
		defer listener.Close()
		peer.StartServer(blobManager, listener)
	})

	wg.Wait()

	fmt.Println("All servers have stopped.")
}

func getTCPListener(hostname string, port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(hostname, strconv.Itoa(port)))
}
