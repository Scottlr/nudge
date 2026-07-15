// Package cli owns Nudge's Cobra command tree and process-facing composition.
package cli

import (
	"context"
	"errors"
	"os"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/spf13/cobra"
)

// BuildInfo contains the build metadata displayed by the version command.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// NewRootCommand creates the Nudge command tree with injected build metadata.
// The root invocation opens the selected local or current-branch review.
func NewRootCommand(info BuildInfo) *cobra.Command {
	var noPersist bool
	var themeName string
	var local bool
	var commitExpression string
	var branchExpression string
	command := &cobra.Command{
		Use:           "nudge [path]",
		Short:         "Review Git changes safely.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if branchExpression != "" && app.ValidateBaseBranchExpression(branchExpression) != nil {
				return errors.New("invalid --branch base expression")
			}
			if commitExpression != "" && app.ValidateCommitExpression(commitExpression) != nil {
				return errors.New("invalid --commit revision expression")
			}
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
			var themeOverride *string
			if cmd.Flags().Changed("theme") {
				themeOverride = &themeName
			}
			_ = local
			return runLocalReview(cmd.Context(), path, noPersist, themeOverride, branchExpression, commitExpression)
		},
	}
	command.Flags().BoolVar(&noPersist, "no-persist", false, "Run without saving review state.")
	command.Flags().StringVar(&themeName, "theme", "", "Use a built-in or protected user theme.")
	command.Flags().BoolVar(&local, "local", false, "Review current working-tree changes.")
	command.Flags().StringVar(&commitExpression, "commit", "", "Review one frozen commit.")
	command.Flags().StringVar(&branchExpression, "branch", "", "Compare the current branch with a base branch.")
	command.MarkFlagsMutuallyExclusive("local", "commit", "branch")
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
