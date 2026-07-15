package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

const displayedContentVersion uint32 = 1

var (
	// ErrInvalidDisplayedContent reports malformed immutable content identity or
	// display metadata.
	ErrInvalidDisplayedContent = errors.New("invalid displayed content")
	// ErrInvalidCodeRowID reports a row identity that is not bound to content.
	ErrInvalidCodeRowID = errors.New("invalid code row ID")
	// ErrInvalidDisplayedRow reports contradictory row-side or placeholder data.
	ErrInvalidDisplayedRow = errors.New("invalid displayed row")
)

// DisplayedContentIdentity is the immutable input to one row-construction
// projection. CaptureIdentity is empty for targets that have no local
// capture; it is still represented separately from target and diff identity.
type DisplayedContentIdentity struct {
	TargetIdentity         string
	CaptureIdentity        string
	Base                   repository.SnapshotRef
	Head                   repository.SnapshotRef
	DiffIdentity           string
	RowConstructionVersion uint32
}

// DisplayedContentID identifies one immutable displayed-content revision.
type DisplayedContentID string

// NewDisplayedContentID derives a stable opaque identity from every input
// that can change row meaning. It does not use display text or native paths.
func NewDisplayedContentID(identity DisplayedContentIdentity) (DisplayedContentID, error) {
	if !validIdentity(identity.TargetIdentity) || !validIdentity(identity.DiffIdentity) || identity.RowConstructionVersion == 0 || identity.Base.Validate() != nil || identity.Head.Validate() != nil {
		return "", ErrInvalidDisplayedContent
	}
	if identity.CaptureIdentity != "" && !validIdentity(identity.CaptureIdentity) {
		return "", ErrInvalidDisplayedContent
	}
	hash := sha256.New()
	writeDisplayedPart(hash, "displayed-content-v1")
	writeDisplayedPart(hash, formatUint(displayedContentVersion))
	writeDisplayedPart(hash, identity.TargetIdentity)
	writeDisplayedPart(hash, identity.CaptureIdentity)
	writeSnapshotPart(hash, identity.Base)
	writeSnapshotPart(hash, identity.Head)
	writeDisplayedPart(hash, identity.DiffIdentity)
	writeDisplayedPart(hash, formatUint(identity.RowConstructionVersion))
	return DisplayedContentID(hex.EncodeToString(hash.Sum(nil))), nil
}

// Validate checks the opaque hexadecimal identity shape.
func (id DisplayedContentID) Validate() error {
	if len(id) != sha256.Size*2 || !validDisplayedHex(string(id)) {
		return ErrInvalidDisplayedContent
	}
	return nil
}

// DisplayMode selects a unified diff or one ordinary immutable source side.
type DisplayMode string

const (
	DisplayUnifiedDiff DisplayMode = "unified_diff"
	DisplaySource      DisplayMode = "source"
)

// ContentStatus describes explicit content states that must remain visible in
// the code pane instead of being rendered as an empty file.
type ContentStatus string

const (
	ContentReady    ContentStatus = "ready"
	ContentBinary   ContentStatus = "binary"
	ContentUnmerged ContentStatus = "unmerged"
	ContentLoading  ContentStatus = "loading"
	ContentError    ContentStatus = "error"
	ContentTooLarge ContentStatus = "too_large"
)

// DisplayedContent is the bounded metadata envelope for one code-pane input.
// Rows arrive separately through DisplayedContentPage.
type DisplayedContent struct {
	ID       DisplayedContentID
	Mode     DisplayMode
	Status   ContentStatus
	BasePath *repository.RepoPath
	HeadPath *repository.RepoPath
	Binary   *BinaryContentMetadata
	Reason   string
}

// BinaryContentMetadata is the bounded, non-text projection of one binary
// change. It contains identities and patch-range evidence, never file bytes.
type BinaryContentMetadata struct {
	BasePresent   bool
	BaseBytes     uint64
	BaseHash      string
	HeadPresent   bool
	HeadBytes     uint64
	HeadHash      string
	Change        string
	Patch         *repository.PatchByteRange
	PatchComplete bool
}

func (m BinaryContentMetadata) Validate() error {
	if !m.BasePresent && (m.BaseBytes != 0 || m.BaseHash != "") || !m.HeadPresent && (m.HeadBytes != 0 || m.HeadHash != "") || !validIdentity(m.Change) {
		return ErrInvalidDisplayedContent
	}
	if m.BasePresent && !validDisplayedHash(m.BaseHash) || m.HeadPresent && !validDisplayedHash(m.HeadHash) {
		return ErrInvalidDisplayedContent
	}
	if m.Patch != nil && m.Patch.Validate() != nil {
		return ErrInvalidDisplayedContent
	}
	return nil
}

// Validate checks the content envelope without reading bytes or rows.
func (c DisplayedContent) Validate() error {
	if c.ID.Validate() != nil || (c.Mode != DisplayUnifiedDiff && c.Mode != DisplaySource) || !validContentStatus(c.Status) {
		return ErrInvalidDisplayedContent
	}
	if c.BasePath != nil && c.BasePath.Validate() != nil || c.HeadPath != nil && c.HeadPath.Validate() != nil {
		return ErrInvalidDisplayedContent
	}
	if c.Binary != nil && c.Binary.Validate() != nil {
		return ErrInvalidDisplayedContent
	}
	if c.Status == ContentReady {
		if c.Reason != "" {
			return ErrInvalidDisplayedContent
		}
	} else if !validIdentity(c.Reason) {
		return ErrInvalidDisplayedContent
	}
	return nil
}

// RowSide identifies the content side a row belongs to.
type RowSide string

const (
	SideNone RowSide = "none"
	SideBase RowSide = "base"
	SideHead RowSide = "head"
	SideBoth RowSide = "both"
)

// DisplayedRowKind identifies one neutral logical row.
type DisplayedRowKind string

const (
	DisplayedRowDiffHeader  DisplayedRowKind = "diff_header"
	DisplayedRowHunkHeader  DisplayedRowKind = "hunk_header"
	DisplayedRowContext     DisplayedRowKind = "context"
	DisplayedRowAdded       DisplayedRowKind = "added"
	DisplayedRowDeleted     DisplayedRowKind = "deleted"
	DisplayedRowNoNewline   DisplayedRowKind = "no_newline"
	DisplayedRowSource      DisplayedRowKind = "source"
	DisplayedRowPlaceholder DisplayedRowKind = "placeholder"
)

// PlaceholderKind gives an explicit reason for a non-source row.
type PlaceholderKind string

const (
	PlaceholderBinary   PlaceholderKind = "binary"
	PlaceholderUnmerged PlaceholderKind = "unmerged"
	PlaceholderLoading  PlaceholderKind = "loading"
	PlaceholderError    PlaceholderKind = "error"
	PlaceholderTooLarge PlaceholderKind = "too_large"
)

// CodeRowID binds a stable ordinal to exactly one displayed-content identity.
type CodeRowID struct {
	Content DisplayedContentID
	Ordinal uint64
}

// Validate checks the content binding. Ordinal zero is valid and is the first
// row in a page-independent logical population.
func (id CodeRowID) Validate() error {
	if id.Content.Validate() != nil {
		return ErrInvalidCodeRowID
	}
	return nil
}

// Matches reports whether the row belongs to the supplied content revision.
func (id CodeRowID) Matches(content DisplayedContentID) bool {
	return id.Content == content && id.Validate() == nil
}

// DisplayedRow is application-owned row evidence. It retains both sides and
// stable identities; terminal styling and viewport state belong to the TUI.
type DisplayedRow struct {
	ID               CodeRowID
	Kind             DisplayedRowKind
	HunkID           string
	BasePath         *repository.RepoPath
	HeadPath         *repository.RepoPath
	BaseLine         *int
	HeadLine         *int
	BaseText         string
	HeadText         string
	Text             string
	Side             RowSide
	Selectable       bool
	ContextGroup     string
	ContextCollapsed bool
	Placeholder      PlaceholderKind
}

// Validate checks side identity and keeps placeholders inert.
func (r DisplayedRow) Validate() error {
	if r.ID.Validate() != nil || !validRowKind(r.Kind) || !validSide(r.Side) || r.BasePath != nil && r.BasePath.Validate() != nil || r.HeadPath != nil && r.HeadPath.Validate() != nil || !validLinePointer(r.BaseLine) || !validLinePointer(r.HeadLine) {
		return ErrInvalidDisplayedRow
	}
	if r.ContextCollapsed && r.ContextGroup == "" || r.ContextGroup != "" && !validIdentity(r.ContextGroup) {
		return ErrInvalidDisplayedRow
	}
	switch r.Kind {
	case DisplayedRowDiffHeader:
		if r.Side != SideNone || r.Selectable || r.HunkID != "" {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowNoNewline:
		if r.Side != SideNone || r.Selectable || !validIdentity(r.HunkID) {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowHunkHeader:
		if r.Side != SideNone || r.Selectable || !validIdentity(r.HunkID) {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowContext:
		if r.Side != SideBoth || !validIdentity(r.HunkID) || !r.Selectable || r.BaseLine == nil || r.HeadLine == nil {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowAdded:
		if r.Side != SideHead || !validIdentity(r.HunkID) || !r.Selectable || r.HeadLine == nil || r.BaseLine != nil {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowDeleted:
		if r.Side != SideBase || !validIdentity(r.HunkID) || !r.Selectable || r.BaseLine == nil || r.HeadLine != nil {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowSource:
		if (r.Side != SideBase && r.Side != SideHead) || !r.Selectable {
			return ErrInvalidDisplayedRow
		}
	case DisplayedRowPlaceholder:
		if r.Side != SideNone || r.Selectable || !validPlaceholder(r.Placeholder) {
			return ErrInvalidDisplayedRow
		}
	}
	return nil
}

// DisplayedContentPage is a bounded response for one immutable row query.
type DisplayedContentPage struct {
	ContentID  DisplayedContentID
	Cursor     string
	NextCursor string
	Rows       []DisplayedRow
}

// Validate checks page identity, cursor shape, and stable ordinal ordering.
func (p DisplayedContentPage) Validate() error {
	if p.ContentID.Validate() != nil || !validCursor(p.Cursor) || !validCursor(p.NextCursor) {
		return ErrInvalidDisplayedContent
	}
	var previous uint64
	for index, row := range p.Rows {
		if row.Validate() != nil || !row.ID.Matches(p.ContentID) || index > 0 && row.ID.Ordinal <= previous {
			return ErrInvalidDisplayedContent
		}
		previous = row.ID.Ordinal
	}
	return nil
}

// Clone returns defensive row/path ownership for a frontend projection.
func (p DisplayedContentPage) Clone() DisplayedContentPage {
	result := p
	result.Rows = make([]DisplayedRow, len(p.Rows))
	for index, row := range p.Rows {
		result.Rows[index] = cloneDisplayedRow(row)
	}
	return result
}

func cloneDisplayedRow(row DisplayedRow) DisplayedRow {
	if row.BasePath != nil {
		path := repository.RepoPath(row.BasePath.Bytes())
		row.BasePath = &path
	}
	if row.HeadPath != nil {
		path := repository.RepoPath(row.HeadPath.Bytes())
		row.HeadPath = &path
	}
	return row
}

func writeSnapshotPart(hash interface{ Write([]byte) (int, error) }, snapshot repository.SnapshotRef) {
	writeDisplayedPart(hash, string(snapshot.Kind))
	writeDisplayedPart(hash, string(snapshot.ObjectID))
	writeDisplayedPart(hash, string(snapshot.WorktreeID))
	writeDisplayedPart(hash, snapshot.Fingerprint)
}

func writeDisplayedPart(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [8]byte
	for index := range length {
		length[len(length)-index-1] = byte(uint64(len(value)) >> (index * 8))
	}
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}

func formatUint(value uint32) string {
	var result [4]byte
	result[0] = byte(value >> 24)
	result[1] = byte(value >> 16)
	result[2] = byte(value >> 8)
	result[3] = byte(value)
	return string(result[:])
}

func validIdentity(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) == value
}

func validDisplayedHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func validDisplayedHash(value string) bool {
	return len(value) == sha256.Size*2 && validDisplayedHex(value)
}

func validContentStatus(status ContentStatus) bool {
	switch status {
	case ContentReady, ContentBinary, ContentUnmerged, ContentLoading, ContentError, ContentTooLarge:
		return true
	default:
		return false
	}
}

func validRowKind(kind DisplayedRowKind) bool {
	switch kind {
	case DisplayedRowDiffHeader, DisplayedRowHunkHeader, DisplayedRowContext, DisplayedRowAdded, DisplayedRowDeleted, DisplayedRowNoNewline, DisplayedRowSource, DisplayedRowPlaceholder:
		return true
	default:
		return false
	}
}

func validSide(side RowSide) bool {
	return side == SideNone || side == SideBase || side == SideHead || side == SideBoth
}

func validPlaceholder(placeholder PlaceholderKind) bool {
	switch placeholder {
	case PlaceholderBinary, PlaceholderUnmerged, PlaceholderLoading, PlaceholderError, PlaceholderTooLarge:
		return true
	default:
		return false
	}
}

func validLinePointer(line *int) bool {
	return line == nil || *line > 0
}

func validCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	if !utf8.ValidString(cursor) {
		return false
	}
	for _, char := range cursor {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}
