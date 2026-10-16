package main

import (
	"github.com/kingfs/Trajecta/internal/cli"
	"github.com/spf13/cobra"
)

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion bash|zsh|fish|powershell",
		Short: "Generate shell completion scripts",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return cli.UsageError("completion requires one shell: bash, zsh, fish, or powershell", "shell")
			}
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return cli.UsageError("unsupported shell "+args[0], "shell")
			}
		},
	}
	return cmd
}
