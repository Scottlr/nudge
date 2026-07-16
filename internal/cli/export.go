package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/artifactspool"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	markdown "github.com/Scottlr/nudge/internal/export"
	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/store/sqlite"
	"github.com/spf13/cobra"
)

type exportOptions struct {
	output  string
	version uint64
}

func newExportCommand() *cobra.Command {
	command := &cobra.Command{Use: "export", Short: "Export one review thread or proposal as local Markdown."}
	thread := &cobra.Command{
		Use:   "thread <thread-id>",
		Short: "Export one review thread.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd.Context(), cmd, app.ExportThreadKind, args[0], 0)
		},
	}
	thread.Flags().String("output", "-", "Write Markdown to this path, or - for stdout.")
	proposalOptions := exportOptions{output: "-"}
	proposal := &cobra.Command{
		Use:   "proposal <proposal-id>",
		Short: "Export one immutable proposal version.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd.Context(), cmd, app.ExportProposalKind, args[0], review.ProposalVersionNumber(proposalOptions.version))
		},
	}
	proposal.Flags().StringVar(&proposalOptions.output, "output", "-", "Write Markdown to this path, or - for stdout.")
	proposal.Flags().Uint64Var(&proposalOptions.version, "version", 0, "Proposal version to export; defaults to the current version.")
	command.AddCommand(thread, proposal)
	return command
}

func runExport(ctx context.Context, command *cobra.Command, kind app.ExportKind, rawID string, version review.ProposalVersionNumber) error {
	if ctx == nil || command == nil || rawID == "" {
		return app.ErrExportInput
	}
	output, err := command.Flags().GetString("output")
	if err != nil {
		return err
	}
	if output == "" {
		return errors.New("export: --output cannot be empty")
	}
	locations, err := paths.Resolve(processEnvironment(os.Environ()))
	if err != nil {
		return fmt.Errorf("export: locations: %w", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(locations.StateRoot, "nudge.db"))
	if err != nil {
		return fmt.Errorf("export: database: %w", err)
	}
	defer store.Close()
	var selection app.ExportSelection
	switch kind {
	case app.ExportThreadKind:
		selection, err = app.SelectThread(ctx, store, domain.ReviewThreadID(rawID))
	case app.ExportProposalKind:
		selection, err = app.SelectProposal(ctx, store, domain.ProposalID(rawID), version)
	default:
		return app.ErrExportInput
	}
	if err != nil {
		return fmt.Errorf("export selection: %w", err)
	}
	var patchReader app.ProposalPatchReader
	if selection.Kind == app.ExportProposalKind && selection.Proposal.Artifact != nil {
		artifacts, managerErr := artifactspool.NewManager(filepath.Join(locations.CacheRoot, "artifacts"))
		if managerErr != nil {
			return fmt.Errorf("export: artifact storage: %w", managerErr)
		}
		patchReader = artifacts
	}
	return writeExport(ctx, output, command.OutOrStdout(), selection, store, patchReader)
}

func writeExport(ctx context.Context, output string, stdout io.Writer, selection app.ExportSelection, source app.ExportSource, patches app.ProposalPatchReader) error {
	if output == "-" {
		return markdown.WriteMarkdown(ctx, selection, source, patches, stdout)
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	parentInfo, err := os.Lstat(filepath.Dir(abs))
	if err != nil {
		return err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return paths.ErrProtectedPath
	}
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(abs)
		}
	}()
	if err := markdown.WriteMarkdown(ctx, selection, source, patches, file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
