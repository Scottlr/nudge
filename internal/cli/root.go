// Package cli owns Nudge's Cobra command tree and process-facing composition.
package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

// BuildInfo contains the build metadata displayed by the version command.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// NewRootCommand creates the Nudge command tree with injected build metadata.
// The root command intentionally shows help until the local review flow is
// supplied by T016.
func NewRootCommand(info BuildInfo) *cobra.Command {
	command := &cobra.Command{
		Use:           "nudge [path]",
		Short:         "Review local Git changes safely.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newVersionCommand(info))
	return command
}

// Execute runs the Nudge command tree with the supplied context and build
// metadata. User-visible error rendering remains the responsibility of main.
func Execute(ctx context.Context, info BuildInfo) error {
	if ctx == nil {
		return errors.New("execute CLI: nil context")
	}
	return NewRootCommand(info).ExecuteContext(ctx)
}
