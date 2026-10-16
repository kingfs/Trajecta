package main

import (
	"os"

	"github.com/kingfs/Trajecta/internal/cli"
)

// Build metadata is injected with -ldflags by the Taskfile and the Dockerfile.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	Branch  = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	root := newRootCommand()
	return cli.Execute(os.Args[1:], root, activeOpsRuntime)
}
