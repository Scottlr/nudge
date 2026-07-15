// Package diff parses immutable Git unified-diff artifacts into bounded
// repository projections. It never executes Git or interprets terminal text.
package diff

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

const (
	// PatchIndexVersion identifies the persisted in-memory index shape.
	PatchIndexVersion  uint32 = 1
	defaultPatchBytes         = 256 * 1024 * 1024
	defaultFileBytes          = 16 * 1024 * 1024
	defaultBinaryBytes        = 256 * 1024 * 1024
	defaultLineBytes          = 1 * 1024 * 1024
	defaultHeaderBytes        = 64 * 1024
	defaultFiles              = 100_000
	defaultHunks              = 500_000
	defaultRows               = 10_000_000
)

var (
	// ErrInvalidPatchSource reports a missing or contradictory immutable source.
	ErrInvalidPatchSource = errors.New("invalid patch source")
	// ErrPatchMalformed reports a structurally invalid Git patch.
	ErrPatchMalformed = errors.New("malformed patch")
	// ErrPatchTruncated reports a source that ended before a complete record.
	ErrPatchTruncated = errors.New("truncated patch")
	// ErrPatchLimit reports a configured parser or resident-memory limit.
	ErrPatchLimit = errors.New("patch limit exceeded")
	// ErrPatchUnsupported reports a Git diff form outside the v1 two-way model.
	ErrPatchUnsupported = errors.New("unsupported patch form")
	// ErrPatchSink reports failure while publishing a complete index.
	ErrPatchSink = errors.New("patch index sink failed")
)

// PatchSource is an immutable, identity-bound patch artifact. Open is useful
// to adapters that need a sequential reader; the parser itself uses ReadAt so
// it can build a bounded index without retaining the complete patch.
type PatchSource interface {
	ID() string
	Size() int64
	Open() (io.ReadCloser, error)
	ReadAt([]byte, int64) (int, error)
}

// PatchParseLimits bounds patch scanning and the one-file parse window.
type PatchParseLimits struct {
	MaxPatchBytes  int64
	MaxFileBytes   int64
	MaxBinaryBytes int64
	MaxLineBytes   int
	MaxHeaderBytes int
	MaxFiles       int
	MaxHunks       int
	MaxRows        int
}

// DefaultPatchParseLimits returns the bounded v1 parser policy.
func DefaultPatchParseLimits() PatchParseLimits {
	return PatchParseLimits{
		MaxPatchBytes:  defaultPatchBytes,
		MaxFileBytes:   defaultFileBytes,
		MaxBinaryBytes: defaultBinaryBytes,
		MaxLineBytes:   defaultLineBytes,
		MaxHeaderBytes: defaultHeaderBytes,
		MaxFiles:       defaultFiles,
		MaxHunks:       defaultHunks,
		MaxRows:        defaultRows,
	}
}

func (l PatchParseLimits) withDefaults() PatchParseLimits {
	d := DefaultPatchParseLimits()
	if l.MaxPatchBytes != 0 {
		d.MaxPatchBytes = l.MaxPatchBytes
	}
	if l.MaxFileBytes != 0 {
		d.MaxFileBytes = l.MaxFileBytes
	}
	if l.MaxBinaryBytes != 0 {
		d.MaxBinaryBytes = l.MaxBinaryBytes
	}
	if l.MaxLineBytes != 0 {
		d.MaxLineBytes = l.MaxLineBytes
	}
	if l.MaxHeaderBytes != 0 {
		d.MaxHeaderBytes = l.MaxHeaderBytes
	}
	if l.MaxFiles != 0 {
		d.MaxFiles = l.MaxFiles
	}
	if l.MaxHunks != 0 {
		d.MaxHunks = l.MaxHunks
	}
	if l.MaxRows != 0 {
		d.MaxRows = l.MaxRows
	}
	return d
}

// Validate checks every configured bound and rejects integer-overflow risks.
func (l PatchParseLimits) Validate() error {
	if l.MaxPatchBytes <= 0 || l.MaxFileBytes <= 0 || l.MaxBinaryBytes <= 0 || l.MaxLineBytes <= 0 || l.MaxHeaderBytes <= 0 || l.MaxFiles <= 0 || l.MaxHunks <= 0 || l.MaxRows <= 0 {
		return ErrPatchLimit
	}
	if l.MaxLineBytes > int(math.MaxInt32) || l.MaxHeaderBytes > int(math.MaxInt32) {
		return ErrPatchLimit
	}
	return nil
}

func validateSource(source PatchSource) error {
	if source == nil || source.ID() == "" || strings.TrimSpace(source.ID()) != source.ID() || source.Size() < 0 {
		return ErrInvalidPatchSource
	}
	return nil
}

// PatchIndexIdentity identifies one completely scanned source.
type PatchIndexIdentity struct {
	Version   uint32
	SourceID  string
	Size      int64
	SHA256    string
	FileCount int
	HunkCount int
	RowCount  int
}

// Validate checks the complete source identity and bounded counts.
func (i PatchIndexIdentity) Validate() error {
	if i.Version != PatchIndexVersion || i.SourceID == "" || i.Size < 0 || !validSHA256(i.SHA256) || i.FileCount < 0 || i.HunkCount < 0 || i.RowCount < 0 {
		return ErrInvalidPatchSource
	}
	return nil
}

// PatchHunkIndex stores one hunk's source offsets and logical accounting.
type PatchHunkIndex struct {
	Version   uint32
	ID        string
	Offset    int64
	Length    int64
	BaseStart int
	BaseCount int
	HeadStart int
	HeadCount int
	Rows      int
	SHA256    string
}

// Validate checks offset, range, and digest shape without reading the source.
func (h PatchHunkIndex) Validate() error {
	if h.Version != PatchIndexVersion || h.ID == "" || h.Offset < 0 || h.Length <= 0 || h.BaseStart < 0 || h.BaseCount < 0 || h.HeadStart < 0 || h.HeadCount < 0 || h.Rows < 0 || !validSHA256(h.SHA256) {
		return ErrPatchMalformed
	}
	return nil
}

// PatchIndexEntry stores one complete file section and its hunk windows.
type PatchIndexEntry struct {
	Version        uint32
	SourceID       string
	Index          int
	Offset         int64
	Length         int64
	HeaderLength   int64
	File           repository.ChangedFile
	Binary         bool
	BinaryComplete bool
	BinaryOffset   int64
	SHA256         string
	Hunks          []PatchHunkIndex
}

// Validate checks the index record without opening the patch bytes.
func (e PatchIndexEntry) Validate() error {
	if e.Version != PatchIndexVersion || e.SourceID == "" || e.Index < 0 || e.Offset < 0 || e.Length <= 0 || e.HeaderLength <= 0 || e.HeaderLength > e.Length || e.File.Validate() != nil || !validSHA256(e.SHA256) {
		return ErrPatchMalformed
	}
	if e.Binary != e.File.Binary {
		return ErrPatchMalformed
	}
	if e.BinaryComplete && !e.Binary {
		return ErrPatchMalformed
	}
	if e.Binary {
		if e.BinaryOffset < e.Offset || e.BinaryOffset-e.Offset >= e.Length {
			return ErrPatchMalformed
		}
	} else if e.BinaryOffset != 0 {
		return ErrPatchMalformed
	}
	for _, hunk := range e.Hunks {
		if err := hunk.Validate(); err != nil || hunk.Offset < e.Offset {
			return ErrPatchMalformed
		}
		relative := hunk.Offset - e.Offset
		if relative > e.Length || hunk.Length > e.Length-relative {
			return ErrPatchMalformed
		}
	}
	return nil
}

// PatchIndexSink receives records only after the complete source has been
// scanned and verified. Implementations must not publish a record before the
// call returns successfully.
type PatchIndexSink interface {
	Append(PatchIndexEntry) error
}

// PatchIndexSinkFunc adapts a function to PatchIndexSink.
type PatchIndexSinkFunc func(PatchIndexEntry) error

// Append implements PatchIndexSink.
func (f PatchIndexSinkFunc) Append(entry PatchIndexEntry) error {
	if f == nil {
		return ErrPatchSink
	}
	return f(entry)
}

// MemoryPatchIndexSink is a bounded test and small-query index sink. Production
// callers may replace it with a journaled temporary index implementation.
type MemoryPatchIndexSink struct {
	entries []PatchIndexEntry
}

// Append stores a defensive copy of one validated entry.
func (s *MemoryPatchIndexSink) Append(entry PatchIndexEntry) error {
	if s == nil {
		return ErrPatchSink
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	copyEntry := entry
	copyEntry.Hunks = append([]PatchHunkIndex(nil), entry.Hunks...)
	s.entries = append(s.entries, copyEntry)
	return nil
}

// Entries returns a defensive copy of the complete published index records.
func (s *MemoryPatchIndexSink) Entries() []PatchIndexEntry {
	if s == nil {
		return nil
	}
	entries := make([]PatchIndexEntry, len(s.entries))
	for index, entry := range s.entries {
		entries[index] = entry
		entries[index].Hunks = append([]PatchHunkIndex(nil), entry.Hunks...)
	}
	return entries
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPatchMalformed, fmt.Sprintf(format, args...))
}

func limited(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPatchLimit, fmt.Sprintf(format, args...))
}
