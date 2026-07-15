package tui

import (
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestApprovalTerminologyDistinct(t *testing.T) {
	if runtimeApprovalTitle != "Codex runtime approval" {
		t.Fatalf("runtime title = %q", runtimeApprovalTitle)
	}
	if proposalApprovalTitle != "Approve proposal" || runtimeApprovalTitle == proposalApprovalTitle {
		t.Fatalf("approval terminology is not distinct: runtime=%q proposal=%q", runtimeApprovalTitle, proposalApprovalTitle)
	}
}

func TestRuntimeApprovalCommandsRequireExactPendingRequest(t *testing.T) {
	model := NewModel(nil, WithDimensions(100, 30))
	executable, _ := filepath.Abs(filepath.Join("Program Files", "Git", "bin", "git.exe"))
	updated, _ := model.Update(RuntimeApprovalMsg{Approval: app.RuntimeApproval{ID: "request-1", ThreadID: "thread-1", OperationID: "operation-1", ProviderTurnRef: "remote-turn", CorrelationID: "correlation-1", Kind: provider.RuntimeApprovalCommand, ExactCommandArgs: executable + " status", RequestedScope: "item/commandExecution/requestApproval", RequestedScopeID: provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalCommand, Executable: executable, ArgumentsDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	model = updated.(*Model)
	registration, matched, err := model.commands.Resolve(model, keyPress("y"), model.activeContexts())
	if err != nil || !matched || registration.Spec.ID != CommandApproveRuntimeOnce {
		t.Fatalf("approve resolution = %#v matched=%v err=%v", registration.Spec, matched, err)
	}
	model.runtimeApproval = nil
	_, matched, err = model.commands.Resolve(model, keyPress("y"), model.activeContexts())
	if err != nil || matched {
		t.Fatalf("approve remained available without pending request: matched=%v err=%v", matched, err)
	}
}

func keyPress(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: value, Code: rune(value[0])}
}
