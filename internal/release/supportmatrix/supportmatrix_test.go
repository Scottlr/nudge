package supportmatrix

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const validMatrix = `{
  "schema_version": 1,
  "matrix_revision": 1,
  "candidates": [
    {
      "id": "darwin-arm64",
      "goos": "darwin",
      "goarch": "arm64",
      "native_runner": {"label": "macos-14", "kind": "github-hosted", "availability": "available", "owner": "T076"},
      "minimum_prerequisites": [
        {"kind": "os", "name": "macOS", "minimum": "12.0", "owner": "Go 1.25 release requirements"},
        {"kind": "tool", "name": "Git", "minimum": "T069 machine-Git policy", "owner": "T069"},
        {"kind": "tool", "name": "Go", "minimum": "1.25.0", "owner": "go.mod"}
      ],
      "required_capability_checks": [
        {"id": "git-policy", "owner": "internal/gitcli", "contract": "T069 deterministic machine-Git and conversion policy"}
      ],
      "wsl_treatment": {"mode": "native_only", "requirement": "Run directly in the macOS environment.", "owner": "T075"},
      "provisional_disposition": {"status": "candidate", "reason": "Candidate only until later release gates and human approval."}
    }
  ]
}`

func TestDecodeAcceptsStrictCandidateMatrix(t *testing.T) {
	matrix, err := Decode(strings.NewReader(validMatrix))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := matrix.Candidates[0].ID; got != "darwin-arm64" {
		t.Fatalf("candidate ID = %q, want darwin-arm64", got)
	}
}

func TestReleaseSupportMatrixFile(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test path")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "release", "support-matrix.json")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open support matrix: %v", err)
	}
	defer file.Close()
	matrix, err := Decode(file)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(matrix.Candidates) != 4 {
		t.Fatalf("candidate count = %d, want 4", len(matrix.Candidates))
	}
	if got := matrix.Candidates[2].NativeRunner.Availability; got != runnerBlocked {
		t.Fatalf("WSL runner availability = %q, want %q", got, runnerBlocked)
	}
}

func TestDecodeRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "unknown field",
			mutate: func(input string) string {
				return strings.Replace(input, `"schema_version": 1,`, `"schema_version": 1, "extra": true,`, 1)
			},
		},
		{
			name: "invalid disposition",
			mutate: func(input string) string {
				return strings.Replace(input, `"status": "candidate"`, `"status": "supported"`, 1)
			},
		},
		{
			name: "missing owner",
			mutate: func(input string) string {
				return strings.Replace(input, `"owner": "internal/gitcli"`, `"owner": ""`, 1)
			},
		},
		{
			name: "nondeterministic prerequisite order",
			mutate: func(input string) string {
				return strings.Replace(input, `{"kind": "os", "name": "macOS", "minimum": "12.0", "owner": "Go 1.25 release requirements"},
        {"kind": "tool", "name": "Git", "minimum": "T069 machine-Git policy", "owner": "T069"},`, `{"kind": "tool", "name": "Git", "minimum": "T069 machine-Git policy", "owner": "T069"},
        {"kind": "os", "name": "macOS", "minimum": "12.0", "owner": "Go 1.25 release requirements"},`, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(test.mutate(validMatrix))); !errors.Is(err, ErrInvalidMatrix) {
				t.Fatalf("Decode() error = %v, want ErrInvalidMatrix", err)
			}
		})
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	matrix, err := Decode(strings.NewReader(validMatrix))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	matrix.Candidates = append(matrix.Candidates, matrix.Candidates[0])
	if err := matrix.Validate(); !errors.Is(err, ErrInvalidMatrix) {
		t.Fatalf("Validate() error = %v, want ErrInvalidMatrix", err)
	}
}

func TestDecodeRejectsTrailingDocument(t *testing.T) {
	if _, err := Decode(strings.NewReader(validMatrix + ` {}`)); !errors.Is(err, ErrInvalidMatrix) {
		t.Fatalf("Decode() error = %v, want ErrInvalidMatrix", err)
	}
}

func TestDecodeRequiresBlockedReasonForUnavailableRunner(t *testing.T) {
	input := strings.Replace(validMatrix, `"availability": "available"`, `"availability": "blocked"`, 1)
	if _, err := Decode(strings.NewReader(input)); !errors.Is(err, ErrInvalidMatrix) {
		t.Fatalf("Decode() error = %v, want ErrInvalidMatrix", err)
	}
}
