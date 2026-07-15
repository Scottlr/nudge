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
