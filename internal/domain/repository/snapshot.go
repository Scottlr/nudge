package repository

import (
	"fmt"

	"github.com/Scottlr/nudge/internal/domain"
)

// ObjectID is an opaque identity for an installed Git object. Its format and
// length are validated by the Git adapter, not assumed by domain code.
type ObjectID string

// NewObjectID constructs an opaque object identity without imposing SHA-1 or
// SHA-256 length rules.
func NewObjectID(value string) (ObjectID, error) {
	if value == "" || !validText(value) {
		return "", ErrInvalidObjectID
	}
	return ObjectID(value), nil
}

// SnapshotKind identifies the source of a snapshot reference.
type SnapshotKind string

const (
	// SnapshotCommit identifies a frozen commit object.
	SnapshotCommit SnapshotKind = "commit"
	// SnapshotTree identifies a frozen tree object.
	SnapshotTree SnapshotKind = "tree"
	// SnapshotWorkingTree identifies an immutable fingerprinted working-tree capture.
	SnapshotWorkingTree SnapshotKind = "working_tree"
	// SnapshotEmpty identifies an explicitly empty snapshot.
	SnapshotEmpty SnapshotKind = "empty"
)

// SnapshotRef identifies immutable content by kind and the identity evidence
// appropriate to that kind.
type SnapshotRef struct {
	Kind        SnapshotKind
	ObjectID    ObjectID
	WorktreeID  domain.WorktreeID
	Fingerprint string
}

// NewSnapshotRef validates and returns a snapshot reference.
func NewSnapshotRef(ref SnapshotRef) (SnapshotRef, error) {
	if err := ref.Validate(); err != nil {
		return SnapshotRef{}, err
	}
	return ref, nil
}

// Validate enforces the identity rules for each snapshot kind. A working-tree
// snapshot is identified by its worktree and fingerprint, never by a
// fabricated Git object ID.
func (r SnapshotRef) Validate() error {
	switch r.Kind {
	case SnapshotCommit, SnapshotTree:
		if r.ObjectID == "" || r.WorktreeID != "" || r.Fingerprint != "" {
			return ErrInvalidSnapshotRef
		}
		if _, err := NewObjectID(string(r.ObjectID)); err != nil {
			return fmt.Errorf("%w: object ID", ErrInvalidSnapshotRef)
		}
	case SnapshotWorkingTree:
		if r.ObjectID != "" || r.WorktreeID == "" || r.Fingerprint == "" || !validText(r.Fingerprint) {
			return ErrInvalidSnapshotRef
		}
	case SnapshotEmpty:
		if r.WorktreeID != "" || r.Fingerprint != "" {
			return ErrInvalidSnapshotRef
		}
		if r.ObjectID != "" {
			if _, err := NewObjectID(string(r.ObjectID)); err != nil {
				return fmt.Errorf("%w: object ID", ErrInvalidSnapshotRef)
			}
		}
	default:
		return ErrInvalidSnapshotRef
	}
	return nil
}
