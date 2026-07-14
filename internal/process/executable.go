package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// executablePathLimit mirrors T070 Input.ExecutablePathBytes.
	executablePathLimit = 32 * 1024
	hashReadChunk       = 64 * 1024
	identityHashPrefix  = 12
)

// ExecutableKind identifies the supported Nudge child executable.
type ExecutableKind string

const (
	ExecutableGit   ExecutableKind = "git"
	ExecutableCodex ExecutableKind = "codex"
)

// ExecutableSource identifies how an executable was selected.
type ExecutableSource string

const (
	ExecutableConfigured ExecutableSource = "configured_absolute"
	ExecutablePATH       ExecutableSource = "sanitized_path"
)

// ResolveExecutableRequest defines the complete trust context for one
// executable lookup. All roots are caller-provided absolute paths and are
// treated as excluded from both discovery and launch.
type ResolveExecutableRequest struct {
	Kind               ExecutableKind
	ConfiguredPath     string
	SearchPath         string
	CurrentDir         string
	RepositoryRoots    []string
	WorkspaceRoots     []string
	NudgeWritableRoots []string
}

// ExecutableIdentity is the immutable launch identity recorded by Resolve.
// NativeID contains platform file identity and executable-mode evidence; the
// content digest detects same-file replacement or in-place changes.
type ExecutableIdentity struct {
	Kind          ExecutableKind
	Source        ExecutableSource
	CanonicalPath string
	NativeID      []byte
	Size          int64
	ModTime       time.Time
	SHA256        [32]byte

	excludedRoots []string
}

// ExecutableHealth is a safe projection for human or JSON health output.
// CanonicalPath and Version are terminal-safe projections, and the identity
// is represented only by a short digest prefix.
type ExecutableHealth struct {
	Kind               ExecutableKind   `json:"kind"`
	Source             ExecutableSource `json:"source"`
	CanonicalPath      string           `json:"canonical_path"`
	Version            string           `json:"version,omitempty"`
	IdentityHashPrefix string           `json:"identity_hash_prefix"`
	Trusted            bool             `json:"trusted"`
}

// ExecutableResolver resolves and revalidates trusted child executables.
type ExecutableResolver interface {
	Resolve(ctx context.Context, request ResolveExecutableRequest) (ExecutableIdentity, error)
	RevalidateForLaunch(ctx context.Context, expected ExecutableIdentity) (ExecutableIdentity, error)
}

// TrustedExecutableResolver is the platform-neutral trusted executable
// resolver. OS-specific identity evidence is supplied by small build-tagged
// functions.
type TrustedExecutableResolver struct{}

// NewExecutableResolver constructs a resolver with no mutable global state.
func NewExecutableResolver() *TrustedExecutableResolver { return &TrustedExecutableResolver{} }

var (
	// ErrExecutableNotFound indicates that no trusted candidate was available.
	ErrExecutableNotFound = &ExecutableError{Code: ExecutableErrorNotFound}
	// ErrExecutableIdentityChanged indicates a stale or replaced launch target.
	ErrExecutableIdentityChanged = &ExecutableError{Code: ExecutableErrorIdentityChanged}
)

// ExecutableErrorCode identifies a stable resolver failure category.
type ExecutableErrorCode string

const (
	ExecutableErrorInvalidInput    ExecutableErrorCode = "executable_invalid_input"
	ExecutableErrorNotFound        ExecutableErrorCode = "executable_not_found"
	ExecutableErrorExcluded        ExecutableErrorCode = "executable_excluded"
	ExecutableErrorIdentityChanged ExecutableErrorCode = "executable_identity_changed"
	ExecutableErrorUnsupported     ExecutableErrorCode = "executable_unsupported"
	ExecutableErrorCanceled        ExecutableErrorCode = "executable_canceled"
)

// ExecutableError retains a private cause while exposing only a safe stable
// category to callers and presentation layers.
type ExecutableError struct {
	Code  ExecutableErrorCode
	Cause error
}

func (e *ExecutableError) Error() string {
	if e == nil || e.Code == "" {
		return string(ExecutableErrorInvalidInput)
	}
	return string(e.Code)
}

func (e *ExecutableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ExecutableError) Is(target error) bool {
	other, ok := target.(*ExecutableError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// Validate checks that an identity contains all evidence required for launch.
func (i ExecutableIdentity) Validate() error {
	if !validExecutableKind(i.Kind) || !validExecutableSource(i.Source) || i.CanonicalPath == "" || !filepath.IsAbs(i.CanonicalPath) || !validSafeText(i.CanonicalPath) || len(i.NativeID) == 0 || i.Size < 0 || i.ModTime.IsZero() || i.SHA256 == ([32]byte{}) {
		return executableError(ExecutableErrorInvalidInput, nil)
	}
	return nil
}

// Health projects an identity into a safe operational-health value.
func (i ExecutableIdentity) Health(version string) ExecutableHealth {
	digest := hex.EncodeToString(i.SHA256[:])
	if len(digest) > identityHashPrefix {
		digest = digest[:identityHashPrefix]
	}
	return ExecutableHealth{
		Kind:               i.Kind,
		Source:             i.Source,
		CanonicalPath:      safeHealthText(i.CanonicalPath),
		Version:            safeHealthText(version),
		IdentityHashPrefix: digest,
		Trusted:            i.Validate() == nil,
	}
}

func (r *TrustedExecutableResolver) Resolve(ctx context.Context, request ResolveExecutableRequest) (ExecutableIdentity, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ExecutableIdentity{}, executableError(ExecutableErrorCanceled, err)
	}
	normalized, err := normalizeResolveRequest(request)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	if normalized.configured != "" {
		return r.resolveCandidate(ctx, normalized.configured, ExecutableConfigured, normalized)
	}

	name := executableName(normalized.kind)
	for _, component := range strings.Split(normalized.searchPath, string(os.PathListSeparator)) {
		if component == "" || len(component) > executablePathLimit || !validSafeText(component) || !filepath.IsAbs(component) {
			continue
		}
		absoluteComponent, err := filepath.Abs(component)
		if err != nil || isExcluded(absoluteComponent, normalized.excludedRoots) {
			continue
		}
		canonicalComponent, err := canonicalExistingPath(absoluteComponent)
		if err != nil || isExcluded(canonicalComponent, normalized.excludedRoots) {
			continue
		}
		if info, err := os.Stat(canonicalComponent); err != nil || !info.IsDir() {
			continue
		}
		candidate, ok := exactCandidate(canonicalComponent, name)
		if !ok {
			continue
		}
		identity, err := r.resolveCandidate(ctx, candidate, ExecutablePATH, normalized)
		if err == nil {
			return identity, nil
		}
		if !errors.Is(err, ErrExecutableIdentityChanged) && !isCandidateSkip(err) {
			return ExecutableIdentity{}, err
		}
	}
	return ExecutableIdentity{}, ErrExecutableNotFound
}

func (r *TrustedExecutableResolver) RevalidateForLaunch(ctx context.Context, expected ExecutableIdentity) (ExecutableIdentity, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ExecutableIdentity{}, executableError(ExecutableErrorCanceled, err)
	}
	if err := expected.Validate(); err != nil {
		return ExecutableIdentity{}, executableError(ExecutableErrorIdentityChanged, err)
	}
	canonical, err := canonicalExistingPath(expected.CanonicalPath)
	if err != nil || canonical != expected.CanonicalPath || isExcluded(expected.CanonicalPath, expected.excludedRoots) || isExcluded(canonical, expected.excludedRoots) {
		return ExecutableIdentity{}, executableError(ExecutableErrorIdentityChanged, err)
	}
	if expected.Source == ExecutableConfigured && !candidateNameAllowed(expected.Kind, canonical, expected.Source) {
		return ExecutableIdentity{}, executableError(ExecutableErrorIdentityChanged, nil)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(filepath.Base(canonical)), ".exe") {
		return ExecutableIdentity{}, executableError(ExecutableErrorIdentityChanged, nil)
	}
	current, err := inspectExecutable(ctx, canonical)
	if err != nil {
		return ExecutableIdentity{}, executableError(ExecutableErrorIdentityChanged, err)
	}
	current.Kind = expected.Kind
	current.Source = expected.Source
	current.CanonicalPath = canonical
	if !sameExecutableIdentity(expected, current) {
		return ExecutableIdentity{}, ErrExecutableIdentityChanged
	}
	current.excludedRoots = append([]string(nil), expected.excludedRoots...)
	return current, nil
}

type normalizedResolveRequest struct {
	kind          ExecutableKind
	configured    string
	searchPath    string
	excludedRoots []string
}

func normalizeResolveRequest(request ResolveExecutableRequest) (normalizedResolveRequest, error) {
	if !validExecutableKind(request.Kind) {
		return normalizedResolveRequest{}, executableError(ExecutableErrorInvalidInput, nil)
	}
	if len(request.SearchPath) > executablePathLimit || !utf8.ValidString(request.SearchPath) || !validSafeText(request.SearchPath) {
		return normalizedResolveRequest{}, executableError(ExecutableErrorInvalidInput, nil)
	}
	current, err := normalizeAbsoluteRoot(request.CurrentDir)
	if err != nil {
		return normalizedResolveRequest{}, err
	}
	roots := []string{current}
	for _, root := range append(append(request.RepositoryRoots, request.WorkspaceRoots...), request.NudgeWritableRoots...) {
		normalized, err := normalizeAbsoluteRoot(root)
		if err != nil {
			return normalizedResolveRequest{}, err
		}
		roots = append(roots, normalized)
	}
	roots = uniquePaths(roots)
	result := normalizedResolveRequest{kind: request.Kind, searchPath: request.SearchPath, excludedRoots: roots}
	if request.ConfiguredPath != "" {
		if len(request.ConfiguredPath) > executablePathLimit || !utf8.ValidString(request.ConfiguredPath) || !validSafeText(request.ConfiguredPath) || !filepath.IsAbs(request.ConfiguredPath) {
			return normalizedResolveRequest{}, executableError(ExecutableErrorInvalidInput, nil)
		}
		result.configured = filepath.Clean(request.ConfiguredPath)
	}
	return result, nil
}

func (r *TrustedExecutableResolver) resolveCandidate(ctx context.Context, path string, source ExecutableSource, request normalizedResolveRequest) (ExecutableIdentity, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || isExcluded(absolute, request.excludedRoots) {
		return ExecutableIdentity{}, executableError(ExecutableErrorExcluded, err)
	}
	if !candidateNameAllowed(request.kind, absolute, source) {
		return ExecutableIdentity{}, executableError(ExecutableErrorExcluded, nil)
	}
	canonical, err := canonicalExistingPath(absolute)
	if err != nil {
		if source == ExecutablePATH {
			return ExecutableIdentity{}, executableError(ExecutableErrorNotFound, err)
		}
		return ExecutableIdentity{}, executableError(ExecutableErrorNotFound, err)
	}
	if isExcluded(canonical, request.excludedRoots) || (source == ExecutableConfigured && !candidateNameAllowed(request.kind, canonical, source)) {
		return ExecutableIdentity{}, executableError(ExecutableErrorExcluded, nil)
	}
	identity, err := inspectExecutable(ctx, canonical)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	identity.Kind = request.kind
	identity.Source = source
	identity.CanonicalPath = canonical
	identity.excludedRoots = append([]string(nil), request.excludedRoots...)
	return identity, nil
}

func sameExecutableIdentity(expected, current ExecutableIdentity) bool {
	return expected.Kind == current.Kind && expected.Source == current.Source && expected.CanonicalPath == current.CanonicalPath && bytes.Equal(expected.NativeID, current.NativeID) && expected.Size == current.Size && expected.ModTime.Equal(current.ModTime) && expected.SHA256 == current.SHA256
}

func executableError(code ExecutableErrorCode, cause error) error {
	return &ExecutableError{Code: code, Cause: cause}
}

func isCandidateSkip(err error) bool {
	var executableErr *ExecutableError
	if !errors.As(err, &executableErr) {
		return false
	}
	return executableErr.Code == ExecutableErrorNotFound || executableErr.Code == ExecutableErrorExcluded || executableErr.Code == ExecutableErrorUnsupported || executableErr.Code == ExecutableErrorIdentityChanged
}

func validExecutableKind(kind ExecutableKind) bool {
	return kind == ExecutableGit || kind == ExecutableCodex
}

func validExecutableSource(source ExecutableSource) bool {
	return source == ExecutableConfigured || source == ExecutablePATH
}

func validSafeText(value string) bool {
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

func safeHealthText(value string) string {
	if !utf8.ValidString(value) {
		return "<invalid>"
	}
	var builder strings.Builder
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			builder.WriteString("?")
		} else {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func normalizeAbsoluteRoot(value string) (string, error) {
	if value == "" || len(value) > executablePathLimit || !validSafeText(value) || !filepath.IsAbs(value) {
		return "", executableError(ExecutableErrorInvalidInput, nil)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", executableError(ExecutableErrorInvalidInput, err)
	}
	if canonical, err := canonicalExistingPath(abs); err == nil {
		return canonical, nil
	}
	return filepath.Clean(abs), nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		key := comparisonPath(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func isExcluded(path string, roots []string) bool {
	for _, root := range roots {
		if sameOrDescendant(root, path) {
			return true
		}
	}
	return false
}

func sameOrDescendant(root, path string) bool {
	root = comparisonPath(root)
	path = comparisonPath(path)
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	parent := ".." + string(os.PathSeparator)
	return relative != ".." && !strings.HasPrefix(relative, parent)
}

func comparisonPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func executableName(kind ExecutableKind) string {
	if runtime.GOOS == "windows" {
		return string(kind) + ".exe"
	}
	return string(kind)
}

func exactCandidate(directory, name string) (string, bool) {
	if runtime.GOOS != "windows" {
		return filepath.Join(directory, name), true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return filepath.Join(directory, entry.Name()), true
		}
	}
	return "", false
}

func candidateNameAllowed(kind ExecutableKind, path string, source ExecutableSource) bool {
	base := filepath.Base(path)
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Ext(base), ".exe") {
			return false
		}
		return source == ExecutableConfigured || strings.EqualFold(base, executableName(kind))
	}
	return source == ExecutableConfigured || base == executableName(kind)
}

func inspectExecutable(ctx context.Context, path string) (ExecutableIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ExecutableIdentity{}, executableError(ExecutableErrorCanceled, err)
	}
	before, err := nativeExecutableIdentity(path)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	digest, err := digestExecutable(ctx, path)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	after, err := nativeExecutableIdentity(path)
	if err != nil || !sameFileEvidence(before, after) {
		return ExecutableIdentity{}, ErrExecutableIdentityChanged
	}
	return ExecutableIdentity{NativeID: after.NativeID, Size: after.Size, ModTime: after.ModTime, SHA256: digest}, nil
}

type fileEvidence struct {
	NativeID []byte
	Size     int64
	ModTime  time.Time
}

func sameFileEvidence(left, right fileEvidence) bool {
	return bytes.Equal(left.NativeID, right.NativeID) && left.Size == right.Size && left.ModTime.Equal(right.ModTime)
}

func digestExecutable(ctx context.Context, path string) ([32]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, executableError(ExecutableErrorNotFound, err)
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, hashReadChunk)
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, executableError(ExecutableErrorCanceled, err)
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return [32]byte{}, executableError(ExecutableErrorNotFound, readErr)
		}
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

var _ ExecutableResolver = (*TrustedExecutableResolver)(nil)
