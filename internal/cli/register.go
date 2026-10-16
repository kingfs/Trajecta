package cli

import "github.com/spf13/cobra"

// Register adds every server-management command group to root.
//
// The caller owns the root command and its persistent --config/--format flags;
// rt must be bound to those flags so the groups resolve the same values. The
// per-binary commands (version, completion) and the runtime command (serve) are
// deliberately not registered here.
func Register(root *cobra.Command, rt *Runtime, opts Options) {
	if root == nil {
		return
	}
	if rt == nil {
		rt = NewRuntime()
	}
	rt.name = opts.Name
	rt.version = opts.Version

	root.AddCommand(
		newAnalyzeCommand(rt),
		newMigrateCommand(rt),
		newDBCommand(rt),
		newConfigCommand(rt),
		newDoctorCommand(rt),
		newProviderCommand(rt),
		newModelsCommand(rt),
		newToolsCommand(rt),
		newAuditCommand(rt),
		newAuthCommand(rt),
		newSchemaCommand(rt, root),
	)
}
