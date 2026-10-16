package cli

import (
	"github.com/spf13/cobra"
)

// rootName is the command-tree name of the test root.
const rootName = "trajecta"

// testOpsRuntime is the shared runtime of the most recently built test root. The
// run helper hands it to Execute so the tests exercise the same top-level error
// path the trajecta binary uses.
var testOpsRuntime *Runtime

// newRootCommand builds the command tree under test: the shared --config and
// --format flags plus every registered server-management group.
//
// The version and completion commands belong to the embedding binary, not to
// this package, so they are absent here; the binaries test those themselves.
func newRootCommand() *cobra.Command {
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
	testOpsRuntime = runtime
	Register(root, runtime, Options{Name: "trajecta", Version: Version})
	return root
}

// run executes the test command tree under the shared top-level contract and
// returns the process exit code.
func run(args []string) int {
	cmd := newRootCommand()
	return Execute(args, cmd, testOpsRuntime)
}
