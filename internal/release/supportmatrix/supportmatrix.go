// Package supportmatrix decodes and validates Nudge's release-only platform
// candidate matrix. Production application packages must not import it.
package supportmatrix

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
)

const (
	// SchemaVersion is the JSON schema version understood by this package.
	SchemaVersion uint32 = 1
	// MatrixRevision identifies the candidate meaning and prerequisite policy.
	MatrixRevision uint32 = 1

	dispositionCandidate = "candidate"
	runnerKindGitHub     = "github-hosted"
	runnerKindSelf       = "self-hosted"
	runnerAvailable      = "available"
	runnerBlocked        = "blocked"

	wslNativeOnly           = "native_only"
	wslSameEnvironmentLinux = "same_environment_linux_only"
)

var (
	// ErrInvalidMatrix reports malformed or unsupported matrix content.
	ErrInvalidMatrix = errors.New("invalid support matrix")
	// ErrMatrixTooLarge reports a document beyond the T070 configuration input
	// bound used by release tooling.
	ErrMatrixTooLarge = errors.New("support matrix exceeds resource limit")
)

// Matrix is the complete release candidate source. Its order is part of the
// machine-readable contract so workflow output remains deterministic.
type Matrix struct {
	SchemaVersion  uint32      `json:"schema_version"`
	MatrixRevision uint32      `json:"matrix_revision"`
	Candidates     []Candidate `json:"candidates"`
}

// Candidate identifies one native platform execution environment.
type Candidate struct {
	ID                       string                 `json:"id"`
	GOOS                     string                 `json:"goos"`
	GOARCH                   string                 `json:"goarch"`
	NativeRunner             NativeRunner           `json:"native_runner"`
	MinimumPrerequisites     []Prerequisite         `json:"minimum_prerequisites"`
	RequiredCapabilityChecks []CapabilityCheck      `json:"required_capability_checks"`
	WSLTreatment             WSLTreatment           `json:"wsl_treatment"`
	ProvisionalDisposition   ProvisionalDisposition `json:"provisional_disposition"`
}

// NativeRunner describes the runner that can produce evidence for a
// candidate. A blocked runner must explain the external setup still needed.
type NativeRunner struct {
	Label         string `json:"label"`
	Kind          string `json:"kind"`
	Availability  string `json:"availability"`
	Owner         string `json:"owner"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// Prerequisite records one minimum OS, tool, or environment requirement.
// BlockedReason is used when a minimum cannot be established without inventing
// a version claim.
type Prerequisite struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Minimum       string `json:"minimum,omitempty"`
	Owner         string `json:"owner"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// CapabilityCheck names a meaningful owner contract required for evidence.
type CapabilityCheck struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Contract string `json:"contract"`
}

// WSLTreatment records whether a candidate is direct-native or must run as a
// Linux environment with every relevant path and tool inside the same WSL
// environment.
type WSLTreatment struct {
	Mode        string `json:"mode"`
	Requirement string `json:"requirement"`
	Owner       string `json:"owner"`
}

// ProvisionalDisposition keeps every T075 row non-advertised until later
// native, workload, security, and human-approval gates complete.
type ProvisionalDisposition struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Decode reads one bounded, strict JSON matrix and validates its release
// contract. It rejects unknown fields, trailing documents, unsupported
// vocabulary, missing ownership, and nondeterministic ordering.
func Decode(r io.Reader) (Matrix, error) {
	if r == nil {
		return Matrix{}, fmt.Errorf("%w: nil reader", ErrInvalidMatrix)
	}
	maxBytes := uint64(app.DefaultResourcePolicy().Input.Config.MaxBytes)
	data, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return Matrix{}, fmt.Errorf("read matrix: %w", err)
	}
	if uint64(len(data)) > maxBytes {
		return Matrix{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrMatrixTooLarge, len(data), maxBytes)
	}
	if err := validateJSONBounds(data); err != nil {
		return Matrix{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var matrix Matrix
	if err := decoder.Decode(&matrix); err != nil {
		return Matrix{}, fmt.Errorf("%w: decode matrix: %w", ErrInvalidMatrix, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Matrix{}, fmt.Errorf("%w: multiple JSON documents", ErrInvalidMatrix)
		}
		return Matrix{}, fmt.Errorf("%w: decode trailing matrix content: %w", ErrInvalidMatrix, err)
	}
	if err := matrix.Validate(); err != nil {
		return Matrix{}, err
	}
	return matrix, nil
}

// Validate checks the versioned matrix contract without consulting the host
// platform or making support claims.
func (m Matrix) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return invalid("schema_version", "unsupported version %d", m.SchemaVersion)
	}
	if m.MatrixRevision != MatrixRevision {
		return invalid("matrix_revision", "unsupported revision %d", m.MatrixRevision)
	}
	if len(m.Candidates) == 0 {
		return invalid("candidates", "must not be empty")
	}
	previousID := ""
	seenIDs := make(map[string]struct{}, len(m.Candidates))
	for index, candidate := range m.Candidates {
		if err := candidate.validate(index, previousID); err != nil {
			return err
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return invalid(fmt.Sprintf("candidates[%d].id", index), "duplicate id %q", candidate.ID)
		}
		seenIDs[candidate.ID] = struct{}{}
		previousID = candidate.ID
	}
	return nil
}

func (c Candidate) validate(index int, previousID string) error {
	prefix := fmt.Sprintf("candidates[%d]", index)
	if err := validateID(prefix+".id", c.ID); err != nil {
		return err
	}
	if previousID != "" && c.ID <= previousID {
		return invalid(prefix+".id", "must be strictly sorted after %q", previousID)
	}
	if !supportedGOOS[c.GOOS] {
		return invalid(prefix+".goos", "unsupported GOOS %q", c.GOOS)
	}
	if !supportedGOARCH[c.GOARCH] {
		return invalid(prefix+".goarch", "unsupported GOARCH %q", c.GOARCH)
	}
	if err := c.NativeRunner.validate(prefix + ".native_runner"); err != nil {
		return err
	}
	if err := validatePrerequisites(prefix+".minimum_prerequisites", c.MinimumPrerequisites); err != nil {
		return err
	}
	if err := validateChecks(prefix+".required_capability_checks", c.RequiredCapabilityChecks); err != nil {
		return err
	}
	if err := c.WSLTreatment.validate(prefix+".wsl_treatment", c.GOOS); err != nil {
		return err
	}
	if c.ProvisionalDisposition.Status != dispositionCandidate {
		return invalid(prefix+".provisional_disposition.status", "must be %q", dispositionCandidate)
	}
	if err := meaningfulText(prefix+".provisional_disposition.reason", c.ProvisionalDisposition.Reason); err != nil {
		return err
	}
	return nil
}

func (r NativeRunner) validate(path string) error {
	if err := requiredText(path+".label", r.Label); err != nil {
		return err
	}
	if r.Kind != runnerKindGitHub && r.Kind != runnerKindSelf {
		return invalid(path+".kind", "unsupported runner kind %q", r.Kind)
	}
	if r.Availability != runnerAvailable && r.Availability != runnerBlocked {
		return invalid(path+".availability", "unsupported runner availability %q", r.Availability)
	}
	if err := requiredText(path+".owner", r.Owner); err != nil {
		return err
	}
	if r.Availability == runnerBlocked {
		return requiredText(path+".blocked_reason", r.BlockedReason)
	}
	if r.BlockedReason != "" {
		return invalid(path+".blocked_reason", "must be empty for an available runner")
	}
	return nil
}

func validatePrerequisites(path string, prerequisites []Prerequisite) error {
	if len(prerequisites) < 2 {
		return invalid(path, "requires at least one OS and one tool prerequisite")
	}
	seen := make(map[string]struct{}, len(prerequisites))
	hasOS, hasTool := false, false
	previous := ""
	for index, prerequisite := range prerequisites {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if prerequisite.Kind != "os" && prerequisite.Kind != "tool" && prerequisite.Kind != "environment" {
			return invalid(itemPath+".kind", "unsupported prerequisite kind %q", prerequisite.Kind)
		}
		if prerequisite.Kind == "os" {
			hasOS = true
		}
		if prerequisite.Kind == "tool" {
			hasTool = true
		}
		if err := requiredText(itemPath+".name", prerequisite.Name); err != nil {
			return err
		}
		if err := requiredText(itemPath+".owner", prerequisite.Owner); err != nil {
			return err
		}
		key := prerequisite.Kind + "\x00" + prerequisite.Name
		if _, exists := seen[key]; exists {
			return invalid(itemPath, "duplicate prerequisite %q", prerequisite.Name)
		}
		seen[key] = struct{}{}
		order := key
		if previous != "" && order <= previous {
			return invalid(itemPath, "must be strictly sorted after %q", previous)
		}
		previous = order
		hasMinimum := prerequisite.Minimum != ""
		hasBlockedReason := prerequisite.BlockedReason != ""
		if hasMinimum == hasBlockedReason {
			return invalid(itemPath, "must provide exactly one of minimum or blocked_reason")
		}
		if hasMinimum {
			if err := meaningfulText(itemPath+".minimum", prerequisite.Minimum); err != nil {
				return err
			}
		} else if err := meaningfulText(itemPath+".blocked_reason", prerequisite.BlockedReason); err != nil {
			return err
		}
	}
	if !hasOS || !hasTool {
		return invalid(path, "requires at least one OS and one tool prerequisite")
	}
	return nil
}

func validateChecks(path string, checks []CapabilityCheck) error {
	if len(checks) == 0 {
		return invalid(path, "must not be empty")
	}
	seen := make(map[string]struct{}, len(checks))
	previous := ""
	for index, check := range checks {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateID(itemPath+".id", check.ID); err != nil {
			return err
		}
		if previous != "" && check.ID <= previous {
			return invalid(itemPath+".id", "must be strictly sorted after %q", previous)
		}
		previous = check.ID
		if _, exists := seen[check.ID]; exists {
			return invalid(itemPath+".id", "duplicate id %q", check.ID)
		}
		seen[check.ID] = struct{}{}
		if err := requiredText(itemPath+".owner", check.Owner); err != nil {
			return err
		}
		if err := meaningfulText(itemPath+".contract", check.Contract); err != nil {
			return err
		}
	}
	return nil
}

func (w WSLTreatment) validate(path, goos string) error {
	if w.Mode != wslNativeOnly && w.Mode != wslSameEnvironmentLinux {
		return invalid(path+".mode", "unsupported WSL treatment %q", w.Mode)
	}
	if err := meaningfulText(path+".requirement", w.Requirement); err != nil {
		return err
	}
	if err := requiredText(path+".owner", w.Owner); err != nil {
		return err
	}
	if w.Mode == wslSameEnvironmentLinux && goos != "linux" {
		return invalid(path+".mode", "same-environment Linux treatment requires GOOS linux")
	}
	return nil
}

func validateID(path, value string) error {
	if err := meaningfulText(path, value); err != nil {
		return err
	}
	if value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return invalid(path, "must be a stable lowercase hyphenated id")
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return invalid(path, "must be a stable lowercase hyphenated id")
		}
	}
	return nil
}

func requiredText(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalid(path, "must not be empty")
	}
	return nil
}

func meaningfulText(path, value string) error {
	if err := requiredText(path, value); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tbd", "todo", "unknown", "n/a":
		return invalid(path, "must state a concrete requirement or blocked reason")
	default:
		return nil
	}
}

func invalid(path, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidMatrix, path, fmt.Sprintf(format, args...))
}

var supportedGOOS = map[string]bool{
	"darwin":  true,
	"linux":   true,
	"windows": true,
}

var supportedGOARCH = map[string]bool{
	"amd64": true,
	"arm64": true,
}

type jsonContainer struct {
	kind      byte
	expectKey bool
	members   uint64
}

func validateJSONBounds(data []byte) error {
	limits := app.DefaultResourcePolicy().Input.Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	stack := make([]jsonContainer, 0, int(limits.MaxDepth))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: decode matrix bounds: %w", ErrInvalidMatrix, err)
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{', '[':
				if uint64(len(stack)+1) > uint64(limits.MaxDepth) {
					return fmt.Errorf("%w: JSON depth exceeds %d", ErrMatrixTooLarge, limits.MaxDepth)
				}
				if value == '{' {
					stack = append(stack, jsonContainer{kind: byte(value), expectKey: true})
				} else {
					stack = append(stack, jsonContainer{kind: byte(value)})
				}
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("%w: unbalanced JSON container", ErrInvalidMatrix)
				}
				stack = stack[:len(stack)-1]
				markJSONValueComplete(stack)
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].kind == '{' && stack[len(stack)-1].expectKey {
				if uint64(len(value)) > uint64(limits.MaxScalarBytes) {
					return fmt.Errorf("%w: string scalar exceeds %d bytes", ErrMatrixTooLarge, limits.MaxScalarBytes)
				}
				container := &stack[len(stack)-1]
				container.members++
				if container.members > uint64(limits.MaxEntries) {
					return fmt.Errorf("%w: object members exceed %d", ErrMatrixTooLarge, limits.MaxEntries)
				}
				container.expectKey = false
				continue
			}
			if uint64(len(value)) > uint64(limits.MaxScalarBytes) {
				return fmt.Errorf("%w: string scalar exceeds %d bytes", ErrMatrixTooLarge, limits.MaxScalarBytes)
			}
			markJSONValueComplete(stack)
		case json.Number:
			if uint64(len(value.String())) > uint64(limits.MaxScalarBytes) {
				return fmt.Errorf("%w: numeric scalar exceeds %d bytes", ErrMatrixTooLarge, limits.MaxScalarBytes)
			}
			markJSONValueComplete(stack)
		case bool, nil:
			markJSONValueComplete(stack)
		}
	}
}

func markJSONValueComplete(stack []jsonContainer) {
	if len(stack) == 0 {
		return
	}
	container := &stack[len(stack)-1]
	if container.kind == '{' && !container.expectKey {
		container.expectKey = true
	}
}
