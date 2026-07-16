package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/config"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/gitcli"
	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/store/sqlite"
	"github.com/Scottlr/nudge/internal/terminal"
	"github.com/spf13/cobra"
)

const (
	doctorFormatHuman = "human"
	doctorFormatJSON  = "json"
)

type doctorOptions struct {
	format         string
	liveCodex      bool
	repairPlanID   string
	healthRevision string
	repairYes      bool
}

// doctorExitError indicates that the report was rendered and only the process
// exit status remains to be returned to the thin executable entrypoint.
type doctorExitError struct {
	code    int
	message string
}

func (e doctorExitError) Error() string       { return e.message }
func (e doctorExitError) HealthExitCode() int { return e.code }

// HealthExitCode extracts the already-rendered doctor exit status.
func HealthExitCode(err error) (int, bool) {
	var typed interface{ HealthExitCode() int }
	if !errors.As(err, &typed) {
		return 0, false
	}
	return typed.HealthExitCode(), true
}

func newDoctorCommand() *cobra.Command {
	options := doctorOptions{format: doctorFormatHuman}
	command := &cobra.Command{
		Use:   "doctor [path]",
		Short: "Inspect Nudge health without changing local state.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return doctorExitError{code: app.HealthExitUsage, message: "doctor accepts at most one path"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			if options.format != doctorFormatHuman && options.format != doctorFormatJSON {
				return doctorExitError{code: app.HealthExitUsage, message: "unsupported doctor format"}
			}
			if options.repairPlanID != "" {
				if options.liveCodex || options.healthRevision == "" || !options.repairYes {
					return doctorExitError{code: app.HealthExitUsage, message: "--repair requires --health-revision and --yes, and cannot be combined with --live-codex"}
				}
				report, err := collectDoctor(cmd.Context(), path)
				if err != nil {
					return doctorExitError{code: app.HealthExitUsage, message: "doctor path must be an existing directory"}
				}
				operation, err := executeRepairDoctor(cmd.Context(), report, app.RepairPlanID(options.repairPlanID), options.healthRevision)
				if err != nil {
					return doctorExitError{code: app.HealthExitError, message: repairErrorMessage(err)}
				}
				if err := renderRepairDoctor(cmd.OutOrStdout(), report, operation, options.format); err != nil {
					return err
				}
				return doctorExitError{code: repairExitCode(operation)}
			}
			if options.healthRevision != "" || options.repairYes {
				return doctorExitError{code: app.HealthExitUsage, message: "--health-revision and --yes require --repair"}
			}
			var report app.HealthReport
			var err error
			if options.liveCodex {
				if options.format == doctorFormatHuman {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Live Codex health: starting a local Codex app-server and querying account state."); err != nil {
						return err
					}
				}
				report, err = collectLiveCodexDoctor(cmd.Context(), path)
			} else {
				report, err = collectDoctor(cmd.Context(), path)
			}
			if err != nil {
				return doctorExitError{code: app.HealthExitUsage, message: "doctor path must be an existing directory"}
			}
			if err := renderDoctor(cmd.OutOrStdout(), report, options.format); err != nil {
				return err
			}
			return doctorExitError{code: report.ExitCode()}
		},
	}
	command.Flags().StringVar(&options.format, "format", doctorFormatHuman, "Output format: human or json.")
	command.Flags().BoolVar(&options.liveCodex, "live-codex", false, "Explicitly start Codex app-server and query current account health.")
	command.Flags().StringVar(&options.repairPlanID, "repair", "", "Execute one advertised repair plan by exact ID.")
	command.Flags().StringVar(&options.healthRevision, "health-revision", "", "Bind repair execution to this exact doctor health revision.")
	command.Flags().BoolVar(&options.repairYes, "yes", false, "Confirm the exact advertised repair plan and health revision.")
	return command
}

func executeRepairDoctor(ctx context.Context, report app.HealthReport, planID app.RepairPlanID, healthRevision string) (app.RepairOperation, error) {
	if ctx == nil || planID == "" || report.HealthRevision == "" || healthRevision == "" || report.HealthRevision != healthRevision {
		return app.RepairOperation{}, app.ErrRepairHealthRevision
	}
	locations, err := paths.Resolve(doctorEnvironment(os.Environ()))
	if err != nil {
		return app.RepairOperation{}, err
	}
	store, err := sqlite.Open(ctx, filepath.Join(locations.StateRoot, "nudge.db"))
	if err != nil {
		return app.RepairOperation{}, err
	}
	defer store.Close()
	registry, err := protectedPermissionRepairRegistry(locations)
	if err != nil {
		return app.RepairOperation{}, err
	}
	plans, err := registry.BuildPlans(ctx, report)
	if err != nil {
		return app.RepairOperation{}, err
	}
	for _, plan := range plans {
		if err := store.SaveRepairPlan(ctx, plan); err != nil {
			return app.RepairOperation{}, err
		}
	}
	executor, err := app.NewRepairExecutor(store, registry, nil, app.RandomIDSource{})
	if err != nil {
		return app.RepairOperation{}, err
	}
	return executor.Execute(ctx, app.ExecuteRepair{
		PlanID: planID, HealthRevision: healthRevision, Confirmation: app.RepairConfirmationYes,
		IdempotencyKey: "doctor:" + string(planID) + ":" + healthRevision,
	})
}

func renderRepairDoctor(output io.Writer, report app.HealthReport, operation app.RepairOperation, format string) error {
	if output == nil {
		return errors.New("doctor: nil output")
	}
	if format == doctorFormatJSON {
		return json.NewEncoder(output).Encode(struct {
			Health app.HealthReport    `json:"health"`
			Repair app.RepairOperation `json:"repair"`
		}{Health: report, Repair: operation})
	}
	if err := renderDoctor(output, report, doctorFormatHuman); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "Repair: plan=%s phase=%s outcome=%s\n", presentation.ProjectTerminalText(string(operation.PlanID), presentation.TerminalTextScalar), operation.Phase, operation.Outcome)
	return err
}

func repairExitCode(operation app.RepairOperation) int {
	if operation.Outcome == app.RepairOutcomeSucceeded || operation.Outcome == app.RepairOutcomeAlreadyRepaired {
		return app.HealthExitOK
	}
	return app.HealthExitError
}

func repairErrorMessage(err error) string {
	switch {
	case errors.Is(err, app.ErrRepairHealthRevision):
		return "repair health revision is stale"
	case errors.Is(err, app.ErrRepairPlanNotFound):
		return "repair plan is not advertised"
	case errors.Is(err, app.ErrRepairHandlerUnavailable):
		return "repair handler is unavailable"
	case errors.Is(err, app.ErrRepairConfirmation):
		return "repair confirmation was rejected"
	case errors.Is(err, app.ErrRepairPreconditions):
		return "repair preconditions changed"
	default:
		return "repair could not be completed"
	}
}

func collectLiveCodexDoctor(ctx context.Context, requestedPath string) (app.HealthReport, error) {
	base, err := collectDoctor(ctx, requestedPath)
	if err != nil {
		return app.HealthReport{}, err
	}
	results := make([]app.HealthResult, 0, len(base.Results))
	for _, result := range base.Results {
		if result.Code != app.HealthProviderNotChecked {
			results = append(results, result)
		}
	}
	startPath, _, err := doctorStartPath(requestedPath)
	if err != nil {
		return app.HealthReport{}, err
	}
	locations, locationErr := paths.Resolve(doctorEnvironment(os.Environ()))
	var live app.LiveCodexHealthReport
	var checkErr error
	if locationErr != nil {
		live = app.LiveCodexHealthReport{CheckedAt: time.Now().UTC(), Connection: app.ProviderConnectionUnavailable, ErrorCode: "locations_invalid", Remediation: "Review the protected Nudge locations and retry the explicit live check."}
		checkErr = locationErr
	} else {
		loaded, loadErr := config.Load(ctx, locations, doctorEnvironment(os.Environ()), config.CLIOverrides{})
		if loadErr != nil {
			live = app.LiveCodexHealthReport{CheckedAt: time.Now().UTC(), Connection: app.ProviderConnectionUnavailable, ErrorCode: "configuration_invalid", Remediation: "Fix the protected Nudge configuration and retry the explicit live check."}
			checkErr = loadErr
		} else {
			operation, operationErr := newLiveCodexHealthOperation(ctx, startPath, locations, loaded)
			if operationErr != nil {
				live = app.LiveCodexHealthReport{CheckedAt: time.Now().UTC(), Connection: app.ProviderConnectionUnavailable, ErrorCode: "provider_unavailable", Remediation: "Install Codex or review the trusted executable settings."}
				checkErr = operationErr
			} else {
				correlationID, idErr := domain.NewOperationID("doctor-live-codex")
				if idErr != nil {
					return app.HealthReport{}, idErr
				}
				checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				live, checkErr = operation.Check(checkCtx, app.LiveCodexHealthRequest{CorrelationID: correlationID, RequestedAt: time.Now().UTC()})
				cancel()
			}
		}
	}
	results = append(results, liveHealthResult(live, checkErr))
	report, err := app.AggregateHealth(results, time.Now().UTC())
	if err != nil {
		return app.HealthReport{}, err
	}
	return app.WithLiveCodex(report, live), nil
}

func liveHealthResult(report app.LiveCodexHealthReport, checkErr error) app.HealthResult {
	code := app.HealthProviderLiveUnavailable
	severity := app.HealthWarning
	summary := "Live Codex health could not be completed."
	switch report.Connection {
	case app.ProviderConnectionConnected:
		code = app.HealthProviderLiveConnected
		severity = app.HealthOK
		summary = "Codex app-server initialized and account state was queried."
	case app.ProviderConnectionAuthRequired:
		code = app.HealthProviderAuthRequired
		severity = app.HealthWarning
		summary = "Codex is available but requires authentication."
	case app.ProviderConnectionIncompatible:
		code = app.HealthProviderIncompatible
		severity = app.HealthWarning
		summary = "Codex app-server health is incompatible with the supported protocol."
	}
	evidence := "connection=" + app.NormalizeHealthToken(string(report.Connection), 64) + ",protocol=" + app.NormalizeHealthToken(report.Protocol.State, 64) + ",account=" + app.NormalizeHealthToken(report.Account.State, 64)
	if report.LoginRequired {
		evidence += ",login_required=true"
	}
	result := app.HealthResult{Code: code, Severity: severity, Summary: summary, RedactedEvidence: evidence, Remediation: report.Remediation}
	if result.Remediation == "" && checkErr != nil {
		result.Remediation = "Review the redacted provider state and retry Connect Codex explicitly."
	}
	return result
}

func collectDoctor(ctx context.Context, requestedPath string) (app.HealthReport, error) {
	if ctx == nil {
		return app.HealthReport{}, errors.New("doctor: nil context")
	}
	startPath, explicitPath, err := doctorStartPath(requestedPath)
	if err != nil {
		return app.HealthReport{}, err
	}
	environ := doctorEnvironment(os.Environ())
	results := make([]app.HealthResult, 0, 16)

	locations, locationErr := paths.Resolve(environ)
	if locationErr != nil {
		results = append(results, app.HealthResult{
			Code:             app.HealthConfigInvalid,
			Severity:         app.HealthError,
			Summary:          "Nudge locations are invalid.",
			RedactedEvidence: "location_validation_failed",
			Remediation:      "Review the NUDGE_* home settings and run doctor again.",
		})
	} else {
		results = append(results, protectedRootResults(ctx, locations)...)
	}

	loaded := config.LoadedConfig{}
	if locationErr == nil {
		loaded, err = config.Load(ctx, locations, environ, config.CLIOverrides{})
		if err != nil {
			results = append(results, app.HealthResult{
				Code:             app.HealthConfigInvalid,
				Severity:         app.HealthError,
				Summary:          "Nudge configuration could not be loaded.",
				RedactedEvidence: "configuration_validation_failed",
				Remediation:      "Fix the protected configuration file or environment overlay.",
			})
		} else {
			results = append(results, app.HealthResult{
				Code:             app.HealthConfigValid,
				Severity:         app.HealthOK,
				Summary:          "Nudge configuration is valid.",
				RedactedEvidence: fmt.Sprintf("schema=%d", loaded.Config.Version),
			})
		}
	}

	gitIdentity, gitVersion, gitErr := resolveDoctorExecutable(ctx, process.ExecutableGit, "", startPath, locations)
	if gitErr != nil {
		results = append(results, app.HealthResult{
			Code:             app.HealthGitUnavailable,
			Severity:         app.HealthWarning,
			Summary:          "Trusted Git executable evidence is unavailable.",
			RedactedEvidence: processErrorCode(gitErr),
			Remediation:      "Install Git or configure a trusted executable outside repository and Nudge-owned roots.",
		})
	} else {
		results = append(results, executableHealthResult(app.HealthGitTrusted, "Git", gitIdentity, gitVersion))
	}

	codexConfigured := ""
	if locationErr == nil && loaded.Config.Codex.Executable != "" && filepath.IsAbs(loaded.Config.Codex.Executable) {
		codexConfigured = loaded.Config.Codex.Executable
	}
	codexIdentity, codexVersion, codexErr := resolveDoctorExecutable(ctx, process.ExecutableCodex, codexConfigured, startPath, locations)
	if codexErr != nil {
		results = append(results, app.HealthResult{
			Code:             app.HealthCodexUnavailable,
			Severity:         app.HealthWarning,
			Summary:          "Trusted Codex executable evidence is unavailable.",
			RedactedEvidence: processErrorCode(codexErr),
			Remediation:      "Install Codex or connect it explicitly after reviewing the trusted executable settings.",
		})
	} else {
		results = append(results, executableHealthResult(app.HealthCodexTrusted, "Codex", codexIdentity, codexVersion))
	}

	if locationErr == nil {
		results = append(results, databaseHealthResult(ctx, filepath.Join(locations.StateRoot, "nudge.db")))
	}
	if gitErr == nil {
		results = append(results, repositoryHealthResult(ctx, startPath, gitIdentity, explicitPath))
	} else {
		results = append(results, app.HealthResult{
			Code:             app.HealthRepositoryUnavailable,
			Severity:         app.HealthInfo,
			Summary:          "Repository health was not checked because trusted Git is unavailable.",
			RedactedEvidence: "git_unavailable",
		})
	}

	results = append(results,
		app.HealthResult{
			Code:             app.HealthWorkspaceNotChecked,
			Severity:         app.HealthInfo,
			Summary:          "Workspace lifecycle state was not checked by plain doctor.",
			RedactedEvidence: "owner_state_not_registered",
			Remediation:      "Use the owning recovery or repair command when it is explicitly available.",
		},
		app.HealthResult{
			Code:             app.HealthRecoveryNotChecked,
			Severity:         app.HealthInfo,
			Summary:          "Recovery and apply journals were not mutated or repaired.",
			RedactedEvidence: "query_only",
		},
		app.HealthResult{
			Code:             app.HealthStorageNotChecked,
			Severity:         app.HealthInfo,
			Summary:          "Owned-storage ledger health was not inferred without its owner evidence.",
			RedactedEvidence: "owner_snapshot_unavailable",
		},
		app.HealthResult{
			Code:             app.HealthProviderNotChecked,
			Severity:         app.HealthInfo,
			Summary:          "Current Codex account and provider state were not checked.",
			RedactedEvidence: "not_checked",
			Remediation:      "Use nudge doctor --live-codex or Connect Codex explicitly.",
		},
		terminalHealthResult(environ, loaded),
		app.HealthResult{
			Code:             app.HealthRepairPlansNotRegistered,
			Severity:         app.HealthInfo,
			Summary:          "No repair plan registry is active for plain doctor.",
			RedactedEvidence: "repair_execution_disabled",
		},
	)

	return app.AggregateHealth(results, time.Now().UTC())
}

func doctorStartPath(requested string) (string, bool, error) {
	if requested == "" {
		path, err := os.Getwd()
		return filepath.Clean(path), false, err
	}
	abs, err := filepath.Abs(requested)
	if err != nil {
		return "", true, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", true, fmt.Errorf("doctor path is not an existing directory")
	}
	return filepath.Clean(abs), true, nil
}

func resolveDoctorExecutable(ctx context.Context, kind process.ExecutableKind, configured, startPath string, locations paths.Locations) (process.ExecutableIdentity, string, error) {
	searchPath := doctorEnvironmentValue(os.Environ(), "PATH")
	identity, err := process.NewExecutableResolver().Resolve(ctx, process.ResolveExecutableRequest{
		Kind:               kind,
		ConfiguredPath:     configured,
		SearchPath:         searchPath,
		CurrentDir:         startPath,
		WorkspaceRoots:     rootsIfValid(locations.WorkspaceRoot),
		NudgeWritableRoots: validRoots(locations.ConfigRoot, locations.StateRoot, locations.CacheRoot, locations.LogRoot),
	})
	if err != nil {
		return process.ExecutableIdentity{}, "", err
	}
	runner := process.NewRunner()
	result, err := runner.Run(ctx, process.Spec{
		Executable: identity,
		Args:       []string{"--version"},
		Environment: process.EnvironmentPolicy{
			Mode: process.EnvironmentMinimal,
			Set:  map[string]string{"LC_ALL": "C", "LANG": "C"},
		},
		Timeout:     5 * time.Second,
		StdoutLimit: 1024,
		StderrLimit: 2048,
	})
	if err != nil {
		return process.ExecutableIdentity{}, "", err
	}
	return identity, doctorVersion(result.Stdout), nil
}

func executableHealthResult(code app.HealthCode, name string, identity process.ExecutableIdentity, version string) app.HealthResult {
	health := identity.Health(version)
	return app.HealthResult{
		Code:             code,
		Severity:         app.HealthOK,
		Summary:          name + " executable is trusted.",
		RedactedEvidence: fmt.Sprintf("kind=%s,source=%s,binary=%s,identity=%s,version=%s", health.Kind, health.Source, filepath.Base(health.CanonicalPath), health.IdentityHashPrefix, app.NormalizeHealthToken(health.Version, 96)),
	}
}

func databaseHealthResult(ctx context.Context, path string) app.HealthResult {
	health, err := sqlite.InspectReadOnly(ctx, path)
	evidence := fmt.Sprintf("state=%s,query_only=%t,integrity=%t,version=%d/%d", health.State, health.QueryOnly, health.IntegrityOK, health.AppliedVersion, health.ExpectedVersion)
	switch health.State {
	case sqlite.ReadOnlyDatabaseMissing:
		return app.HealthResult{Code: app.HealthDatabaseMissing, Severity: app.HealthInfo, Summary: "No Nudge database has been initialized.", RedactedEvidence: evidence}
	case sqlite.ReadOnlyDatabaseCurrent:
		return app.HealthResult{Code: app.HealthDatabaseCurrent, Severity: app.HealthOK, Summary: "Nudge database is readable and current.", RedactedEvidence: evidence}
	case sqlite.ReadOnlyDatabaseOutdated:
		return app.HealthResult{Code: app.HealthDatabaseOutdated, Severity: app.HealthWarning, Summary: "Nudge database migrations are behind this binary.", RedactedEvidence: evidence, Remediation: "Start Nudge normally to apply supported migrations after reviewing the backup and ownership policy."}
	case sqlite.ReadOnlyDatabaseCorrupt:
		return app.HealthResult{Code: app.HealthDatabaseCorrupt, Severity: app.HealthError, Summary: "Nudge database integrity or migration evidence is corrupt.", RedactedEvidence: evidence, Remediation: "Use the exact registered database repair plan when available; plain doctor does not repair it."}
	default:
		return app.HealthResult{Code: app.HealthDatabaseUnavailable, Severity: app.HealthWarning, Summary: "Nudge database could not be inspected read-only.", RedactedEvidence: processErrorCode(err), Remediation: "Check protected state-root ownership and database availability without deleting or migrating it."}
	}
}

func repositoryHealthResult(ctx context.Context, startPath string, identity process.ExecutableIdentity, explicit bool) app.HealthResult {
	resolver, err := gitcli.NewResolver(gitcli.ResolverConfig{Executable: identity, Runner: process.NewRunner(), Policy: gitcli.DefaultMachineGitReadPolicyV1()})
	if err != nil {
		return app.HealthResult{Code: app.HealthRepositoryUnavailable, Severity: repositorySeverity(explicit), Summary: "Repository health could not be initialized.", RedactedEvidence: "resolver_unavailable"}
	}
	repository, worktree, err := resolver.ResolveRepository(ctx, startPath)
	if err != nil {
		return app.HealthResult{Code: app.HealthRepositoryUnavailable, Severity: repositorySeverity(explicit), Summary: "The selected directory is not a readable Git worktree.", RedactedEvidence: gitErrorCode(err)}
	}
	return app.HealthResult{Code: app.HealthRepositoryResolved, Severity: app.HealthOK, Summary: "Git repository and worktree are readable.", RedactedEvidence: fmt.Sprintf("object_format=%s,repository_bound=%t,worktree_bound=%t", app.NormalizeHealthToken(repository.Binding.ObjectFormat, 32), repository.ID != "", worktree.ID != "")}
}

func repositorySeverity(explicit bool) app.HealthSeverity {
	if explicit {
		return app.HealthWarning
	}
	return app.HealthInfo
}

func protectedPermissionRepairRegistry(locations paths.Locations) (*app.RepairRegistry, error) {
	registry := app.NewRepairRegistry()
	service, err := paths.NewProtectedPermissionService(locations)
	if err != nil {
		return nil, err
	}
	lockRoot, err := protectedPermissionLockRoot(locations)
	if err != nil {
		return nil, err
	}
	locks, err := filelock.NewProtectedPermissionLeaseManager(lockRoot)
	if err != nil {
		return nil, err
	}
	owner, err := app.NewProtectedPermissionRepairOwner(service, service, locks, nil)
	if err != nil {
		return nil, err
	}
	if err := app.RegisterProtectedPermissionRepairOwner(registry, owner); err != nil {
		return nil, err
	}
	return registry, nil
}

func protectedPermissionLockRoot(locations paths.Locations) (string, error) {
	for _, root := range []string{locations.StateRoot, locations.ConfigRoot, locations.CacheRoot, locations.LogRoot, locations.WorkspaceRoot} {
		if err := paths.ValidatePrivateDir(root); err == nil {
			return root, nil
		}
	}
	return "", errors.New("no private Nudge root is available for permission repair locking")
}

func protectedRootResults(ctx context.Context, locations paths.Locations) []app.HealthResult {
	permissionPlans := make(map[string]string)
	if service, err := paths.NewProtectedPermissionService(locations); err == nil {
		if targets, listErr := service.ListProtectedPermissionTargets(ctx); listErr == nil {
			for _, target := range targets {
				permissionPlans[string(target.Kind)] = "protected-permission-" + target.ResourceID
			}
		}
	}
	roots := []struct {
		name string
		kind app.ProtectedPermissionRootKind
		path string
	}{
		{"config", app.ProtectedConfigRoot, locations.ConfigRoot},
		{"state", app.ProtectedStateRoot, locations.StateRoot},
		{"cache", app.ProtectedCacheRoot, locations.CacheRoot},
		{"workspace", app.ProtectedWorkspaceRoot, locations.WorkspaceRoot},
		{"log", app.ProtectedLogRoot, locations.LogRoot},
	}
	results := make([]app.HealthResult, 0, len(roots))
	for _, root := range roots {
		err := paths.ValidatePrivateDir(root.path)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, app.HealthResult{Code: app.HealthProtectedRootMissing, Severity: app.HealthInfo, Summary: root.name + " root is not initialized.", RedactedEvidence: "not_initialized"})
			continue
		}
		if err != nil {
			results = append(results, app.HealthResult{Code: app.HealthProtectedRootRejected, Severity: app.HealthWarning, Summary: root.name + " root failed protected-path validation.", RedactedEvidence: protectedPathError(err), Remediation: "Review ownership, alias, and permission evidence for the Nudge-owned root.", RepairPlanID: permissionPlans[string(root.kind)]})
			continue
		}
		results = append(results, app.HealthResult{Code: app.HealthProtectedRootPresent, Severity: app.HealthOK, Summary: root.name + " root is present and protected.", RedactedEvidence: "present,owner_only=true"})
	}
	return results
}

func terminalHealthResult(environ map[string]string, loaded config.LoadedConfig) app.HealthResult {
	preferences := terminal.Preferences{Unicode: true}
	if loaded.Config.Version != 0 {
		preferences.Unicode = loaded.Config.UI.Unicode
		preferences.ReducedMotion = loaded.Config.UI.ReducedMotion
	}
	evidence := terminal.DetectHealth(io.Discard, environmentEntries(environ), preferences)
	return app.HealthResult{Code: app.HealthTerminalCapability, Severity: app.HealthOK, Summary: "Terminal capability detection is query-only and bounded.", RedactedEvidence: fmt.Sprintf("color=%s,ascii=%t,motion=%s,query_only=%t", evidence.Policy.Color.String(), evidence.Policy.ASCII, evidence.Policy.Motion.String(), evidence.QueryOnly)}
}

func renderDoctor(output io.Writer, report app.HealthReport, format string) error {
	if output == nil {
		return errors.New("doctor: nil output")
	}
	if format == doctorFormatJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintln(output, "Nudge doctor"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Health revision: "+presentation.ProjectTerminalText(report.HealthRevision, presentation.TerminalTextScalar)); err != nil {
		return err
	}
	for _, result := range report.Results {
		line := fmt.Sprintf("[%s] %s: %s", result.Severity, result.Code, result.Summary)
		if result.RedactedEvidence != "" {
			line += " (" + result.RedactedEvidence + ")"
		}
		if result.Remediation != "" {
			line += " Remediation: " + result.Remediation
		}
		if _, err := fmt.Fprintln(output, presentation.ProjectTerminalText(line, presentation.TerminalTextScalar)); err != nil {
			return err
		}
	}
	if report.LiveCodex != nil {
		live := report.LiveCodex
		line := fmt.Sprintf("Live Codex: connection=%s protocol=%s account=%s", live.Connection, live.Protocol.State, live.Account.State)
		if live.LoginRequired {
			line += " login required"
		}
		if live.Remediation != "" {
			line += " Remediation: " + live.Remediation
		}
		if _, err := fmt.Fprintln(output, presentation.ProjectTerminalText(line, presentation.TerminalTextScalar)); err != nil {
			return err
		}
	}
	return nil
}

func doctorVersion(output []byte) string {
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	return app.NormalizeHealthToken(line, 96)
}

func processErrorCode(err error) string {
	if err == nil {
		return "unknown"
	}
	var executableErr *process.ExecutableError
	if errors.As(err, &executableErr) {
		return string(executableErr.Code)
	}
	var spawnErr *process.SpawnError
	if errors.As(err, &spawnErr) {
		return "process_spawn_failed"
	}
	if errors.Is(err, sqlite.ErrReadOnlyWALPending) {
		return "wal_sidecar_pending"
	}
	return "static_check_failed"
}

func gitErrorCode(err error) string {
	var gitErr *gitcli.GitError
	if errors.As(err, &gitErr) {
		return string(gitErr.Code)
	}
	return "repository_check_failed"
}

func protectedPathError(err error) string {
	switch {
	case errors.Is(err, paths.ErrProtectedAlias):
		return "alias_rejected"
	case errors.Is(err, paths.ErrProtectedPermissions):
		return "permissions_rejected"
	case errors.Is(err, paths.ErrProtectedPath):
		return "path_rejected"
	default:
		return "protected_path_unavailable"
	}
}

func doctorEnvironment(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, raw, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			continue
		}
		result[key] = raw
		if strings.EqualFold(key, "PATH") {
			result["PATH"] = raw
		}
	}
	return result
}

func doctorEnvironmentValue(values []string, wanted string) string {
	for _, value := range values {
		key, raw, ok := strings.Cut(value, "=")
		if ok && strings.EqualFold(key, wanted) {
			return raw
		}
	}
	return ""
}

func environmentEntries(values map[string]string) []string {
	entries := make([]string, 0, len(values))
	for key, value := range values {
		entries = append(entries, key+"="+value)
	}
	return entries
}

func validRoots(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func rootsIfValid(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}
