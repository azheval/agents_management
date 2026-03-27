package main

import (
	"agent-management/agent/internal"
	"flag"
)

func main() {
	serverAddr := flag.String("server-addr", "localhost:50051", "The address of the gRPC server.")
	flag.Parse()
	internal.StartAgent(*serverAddr)
}
