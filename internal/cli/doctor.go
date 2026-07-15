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
	format string
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
			report, err := collectDoctor(cmd.Context(), path)
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
	return command
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
		results = append(results, protectedRootResults(locations)...)
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

func protectedRootResults(locations paths.Locations) []app.HealthResult {
	roots := []struct {
		name string
		path string
	}{
		{"config", locations.ConfigRoot},
		{"state", locations.StateRoot},
		{"cache", locations.CacheRoot},
		{"workspace", locations.WorkspaceRoot},
		{"log", locations.LogRoot},
	}
	results := make([]app.HealthResult, 0, len(roots))
	for _, root := range roots {
		err := paths.ValidatePrivateDir(root.path)
		if errors.Is(err, os.ErrNotExist) {
			results = append(results, app.HealthResult{Code: app.HealthProtectedRootMissing, Severity: app.HealthInfo, Summary: root.name + " root is not initialized.", RedactedEvidence: "not_initialized"})
			continue
		}
		if err != nil {
			results = append(results, app.HealthResult{Code: app.HealthProtectedRootRejected, Severity: app.HealthWarning, Summary: root.name + " root failed protected-path validation.", RedactedEvidence: protectedPathError(err), Remediation: "Review ownership, alias, and permission evidence for the Nudge-owned root."})
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
