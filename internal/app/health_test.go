package app

import (
	"strings"
	"testing"
	"time"
)

func TestAggregateHealthSortsAndBindsCompleteRevision(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	report, err := AggregateHealth([]HealthResult{
		{Code: HealthGitTrusted, Severity: HealthOK, Summary: "Git is trusted."},
		{Code: HealthConfigValid, Severity: HealthOK, Summary: "Config is valid."},
	}, now)
	if err != nil {
		t.Fatalf("AggregateHealth() error = %v", err)
	}
	if report.SchemaVersion != HealthSchemaVersion || report.GeneratedAt != now || len(report.HealthRevision) != 64 {
		t.Fatalf("report metadata = %#v", report)
	}
	if report.Results[0].Code != HealthConfigValid || report.Results[1].Code != HealthGitTrusted {
		t.Fatalf("results were not ordered by stable code: %#v", report.Results)
	}
	if report.ExitCode() != HealthExitOK {
		t.Fatalf("ExitCode() = %d, want %d", report.ExitCode(), HealthExitOK)
	}
	changed, err := AggregateHealth([]HealthResult{
		{Code: HealthGitTrusted, Severity: HealthWarning, Summary: "Git is trusted."},
		{Code: HealthConfigValid, Severity: HealthOK, Summary: "Config is valid."},
	}, now)
	if err != nil || changed.HealthRevision == report.HealthRevision {
		t.Fatalf("revision did not bind complete result set: old=%s new=%s err=%v", report.HealthRevision, changed.HealthRevision, err)
	}
}

func TestAggregateHealthRejectsUnsafeAndUnknownResults(t *testing.T) {
	cases := []HealthResult{
		{Code: HealthCode("unknown"), Severity: HealthOK, Summary: "safe"},
		{Code: HealthConfigValid, Severity: HealthOK, Summary: "bad\x1b[31m"},
	}
	for _, result := range cases {
		if _, err := AggregateHealth([]HealthResult{result}, time.Time{}); err == nil {
			t.Fatalf("unsafe result %#v unexpectedly accepted", result)
		}
	}
	if got := NormalizeHealthToken(" git\x1b[31m version ", 32); strings.ContainsAny(got, "\x00\x1b") || got != "git[31m version" {
		t.Fatalf("NormalizeHealthToken() = %q", got)
	}
}

func TestHealthExitCodeEscalatesOnlyWarningsAndErrors(t *testing.T) {
	for _, test := range []struct {
		severity HealthSeverity
		want     int
	}{
		{HealthInfo, HealthExitOK},
		{HealthWarning, HealthExitWarning},
		{HealthError, HealthExitError},
	} {
		report, err := AggregateHealth([]HealthResult{{Code: HealthProviderNotChecked, Severity: test.severity, Summary: "state"}}, time.Time{})
		if err != nil {
			t.Fatalf("AggregateHealth(%s): %v", test.severity, err)
		}
		if report.ExitCode() != test.want {
			t.Fatalf("ExitCode(%s) = %d, want %d", test.severity, report.ExitCode(), test.want)
		}
	}
}
