package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const (
	// MaxAnchorSelectionBytes is the mandatory selected-evidence limit from
	// the owned-storage policy. The selected evidence is never truncated.
	MaxAnchorSelectionBytes = 256 << 10
	// MaxAnchorContextBytes bounds optional whole-line context evidence.
	MaxAnchorContextBytes = 512 << 10
	// AnchorContextLines is the number of same-side lines considered on each
	// side of a selected range.
	AnchorContextLines = 3
)

var (
	// ErrInvalidDisplayedContentSnapshot reports incomplete or contradictory
	// immutable display evidence.
	ErrInvalidDisplayedContentSnapshot = errors.New("invalid displayed content snapshot")
	// ErrInvalidAnchorSelection reports a selection that cannot become a
	// same-side, same-hunk anchor.
	ErrInvalidAnchorSelection = errors.New("invalid anchor selection")
	// ErrAnchorSelectionStale reports row IDs from a different immutable
	// displayed-content revision.
	ErrAnchorSelectionStale = errors.New("stale anchor selection")
	// ErrAnchorEvidenceTooLarge reports mandatory selected evidence over the
	// bounded anchor limit.
	ErrAnchorEvidenceTooLarge = errors.New("anchor evidence too large")
)

// DisplayedContentSnapshot is the immutable application evidence used to
// construct an anchor. Rows are copied at the boundary and no live path is
// consulted by BuildCodeAnchor.
type DisplayedContentSnapshot struct {
	Content   DisplayedContent
	ContentID DisplayedContentID
	Revision  uint64
	Target    repository.ResolvedTarget
	CaptureID domain.CaptureID
	Rows      []DisplayedRow

	// BaseContent and HeadContent are optional bounded material retained by a
	// loader. The row evidence remains sufficient for normal diff anchors.
	BaseContent *repository.FileContent
	HeadContent *repository.FileContent
}

// Validate checks the snapshot envelope, target identity, and all displayed
// rows without reading or resolving any repository path.
func (s DisplayedContentSnapshot) Validate() error {
	if err := s.Content.Validate(); err != nil || s.Revision == 0 {
		return ErrInvalidDisplayedContentSnapshot
	}
	if s.ContentID != "" && s.ContentID != s.Content.ID {
		return ErrInvalidDisplayedContentSnapshot
	}
	if err := s.Target.Validate(); err != nil {
		return fmt.Errorf("%w: target: %v", ErrInvalidDisplayedContentSnapshot, err)
	}
	if s.CaptureID != "" {
		if _, err := domain.NewCaptureID(string(s.CaptureID)); err != nil {
			return fmt.Errorf("%w: capture: %v", ErrInvalidDisplayedContentSnapshot, err)
		}
	}
	page := DisplayedContentPage{ContentID: s.Content.ID, Rows: s.Rows}
	if err := page.Validate(); err != nil {
		return fmt.Errorf("%w: rows: %v", ErrInvalidDisplayedContentSnapshot, err)
	}
	if err := validateAnchorMaterial(s.BaseContent, s.Target.Base); err != nil {
		return fmt.Errorf("%w: base material: %v", ErrInvalidDisplayedContentSnapshot, err)
	}
	if err := validateAnchorMaterial(s.HeadContent, s.Target.Head); err != nil {
		return fmt.Errorf("%w: head material: %v", ErrInvalidDisplayedContentSnapshot, err)
	}
	return nil
}

func validateAnchorMaterial(content *repository.FileContent, snapshot repository.SnapshotRef) error {
	if content == nil {
		return nil
	}
	if err := content.Validate(); err != nil || content.Snapshot != snapshot || content.Binary || content.Truncated {
		return ErrInvalidDisplayedContentSnapshot
	}
	return nil
}

// AnchorSelection identifies one contiguous range in the immutable displayed
// row population. The side is explicit for shared context rows.
type AnchorSelection struct {
	Side       repository.DiffSide
	StartRowID CodeRowID
	EndRowID   CodeRowID
	HunkID     string
}

// BuildCodeAnchor converts displayed immutable row evidence into one durable
// review anchor. It does not persist the anchor or create a review thread.
func BuildCodeAnchor(target repository.ResolvedTarget, displayed DisplayedContentSnapshot, selection AnchorSelection, now time.Time, storeText bool) (review.CodeAnchor, error) {
	if err := target.Validate(); err != nil {
		return review.CodeAnchor{}, fmt.Errorf("%w: target: %v", ErrInvalidAnchorSelection, err)
	}
	if err := displayed.Validate(); err != nil {
		return review.CodeAnchor{}, err
	}
	if displayed.Content.Status != ContentReady {
		return review.CodeAnchor{}, fmt.Errorf("%w: content status %s is not anchorable", ErrInvalidAnchorSelection, displayed.Content.Status)
	}
	if !reflect.DeepEqual(target, displayed.Target) {
		return review.CodeAnchor{}, fmt.Errorf("%w: target snapshot mismatch", ErrAnchorSelectionStale)
	}
	if now.IsZero() {
		return review.CodeAnchor{}, fmt.Errorf("%w: zero creation time", ErrInvalidAnchorSelection)
	}
	if selection.Side != repository.DiffBase && selection.Side != repository.DiffHead {
		return review.CodeAnchor{}, fmt.Errorf("%w: side", ErrInvalidAnchorSelection)
	}
	if !validAnchorToken(selection.HunkID) {
		return review.CodeAnchor{}, fmt.Errorf("%w: hunk", ErrInvalidAnchorSelection)
	}
	if !selection.StartRowID.Matches(displayed.Content.ID) || !selection.EndRowID.Matches(displayed.Content.ID) {
		return review.CodeAnchor{}, ErrAnchorSelectionStale
	}

	rows := make(map[uint64]DisplayedRow, len(displayed.Rows))
	for _, row := range displayed.Rows {
		rows[row.ID.Ordinal] = row
	}
	start, startOK := rows[selection.StartRowID.Ordinal]
	end, endOK := rows[selection.EndRowID.Ordinal]
	if !startOK || !endOK {
		return review.CodeAnchor{}, ErrAnchorSelectionStale
	}
	if start.ID != selection.StartRowID || end.ID != selection.EndRowID {
		return review.CodeAnchor{}, ErrAnchorSelectionStale
	}
	firstOrdinal, lastOrdinal := selection.StartRowID.Ordinal, selection.EndRowID.Ordinal
	if firstOrdinal > lastOrdinal {
		firstOrdinal, lastOrdinal = lastOrdinal, firstOrdinal
	}
	if lastOrdinal-firstOrdinal >= uint64(len(displayed.Rows)) {
		return review.CodeAnchor{}, ErrAnchorSelectionStale
	}

	selected := make([]anchorLine, 0, len(displayed.Rows))
	var rangePath, rangePreviousPath repository.RepoPath
	for ordinal := firstOrdinal; ; ordinal++ {
		row, ok := rows[ordinal]
		if !ok {
			return review.CodeAnchor{}, ErrAnchorSelectionStale
		}
		if row.HunkID != selection.HunkID {
			return review.CodeAnchor{}, fmt.Errorf("%w: row crosses hunk", ErrInvalidAnchorSelection)
		}
		rowPath, rowPreviousPath, err := pathsForSide(displayed.Content, row, selection.Side)
		if err != nil {
			return review.CodeAnchor{}, fmt.Errorf("%w: row path: %v", ErrInvalidAnchorSelection, err)
		}
		if len(rangePath) == 0 {
			rangePath, rangePreviousPath = rowPath, rowPreviousPath
		} else if string(rangePath) != string(rowPath) || string(rangePreviousPath) != string(rowPreviousPath) {
			return review.CodeAnchor{}, fmt.Errorf("%w: range crosses file identity", ErrInvalidAnchorSelection)
		}
		line, err := lineForSide(row, selection.Side)
		if err != nil {
			return review.CodeAnchor{}, fmt.Errorf("%w: row %d: %v", ErrInvalidAnchorSelection, ordinal, err)
		}
		selected = append(selected, line)
		if ordinal == lastOrdinal {
			break
		}
	}
	if len(selected) == 0 {
		return review.CodeAnchor{}, fmt.Errorf("%w: empty range", ErrInvalidAnchorSelection)
	}
	for index := 1; index < len(selected); index++ {
		if selected[index].line != selected[index-1].line+1 {
			return review.CodeAnchor{}, fmt.Errorf("%w: non-contiguous lines", ErrInvalidAnchorSelection)
		}
	}

	path, previousPath, err := pathsForSide(displayed.Content, start, selection.Side)
	if err != nil {
		return review.CodeAnchor{}, fmt.Errorf("%w: path: %v", ErrInvalidAnchorSelection, err)
	}
	selectedText := joinAnchorLines(selected)
	if !utf8.ValidString(selectedText) {
		return review.CodeAnchor{}, fmt.Errorf("%w: invalid UTF-8 evidence", ErrInvalidAnchorSelection)
	}
	if len([]byte(selectedText)) > MaxAnchorSelectionBytes {
		return review.CodeAnchor{}, ErrAnchorEvidenceTooLarge
	}

	hunkFingerprint := fingerprintHunk(displayed.Rows, selection.HunkID)
	selectionHash := fingerprintSelection(selection.Side, path, selected[0].line, selected[len(selected)-1].line, selectedText)
	beforeHash := contextFingerprint(displayed.Rows, firstOrdinal, -1, selection.Side, selection.HunkID)
	afterHash := contextFingerprint(displayed.Rows, lastOrdinal, 1, selection.Side, selection.HunkID)

	anchor := review.CodeAnchor{
		Path:              path,
		PreviousPath:      previousPath,
		Side:              selection.Side,
		StartLine:         selected[0].line,
		EndLine:           selected[len(selected)-1].line,
		TargetGeneration:  target.Generation,
		Base:              target.Base,
		Head:              target.Head,
		HunkFingerprint:   hunkFingerprint,
		SelectionHash:     selectionHash,
		BeforeContextHash: beforeHash,
		AfterContextHash:  afterHash,
		State:             review.AnchorValid,
		CreatedAt:         now.UTC(),
	}
	if storeText {
		anchor.SelectedText = selectedText
	}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		return review.CodeAnchor{}, fmt.Errorf("%w: %v", ErrInvalidAnchorSelection, err)
	}
	return validated, nil
}

type anchorLine struct {
	line int
	text string
}

func lineForSide(row DisplayedRow, side repository.DiffSide) (anchorLine, error) {
	if !row.Selectable || row.Kind == DisplayedRowSource || row.Kind == DisplayedRowNoNewline || row.Kind == DisplayedRowDiffHeader || row.Kind == DisplayedRowHunkHeader || row.Kind == DisplayedRowPlaceholder {
		return anchorLine{}, errors.New("row is not mappable code evidence")
	}
	switch side {
	case repository.DiffBase:
		if row.Side != SideBase && row.Side != SideBoth || row.BaseLine == nil {
			return anchorLine{}, errors.New("row is not selectable on base side")
		}
		text := row.BaseText
		if text == "" && row.Text != "" {
			text = row.Text
		}
		if !utf8.ValidString(text) {
			return anchorLine{}, errors.New("invalid UTF-8 row evidence")
		}
		return anchorLine{line: *row.BaseLine, text: text}, nil
	case repository.DiffHead:
		if row.Side != SideHead && row.Side != SideBoth || row.HeadLine == nil {
			return anchorLine{}, errors.New("row is not selectable on head side")
		}
		text := row.HeadText
		if text == "" && row.Text != "" {
			text = row.Text
		}
		if !utf8.ValidString(text) {
			return anchorLine{}, errors.New("invalid UTF-8 row evidence")
		}
		return anchorLine{line: *row.HeadLine, text: text}, nil
	default:
		return anchorLine{}, errors.New("invalid diff side")
	}
}

func pathsForSide(content DisplayedContent, row DisplayedRow, side repository.DiffSide) (repository.RepoPath, repository.RepoPath, error) {
	base := pathValue(row.BasePath, content.BasePath)
	head := pathValue(row.HeadPath, content.HeadPath)
	var selected, previous repository.RepoPath
	switch side {
	case repository.DiffBase:
		selected, previous = base, head
	case repository.DiffHead:
		selected, previous = head, base
	}
	if err := selected.Validate(); err != nil {
		return nil, nil, err
	}
	if string(selected) == string(previous) {
		previous = nil
	}
	if len(previous) > 0 {
		if err := previous.Validate(); err != nil {
			return nil, nil, err
		}
	}
	return append(repository.RepoPath(nil), selected...), append(repository.RepoPath(nil), previous...), nil
}

func pathValue(rowPath, contentPath *repository.RepoPath) repository.RepoPath {
	if rowPath != nil {
		return repository.RepoPath(rowPath.Bytes())
	}
	if contentPath != nil {
		return repository.RepoPath(contentPath.Bytes())
	}
	return nil
}

func joinAnchorLines(lines []anchorLine) string {
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = line.text
	}
	return strings.Join(values, "\n")
}

func fingerprintHunk(rows []DisplayedRow, hunkID string) string {
	hash := sha256.New()
	writeHashPart(hash, "nudge-anchor-hunk-v1")
	writeHashPart(hash, hunkID)
	for _, row := range rows {
		if row.HunkID != hunkID {
			continue
		}
		writeHashPart(hash, strconv.FormatUint(row.ID.Ordinal, 10))
		writeHashPart(hash, string(row.Kind))
		writeHashPart(hash, string(row.Side))
		writeHashPart(hash, linePointerValue(row.BaseLine))
		writeHashPart(hash, linePointerValue(row.HeadLine))
		writeHashPart(hash, normalizeAnchorText(row.BaseText))
		writeHashPart(hash, normalizeAnchorText(row.HeadText))
		writeHashPart(hash, normalizeAnchorText(row.Text))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func fingerprintSelection(side repository.DiffSide, path repository.RepoPath, start, end int, text string) string {
	hash := sha256.New()
	writeHashPart(hash, "nudge-anchor-selection-v1")
	writeHashPart(hash, string(side))
	writeHashPart(hash, string(path))
	writeHashPart(hash, strconv.Itoa(start))
	writeHashPart(hash, strconv.Itoa(end))
	writeHashPart(hash, normalizeAnchorText(text))
	return hex.EncodeToString(hash.Sum(nil))
}

func contextFingerprint(rows []DisplayedRow, edge uint64, direction int, side repository.DiffSide, hunkID string) string {
	selected := make([]anchorLine, 0, AnchorContextLines)
	ordinal := edge
	for len(selected) < AnchorContextLines {
		if direction < 0 {
			if ordinal == 0 {
				break
			}
			ordinal--
		} else {
			if ordinal == ^uint64(0) {
				break
			}
			ordinal++
		}
		row, ok := displayedRowAt(rows, ordinal)
		if !ok || row.HunkID != hunkID {
			break
		}
		line, err := lineForSide(row, side)
		if err != nil {
			continue
		}
		selected = append(selected, line)
	}
	if len(selected) == 0 {
		return ""
	}
	if direction < 0 {
		for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
			selected[left], selected[right] = selected[right], selected[left]
		}
	}
	var payload strings.Builder
	for index, line := range selected {
		if len(line.text) > MaxAnchorContextBytes {
			break
		}
		normalized := normalizeAnchorText(line.text)
		if len(normalized) > MaxAnchorContextBytes {
			break
		}
		part := strconv.Itoa(line.line) + ":" + normalized
		if index > 0 {
			part = "\n" + part
		}
		if payload.Len()+len(part) > MaxAnchorContextBytes {
			break
		}
		payload.WriteString(part)
	}
	hash := sha256.New()
	writeHashPart(hash, "nudge-anchor-context-v1")
	writeHashPart(hash, payload.String())
	return hex.EncodeToString(hash.Sum(nil))
}

func displayedRowAt(rows []DisplayedRow, ordinal uint64) (DisplayedRow, bool) {
	for _, row := range rows {
		if row.ID.Ordinal == ordinal {
			return row, true
		}
	}
	return DisplayedRow{}, false
}

func normalizeAnchorText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	return strings.Join(lines, "\n")
}

func linePointerValue(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func writeHashPart(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

func validAnchorToken(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}
