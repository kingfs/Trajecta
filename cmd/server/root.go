package main

import (
	"github.com/kingfs/Trajecta/internal/cli"
	"github.com/spf13/cobra"
)

// cliName is the server artifact name: it drives the usage text and the
// version payload. The product name (for example the Codex model_provider) is
// separate and stays "trajecta".
const cliName = "server"

func run(args []string) int {
	runtime := cli.NewRuntime()
	cmd := newRootCommandWithRuntime(runtime)
	return cli.Execute(args, cmd, runtime)
}

// newRootCommand builds the server command tree. It exists for the tests; the
// process entry point builds the tree with its own runtime.
func newRootCommand() *cobra.Command {
	return newRootCommandWithRuntime(cli.NewRuntime())
}

// newRootCommandWithRuntime wires the server binary: the bare invocation and
// `serve` start the proxy, and completion and version describe this artifact.
//
// Every server-management operation (db, auth, analyze, audit, doctor, models,
// provider, tools, config, migrate, schema) lives in the trajecta CLI instead;
// see internal/cli.
func newRootCommandWithRuntime(runtime *cli.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           cliName,
		Short:         "Postgres-first LLM gateway with local Responses runtime and record/replay",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return runtime.ValidateOutputFormat()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.RunCode(func() int {
				return runServeWithConfig(runtime.ConfigPath())
			})
		},
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return cli.UsageError(err.Error(), "flag")
	})
	cmd.PersistentFlags().StringP("config", "c", "config.yaml", "Path to configuration file")
	cmd.PersistentFlags().String("format", "text", "Output format: text or json")
	runtime.BindPFlag("config", cmd.PersistentFlags().Lookup("config"))
	runtime.BindPFlag("format", cmd.PersistentFlags().Lookup("format"))
	cmd.AddCommand(
		newServeCommand(runtime),
		newVersionCommand(runtime),
		newCompletionCommand(cmd),
	)
	return cmd
}
