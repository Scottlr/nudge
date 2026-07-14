package tui

import (
	"strings"
	"testing"
)

func TestOverlayStackIsBoundedAndTerminalSafe(t *testing.T) {
	t.Parallel()

	var stack OverlayStack
	if !stack.Push(Overlay{ID: "first", Title: "Warning", Body: "safe\x1b[31m text", Dismissible: true}) {
		t.Fatal("first overlay was rejected")
	}
	for i := 0; i < maxOverlayCount-1; i++ {
		if !stack.Push(Overlay{ID: "overlay-" + string(rune('a'+i)), Body: strings.Repeat("x", maxOverlayBytes*2)}) {
			t.Fatalf("overlay %d was rejected before the bound", i)
		}
	}
	if stack.Push(Overlay{ID: "overflow"}) {
		t.Fatal("overlay stack exceeded its bound")
	}
	if stack.Len() != maxOverlayCount {
		t.Fatalf("stack length = %d, want %d", stack.Len(), maxOverlayCount)
	}
	top, ok := stack.Top()
	if !ok || len(top.Body) > maxOverlayBytes || strings.ContainsRune(top.Body, '\x1b') {
		t.Fatalf("top overlay was not bounded and safe: %#v", top)
	}
	if _, ok := stack.Pop(); !ok || stack.Len() != maxOverlayCount-1 {
		t.Fatal("overlay stack did not pop newest item")
	}
}
