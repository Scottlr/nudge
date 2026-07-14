// Package cli owns Nudge's Cobra command tree and process-facing composition.
package cli

import (
	"context"
	"errors"
	"os"

	"github.com/spf13/cobra"
)

// BuildInfo contains the build metadata displayed by the version command.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// NewRootCommand creates the Nudge command tree with injected build metadata.
// The root invocation is the local-review command; future target modes are
// added only when their complete behavior is implemented.
func NewRootCommand(info BuildInfo) *cobra.Command {
	command := &cobra.Command{
		Use:           "nudge [path]",
		Short:         "Review local Git changes safely.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			var err error
			if len(args) == 1 {
				path = args[0]
			}
			if path == "" {
				path, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			return runLocalReview(cmd.Context(), path)
		},
	}
	command.CompletionOptions.DisableDefaultCmd = true
	command.AddCommand(newVersionCommand(info))
	command.AddCommand(newConfigCommand())
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
