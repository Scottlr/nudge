package cli

import (
	"fmt"

	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Inspect Nudge configuration.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the Nudge configuration path.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			locations, err := paths.Resolve(nil)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), presentation.ProjectTerminalText(locations.ConfigPath(), presentation.TerminalTextScalar))
			return err
		},
	})
	return command
}
