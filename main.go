package main

import "lbry/daemon/rpc"

func main() {
	rpc.StartServer(rpc.CreateServer(), 5279)
}
