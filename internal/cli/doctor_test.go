package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

func TestDoctorCommandRejectsInvalidFormatWithUsageExit(t *testing.T) {
	command := NewRootCommand(BuildInfo{})
	command.SetArgs([]string{"doctor", "--format", "xml"})
	err := command.Execute()
	if code, ok := HealthExitCode(err); !ok || code != app.HealthExitUsage {
		t.Fatalf("invalid format error = %v, code=%d, ok=%t", err, code, ok)
	}
}

func TestDoctorRepairRequiresExactConfirmationMode(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--repair", "plan-1", "--health-revision", strings.Repeat("a", 64)},
		{"doctor", "--repair", "plan-1", "--health-revision", strings.Repeat("a", 64), "--yes", "--live-codex"},
	} {
		command := NewRootCommand(BuildInfo{})
		command.SetArgs(args)
		err := command.Execute()
		if code, ok := HealthExitCode(err); !ok || code != app.HealthExitUsage {
			t.Fatalf("repair args %#v error=%v code=%d ok=%t", args, err, code, ok)
		}
	}
}

func TestRenderDoctorJSONIsVersionedAndBounded(t *testing.T) {
	report, err := app.AggregateHealth([]app.HealthResult{{
		Code:             app.HealthConfigValid,
		Severity:         app.HealthOK,
		Summary:          "Configuration is valid.",
		RedactedEvidence: "schema=1",
	}}, time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("AggregateHealth() error = %v", err)
	}
	var output bytes.Buffer
	if err := renderDoctor(&output, report, doctorFormatJSON); err != nil {
		t.Fatalf("renderDoctor() error = %v", err)
	}
	var decoded app.HealthReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output is invalid: %v", err)
	}
	if decoded.SchemaVersion != app.HealthSchemaVersion || decoded.HealthRevision != report.HealthRevision || len(decoded.Results) != 1 {
		t.Fatalf("decoded report = %#v", decoded)
	}
	if strings.ContainsAny(output.String(), "\x00\x1b") {
		t.Fatalf("JSON output contains terminal controls: %q", output.String())
	}
}

func TestDoctorEnvironmentFindsWindowsPathCaseInsensitively(t *testing.T) {
	values := []string{"Path=C:\\trusted", "TERM=xterm"}
	if got := doctorEnvironmentValue(values, "PATH"); got != "C:\\trusted" {
		t.Fatalf("doctorEnvironmentValue() = %q", got)
	}
	environ := doctorEnvironment(values)
	if environ["PATH"] != "C:\\trusted" {
		t.Fatalf("normalized environment = %#v", environ)
	}
}

func TestLiveDoctorJSONIncludesRedactedCodexHealth(t *testing.T) {
	report, err := app.AggregateHealth([]app.HealthResult{{Code: app.HealthProviderLiveConnected, Severity: app.HealthOK, Summary: "Codex is healthy."}}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	report = app.WithLiveCodex(report, app.LiveCodexHealthReport{
		CheckedAt:  time.Unix(2, 0).UTC(),
		Connection: app.ProviderConnectionConnected,
		Protocol:   app.ProtocolHealthSummary{State: "connected", Version: "0.144.0-alpha.4", Initialized: true},
		Account:    app.AccountHealthSummary{State: "authenticated", AuthMode: "chatgpt", PlanType: "plus"},
	})
	var output bytes.Buffer
	if err := renderDoctor(&output, report, doctorFormatJSON); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchemaVersion uint32                    `json:"schema_version"`
		LiveCodex     app.LiveCodexHealthReport `json:"live_codex"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != app.HealthSchemaVersion || decoded.LiveCodex.Connection != app.ProviderConnectionConnected || decoded.LiveCodex.Account.AuthMode != "chatgpt" {
		t.Fatalf("decoded live report = %+v", decoded)
	}
}
