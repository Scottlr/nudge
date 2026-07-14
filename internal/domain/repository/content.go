package repository

import "errors"

var (
	// ErrInvalidFileContent reports contradictory content metadata.
	ErrInvalidFileContent = errors.New("invalid file content")
)

// FileContent is snapshot-bound file data. Bytes remain bytes until a
// presentation layer explicitly decodes them.
type FileContent struct {
	Snapshot    SnapshotRef
	Path        RepoPath
	Kind        FileKind
	Mode        uint32
	Bytes       []byte
	ContentHash string
	Binary      bool
	Truncated   bool
	LimitReason string
}

// Validate checks content identity and bounded-read metadata.
func (c FileContent) Validate() error {
	if c.Snapshot.Validate() != nil || c.Path.Validate() != nil || !c.Kind.valid() || c.Kind == FileKindDirectory || c.Mode == 0 || !validContentHash(c.ContentHash) {
		return ErrInvalidFileContent
	}
	if c.Truncated {
		if !validText(c.LimitReason) {
			return ErrInvalidFileContent
		}
	} else if c.LimitReason != "" {
		return ErrInvalidFileContent
	}
	return nil
}

func validContentHash(value string) bool {
	return value != "" && validText(value)
}
