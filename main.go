package main

import (
	"fmt"
	"lbry/daemon/auth"
	"lbry/daemon/blob"
	"lbry/daemon/dht"
	"lbry/daemon/peer"
	"lbry/daemon/reflector"
	"lbry/daemon/rpc"
	"lbry/daemon/settings"
	"lbry/daemon/stream"
	"lbry/daemon/wallet"
	"net"
	"strconv"
	"sync"
)

var wg sync.WaitGroup

func main() {
	config := settings.InitializeConfiguration()

	node, _ := dht.NewNode(4444)
	//blob.NewManager(node)

	authManager, authenticated := auth.NewAuthManager()
	if authenticated {
		fmt.Println("Requests need the admin credentials (ADMIN_USERNAME and ADMIN_PASSWORD).")
	} else {
		fmt.Println("WARNING: ADMIN_USERNAME and ADMIN_PASSWORD are not set, so every request is accepted.")
	}

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
			listener, err := getTCPListener("127.0.0.1", 5279)
			if err != nil {
				fmt.Println("Failed listening on TCP port for RPC server.")
				return
			}
			defer func(listener net.Listener) {
				err := listener.Close()
				if err != nil {
					fmt.Println("Failed stopping RPC server.")
					return
				}
			}(listener)
			rpcServer.Start(listener)
		})
		runningServers++
	}

	if contentServer != nil {
		wg.Go(func() {
			fmt.Println("Starting content server on port 5280.")
			listener, err := getTCPListener("", 5280)
			if err != nil {
				fmt.Println("Failed listening on TCP port for content server.")
				return
			}
			defer func(listener net.Listener) {
				err := listener.Close()
				if err != nil {
					fmt.Println("Failed stopping content server.")
					return
				}
			}(listener)
			contentServer.Start(listener)
		})
		runningServers++
	}

	if reflectorServer != nil {
		wg.Go(func() {
			fmt.Println("Starting reflector server on port 5566.")
			listener, err := getTCPListener("", 5566)
			if err != nil {
				fmt.Println("Failed listening on TCP port for reflector server.")
				return
			}
			defer func(listener net.Listener) {
				err := listener.Close()
				if err != nil {
					fmt.Println("Failed stopping reflector server.")
					return
				}
			}(listener)
			reflectorServer.Start(listener)
		})
		runningServers++
	}

	if peerServer != nil {
		wg.Go(func() {
			fmt.Println("Starting peer server on port 5567.")
			listener, err := getTCPListener("", 5567)
			if err != nil {
				fmt.Println("Failed listening on TCP port for peer server.")
				return
			}
			defer func(listener net.Listener) {
				err := listener.Close()
				if err != nil {
					fmt.Println("Failed stopping peer server.")
					return
				}
			}(listener)
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
