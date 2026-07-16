package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/artifactspool"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/gitcli"
	"github.com/Scottlr/nudge/internal/paths"
	protectedlogging "github.com/Scottlr/nudge/internal/privacy/logging"
	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/store/sqlite"
	"github.com/Scottlr/nudge/internal/workspace"
	"github.com/spf13/cobra"
)

type cleanupOptions struct {
	planID string
	yes    bool
}

type cleanupRuntime struct {
	service *app.CleanupService
	store   *sqlite.Store
}

func newCleanupCommand() *cobra.Command {
	options := cleanupOptions{}
	command := &cobra.Command{
		Use:   "cleanup [path]",
		Short: "Preview and remove one repository's Nudge state.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.planID != "" {
				if !options.yes || len(args) != 0 {
					return errors.New("cleanup execution requires --plan <id> --yes and no path")
				}
				return runCleanupPlan(cmd.Context(), cmd, options.planID)
			}
			if options.yes {
				return errors.New("--yes requires an exact --plan <id>")
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return runCleanupPreview(cmd.Context(), cmd, path)
		},
	}
	command.Flags().StringVar(&options.planID, "plan", "", "Execute one previously previewed plan by exact ID.")
	command.Flags().BoolVar(&options.yes, "yes", false, "Confirm the exact plan; only valid with --plan.")
	return command
}

func runCleanupPreview(ctx context.Context, command *cobra.Command, startPath string) error {
	locations, repositoryID, err := resolveCleanupRepository(ctx, startPath)
	if err != nil {
		return err
	}
	runtime, err := newCleanupRuntime(ctx, locations, repositoryID)
	if err != nil {
		return err
	}
	defer runtime.store.Close()
	plan, err := runtime.service.PlanRepositoryCleanup(ctx, repositoryID)
	if err != nil {
		return fmt.Errorf("cleanup preview: %w", err)
	}
	if err := writeCleanupPlan(command.OutOrStdout(), plan); err != nil {
		return err
	}
	if len(plan.Blockers) != 0 {
		return app.ErrCleanupConflict
	}
	if _, err := fmt.Fprintln(command.OutOrStdout(), "Type yes to continue, or use the printed --plan command for an explicit non-interactive confirmation:"); err != nil {
		return err
	}
	if _, err := io.WriteString(command.OutOrStdout(), "confirmation> "); err != nil {
		return err
	}
	line, readErr := bufio.NewReader(io.LimitReader(command.InOrStdin(), 32)).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if strings.TrimSpace(line) != "yes" {
		return app.ErrCleanupConfirmation
	}
	operation, err := runtime.service.Execute(ctx, app.CleanupRequest{PlanID: plan.ID, Confirmation: "yes"})
	if err != nil {
		return fmt.Errorf("cleanup execute: %w", err)
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Cleanup complete: operation %s (%s).\n", operation.ID, operation.Outcome)
	return err
}

func runCleanupPlan(ctx context.Context, command *cobra.Command, planID string) error {
	locations, err := paths.Resolve(processEnvironment(os.Environ()))
	if err != nil {
		return fmt.Errorf("cleanup: locations: %w", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(locations.StateRoot, "nudge.db"))
	if err != nil {
		return fmt.Errorf("cleanup: database: %w", err)
	}
	plan, err := store.LoadCleanupPlan(ctx, planID)
	_ = store.Close()
	if err != nil {
		return fmt.Errorf("cleanup plan: %w", err)
	}
	runtime, err := newCleanupRuntime(ctx, locations, plan.RepositoryID)
	if err != nil {
		return err
	}
	defer runtime.store.Close()
	operation, err := runtime.service.Execute(ctx, app.CleanupRequest{PlanID: planID, Confirmation: "yes"})
	if err != nil {
		return fmt.Errorf("cleanup execute: %w", err)
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Cleanup complete: operation %s (%s).\n", operation.ID, operation.Outcome)
	return err
}

func resolveCleanupRepository(ctx context.Context, startPath string) (paths.Locations, domain.RepositoryID, error) {
	if ctx == nil {
		return paths.Locations{}, "", errors.New("cleanup: nil context")
	}
	if startPath == "" {
		var err error
		startPath, err = os.Getwd()
		if err != nil {
			return paths.Locations{}, "", fmt.Errorf("cleanup: current directory: %w", err)
		}
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return paths.Locations{}, "", fmt.Errorf("cleanup: path: %w", err)
	}
	startPath = filepath.Clean(abs)
	environ := processEnvironment(os.Environ())
	locations, err := paths.Resolve(environ)
	if err != nil {
		return paths.Locations{}, "", fmt.Errorf("cleanup: locations: %w", err)
	}
	trusted, err := process.NewExecutableResolver().Resolve(ctx, process.ResolveExecutableRequest{
		Kind: process.ExecutableGit, SearchPath: environ["PATH"], CurrentDir: startPath,
		WorkspaceRoots:     []string{locations.WorkspaceRoot},
		NudgeWritableRoots: []string{locations.ConfigRoot, locations.StateRoot, locations.CacheRoot, locations.LogRoot},
	})
	if err != nil {
		return paths.Locations{}, "", fmt.Errorf("cleanup: resolve Git: %w", err)
	}
	resolver, err := gitcli.NewResolver(gitcli.ResolverConfig{Executable: trusted, Runner: process.NewRunner(), Policy: gitcli.DefaultMachineGitReadPolicyV1()})
	if err != nil {
		return paths.Locations{}, "", fmt.Errorf("cleanup: repository resolver: %w", err)
	}
	repository, _, err := resolver.ResolveRepository(ctx, startPath)
	if err != nil {
		return paths.Locations{}, "", fmt.Errorf("cleanup: repository: %w", err)
	}
	return locations, repository.ID, nil
}

func newCleanupRuntime(ctx context.Context, locations paths.Locations, repositoryID domain.RepositoryID) (*cleanupRuntime, error) {
	store, err := sqlite.Open(ctx, filepath.Join(locations.StateRoot, "nudge.db"))
	if err != nil {
		return nil, fmt.Errorf("cleanup: database: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = store.Close()
		}
	}()
	leases, err := filelock.NewSessionLeaseManager(locations.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("cleanup: session locks: %w", err)
	}
	gate, err := filelock.NewRepositoryMaintenanceGate(locations.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("cleanup: maintenance gate: %w", err)
	}
	artifactManager, err := artifactspool.NewManager(filepath.Join(locations.CacheRoot, "artifacts"))
	if err != nil {
		return nil, fmt.Errorf("cleanup: artifact storage: %w", err)
	}
	allocator, err := workspace.NewAllocator(locations.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("cleanup: workspace storage: %w", err)
	}
	retirement, err := workspace.NewRetirementExecutor(allocator, store)
	if err != nil {
		return nil, fmt.Errorf("cleanup: workspace owner: %w", err)
	}
	snapshots, err := workspace.NewReviewSnapshotManager(workspace.ReviewSnapshotConfig{
		Root: filepath.Join(locations.CacheRoot, "review-snapshots"), CleanupOnly: true, Store: store, Persist: true,
		Policy: app.DefaultResourcePolicy(),
	})
	if err != nil {
		return nil, fmt.Errorf("cleanup: snapshot owner: %w", err)
	}
	logs, err := protectedlogging.NewRepositoryCleanupOwner(filepath.Join(locations.LogRoot, "process"), repositoryID)
	if err != nil {
		return nil, fmt.Errorf("cleanup: log owner: %w", err)
	}
	service, err := app.NewCleanupService(app.CleanupService{
		Inventory: store, Journal: store, Gate: gate,
		Quiescer:    app.SessionLockQuiescer{Store: store, Leases: leases},
		Enumerators: []app.CleanupResourceEnumerator{logs},
		Owners: map[app.CleanupResourceKind]app.CleanupResourceOwner{
			app.CleanupResourceCapture:        artifactspool.CleanupOwner{Manager: artifactManager},
			app.CleanupResourceProposal:       artifactspool.CleanupOwner{Manager: artifactManager},
			app.CleanupResourceReviewSnapshot: workspace.ReviewSnapshotCleanupOwner{Manager: snapshots},
			app.CleanupResourceWorkspace:      workspace.WorkspaceCleanupOwner{Executor: retirement},
			app.CleanupResourceLog:            logs,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("cleanup: service: %w", err)
	}
	closeOnError = false
	return &cleanupRuntime{service: service, store: store}, nil
}

func writeCleanupPlan(output io.Writer, plan app.CleanupPlan) error {
	if output == nil || plan.Validate() != nil {
		return app.ErrCleanupInvalid
	}
	if _, err := fmt.Fprintf(output, "Nudge cleanup preview\nRepository: %s\nPlan: %s\nRevision: %s\nManifest: %s\n\n", plan.RepositoryDisplay, plan.ID, plan.ObservedRevision, plan.ManifestHash); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Database rows:"); err != nil {
		return err
	}
	rows := []struct {
		name  string
		value uint64
	}{
		{"repositories", plan.Rows.Repositories}, {"worktrees", plan.Rows.Worktrees}, {"sessions", plan.Rows.Sessions},
		{"generations", plan.Rows.Generations}, {"threads", plan.Rows.Threads}, {"messages", plan.Rows.Messages},
		{"provider conversations", plan.Rows.ProviderConversations}, {"provider turns", plan.Rows.ProviderTurns},
		{"review snapshots", plan.Rows.ReviewSnapshots}, {"snapshot leases", plan.Rows.ReviewSnapshotLeases},
		{"proposal workspaces", plan.Rows.ProposalWorkspaces}, {"proposals", plan.Rows.Proposals}, {"proposal attempts", plan.Rows.ProposalAttempts},
		{"proposal versions", plan.Rows.ProposalVersions}, {"patch artifacts", plan.Rows.ProposalPatchArtifacts}, {"apply operations", plan.Rows.ApplyOperations},
		{"owned artifacts", plan.Rows.OwnedArtifacts}, {"capacity reservations", plan.Rows.CapacityReservations},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(output, "  %-24s %d\n", row.name, row.value); err != nil {
			return err
		}
	}
	counts := make(map[app.CleanupResourceKind]int)
	for _, resource := range plan.Resources {
		counts[resource.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	if _, err := fmt.Fprintln(output, "\nOwned resources:"); err != nil {
		return err
	}
	for _, kind := range kinds {
		if _, err := fmt.Fprintf(output, "  %-24s %d\n", kind, counts[app.CleanupResourceKind(kind)]); err != nil {
			return err
		}
	}
	if err := writeCleanupText(output, "Exclusions", plan.Exclusions); err != nil {
		return err
	}
	if err := writeCleanupText(output, "Irreversible effects", plan.Effects); err != nil {
		return err
	}
	if len(plan.Blockers) != 0 {
		if err := writeCleanupText(output, "Blockers", plan.Blockers); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(output, "\nExplicit confirmation: nudge cleanup --plan %s --yes\n", plan.ID)
	return err
}

func writeCleanupText(output io.Writer, title string, values []string) error {
	if _, err := fmt.Fprintf(output, "\n%s:\n", title); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(output, "  - %s\n", value); err != nil {
			return err
		}
	}
	return nil
}
