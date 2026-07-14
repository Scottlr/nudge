package repository

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidChangedFile reports contradictory change metadata.
	ErrInvalidChangedFile = errors.New("invalid changed file")
	// ErrInvalidConflictEvidence reports an impossible unmerged-stage set.
	ErrInvalidConflictEvidence = errors.New("invalid index conflict evidence")
)

// ChangeKind describes the semantic relationship between two tree entries.
type ChangeKind string

const (
	ChangeAdded       ChangeKind = "added"
	ChangeModified    ChangeKind = "modified"
	ChangeDeleted     ChangeKind = "deleted"
	ChangeRenamed     ChangeKind = "renamed"
	ChangeCopied      ChangeKind = "copied"
	ChangeTypeChanged ChangeKind = "type_changed"
	ChangeUntracked   ChangeKind = "untracked"
)

func (k ChangeKind) valid() bool {
	switch k {
	case ChangeAdded, ChangeModified, ChangeDeleted, ChangeRenamed, ChangeCopied, ChangeTypeChanged, ChangeUntracked:
		return true
	default:
		return false
	}
}

// IndexStage preserves one unmerged index stage without interpreting it as an
// instruction to resolve or mutate the index.
type IndexStage struct {
	Mode     uint32
	ObjectID ObjectID
}

func (s IndexStage) validate() error {
	if s.Mode == 0 || validatePresentObjectID(s.ObjectID) != nil {
		return ErrInvalidConflictEvidence
	}
	return nil
}

// IndexConflictEvidence preserves the porcelain-v2 unmerged record and its
// optional base, ours, and theirs entries.
type IndexConflictEvidence struct {
	Code   string
	Stage1 *IndexStage
	Stage2 *IndexStage
	Stage3 *IndexStage
}

// Validate rejects contradictory stage combinations while retaining valid
// delete/modify conflict evidence.
func (e IndexConflictEvidence) Validate() error {
	if e.Code != "u" || e.Stage1 == nil || (e.Stage2 == nil && e.Stage3 == nil) {
		return ErrInvalidConflictEvidence
	}
	if e.Stage1.validate() != nil {
		return ErrInvalidConflictEvidence
	}
	if e.Stage2 != nil && e.Stage2.validate() != nil {
		return ErrInvalidConflictEvidence
	}
	if e.Stage3 != nil && e.Stage3.validate() != nil {
		return ErrInvalidConflictEvidence
	}
	return nil
}

// ChangedFile describes one repository entry change. A nil path or object ID
// is meaningful absence, never a fabricated all-zero Git object.
type ChangedFile struct {
	OldPath     *RepoPath
	NewPath     *RepoPath
	Kind        ChangeKind
	OldFileKind FileKind
	NewFileKind FileKind
	OldMode     uint32
	NewMode     uint32
	OldObjectID *ObjectID
	NewObjectID *ObjectID
	Binary      bool
	Staged      bool
	Unstaged    bool
	Conflict    *IndexConflictEvidence
}

// Validate enforces path-side, object-side, and change-kind invariants.
func (f ChangedFile) Validate() error {
	if !f.Kind.valid() {
		return ErrInvalidChangedFile
	}
	if err := validateChangeSide(f.OldPath, f.OldFileKind, f.OldMode, f.OldObjectID); err != nil {
		return err
	}
	if err := validateChangeSide(f.NewPath, f.NewFileKind, f.NewMode, f.NewObjectID); err != nil {
		return err
	}

	oldPresent := f.OldPath != nil
	newPresent := f.NewPath != nil
	switch f.Kind {
	case ChangeAdded:
		if oldPresent || !newPresent {
			return ErrInvalidChangedFile
		}
	case ChangeModified:
		if !oldPresent || !newPresent || f.OldFileKind != f.NewFileKind {
			return ErrInvalidChangedFile
		}
	case ChangeDeleted:
		if !oldPresent || newPresent {
			return ErrInvalidChangedFile
		}
	case ChangeRenamed, ChangeCopied:
		if !oldPresent || !newPresent {
			return ErrInvalidChangedFile
		}
	case ChangeTypeChanged:
		if !oldPresent || !newPresent || f.OldFileKind == f.NewFileKind {
			return ErrInvalidChangedFile
		}
	case ChangeUntracked:
		if oldPresent || !newPresent || f.Staged || f.OldObjectID != nil {
			return ErrInvalidChangedFile
		}
	}
	if f.Conflict != nil && f.Conflict.Validate() != nil {
		return ErrInvalidChangedFile
	}
	return nil
}

func validateChangeSide(path *RepoPath, kind FileKind, mode uint32, objectID *ObjectID) error {
	if path == nil {
		if (kind != "" && kind != FileKindUnknown) || mode != 0 || objectID != nil {
			return ErrInvalidChangedFile
		}
		return nil
	}
	if err := path.Validate(); err != nil || !kind.valid() {
		return ErrInvalidChangedFile
	}
	if kind != FileKindUnknown && mode == 0 {
		return ErrInvalidChangedFile
	}
	if objectID != nil && validatePresentObjectID(*objectID) != nil {
		return ErrInvalidChangedFile
	}
	return nil
}

func validatePresentObjectID(id ObjectID) error {
	if id == "" || strings.Trim(string(id), "0") == "" {
		return ErrInvalidObjectID
	}
	if _, err := NewObjectID(string(id)); err != nil {
		return fmt.Errorf("%w: object ID", ErrInvalidObjectID)
	}
	return nil
}
