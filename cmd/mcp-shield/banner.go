package main

import (
	"fmt"
	"os"
)

// printBanner writes a prominent startup banner to stderr so operators can
// immediately confirm that mcp-shield is active.
func printBanner(securityMode, monitorAddr, transport string) {
	fmt.Fprintf(
		// stderr — stdout is reserved for JSON-RPC in stdio mode.
		os.Stderr,
		"\n"+
			"========================================\n"+
			"  mcp-shield is running\n"+
			"  transport : %s\n"+
			"  security  : %s\n"+
			"  monitor   : http://%s\n"+
			"========================================\n\n",
		transport, securityMode, monitorAddr,
	)
}
