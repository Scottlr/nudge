package repository

import (
	"bytes"
	"errors"
)

var (
	// ErrInvalidRepoPath reports a path that cannot be retained as a Git path identity.
	ErrInvalidRepoPath = errors.New("invalid repository path")
	// ErrInvalidTreeEntry reports contradictory tree-entry metadata.
	ErrInvalidTreeEntry = errors.New("invalid tree entry")
)

// ByteRange identifies a half-open byte interval in one immutable path.
// Ranges are semantic match evidence; presentation must sanitize path bytes
// separately before mapping them to terminal cells.
type ByteRange struct {
	Start uint32
	End   uint32
}

// Validate checks the shape of a byte interval without interpreting its
// contents.
func (r ByteRange) Validate(size int) error {
	if r.Start >= r.End || uint64(r.End) > uint64(size) {
		return ErrInvalidRepoPath
	}
	return nil
}

// RepoPath is raw, repository-relative Git path identity. It is not a native
// path and has no display or terminal semantics.
type RepoPath []byte

// RepoPathKey is the lossless comparable form of a RepoPath.
type RepoPathKey string

// NewRepoPath retains a copy of raw Git path bytes. Traversal-looking,
// absolute, and native-invalid bytes remain representable; only an empty path
// or an embedded NUL is rejected because neither can be a lossless key.
func NewRepoPath(raw []byte) (RepoPath, error) {
	if len(raw) == 0 || bytes.IndexByte(raw, 0) >= 0 {
		return nil, ErrInvalidRepoPath
	}
	return RepoPath(append([]byte(nil), raw...)), nil
}

// NewRepoPathKey constructs a comparable repository path key.
func NewRepoPathKey(raw []byte) (RepoPathKey, error) {
	path, err := NewRepoPath(raw)
	if err != nil {
		return "", err
	}
	return path.Key(), nil
}

// Bytes returns a copy of the path's raw bytes.
func (p RepoPath) Bytes() []byte {
	return append([]byte(nil), p...)
}

// Key returns the lossless comparable key for the path.
func (p RepoPath) Key() RepoPathKey {
	return RepoPathKey(string(p))
}

// Path returns the raw path represented by a key.
func (k RepoPathKey) Path() (RepoPath, error) {
	return NewRepoPath([]byte(string(k)))
}

// Validate verifies that the path is a retained repository path.
func (p RepoPath) Validate() error {
	if len(p) == 0 || bytes.IndexByte(p, 0) >= 0 {
		return ErrInvalidRepoPath
	}
	return nil
}

// FileKind describes the kind of a repository entry.
type FileKind string

const (
	FileKindRegular   FileKind = "regular"
	FileKindSymlink   FileKind = "symlink"
	FileKindGitlink   FileKind = "gitlink"
	FileKindDirectory FileKind = "directory"
	FileKindUnknown   FileKind = "unknown"
)

func (k FileKind) valid() bool {
	switch k {
	case FileKindRegular, FileKindSymlink, FileKindGitlink, FileKindDirectory, FileKindUnknown:
		return true
	default:
		return false
	}
}

// TreeEntry is bounded repository tree metadata. Contents are loaded through
// a separate FileContent value.
type TreeEntry struct {
	Path           RepoPath
	Name           RepoPath
	Parent         RepoPath
	Kind           FileKind
	Mode           uint32
	ModeClass      GitModeClass
	ObjectID       *ObjectID
	ReviewOnly     *ReviewOnlyEntryEvidence
	LazyChild      bool
	ChangedSummary *ChangedFile
}

// Validate checks tree identity and metadata without interpreting paths as
// native paths.
func (e TreeEntry) Validate() error {
	if err := e.Path.Validate(); err != nil {
		return ErrInvalidTreeEntry
	}
	if err := e.Name.Validate(); err != nil || bytes.IndexByte(e.Name, '/') >= 0 {
		return ErrInvalidTreeEntry
	}
	if len(e.Parent) > 0 {
		if err := e.Parent.Validate(); err != nil {
			return ErrInvalidTreeEntry
		}
	}
	var expected []byte
	if len(e.Parent) > 0 {
		expected = append(append([]byte(nil), e.Parent...), '/')
	}
	expected = append(expected, e.Name...)
	if !bytes.Equal(expected, e.Path) {
		return ErrInvalidTreeEntry
	}
	if !e.Kind.valid() || (e.Kind == FileKindUnknown && e.Mode != 0) || (e.Kind != FileKindUnknown && ValidateGitMode(e.Mode) != nil) || e.Kind != FileKindUnknown && gitModeClassFileKind(ClassifyGitMode(e.Mode)) != e.Kind || e.ModeClass != "" && (e.ModeClass.Validate() != nil || e.ModeClass != ClassifyGitMode(e.Mode)) {
		return ErrInvalidTreeEntry
	}
	if e.LazyChild && e.Kind != FileKindDirectory {
		return ErrInvalidTreeEntry
	}
	if e.ObjectID != nil && validatePresentObjectID(*e.ObjectID) != nil {
		return ErrInvalidTreeEntry
	}
	if e.ReviewOnly != nil && (e.ReviewOnly.Validate() != nil || e.Kind != FileKindUnknown) {
		return ErrInvalidTreeEntry
	}
	if e.ChangedSummary != nil && e.ChangedSummary.Validate() != nil {
		return ErrInvalidTreeEntry
	}
	return nil
}
