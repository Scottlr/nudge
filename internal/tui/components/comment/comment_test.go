package comment

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestCommentEditorExplicitSend(t *testing.T) {
	model := NewModel(structuredAnchorForTest())
	model.SetValue("  first line\n\n  second line  \n")

	intent, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent.CreateThread != nil || !strings.Contains(model.Value(), "\n") {
		t.Fatalf("bare Enter did not remain a multiline edit: intent=%+v value=%q", intent, model.Value())
	}
	intent, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if intent.CreateThread == nil || intent.CreateThread.Comment != "  first line\n\n  second line  " {
		t.Fatalf("send intent = %+v", intent.CreateThread)
	}
	if model.Value() == "" {
		t.Fatal("successful send erased the draft before the root handled the intent")
	}
}

func TestCommentEditorCancellationAndLimit(t *testing.T) {
	model := NewModel(structuredAnchorForTest())
	model.SetValue("draft")
	intent, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !intent.Cancelled || model.Value() != "draft" {
		t.Fatalf("cancel intent=%+v draft=%q", intent, model.Value())
	}

	model.SetValue(strings.Repeat("x", MaxCommentBytes))
	if !model.CanSubmit() || model.RemainingBytes() != 0 {
		t.Fatalf("exact concern limit rejected: error=%v remaining=%d", model.LastError(), model.RemainingBytes())
	}
	model.SetValue(strings.Repeat("x", MaxCommentBytes+1))
	if model.CanSubmit() || model.LastError() != ErrCommentTooLarge {
		t.Fatalf("over-limit draft state: canSubmit=%v error=%v", model.CanSubmit(), model.LastError())
	}
	intent, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if intent.CreateThread != nil {
		t.Fatal("over-limit draft emitted a thread intent")
	}
	if !strings.Contains(model.View(), "bytes") {
		t.Fatal("over-limit view omitted visible byte state")
	}
}

func structuredAnchorForTest() review.CodeAnchor {
	return review.CodeAnchor{}
}
