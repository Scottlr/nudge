package highlight

import (
	"context"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestHighlightMultilineTokens(t *testing.T) {
	t.Parallel()

	highlighter, err := NewChromaHighlighter(4096, NewCache(8, 64*1024))
	if err != nil {
		t.Fatal(err)
	}
	content := testContent([]byte("package demo\n/* first line\nsecond line */\nfunc main() {}\n"), "source-hash")
	result, err := highlighter.Highlight(context.Background(), content, "main.go", "nudge-dark")
	if err != nil {
		t.Fatal(err)
	}
	if result.PlainText || result.Lexer == "" || len(result.Lines) != 5 {
		t.Fatalf("unexpected result metadata: %#v", result)
	}
	for _, lineIndex := range []int{1, 2} {
		if !lineHasTokenContaining(result.Lines[lineIndex], "Comment") {
			t.Fatalf("line %d was not retained as a multiline comment: %#v", lineIndex, result.Lines[lineIndex])
		}
	}
	for _, line := range result.Lines {
		for _, span := range line {
			if strings.ContainsRune(span.Text, '\x1b') {
				t.Fatal("terminal escape survived the projection")
			}
		}
	}
}

func TestHighlightCacheKey(t *testing.T) {
	t.Parallel()

	cache := NewCache(8, 64*1024)
	highlighter, err := NewChromaHighlighter(4096, cache)
	if err != nil {
		t.Fatal(err)
	}
	content := testContent([]byte("package demo\n"), "same-content")
	if _, err := highlighter.Highlight(context.Background(), content, "first.go", "nudge-dark"); err != nil {
		t.Fatal(err)
	}
	if _, err := highlighter.Highlight(context.Background(), content, "second.go", "nudge-dark"); err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 1 {
		t.Fatalf("mutable path incorrectly affected cache identity: got %d entries", cache.Len())
	}
	if _, err := highlighter.Highlight(context.Background(), content, "first.go", "nudge-light"); err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 2 {
		t.Fatalf("syntax style was omitted from cache identity: got %d entries", cache.Len())
	}
	if _, err := highlighter.Highlight(context.Background(), testContent([]byte("package demo\n"), "different-content"), "first.go", "nudge-dark"); err != nil {
		t.Fatal(err)
	}
	if cache.Len() != 3 {
		t.Fatalf("content hash was omitted from cache identity: got %d entries", cache.Len())
	}
}

func TestHighlightFallsBackForLimit(t *testing.T) {
	t.Parallel()

	cache := NewCache(8, 64*1024)
	highlighter, err := NewChromaHighlighter(4, cache)
	if err != nil {
		t.Fatal(err)
	}
	result, err := highlighter.Highlight(context.Background(), testContent([]byte("package demo"), "too-large"), "main.go", "nudge-dark")
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlainText || result.LimitReason != "highlight_limit" || cache.Len() != 0 {
		t.Fatalf("over-limit content was not a plain uncached result: %#v", result)
	}

	binary := testContent([]byte{0, 1, 2}, "binary")
	binary.Binary = true
	result, err = highlighter.Highlight(context.Background(), binary, "data.bin", "nudge-dark")
	if err != nil {
		t.Fatal(err)
	}
	if !result.PlainText || result.LimitReason != "binary" {
		t.Fatalf("binary content was not an explicit plain fallback: %#v", result)
	}
}

func testContent(bytes []byte, hash string) repository.FileContent {
	path, err := repository.NewRepoPath([]byte("main.go"))
	if err != nil {
		panic(err)
	}
	return repository.FileContent{
		Snapshot:    repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Path:        path,
		Kind:        repository.FileKindRegular,
		Mode:        0o100644,
		Bytes:       bytes,
		ContentHash: hash,
	}
}

func lineHasTokenContaining(line []StyledSpan, fragment string) bool {
	for _, span := range line {
		if strings.Contains(span.Token, fragment) {
			return true
		}
	}
	return false
}
