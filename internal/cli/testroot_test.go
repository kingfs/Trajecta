package cli

import (
	"github.com/spf13/cobra"
)

// rootName is the command-tree name of the test root.
const rootName = "trajecta"

// newRootCommand builds the command tree under test: the shared --config and
// --format flags plus every registered server-management group.
//
// The version and completion commands belong to the embedding binary, not to
// this package, so they are absent here; the binaries test those themselves.
func newRootCommand() *cobra.Command {
	root, _ := newRootCommandWithRuntime()
	return root
}

func newRootCommandWithRuntime() (*cobra.Command, *Runtime) {
	root := &cobra.Command{
		Use:           "trajecta",
		Short:         "Trajecta command-line tool",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringP("config", "c", "config.yaml", "path to the configuration file")
	root.PersistentFlags().String("format", "text", "output format: text or json")

	runtime := NewRuntime()
	runtime.BindPFlag("config", root.PersistentFlags().Lookup("config"))
	runtime.BindPFlag("format", root.PersistentFlags().Lookup("format"))
	Register(root, runtime, Options{Name: "trajecta", Version: Version})
	return root, runtime
}

// run executes the test command tree under the shared top-level contract and
// returns the process exit code.
func run(args []string) int {
	cmd, runtime := newRootCommandWithRuntime()
	return Execute(args, cmd, runtime)
}
