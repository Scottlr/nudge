package repository

import "errors"

var (
	// ErrInvalidFileContent reports contradictory content metadata.
	ErrInvalidFileContent = errors.New("invalid file content")
)

// FileContent is snapshot-bound file data. Bytes remain bytes until a
// presentation layer explicitly decodes them.
type FileContent struct {
	Snapshot      SnapshotRef
	Path          RepoPath
	Kind          FileKind
	Mode          uint32
	Bytes         []byte
	ByteLength    uint64
	ContentHash   string
	ContentClass  ContentClassV1
	TextSemantics *TextByteSemantics
	MetadataOnly  bool
	Binary        bool
	Truncated     bool
	LimitReason   string
}

// Validate checks content identity and bounded-read metadata.
func (c FileContent) Validate() error {
	if c.Snapshot.Validate() != nil || c.Path.Validate() != nil || !c.Kind.valid() || c.Kind == FileKindDirectory || ValidateGitMode(c.Mode) != nil || gitModeClassFileKind(ClassifyGitMode(c.Mode)) != c.Kind || !validContentHash(c.ContentHash) {
		return ErrInvalidFileContent
	}
	if c.MetadataOnly && c.ContentClass == "" {
		return ErrInvalidFileContent
	}
	if c.ContentClass != "" {
		if c.ContentClass.Validate() != nil || c.ContentClass.IsByteOriented() != c.Binary {
			return ErrInvalidFileContent
		}
		if c.MetadataOnly && !c.ContentClass.IsByteOriented() {
			return ErrInvalidFileContent
		}
		if c.ByteLength < uint64(len(c.Bytes)) {
			return ErrInvalidFileContent
		}
	} else if c.ByteLength != 0 && c.ByteLength < uint64(len(c.Bytes)) {
		return ErrInvalidFileContent
	}
	if c.TextSemantics != nil {
		if c.ContentClass != ContentClassRegularTextUTF8 || c.TextSemantics.Validate() != nil || c.TextSemantics.ByteLength != c.ByteLength || c.TextSemantics.SHA256 != c.ContentHash || c.Binary || c.MetadataOnly {
			return ErrInvalidFileContent
		}
	}
	if c.ContentClass == ContentClassRegularTextUTF8 && !c.Truncated && c.TextSemantics != nil && c.ByteLength != uint64(len(c.Bytes)) {
		return ErrInvalidFileContent
	}
	if !c.MetadataOnly && !c.Truncated && c.ByteLength != 0 && c.ByteLength != uint64(len(c.Bytes)) {
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
