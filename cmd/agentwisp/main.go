package main

import (
	"agentwisp/internal/agentwisp"
	"fmt"
	"os"
)

func main() {
	if err := agentwisp.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agentwisp:", err)
		os.Exit(1)
	}
}
