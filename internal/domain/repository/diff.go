package repository

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	// ErrInvalidDiff reports a file diff with contradictory binary or hunk data.
	ErrInvalidDiff = errors.New("invalid file diff")
	// ErrInvalidDiffHunk reports an invalid hunk range or line accounting.
	ErrInvalidDiffHunk = errors.New("invalid diff hunk")
	// ErrInvalidDiffLine reports a line whose side identity does not match its kind.
	ErrInvalidDiffLine = errors.New("invalid diff line")
	// ErrInvalidPatchRange reports an invalid artifact range or digest.
	ErrInvalidPatchRange = errors.New("invalid patch byte range")
)

// DiffSide identifies one side of a two-way diff.
type DiffSide string

const (
	DiffBase DiffSide = "base"
	DiffHead DiffSide = "head"
)

// DiffLineKind describes the semantic role of one unified-diff line.
type DiffLineKind string

const (
	DiffLineContext   DiffLineKind = "context"
	DiffLineAdded     DiffLineKind = "added"
	DiffLineDeleted   DiffLineKind = "deleted"
	DiffLineNoNewline DiffLineKind = "no_newline"
)

func (k DiffLineKind) valid() bool {
	switch k {
	case DiffLineContext, DiffLineAdded, DiffLineDeleted, DiffLineNoNewline:
		return true
	default:
		return false
	}
}

// PatchArtifactID identifies immutable bytes stored outside the domain
// projection.
type PatchArtifactID string

// PatchByteRange identifies a bounded byte range in an immutable patch
// artifact. It never embeds the artifact bytes.
type PatchByteRange struct {
	ArtifactID PatchArtifactID
	Offset     int64
	Length     int64
	SHA256     string
}

// Validate checks identity, digest shape, and arithmetic safety.
func (r PatchByteRange) Validate() error {
	if r.ArtifactID == "" || !validText(string(r.ArtifactID)) || r.Offset < 0 || r.Length < 0 {
		return ErrInvalidPatchRange
	}
	if r.Offset > math.MaxInt64-r.Length || !validSHA256(r.SHA256) {
		return ErrInvalidPatchRange
	}
	return nil
}

// ValidateAgainstArtifact checks the range against independently known
// artifact identity, size, and digest without allocating the artifact.
func (r PatchByteRange) ValidateAgainstArtifact(id PatchArtifactID, size int64, sha256 string) error {
	if err := r.Validate(); err != nil || size < 0 || r.ArtifactID != id || r.Offset > size || r.Length > size-r.Offset || !validSHA256(sha256) || !strings.EqualFold(r.SHA256, sha256) {
		return ErrInvalidPatchRange
	}
	return nil
}

// DiffLine is one semantically classified line. Text is kept opaque to the
// domain; terminal projection happens elsewhere.
type DiffLine struct {
	Kind     DiffLineKind
	BaseLine *int
	HeadLine *int
	Text     string
}

// Validate checks that line numbers appear only on their owning diff sides.
func (l DiffLine) Validate() error {
	if !l.Kind.valid() {
		return ErrInvalidDiffLine
	}
	switch l.Kind {
	case DiffLineContext:
		if !positiveLine(l.BaseLine) || !positiveLine(l.HeadLine) {
			return ErrInvalidDiffLine
		}
	case DiffLineAdded:
		if l.BaseLine != nil || !positiveLine(l.HeadLine) {
			return ErrInvalidDiffLine
		}
	case DiffLineDeleted:
		if !positiveLine(l.BaseLine) || l.HeadLine != nil {
			return ErrInvalidDiffLine
		}
	case DiffLineNoNewline:
		if l.BaseLine != nil || l.HeadLine != nil {
			return ErrInvalidDiffLine
		}
	}
	return nil
}

func positiveLine(line *int) bool {
	return line != nil && *line > 0
}

// DiffHunk is one bounded structured diff hunk.
type DiffHunk struct {
	ID        string
	BaseStart int
	BaseCount int
	HeadStart int
	HeadCount int
	Header    string
	Lines     []DiffLine
}

// Validate checks hunk range arithmetic, line-side identity, and line counts.
func (h DiffHunk) Validate() error {
	if h.ID == "" || !validText(h.ID) || !validHunkRange(h.BaseStart, h.BaseCount) || !validHunkRange(h.HeadStart, h.HeadCount) {
		return ErrInvalidDiffHunk
	}
	baseLine := h.BaseStart
	headLine := h.HeadStart
	baseCount, headCount := 0, 0
	for _, line := range h.Lines {
		if err := line.Validate(); err != nil {
			return fmt.Errorf("%w: line", ErrInvalidDiffHunk)
		}
		switch line.Kind {
		case DiffLineContext:
			if *line.BaseLine != baseLine || *line.HeadLine != headLine {
				return ErrInvalidDiffHunk
			}
			baseLine++
			headLine++
			baseCount++
			headCount++
		case DiffLineAdded:
			if *line.HeadLine != headLine {
				return ErrInvalidDiffHunk
			}
			headLine++
			headCount++
		case DiffLineDeleted:
			if *line.BaseLine != baseLine {
				return ErrInvalidDiffHunk
			}
			baseLine++
			baseCount++
		case DiffLineNoNewline:
		}
	}
	if baseCount != h.BaseCount || headCount != h.HeadCount {
		return ErrInvalidDiffHunk
	}
	return nil
}

func validHunkRange(start, count int) bool {
	return start >= 0 && count >= 0 && (count == 0 || start > 0)
}

// FileDiff is a structured diff for one changed file.
type FileDiff struct {
	File           ChangedFile
	Hunks          []DiffHunk
	BinaryPatch    *PatchByteRange
	BinaryComplete bool
}

// Validate keeps binary bytes and textual hunks on separate representations.
func (d FileDiff) Validate() error {
	if err := d.File.Validate(); err != nil {
		return ErrInvalidDiff
	}
	if d.BinaryPatch != nil {
		if !d.File.Binary || len(d.Hunks) != 0 || d.BinaryPatch.Validate() != nil {
			return ErrInvalidDiff
		}
	}
	if d.BinaryComplete && !d.File.Binary {
		return ErrInvalidDiff
	}
	if d.File.Binary && len(d.Hunks) != 0 {
		return ErrInvalidDiff
	}
	for _, hunk := range d.Hunks {
		if err := hunk.Validate(); err != nil {
			return ErrInvalidDiff
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
