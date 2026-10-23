// Command memorycard is a local episodic memory for coding agents.
package main

import (
	"os"

	"github.com/labdrian-ai/memorycard/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
