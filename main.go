package main

import "fmt"
import "lbry/daemon/auth"
import "lbry/daemon/blob"
import "lbry/daemon/dht"
import "lbry/daemon/peer"
import "lbry/daemon/settings"
import "lbry/daemon/stream"
import "lbry/daemon/reflector"
import "lbry/daemon/rpc"
import "lbry/daemon/wallet"
import "net"
import "strconv"
import "sync"

var wg sync.WaitGroup

func main() {
	config := settings.InitializeConfiguration()

	node, _ := dht.NewNode(4444)
	//blob.NewManager(node)

	authManager := &auth.AuthManager{}
	blobManager := &blob.BlobManager{
		Blobs: map[string][]byte{},
	}
	walletManager := wallet.NewDefaultWalletManager(wallet.GetDefaultWalletDirectory())
	if err := walletManager.(*wallet.DefaultWalletManager).Load(); err != nil {
		fmt.Println("Error when loading wallets:", err)
	}

	rpcServer := rpc.NewRPCServer(config, authManager, blobManager, node, walletManager)
	contentServer := stream.NewContentServer(blobManager)
	reflectorServer := reflector.NewReflectorServer(blobManager)
	peerServer := peer.NewPeerServer(blobManager)

	runningServers := 0

	if node != nil {
		wg.Go(func() {
			fmt.Println("Starting DHT server on port 4444.")
			// node.TCPPort = 5567
			node.Start()
		})
		runningServers++
	}

	if rpcServer != nil {
		wg.Go(func() {
			fmt.Println("Starting RPC server on port 5279.")
			listener, err := getTCPListener("", 5279)
			if err != nil {
				fmt.Println("Error when getting TCP listener.")
				return
			}
			defer listener.Close()
			rpcServer.Start(listener)
		})
		runningServers++
	}

	if contentServer != nil {
		wg.Go(func() {
			fmt.Println("Starting content server on port 5280.")
			listener, err := getTCPListener("", 5280)
			if err != nil {
				fmt.Println("Error when getting TCP listener.")
				return
			}
			defer listener.Close()
			contentServer.Start(listener)
		})
		runningServers++
	}

	if reflectorServer != nil {
		wg.Go(func() {
			fmt.Println("Starting reflector server on port 5566.")
			listener, err := getTCPListener("", 5566)
			if err != nil {
				fmt.Println("Error when getting TCP listener.")
				return
			}
			defer listener.Close()
			reflectorServer.Start(listener)
		})
		runningServers++
	}

	if peerServer != nil {
		wg.Go(func() {
			fmt.Println("Starting peer server on port 5567.")
			listener, err := getTCPListener("", 5567)
			if err != nil {
				fmt.Println("Error when getting TCP listener.")
				return
			}
			defer listener.Close()
			peerServer.Start(listener)
		})
		runningServers++
	}

	if runningServers == 0 {
		fmt.Println("No servers running. Exiting.")
	}

	wg.Wait()

	if runningServers != 0 {
		fmt.Println("All servers have stopped.")
	}
}

func getTCPListener(hostname string, port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort(hostname, strconv.Itoa(port)))
}
