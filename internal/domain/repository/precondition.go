package repository

import "errors"

var (
	// ErrInvalidPathPrecondition reports an absent/present or identity mismatch.
	ErrInvalidPathPrecondition = errors.New("invalid path precondition")
)

// NativeAliasEvidence records platform-qualified identity evidence for a
// native regular file. The values are opaque hashes, not display text or
// paths.
type NativeAliasEvidence struct {
	Platform           string
	VolumeIdentityHash string
	FileIdentityHash   string
	LinkCount          uint64
}

func (e NativeAliasEvidence) Validate() error {
	if !validText(e.Platform) || !validContentHash(e.VolumeIdentityHash) || !validContentHash(e.FileIdentityHash) || e.LinkCount == 0 {
		return ErrInvalidPathPrecondition
	}
	return nil
}

// PathPrecondition is the exact destination state required by a proposed
// patch for one repository path.
type PathPrecondition struct {
	Path              RepoPath
	MustExist         bool
	Kind              FileKind
	Mode              uint32
	ContentBytes      uint64
	ContentHash       string
	ContentClass      ContentClassV1
	TextSemantics     *TextByteSemantics
	SymlinkTargetHash string
	NativeAlias       *NativeAliasEvidence
	NativePath        *NativePathEvidence
}

// Validate preserves absent-file semantics and rejects contradictory
// metadata without converting repository paths to native paths.
func (p PathPrecondition) Validate() error {
	if p.Path.Validate() != nil {
		return ErrInvalidPathPrecondition
	}
	if !p.MustExist {
		if (p.Kind != "" && p.Kind != FileKindUnknown) || p.Mode != 0 || p.ContentBytes != 0 || p.ContentHash != "" || p.ContentClass != "" || p.TextSemantics != nil || p.SymlinkTargetHash != "" || p.NativeAlias != nil || p.NativePath != nil {
			return ErrInvalidPathPrecondition
		}
		return nil
	}
	if !p.Kind.valid() || p.Kind == FileKindUnknown {
		return ErrInvalidPathPrecondition
	}
	if ValidateGitMode(p.Mode) != nil || gitModeClassFileKind(ClassifyGitMode(p.Mode)) != p.Kind || (p.ContentHash != "" && !validContentHash(p.ContentHash)) || (p.ContentClass != "" && p.ContentClass.Validate() != nil) || (p.SymlinkTargetHash != "" && !validContentHash(p.SymlinkTargetHash)) {
		return ErrInvalidPathPrecondition
	}
	if p.Kind == FileKindSymlink {
		if p.SymlinkTargetHash == "" {
			return ErrInvalidPathPrecondition
		}
	} else if p.SymlinkTargetHash != "" {
		return ErrInvalidPathPrecondition
	}
	if p.Kind == FileKindRegular && p.ContentHash == "" {
		return ErrInvalidPathPrecondition
	}
	if p.Kind != FileKindRegular && (p.ContentBytes != 0 || p.ContentClass != "" || p.TextSemantics != nil) {
		return ErrInvalidPathPrecondition
	}
	if p.TextSemantics != nil && (p.Kind != FileKindRegular || p.ContentClass != ContentClassRegularTextUTF8 || p.TextSemantics.Validate() != nil || p.TextSemantics.ByteLength != p.ContentBytes || p.TextSemantics.SHA256 != p.ContentHash) {
		return ErrInvalidPathPrecondition
	}
	if p.NativeAlias != nil {
		if p.Kind != FileKindRegular || p.NativeAlias.Validate() != nil {
			return ErrInvalidPathPrecondition
		}
	}
	if p.NativePath != nil && (p.NativePath.Validate() != nil || p.NativePath.RepoPathKey != p.Path.Key()) {
		return ErrInvalidPathPrecondition
	}
	return nil
}
