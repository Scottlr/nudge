package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/artifactspool"
	"github.com/Scottlr/nudge/internal/capacityprobe"
	"github.com/Scottlr/nudge/internal/capacitystore"
	"github.com/Scottlr/nudge/internal/config"
	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/gitcli"
	"github.com/Scottlr/nudge/internal/highlight"
	"github.com/Scottlr/nudge/internal/paths"
	protectedlogging "github.com/Scottlr/nudge/internal/privacy/logging"
	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/store/sqlite"
	"github.com/Scottlr/nudge/internal/terminal"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui"
	"github.com/Scottlr/nudge/internal/workspace"
)

func runLocalReview(ctx context.Context, startPath string, noPersist bool, themeOverride *string, branchBase, commitExpression string) error {
	if ctx == nil {
		return errors.New("local review: nil context")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if startPath == "" {
		var err error
		startPath, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("local review: current directory: %w", err)
		}
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return fmt.Errorf("local review: path: %w", err)
	}
	startPath = filepath.Clean(abs)
	environmentValues := os.Environ()
	environ := processEnvironment(environmentValues)
	locations, err := paths.Resolve(environ)
	if err != nil {
		return fmt.Errorf("local review: locations: %w", err)
	}
	loaded, err := config.Load(runCtx, locations, environ, config.CLIOverrides{UITheme: themeOverride})
	if err != nil {
		return fmt.Errorf("local review: configuration: %w", err)
	}
	presentationPolicy := theme.RenderPolicy{ASCII: true, Explicit: true}
	resolvedTheme, err := theme.Load(runCtx, locations, loaded.Config.UI.Theme, presentationPolicy)
	if err != nil {
		return fmt.Errorf("local review: theme: %w", err)
	}
	persistenceMode := app.PersistenceDurable
	if noPersist || !loaded.Config.Persistence.Enabled {
		persistenceMode = app.PersistenceNoPersist
	}
	var sessionManager *app.SessionManager
	var preferenceStore app.RepositoryPreferenceStore
	var storageLedger app.OwnedStorageLedger
	var durableStore *sqlite.Store
	persistenceDegraded := false
	if persistenceMode == app.PersistenceDurable {
		store, storeErr := sqlite.Open(runCtx, filepath.Join(locations.StateRoot, "nudge.db"))
		if storeErr != nil {
			persistenceDegraded = true
		} else {
			durableStore = store
			storageLedger = store
			preferenceStore = durableStore
			leaseManager, leaseErr := filelock.NewSessionLeaseManager(locations.StateRoot)
			if leaseErr != nil {
				_ = durableStore.Close()
				durableStore = nil
				storageLedger = nil
				persistenceDegraded = true
			} else {
				maintenanceGate, gateErr := filelock.NewRepositoryMaintenanceGate(locations.StateRoot)
				if gateErr != nil {
					_ = durableStore.Close()
					durableStore = nil
					storageLedger = nil
					preferenceStore = nil
					persistenceDegraded = true
				} else {
					sessionManager, leaseErr = app.NewSessionManager(app.SessionManagerConfig{
						Store: durableStore, Leases: leaseManager, Maintenance: maintenanceGate, AllowEphemeralFallback: true,
					})
					if leaseErr != nil {
						_ = durableStore.Close()
						durableStore = nil
						storageLedger = nil
						preferenceStore = nil
						persistenceDegraded = true
					} else {
						defer durableStore.Close()
					}
				}
			}
		}
	}

	trusted, err := process.NewExecutableResolver().Resolve(runCtx, process.ResolveExecutableRequest{
		Kind:               process.ExecutableGit,
		SearchPath:         environ["PATH"],
		CurrentDir:         startPath,
		WorkspaceRoots:     []string{locations.WorkspaceRoot},
		NudgeWritableRoots: []string{locations.ConfigRoot, locations.StateRoot, locations.CacheRoot, locations.LogRoot},
	})
	if err != nil {
		return fmt.Errorf("local review: resolve Git: %w", err)
	}
	runner := process.NewRunner()
	policy := gitcli.DefaultMachineGitReadPolicyV1()
	resolver, err := gitcli.NewResolver(gitcli.ResolverConfig{Executable: trusted, Runner: runner, Policy: policy})
	if err != nil {
		return fmt.Errorf("local review: repository resolver: %w", err)
	}

	artifactManager, err := artifactspool.NewManager(filepath.Join(locations.CacheRoot, "artifacts"))
	if err != nil {
		return fmt.Errorf("local review: artifact storage: %w", err)
	}
	capacityManager, err := capacitystore.NewManager(filepath.Join(locations.CacheRoot, "capacity"))
	if err != nil {
		return fmt.Errorf("local review: capacity storage: %w", err)
	}
	evidence, err := capacityprobe.New().Observe(runCtx, locations.CacheRoot)
	if err != nil {
		return fmt.Errorf("local review: capacity evidence: %w", err)
	}
	policyResource := app.DefaultResourcePolicy()
	operationID, err := domain.NewOperationID(app.RandomIDSource{}.NewID())
	if err != nil {
		return fmt.Errorf("local review: operation identity: %w", err)
	}
	logConfig := protectedlogging.DefaultConfig(filepath.Join(locations.LogRoot, "process"))
	if logRepository, _, resolveErr := resolver.ResolveRepository(runCtx, startPath); resolveErr == nil {
		logConfig.RepositoryID = logRepository.ID
	}
	logConfig.Level = protectedlogging.ParseLevel(loaded.Config.Logging.Level)
	var logReservation app.CapacityReservation
	retainedLogBytes, logSizeErr := logConfig.MaxBytes.Mul(logConfig.MaxFiles)
	if logSizeErr == nil {
		logPlan := app.CapacityPlan{
			OperationID:   operationID,
			VolumePeaks:   []app.VolumePeak{{ID: evidence.ID, Finals: retainedLogBytes, RetainedDelta: retainedLogBytes, Reserve: policyResource.Storage.MinimumFreeBytes}},
			RetainedDelta: retainedLogBytes,
			PolicyVersion: policyResource.Version,
		}
		logReservation, err = capacityManager.Reserve(runCtx, logPlan, policyResource, []app.VolumeEvidence{evidence})
		if err == nil && storageLedger != nil {
			logConfig.Capacity = &protectedlogging.CapacityBinding{
				Ledger: storageLedger, Reservations: capacityManager, Reservation: logReservation,
				Plan: logPlan, Policy: policyResource, VolumeID: evidence.ID,
			}
		} else if err == nil {
			_ = capacityManager.Release(runCtx, logReservation, logPlan, policyResource)
		}
	}
	if logReservation.Marker() == "" && logSizeErr == nil {
		logConfig.MaxBytes = 0
	}
	operationalLogger := protectedlogging.New(runCtx, logConfig)
	defer operationalLogger.Close()
	captureAdapter, err := gitcli.NewLocalCaptureAdapter(gitcli.LocalCaptureConfig{
		Executable:     trusted,
		Runner:         runner,
		Policy:         policyResource,
		Capacity:       capacityManager,
		Spools:         artifactManager,
		OperationID:    operationID,
		VolumeID:       evidence.ID,
		VolumeEvidence: []app.VolumeEvidence{evidence},
	})
	if err != nil {
		return fmt.Errorf("local review: capture adapter: %w", err)
	}
	manifestStore := newLocalReviewManifests(durableStore)
	captureStore, err := workspace.NewCaptureStore(workspace.CaptureStoreConfig{
		Committer: manifestStore,
		Manifests: manifestStore,
		Reader:    artifactManager,
		Releaser:  artifactManager,
	})
	if err != nil {
		return fmt.Errorf("local review: capture store: %w", err)
	}
	treeReader, err := gitcli.NewTreeReader(gitcli.TreeReaderConfig{Executable: trusted, Runner: runner, StartPath: startPath, Policy: policy, Limits: policyResource})
	if err != nil {
		return fmt.Errorf("local review: tree reader: %w", err)
	}
	contentLoader, err := gitcli.NewContentLoader(gitcli.ContentLoaderConfig{
		Executable:      trusted,
		Runner:          runner,
		StartPath:       startPath,
		Policy:          policy,
		Manifests:       captureStore,
		Artifacts:       artifactManager,
		MaxContentBytes: app.ByteSize(loaded.Config.Review.LargeFileBytes),
		PatchLimits:     diff.DefaultPatchParseLimits(),
	})
	if err != nil {
		return fmt.Errorf("local review: content loader: %w", err)
	}
	highlighter, err := highlight.NewChromaHighlighter(int(loaded.Config.Review.HighlightFileBytes), highlight.NewCache(int(policyResource.MetadataCache.MaxEntries), int(policyResource.MetadataCache.MaxBytes)))
	if err != nil {
		return fmt.Errorf("local review: highlighter: %w", err)
	}
	runtime, err := app.NewLocalReview(app.LocalReviewConfig{
		Source: app.LocalReviewSource{
			Resolver:    resolver,
			Capture:     captureSource{adapter: captureAdapter},
			Store:       captureStore,
			Tree:        treeReader,
			Changed:     treeReader,
			Content:     contentLoader,
			Highlighter: highlighter,
		},
		Persistence:         persistenceMode,
		Logger:              operationalLogger,
		Sessions:            sessionManager,
		PersistenceDegraded: persistenceDegraded,
		Branch: func() *app.BranchReviewConfig {
			if branchBase == "" {
				return nil
			}
			return &app.BranchReviewConfig{
				ExplicitBaseExpression: branchBase,
				Preferences:            preferenceStore,
				Discover:               resolver,
				Resolver:               resolver,
			}
		}(),
		Commit: func() *app.CommitReviewConfig {
			if commitExpression == "" {
				return nil
			}
			return &app.CommitReviewConfig{Expression: commitExpression, Resolver: resolver}
		}(),
	})
	if err != nil {
		return fmt.Errorf("local review: runtime: %w", err)
	}
	stream, err := runtime.Start(runCtx, startPath)
	if err != nil {
		return fmt.Errorf("local review: start: %w", err)
	}
	model := tui.NewModel(nil,
		tui.WithContext(runCtx),
		tui.WithLocalReviewStream(stream),
		tui.WithThemeResolution(resolvedTheme),
		tui.WithTerminalPreferences(terminal.Input{
			Environment: terminal.NormalizeEnvironment(environmentValues),
			Preferences: terminal.Preferences{Unicode: loaded.Config.UI.Unicode, ReducedMotion: loaded.Config.UI.ReducedMotion},
		}),
		tui.WithAltScreen(true),
		tui.WithReportFocus(true),
	)
	program := tea.NewProgram(model, tea.WithContext(runCtx))
	_, err = program.Run()
	return err
}

type captureSource struct {
	adapter *gitcli.LocalCaptureAdapter
}

func (s captureSource) Capture(ctx context.Context, repo repository.Repository, worktree repository.WorktreeRef) (app.LocalCaptureArtifacts, error) {
	result, err := s.adapter.Capture(ctx, repo, worktree)
	if err != nil {
		return app.LocalCaptureArtifacts{}, err
	}
	return app.LocalCaptureArtifacts{Candidate: result.Candidate, PatchSpool: result.PatchSpool, BlobSpool: result.BlobSpool, Reservation: result.Reservation, Plan: result.Plan, Policy: result.Policy, Capacity: result.Capacity()}, nil
}

type localReviewManifests struct {
	mu        sync.Mutex
	manifests map[domain.CaptureID]app.CaptureManifest
	durable   app.CaptureManifestStore
}

func newLocalReviewManifests(durable app.CaptureManifestStore) *localReviewManifests {
	return &localReviewManifests{manifests: make(map[domain.CaptureID]app.CaptureManifest), durable: durable}
}

func (m *localReviewManifests) CommitLocalCapture(ctx context.Context, _ app.CaptureSessionState, generation app.CaptureGeneration, manifest app.CaptureManifest, _ app.CapacityReservation, _ app.CapacityPlan) error {
	if m == nil || generation.Validate() != nil || manifest.Validate() != nil {
		return app.ErrInvalidLocalCaptureManifest
	}
	if m.durable != nil {
		if err := m.durable.SaveCaptureManifest(ctx, manifest); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.manifests[generation.CaptureID] = manifest
	m.mu.Unlock()
	return nil
}

func (m *localReviewManifests) OpenCaptureManifest(ctx context.Context, captureID domain.CaptureID) (app.CaptureManifest, error) {
	if m == nil || captureID == "" {
		return app.CaptureManifest{}, app.ErrCaptureNotFound
	}
	if m.durable != nil {
		if manifest, err := m.durable.OpenCaptureManifest(ctx, captureID); err == nil {
			return manifest, nil
		} else if !errors.Is(err, app.ErrCaptureNotFound) {
			return app.CaptureManifest{}, err
		}
	}
	m.mu.Lock()
	manifest, ok := m.manifests[captureID]
	m.mu.Unlock()
	if !ok {
		return app.CaptureManifest{}, app.ErrCaptureNotFound
	}
	return manifest, nil
}

func processEnvironment(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		for index := 0; index < len(value); index++ {
			if value[index] == '=' && index > 0 {
				result[value[:index]] = value[index+1:]
				break
			}
		}
	}
	return result
}
