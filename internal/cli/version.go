package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCommand(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), formatVersion(info))
			return err
		},
	}
}

func formatVersion(info BuildInfo) string {
	version := info.Version
	if version == "" {
		version = "dev"
	}
	commit := info.Commit
	if commit == "" {
		commit = "unknown"
	}
	date := info.Date
	if date == "" {
		date = "unknown"
	}
	return fmt.Sprintf("nudge version=%s commit=%s date=%s", version, commit, date)
}
