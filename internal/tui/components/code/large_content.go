package code

import (
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

const maxLargeContentSegments = 256

// largeContentProjection retains only the bounded segment window needed by
// the code pane. It never stores a complete line or file.
type largeContentProjection struct {
	open      *app.LargeContentOpen
	segments  []app.ContentSegment
	operation domain.OperationID
	pending   uint64
	nextLine  uint64
	token     uint64
}

func newLargeContentProjection() *largeContentProjection {
	return &largeContentProjection{}
}

func (p *largeContentProjection) reset() {
	*p = largeContentProjection{}
}

func (p *largeContentProjection) requestWindow() []Intent {
	if p == nil || p.open == nil || p.operation == "" {
		return nil
	}
	p.token++
	p.pending = p.token
	request := app.LargeContentWindowRequest{
		OpenID:                p.open.ID,
		Identity:              p.open.Identity,
		ExpectedQueryRevision: p.open.QueryRevision,
		OperationID:           p.operation,
		Window:                app.LineWindow{StartLine: p.nextLine, MaxLines: 256, MaxEncodedBytes: 1 * app.MiB},
	}
	return []Intent{{LargeWindow: &LargeContentWindowRequest{Request: request, Token: p.token}}}
}

func (p *largeContentProjection) acceptOpen(result app.LargeContentOpen, token uint64, operation domain.OperationID) []Intent {
	if p == nil || token == 0 || token != p.pending || result.Validate() != nil || operation == "" {
		return nil
	}
	copyResult := result
	p.open = &copyResult
	p.operation = operation
	p.pending = 0
	p.segments = nil
	p.nextLine = 0
	return p.requestWindow()
}

func (p *largeContentProjection) acceptWindow(result app.ContentWindow, token uint64) {
	if p == nil || p.open == nil || token == 0 || token != p.pending || result.OpenID != p.open.ID || result.Identity != p.open.Identity || result.Validate() != nil {
		return
	}
	p.pending = 0
	p.segments = append(p.segments[:0], result.Segments...)
	if len(p.segments) > maxLargeContentSegments {
		p.segments = p.segments[:maxLargeContentSegments]
	}
	p.nextLine = result.NextLine
}

func (p *largeContentProjection) segmentsCopy() []app.ContentSegment {
	if p == nil {
		return nil
	}
	return append([]app.ContentSegment(nil), p.segments...)
}

func (p *largeContentProjection) view(width, height int, styles theme.Theme) string {
	if p == nil || p.open == nil {
		return ""
	}
	if len(p.segments) == 0 {
		if p.pending != 0 {
			return "loading immutable content"
		}
		return "no immutable content window"
	}
	if height <= 0 {
		height = len(p.segments)
	}
	if width <= 0 {
		width = 120
	}
	lines := make([]string, 0, minLargeInt(height, len(p.segments)))
	for _, segment := range p.segments {
		marker := " "
		if segment.ContinuationBefore {
			marker = styles.Glyph(theme.GlyphContinuation)
		}
		if segment.ContinuationAfter {
			marker += styles.Glyph(theme.GlyphEllipsis)
		}
		line := fmt.Sprintf("%s %d:%d-%d %s", marker, segment.Line+1, segment.Range.Start, segment.Range.End, segment.Text)
		if segment.InvalidEncoding {
			line += " [invalid bytes]"
		}
		if segment.ElidedBytes != 0 {
			line += fmt.Sprintf(" [elided %d bytes]", segment.ElidedBytes)
		}
		lines = append(lines, ansi.Truncate(strings.TrimRight(line, "\n"), width, ""))
		if len(lines) >= height {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func minLargeInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// RequestLargeContentOpen emits an explicit confirmation-bound open intent.
func (m *Model) RequestLargeContentOpen(identity app.ContentIdentity, revision uint64, operationID domain.OperationID, confirmed bool) ([]Intent, error) {
	if m == nil {
		return nil, app.ErrInvalidLargeContentRequest
	}
	m.nextToken++
	intent, err := LargeContentOpenIntent(identity, revision, operationID, confirmed, m.nextToken)
	if err != nil {
		return nil, err
	}
	m.large.operation = operationID
	m.large.pending = m.nextToken
	return []Intent{intent}, nil
}

// LargeContentSegments returns only the current bounded segment projection.
func (m *Model) LargeContentSegments() []app.ContentSegment {
	if m == nil {
		return nil
	}
	return m.large.segmentsCopy()
}

// CloseLargeContent emits a lease-release intent for the current immutable
// content identity. It does not mutate canonical application state.
func (m *Model) CloseLargeContent() []Intent {
	if m == nil || m.large == nil || m.large.open == nil {
		return nil
	}
	m.nextToken++
	request := app.CloseLargeContent{ID: m.large.open.ID, Identity: m.large.open.Identity, ExpectedQueryRevision: m.large.open.QueryRevision}
	m.large.pending = m.nextToken
	return []Intent{{LargeClose: &LargeContentCloseRequest{Request: request, Token: m.nextToken}}}
}
