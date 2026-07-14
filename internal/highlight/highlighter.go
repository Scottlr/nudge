// Package highlight tokenizes immutable repository content into bounded,
// terminal-safe per-line spans without knowing how a TUI lays them out.
package highlight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/presentation"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

var (
	// ErrInvalidHighlighterInput reports invalid immutable repository content or
	// an invalid highlighter configuration.
	ErrInvalidHighlighterInput = errors.New("invalid highlighter input")
	// ErrHighlightFailed reports a lexer or tokenization failure.
	ErrHighlightFailed = errors.New("highlight failed")
)

// StyledSpan is a safe text fragment plus the Chroma token identity. Theme and
// renderer composition remain separate from tokenization.
type StyledSpan struct {
	Text  string
	Token string
}

// HighlightedFile is a complete, immutable-by-convention result. Lines are
// independent display rows, but tokenization always happened over the file.
type HighlightedFile struct {
	ContentHash string
	Lexer       string
	SyntaxStyle string
	Lines       [][]StyledSpan
	PlainText   bool
	LimitReason string
}

// Highlighter owns the application boundary for syntax highlighting.
type Highlighter interface {
	Highlight(ctx context.Context, content repository.FileContent, filename string, syntaxStyle string) (HighlightedFile, error)
}

// ChromaHighlighter implements Highlighter with a bounded result cache.
type ChromaHighlighter struct {
	MaxBytes int
	Cache    *Cache
}

// NewChromaHighlighter creates a highlighter with an explicit content
// threshold and optional bounded result cache.
func NewChromaHighlighter(maxBytes int, cache *Cache) (*ChromaHighlighter, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: max bytes must be positive", ErrInvalidHighlighterInput)
	}
	return &ChromaHighlighter{MaxBytes: maxBytes, Cache: cache}, nil
}

// Highlight detects a lexer, tokenizes the whole projected file, and maps the
// token stream to line spans. Binary and over-limit content never reaches
// Chroma and remains explicit plain text.
func (h *ChromaHighlighter) Highlight(ctx context.Context, content repository.FileContent, filename string, syntaxStyle string) (HighlightedFile, error) {
	if h == nil || h.MaxBytes <= 0 || content.Validate() != nil || strings.TrimSpace(syntaxStyle) == "" {
		return HighlightedFile{}, fmt.Errorf("%w: invalid content, syntax style, or configuration", ErrInvalidHighlighterInput)
	}
	if err := ctx.Err(); err != nil {
		return HighlightedFile{}, err
	}
	if content.Binary || content.Truncated || len(content.Bytes) > h.MaxBytes {
		reason := content.LimitReason
		if reason == "" {
			if content.Binary {
				reason = "binary"
			} else {
				reason = "highlight_limit"
			}
		}
		return plainResult(content, syntaxStyle, reason), nil
	}

	projected := presentation.ProjectTerminalText(string(content.Bytes), presentation.TerminalTextMultiline)
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(projected)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexerName := lexer.Config().Name
	if lexerName == "" {
		lexerName = "fallback"
	}
	key := CacheKey{ContentHash: content.ContentHash, Lexer: lexerName, SyntaxStyle: syntaxStyle}
	if cached, ok := h.Cache.Get(key); ok {
		return cached, nil
	}

	if err := ctx.Err(); err != nil {
		return HighlightedFile{}, err
	}
	tokens, err := lexer.Tokenise(nil, projected)
	if err != nil {
		return HighlightedFile{}, fmt.Errorf("%w: %v", ErrHighlightFailed, err)
	}
	result := HighlightedFile{
		ContentHash: content.ContentHash,
		Lexer:       lexerName,
		SyntaxStyle: syntaxStyle,
		Lines:       make([][]StyledSpan, 1),
	}
	for token := tokens(); token != chroma.EOF; token = tokens() {
		if err := ctx.Err(); err != nil {
			return HighlightedFile{}, err
		}
		appendTokenLines(&result.Lines, token)
	}
	h.Cache.Put(key, result)
	return result, nil
}

func plainResult(content repository.FileContent, syntaxStyle, reason string) HighlightedFile {
	projected := presentation.ProjectTerminalText(string(content.Bytes), presentation.TerminalTextMultiline)
	lines := strings.Split(projected, "\n")
	spans := make([][]StyledSpan, len(lines))
	for i, line := range lines {
		if line != "" {
			spans[i] = []StyledSpan{{Text: presentation.ProjectTerminalText(line, presentation.TerminalTextScalar), Token: "Text"}}
		}
	}
	return HighlightedFile{
		ContentHash: content.ContentHash,
		SyntaxStyle: syntaxStyle,
		Lines:       spans,
		PlainText:   true,
		LimitReason: reason,
	}
}

func appendTokenLines(lines *[][]StyledSpan, token chroma.Token) {
	parts := strings.Split(token.Value, "\n")
	for i, part := range parts {
		if part != "" {
			span := StyledSpan{Text: presentation.ProjectTerminalText(part, presentation.TerminalTextScalar), Token: token.Type.String()}
			*lines = appendSpan(*lines, span)
		}
		if i < len(parts)-1 {
			*lines = append(*lines, nil)
		}
	}
}

func appendSpan(lines [][]StyledSpan, span StyledSpan) [][]StyledSpan {
	if len(lines) == 0 {
		lines = append(lines, nil)
	}
	last := len(lines) - 1
	lines[last] = append(lines[last], span)
	return lines
}
