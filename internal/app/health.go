package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// HealthSchemaVersion is the version of the machine-readable doctor output.
// Version 2 adds the optional live_codex projection used only by the
// explicitly requested live provider-health path.
const HealthSchemaVersion uint32 = 2

// HealthSeverity controls doctor exit status and presentation.
type HealthSeverity string

const (
	HealthOK      HealthSeverity = "ok"
	HealthInfo    HealthSeverity = "info"
	HealthWarning HealthSeverity = "warning"
	HealthError   HealthSeverity = "error"
)

// HealthCode is a stable doctor finding identity.
type HealthCode string

const (
	HealthConfigValid              HealthCode = "config.valid"
	HealthConfigInvalid            HealthCode = "config.invalid"
	HealthGitTrusted               HealthCode = "git.executable_trusted"
	HealthGitUnavailable           HealthCode = "git.executable_unavailable"
	HealthCodexTrusted             HealthCode = "codex.executable_trusted"
	HealthCodexUnavailable         HealthCode = "codex.executable_unavailable"
	HealthRepositoryResolved       HealthCode = "repository.resolved"
	HealthRepositoryUnavailable    HealthCode = "repository.unavailable"
	HealthDatabaseCurrent          HealthCode = "database.current"
	HealthDatabaseMissing          HealthCode = "database.missing"
	HealthDatabaseOutdated         HealthCode = "database.outdated"
	HealthDatabaseCorrupt          HealthCode = "database.corrupt"
	HealthDatabaseUnavailable      HealthCode = "database.unavailable"
	HealthProtectedRootPresent     HealthCode = "protected_root.present"
	HealthProtectedRootMissing     HealthCode = "protected_root.missing"
	HealthProtectedRootRejected    HealthCode = "protected_root.rejected"
	HealthSessionLeaseStale        HealthCode = "session.lease_stale"
	HealthWorkspaceNotChecked      HealthCode = "workspace.not_checked"
	HealthRecoveryNotChecked       HealthCode = "recovery.not_checked"
	HealthStorageNotChecked        HealthCode = "storage.not_checked"
	HealthStorageReconciliation    HealthCode = "storage.reconciliation"
	HealthWorkspaceRepairRequired  HealthCode = "workspace.repair_required"
	HealthProviderNotChecked       HealthCode = "provider.not_checked"
	HealthProviderLiveConnected    HealthCode = "provider.live_connected"
	HealthProviderAuthRequired     HealthCode = "provider.auth_required"
	HealthProviderLiveUnavailable  HealthCode = "provider.live_unavailable"
	HealthProviderIncompatible     HealthCode = "provider.incompatible"
	HealthTerminalCapability       HealthCode = "terminal.capability"
	HealthRepairPlansNotRegistered HealthCode = "repair.plans_not_registered"
)

// HealthResult is one bounded, redacted doctor finding. Evidence is a safe
// summary, never a source path, prompt, patch, credential, or raw error.
type HealthResult struct {
	Code             HealthCode     `json:"code"`
	Severity         HealthSeverity `json:"severity"`
	Summary          string         `json:"summary"`
	RedactedEvidence string         `json:"evidence,omitempty"`
	Remediation      string         `json:"remediation,omitempty"`
	RepairPlanID     string         `json:"repair_plan_id,omitempty"`
}

// HealthReport is the complete versioned output of one query-only doctor run.
type HealthReport struct {
	SchemaVersion  uint32                 `json:"schema_version"`
	HealthRevision string                 `json:"health_revision"`
	GeneratedAt    time.Time              `json:"generated_at"`
	Results        []HealthResult         `json:"results"`
	LiveCodex      *LiveCodexHealthReport `json:"live_codex,omitempty"`
}

var errInvalidHealthResult = errors.New("invalid health result")

// AggregateHealth validates and deterministically aggregates health results.
// The revision covers the complete ordered result set and is suitable for
// exact future repair-plan binding.
func AggregateHealth(results []HealthResult, now time.Time) (HealthReport, error) {
	if len(results) == 0 || len(results) > 128 {
		return HealthReport{}, errInvalidHealthResult
	}
	ordered := append([]HealthResult(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Code != ordered[j].Code {
			return ordered[i].Code < ordered[j].Code
		}
		return ordered[i].Severity < ordered[j].Severity
	})
	for _, result := range ordered {
		if err := result.Validate(); err != nil {
			return HealthReport{}, err
		}
	}
	canonical, err := json.Marshal(ordered)
	if err != nil {
		return HealthReport{}, err
	}
	digest := sha256.Sum256(canonical)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return HealthReport{
		SchemaVersion:  HealthSchemaVersion,
		HealthRevision: hex.EncodeToString(digest[:]),
		GeneratedAt:    now.UTC(),
		Results:        ordered,
	}, nil
}

// WithLiveCodex attaches one explicit live observation and refreshes the
// revision so the versioned report covers both static findings and the live
// projection.
func WithLiveCodex(report HealthReport, live LiveCodexHealthReport) HealthReport {
	report.LiveCodex = &live
	canonical, err := json.Marshal(struct {
		Results   []HealthResult         `json:"results"`
		LiveCodex *LiveCodexHealthReport `json:"live_codex,omitempty"`
	}{Results: report.Results, LiveCodex: report.LiveCodex})
	if err == nil {
		digest := sha256.Sum256(canonical)
		report.HealthRevision = hex.EncodeToString(digest[:])
	}
	return report
}

// Validate checks one result's bounded public contract.
func (r HealthResult) Validate() error {
	if !validHealthCode(r.Code) || !validSeverity(r.Severity) || !safeHealthText(r.Summary) || r.Summary == "" || !safeHealthText(r.RedactedEvidence) || !safeHealthText(r.Remediation) || !safeHealthText(r.RepairPlanID) {
		return errInvalidHealthResult
	}
	if len(r.Summary) > 512 || len(r.RedactedEvidence) > 512 || len(r.Remediation) > 512 || len(r.RepairPlanID) > 128 {
		return errInvalidHealthResult
	}
	return nil
}

// ExitCode returns the doctor exit status for the report.
func (r HealthReport) ExitCode() int {
	severity := HealthOK
	for _, result := range r.Results {
		if healthSeverityRank(result.Severity) > healthSeverityRank(severity) {
			severity = result.Severity
		}
	}
	switch severity {
	case HealthError:
		return HealthExitError
	case HealthWarning:
		return HealthExitWarning
	default:
		return HealthExitOK
	}
}

const (
	// HealthExitOK means the report contains no warning or error.
	HealthExitOK = 0
	// HealthExitWarning means a non-blocking condition needs attention.
	HealthExitWarning = 1
	// HealthExitError means a blocking static/query-only condition exists.
	HealthExitError = 2
	// HealthExitUsage is reserved for invalid doctor arguments or flags.
	HealthExitUsage = 64
)

func validHealthCode(code HealthCode) bool {
	for _, known := range []HealthCode{
		HealthConfigValid, HealthConfigInvalid, HealthGitTrusted, HealthGitUnavailable,
		HealthCodexTrusted, HealthCodexUnavailable, HealthRepositoryResolved,
		HealthRepositoryUnavailable, HealthDatabaseCurrent, HealthDatabaseMissing,
		HealthDatabaseOutdated, HealthDatabaseCorrupt, HealthDatabaseUnavailable,
		HealthProtectedRootPresent, HealthProtectedRootMissing, HealthProtectedRootRejected,
		HealthSessionLeaseStale,
		HealthWorkspaceNotChecked, HealthRecoveryNotChecked, HealthStorageNotChecked, HealthStorageReconciliation, HealthWorkspaceRepairRequired,
		HealthProviderNotChecked, HealthTerminalCapability, HealthRepairPlansNotRegistered,
		HealthProviderLiveConnected, HealthProviderAuthRequired, HealthProviderLiveUnavailable,
		HealthProviderIncompatible,
	} {
		if code == known {
			return true
		}
	}
	return false
}

func validSeverity(severity HealthSeverity) bool {
	return severity == HealthOK || severity == HealthInfo || severity == HealthWarning || severity == HealthError
}

func healthSeverityRank(severity HealthSeverity) int {
	switch severity {
	case HealthError:
		return 3
	case HealthWarning:
		return 2
	case HealthInfo:
		return 1
	default:
		return 0
	}
}

func safeHealthText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// NormalizeHealthToken returns a bounded safe token for adapter evidence.
func NormalizeHealthToken(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	var builder strings.Builder
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
