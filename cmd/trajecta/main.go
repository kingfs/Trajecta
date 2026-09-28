package main

import (
	"os"
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
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	if err := root.Execute(); err != nil {
		command := ""
		if activeRuntime != nil {
			command = activeRuntime.command
		}
		activeRuntime.writeError(os.Stdout, command, err)
		return exitCodeFor(err)
	}
	return exitOK
}
