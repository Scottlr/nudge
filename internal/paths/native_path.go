package paths

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrNativePathReviewOnly reports a retained raw path that cannot receive a
	// native effect on the current platform or filesystem.
	ErrNativePathReviewOnly = errors.New("native path is review-only")
	// ErrNativePathStale reports root, parent, leaf, or collision evidence that
	// changed after resolution.
	ErrNativePathStale = errors.New("native path evidence is stale")
	// ErrNativePathOperation reports an operation outside the closed native
	// leaf-operation set.
	ErrNativePathOperation = errors.New("invalid native leaf operation")
)

// NativePathEvidence is re-exported from the repository domain so adapters
// can use the path-policy vocabulary without duplicating its shape.
type NativePathEvidence = repository.NativePathEvidence

// NativePathPurpose is the purpose-specific path contract used by the
// executor.
type NativePathPurpose = repository.NativePathPurpose

const (
	NativeReadExisting = repository.NativeReadExisting
	NativeCreateParent = repository.NativeCreateParent
	NativeReplaceLeaf  = repository.NativeReplaceLeaf
	NativeDeleteLeaf   = repository.NativeDeleteLeaf
)

// NativePathDisposition is re-exported for adapter callers.
type NativePathDisposition = repository.NativePathDisposition

const (
	NativePathSafe       = repository.NativePathSafe
	NativePathReviewOnly = repository.NativePathReviewOnly
)

// NativePathReason is re-exported for callers that own native qualification.
type NativePathReason = repository.NativePathReason

const (
	NativeReasonPathUnrepresentable      = repository.NativeReasonPathUnrepresentable
	NativeReasonGitAdminAlias            = repository.NativeReasonGitAdminAlias
	NativeReasonPathCollision            = repository.NativeReasonPathCollision
	NativeReasonPathContainmentUnproven  = repository.NativeReasonPathContainmentUnproven
	NativeReasonPathTraversal            = repository.NativeReasonPathTraversal
	NativeReasonInvalidSeparator         = repository.NativeReasonInvalidSeparator
	NativeReasonReservedName             = repository.NativeReasonReservedName
	NativeReasonNativeAlias              = repository.NativeReasonNativeAlias
	NativeReasonUnsupportedNormalization = repository.NativeReasonUnsupportedNormalization
	NativeReasonStaleEvidence            = repository.NativeReasonStaleEvidence
)

// VerifiedRoot is an immutable, canonical directory identity accepted by the
// native resolver. It is not a general filesystem capability.
type VerifiedRoot struct {
	Path     string
	Identity repository.NativeIdentity
}

// NewVerifiedRoot canonicalizes and verifies one existing real directory.
func NewVerifiedRoot(path string) (VerifiedRoot, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return VerifiedRoot{}, ErrProtectedPath
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(canonical) != canonical {
		return VerifiedRoot{}, ErrProtectedAlias
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return VerifiedRoot{}, ErrProtectedAlias
	}
	identity, err := NativeDirectoryIdentity(canonical)
	if err != nil {
		return VerifiedRoot{}, err
	}
	return VerifiedRoot{Path: canonical, Identity: identity}, nil
}

func (r VerifiedRoot) Validate() error {
	if r.Path == "" || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Identity == "" {
		return ErrProtectedPath
	}
	return nil
}

// NativeLeafOperationKind is the closed set of leaf effects understood by the
// executor. No arbitrary callback or shell command is accepted.
type NativeLeafOperationKind string

const (
	NativeLeafInspect        NativeLeafOperationKind = "inspect"
	NativeLeafRead           NativeLeafOperationKind = "read"
	NativeLeafCreate         NativeLeafOperationKind = "create"
	NativeLeafReplace        NativeLeafOperationKind = "replace"
	NativeLeafDelete         NativeLeafOperationKind = "delete"
	NativeLeafReadlink       NativeLeafOperationKind = "readlink"
	NativeLeafSymlink        NativeLeafOperationKind = "symlink"
	NativeLeafReplaceSymlink NativeLeafOperationKind = "replace_symlink"
)

// NativeLeafResult carries bounded data from typed read operations. It is
// caller-owned and never stored in a token or evidence value.
type NativeLeafResult struct {
	Data            []byte
	Target          string
	SymlinkEvidence *repository.SymlinkEvidence
}

// NativeLeafOperation describes one typed no-follow leaf request.
type NativeLeafOperation struct {
	Kind            NativeLeafOperationKind
	Data            []byte
	Mode            os.FileMode
	Target          string
	MaxBytes        int64
	ExpectedKind    repository.FileKind
	SymlinkEvidence *repository.SymlinkEvidence
	Result          *NativeLeafResult
}

func (o NativeLeafOperation) validate() error {
	switch o.Kind {
	case NativeLeafInspect, NativeLeafRead, NativeLeafCreate, NativeLeafReplace, NativeLeafDelete, NativeLeafReadlink, NativeLeafSymlink, NativeLeafReplaceSymlink:
	default:
		return ErrNativePathOperation
	}
	if o.Result == nil && (o.Kind == NativeLeafRead || o.Kind == NativeLeafReadlink) {
		return ErrNativePathOperation
	}
	if o.Result != nil && o.Kind != NativeLeafRead && o.Kind != NativeLeafReadlink {
		return ErrNativePathOperation
	}
	if o.Kind == NativeLeafRead && o.MaxBytes <= 0 {
		return ErrNativePathOperation
	}
	if (o.Kind == NativeLeafCreate || o.Kind == NativeLeafReplace) && o.Mode.Perm() == 0 {
		return ErrNativePathOperation
	}
	if (o.Kind == NativeLeafSymlink || o.Kind == NativeLeafReplaceSymlink) && (o.Target == "" || len(o.Target) > repository.SymlinkNativeActionMax || strings.IndexByte(o.Target, 0) >= 0 || o.SymlinkEvidence == nil) {
		return ErrNativePathOperation
	}
	if o.ExpectedKind != "" && o.ExpectedKind != repository.FileKindRegular && o.ExpectedKind != repository.FileKindSymlink {
		return ErrNativePathOperation
	}
	return nil
}

// NativePathToken is an ephemeral adapter-owned resolution. Its native path
// and open parent handles are intentionally inaccessible to callers.
type NativePathToken struct {
	state *nativePathToken
}

type nativePathToken struct {
	root       VerifiedRoot
	path       repository.RepoPath
	nativePath string
	parentPath string
	leaf       string
	purpose    NativePathPurpose
	evidence   NativePathEvidence
	parents    []pathObservation
	leafState  fileObservation
	leafExists bool
	rootHandle *os.File
	parentFile *os.File
	closed     bool
}

// Close releases the ephemeral root and parent handles. ExecuteLeaf also
// closes the token after the typed operation.
func (t *NativePathToken) Close() error {
	if t == nil || t.state == nil || t.state.closed {
		return nil
	}
	t.state.closed = true
	var first error
	if t.state.parentFile != nil {
		if err := t.state.parentFile.Close(); err != nil && first == nil {
			first = err
		}
	}
	if t.state.rootHandle != nil {
		if err := t.state.rootHandle.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// NativePathExecutor is the sole native repository-relative path effect seam.
type NativePathExecutor interface {
	Resolve(context.Context, VerifiedRoot, repository.RepoPath, NativePathPurpose) (NativePathToken, NativePathEvidence, error)
	Revalidate(context.Context, NativePathToken, NativePathEvidence) error
	ExecuteLeaf(context.Context, NativePathToken, NativePathEvidence, NativeLeafOperation) (NativePathEvidence, error)
}

// NativePathResolver is the platform-qualified native path executor.
type NativePathResolver struct {
	platform        string
	filesystemClass string
}

var _ NativePathExecutor = (*NativePathResolver)(nil)

// NewNativePathExecutor returns the current platform's path executor.
func NewNativePathExecutor() NativePathExecutor {
	return NewNativePathResolver()
}

// NewNativePathResolver returns a resolver whose policy is fixed to the
// current process platform and does not depend on locale casing.
func NewNativePathResolver() *NativePathResolver {
	filesystemClass := "case_sensitive"
	if runtime.GOOS == "windows" {
		filesystemClass = "case_insensitive"
	} else if runtime.GOOS == "darwin" {
		filesystemClass = "case_insensitive_normalization_unproven"
	}
	return &NativePathResolver{platform: runtime.GOOS, filesystemClass: filesystemClass}
}

// QualifyRepoPath classifies raw repository bytes without dropping the
// identity. It performs no filesystem access.
func (r *NativePathResolver) QualifyRepoPath(path repository.RepoPath) (repository.NativePathDisposition, string) {
	if r == nil {
		return repository.NativePathReviewOnly, string(repository.NativeReasonPathContainmentUnproven)
	}
	if err := path.Validate(); err != nil {
		return repository.NativePathReviewOnly, string(repository.NativeReasonPathUnrepresentable)
	}
	if runtime.GOOS == "windows" && !utf8.Valid(path) {
		return repository.NativePathReviewOnly, string(repository.NativeReasonPathUnrepresentable)
	}
	if len(path) == 0 || path[0] == '/' || runtime.GOOS == "windows" && path[0] == '\\' {
		return repository.NativePathReviewOnly, string(repository.NativeReasonPathTraversal)
	}
	components := bytes.Split(path, []byte{'/'})
	for _, component := range components {
		if len(component) == 0 || bytes.Equal(component, []byte(".")) || bytes.Equal(component, []byte("..")) {
			return repository.NativePathReviewOnly, string(repository.NativeReasonPathTraversal)
		}
		if bytes.EqualFold(component, []byte(".git")) {
			return repository.NativePathReviewOnly, string(repository.NativeReasonGitAdminAlias)
		}
		if runtime.GOOS == "windows" {
			if reason := windowsComponentReason(string(component)); reason != "" {
				return repository.NativePathReviewOnly, reason
			}
		} else if runtime.GOOS == "darwin" && !utf8.Valid(component) {
			return repository.NativePathReviewOnly, string(repository.NativeReasonPathUnrepresentable)
		}
	}
	return repository.NativePathSafe, ""
}

// QualifySymlinkTarget classifies an exact link target against the held root
// and parent chain without following the referent. Unsafe targets remain
// representable evidence but are never actionable.
func (r *NativePathResolver) QualifySymlinkTarget(root VerifiedRoot, path repository.RepoPath, target []byte) (repository.SymlinkEvidence, error) {
	if r == nil || root.Validate() != nil || path.Validate() != nil {
		return repository.SymlinkEvidence{}, ErrProtectedPath
	}
	nativePath, err := nativeRelativePath(path)
	if err != nil {
		return repository.SymlinkEvidence{}, err
	}
	parents, parentFile, err := observeParentChain(root.Path, filepath.Dir(filepath.Join(root.Path, nativePath)))
	if parentFile != nil {
		_ = parentFile.Close()
	}
	if err != nil {
		return repository.SymlinkEvidence{}, err
	}
	return repository.NewSymlinkEvidence(path, target, rootIdentityHash(root.Identity), parentObservationHash(parents), r.platform, repository.SymlinkPrimitiveVersion)
}

// Resolve qualifies and binds one path to a verified root. A review-only path
// returns its stable evidence and a typed error; the raw path remains intact
// for read-only projections.
func (r *NativePathResolver) Resolve(ctx context.Context, root VerifiedRoot, path repository.RepoPath, purpose NativePathPurpose) (NativePathToken, NativePathEvidence, error) {
	if r == nil || ctx == nil || root.Validate() != nil || purpose.Validate() != nil || path.Validate() != nil {
		return NativePathToken{}, NativePathEvidence{}, ErrProtectedPath
	}
	if err := ctx.Err(); err != nil {
		return NativePathToken{}, NativePathEvidence{}, err
	}
	disposition, reason := r.QualifyRepoPath(path)
	nativeRelative, err := nativeRelativePath(path)
	if err != nil {
		disposition, reason = repository.NativePathReviewOnly, string(repository.NativeReasonPathUnrepresentable)
	}
	if disposition != repository.NativePathSafe {
		evidence := r.evidence(root, path, disposition, reason, "", "")
		return NativePathToken{}, evidence, fmt.Errorf("%w: %s", ErrNativePathReviewOnly, reason)
	}
	nativePath := filepath.Join(root.Path, nativeRelative)
	if !contained(root.Path, nativePath) {
		evidence := r.evidence(root, path, repository.NativePathReviewOnly, string(repository.NativeReasonPathContainmentUnproven), "", "")
		return NativePathToken{}, evidence, fmt.Errorf("%w: %s", ErrNativePathReviewOnly, evidence.ReasonCode)
	}
	parentPath := filepath.Dir(nativePath)
	parents, parentFile, err := observeParentChain(root.Path, parentPath)
	if err != nil {
		evidence := r.evidence(root, path, repository.NativePathReviewOnly, string(repository.NativeReasonPathContainmentUnproven), "", parentObservationHash(parents))
		return NativePathToken{}, evidence, fmt.Errorf("%w: %s", ErrNativePathReviewOnly, evidence.ReasonCode)
	}
	if reason = collisionReason(parentPath, filepath.Base(nativePath), r.filesystemClass); reason != "" {
		_ = parentFile.Close()
		evidence := r.evidence(root, path, repository.NativePathReviewOnly, reason, "", parentObservationHash(parents))
		return NativePathToken{}, evidence, fmt.Errorf("%w: %s", ErrNativePathReviewOnly, reason)
	}
	leafState, leafExists, err := observeLeaf(nativePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = parentFile.Close()
		return NativePathToken{}, NativePathEvidence{}, err
	}
	if purpose == repository.NativeReadExisting || purpose == repository.NativeReplaceLeaf || purpose == repository.NativeDeleteLeaf {
		if !leafExists {
			_ = parentFile.Close()
			return NativePathToken{}, NativePathEvidence{}, os.ErrNotExist
		}
	}
	if purpose == repository.NativeCreateParent && leafExists {
		// A create-parent resolution is allowed to inspect an existing leaf; the
		// typed create operation will apply exact no-replace semantics later.
	}
	rootHandle, err := os.Open(root.Path)
	if err != nil {
		_ = parentFile.Close()
		return NativePathToken{}, NativePathEvidence{}, err
	}
	evidence := r.evidence(root, path, repository.NativePathSafe, "", comparisonKeyHash(root.Path, path, r.filesystemClass), parentObservationHash(parents))
	token := NativePathToken{state: &nativePathToken{root: root, path: repository.RepoPath(path.Bytes()), nativePath: nativePath, parentPath: parentPath, leaf: filepath.Base(nativePath), purpose: purpose, evidence: evidence, parents: parents, leafState: leafState, leafExists: leafExists, rootHandle: rootHandle, parentFile: parentFile}}
	return token, evidence, nil
}

// Revalidate rechecks the root, parent chain, leaf presence, and collision
// set. It never authorizes an operation by itself.
func (r *NativePathResolver) Revalidate(ctx context.Context, token NativePathToken, expected NativePathEvidence) error {
	if r == nil || ctx == nil || token.state == nil || token.state.closed || expected.Validate() != nil || token.state.evidence != expected {
		return ErrNativePathStale
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentRoot, err := NewVerifiedRoot(token.state.root.Path)
	if err != nil || currentRoot != token.state.root {
		return ErrNativePathStale
	}
	parents, parentFile, err := observeParentChain(token.state.root.Path, token.state.parentPath)
	if parentFile != nil {
		defer parentFile.Close()
	}
	if err != nil || parentObservationHash(parents) != expected.ParentChainHash {
		return ErrNativePathStale
	}
	if reason := collisionReason(token.state.parentPath, token.state.leaf, r.filesystemClass); reason != "" {
		return ErrNativePathStale
	}
	leafState, leafExists, err := observeLeaf(token.state.nativePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) || leafExists != token.state.leafExists || leafExists && leafState != token.state.leafState {
		return ErrNativePathStale
	}
	return nil
}

// ExecuteLeaf revalidates immediately before one typed operation and closes
// the ephemeral token after the operation.
func (r *NativePathResolver) ExecuteLeaf(ctx context.Context, token NativePathToken, expected NativePathEvidence, operation NativeLeafOperation) (NativePathEvidence, error) {
	if err := operation.validate(); err != nil {
		return NativePathEvidence{}, err
	}
	if err := r.Revalidate(ctx, token, expected); err != nil {
		return NativePathEvidence{}, err
	}
	defer token.Close()
	if token.state == nil {
		return NativePathEvidence{}, ErrNativePathStale
	}
	path := token.state.nativePath
	var err error
	switch operation.Kind {
	case NativeLeafInspect:
		_, err = os.Lstat(path)
	case NativeLeafRead:
		var data []byte
		data, err = readNativeRegularFile(path, operation.MaxBytes)
		if err == nil {
			operation.Result.Data = data
		}
	case NativeLeafCreate:
		err = writeNativeFile(path, operation.Data, operation.Mode, true)
	case NativeLeafReplace:
		err = writeNativeFile(path, operation.Data, operation.Mode, false)
	case NativeLeafDelete:
		info, statErr := os.Lstat(path)
		if statErr == nil && info.IsDir() {
			return NativePathEvidence{}, ErrNativePathOperation
		}
		if statErr == nil && operation.ExpectedKind == repository.FileKindSymlink && info.Mode()&os.ModeSymlink == 0 {
			return NativePathEvidence{}, ErrNativePathOperation
		}
		if statErr == nil {
			err = os.Remove(path)
		} else {
			err = statErr
		}
	case NativeLeafReadlink:
		var info os.FileInfo
		info, err = os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink == 0 {
			err = ErrNativePathOperation
		}
		if err == nil {
			operation.Result.Target, err = os.Readlink(path)
		}
		if err == nil && operation.MaxBytes > 0 && int64(len(operation.Result.Target)) > operation.MaxBytes {
			err = io.ErrShortBuffer
		}
		if err == nil {
			value, evidenceErr := r.symlinkEvidence(token, []byte(operation.Result.Target))
			if evidenceErr != nil {
				err = evidenceErr
			} else {
				operation.Result.SymlinkEvidence = &value
			}
		}
	case NativeLeafSymlink:
		err = r.createSymlink(token, operation, false)
	case NativeLeafReplaceSymlink:
		err = r.createSymlink(token, operation, true)
	}
	if err != nil {
		return NativePathEvidence{}, err
	}
	return expected, nil
}

func (r *NativePathResolver) symlinkEvidence(token NativePathToken, target []byte) (repository.SymlinkEvidence, error) {
	if token.state == nil {
		return repository.SymlinkEvidence{}, ErrNativePathStale
	}
	return repository.NewSymlinkEvidence(token.state.path, target, rootIdentityHash(token.state.root.Identity), parentObservationHash(token.state.parents), r.platform, repository.SymlinkPrimitiveVersion)
}

func (r *NativePathResolver) createSymlink(token NativePathToken, operation NativeLeafOperation, replace bool) error {
	if token.state == nil || operation.SymlinkEvidence == nil {
		return ErrNativePathOperation
	}
	if operation.SymlinkEvidence.Validate() != nil || !operation.SymlinkEvidence.IsActionable() {
		return ErrNativePathReviewOnly
	}
	actual, err := r.symlinkEvidence(token, []byte(operation.Target))
	if err != nil || actual != *operation.SymlinkEvidence {
		return ErrNativePathStale
	}
	if replace {
		info, statErr := os.Lstat(token.state.nativePath)
		if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
			return ErrNativePathOperation
		}
		if err := os.Remove(token.state.nativePath); err != nil {
			return err
		}
	}
	if err := os.Symlink(operation.Target, token.state.nativePath); err != nil {
		return err
	}
	info, err := os.Lstat(token.state.nativePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ErrNativePathStale
	}
	readback, err := os.Readlink(token.state.nativePath)
	if err != nil || readback != operation.Target {
		return ErrNativePathStale
	}
	return nil
}

func nativeRelativePath(path repository.RepoPath) (string, error) {
	if err := path.Validate(); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && !utf8.Valid(path) {
		return "", repository.ErrInvalidRepoPath
	}
	value := filepath.FromSlash(string(path))
	if filepath.IsAbs(value) || value == "." || value == ".." || isParentRelative(value) {
		return "", ErrProtectedPath
	}
	return value, nil
}

func windowsComponentReason(component string) string {
	if !utf8.ValidString(component) || strings.ContainsAny(component, `\\:*?"<>|`) || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return string(repository.NativeReasonPathUnrepresentable)
	}
	for _, r := range component {
		if r < 0x20 {
			return string(repository.NativeReasonPathUnrepresentable)
		}
	}
	trimmed := strings.TrimRight(component, " .")
	base := trimmed
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return string(repository.NativeReasonReservedName)
	default:
		return ""
	}
}

type pathObservation struct {
	path     string
	identity repository.NativeIdentity
}

type fileObservation struct {
	mode uint32
	size int64
	mod  int64
	name string
}

func observeParentChain(root, parent string) ([]pathObservation, *os.File, error) {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || isParentRelative(relative) {
		return nil, nil, ErrProtectedPath
	}
	current := root
	parents := []pathObservation{}
	rootIdentity, err := NativeDirectoryIdentity(root)
	if err != nil {
		return nil, nil, err
	}
	parents = append(parents, pathObservation{path: root, identity: rootIdentity})
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." {
				return nil, nil, ErrProtectedPath
			}
			current = filepath.Join(current, component)
			info, statErr := os.Lstat(current)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, nil, ErrProtectedAlias
			}
			identity, identityErr := NativeDirectoryIdentity(current)
			if identityErr != nil {
				return nil, nil, identityErr
			}
			parents = append(parents, pathObservation{path: current, identity: identity})
		}
	}
	file, err := os.Open(parent)
	if err != nil {
		return nil, nil, err
	}
	return parents, file, nil
}

func observeLeaf(path string) (fileObservation, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileObservation{}, false, nil
	}
	if err != nil {
		return fileObservation{}, false, err
	}
	return fileObservation{mode: uint32(info.Mode()), size: info.Size(), mod: info.ModTime().UnixNano(), name: filepath.Base(path)}, true, nil
}

func collisionReason(parent, requested, filesystemClass string) string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return string(repository.NativeReasonPathContainmentUnproven)
	}
	requestedKey := comparisonName(requested, filesystemClass)
	names := make(map[string][]string)
	for _, entry := range entries {
		key := comparisonName(entry.Name(), filesystemClass)
		names[key] = append(names[key], entry.Name())
	}
	for key, values := range names {
		if len(values) > 1 {
			return string(repository.NativeReasonPathCollision)
		}
		if key == requestedKey && values[0] != requested {
			return string(repository.NativeReasonNativeAlias)
		}
	}
	return ""
}

func comparisonName(name, filesystemClass string) string {
	if filesystemClass == "case_sensitive" {
		return name
	}
	return strings.ToLower(strings.TrimRight(name, " ."))
}

func comparisonKeyHash(root string, path repository.RepoPath, filesystemClass string) string {
	value := filepath.ToSlash(filepath.Join(root, string(path)))
	value = comparisonName(value, filesystemClass)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func parentObservationHash(parents []pathObservation) string {
	hash := sha256.New()
	for _, parent := range parents {
		_, _ = io.WriteString(hash, parent.path)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, string(parent.identity))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (r *NativePathResolver) evidence(root VerifiedRoot, path repository.RepoPath, disposition repository.NativePathDisposition, reason, comparison, parent string) repository.NativePathEvidence {
	if comparison == "" {
		comparison = comparisonKeyHash(root.Path, path, r.filesystemClass)
	}
	if parent == "" {
		parent = parentObservationHash(nil)
	}
	return repository.NativePathEvidence{RepoPathKey: path.Key(), RootIdentity: rootIdentityHash(root.Identity), Platform: r.platform, FilesystemClass: r.filesystemClass, ComparisonKeyHash: comparison, ParentChainHash: parent, Disposition: disposition, ReasonCode: reason, EvidenceVersion: repository.NativePathEvidenceVersion}
}

func rootIdentityHash(identity repository.NativeIdentity) string {
	digest := sha256.Sum256([]byte(string(identity)))
	return hex.EncodeToString(digest[:])
}

func readNativeRegularFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrNativePathOperation
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, io.ErrShortBuffer
	}
	return data, nil
}

func writeNativeFile(path string, data []byte, mode os.FileMode, create bool) error {
	info, err := os.Lstat(path)
	if !create && (err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return ErrNativePathOperation
	}
	flags := os.O_WRONLY
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
