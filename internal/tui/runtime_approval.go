package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/provider"
)

// RuntimeApprovalResponder is the narrow frontend seam for one-shot provider
// approval. It cannot approve proposals or change provider roots.
type RuntimeApprovalResponder interface {
	RespondToRuntimeApproval(context.Context, provider.RuntimeApprovalResponse) error
}

const (
	runtimeApprovalTitle  = "Codex runtime approval"
	proposalApprovalTitle = "Approve proposal"
)

func (m *Model) showRuntimeApproval(approval app.RuntimeApproval) {
	if m == nil || approval.ID == "" {
		return
	}
	copyValue := approval
	m.runtimeApproval = &copyValue
	body := runtimeApprovalBody(approval)
	if top, ok := m.overlays.Top(); ok && strings.HasPrefix(top.ID, "runtime-approval:") {
		m.overlays.items[len(m.overlays.items)-1] = Overlay{ID: "runtime-approval:" + string(approval.ID), Title: runtimeApprovalTitle, Body: body, Dismissible: false}
		return
	}
	m.showOverlay(Overlay{ID: "runtime-approval:" + string(approval.ID), Title: runtimeApprovalTitle, Body: body, Dismissible: false})
}

func (m *Model) clearRuntimeApproval() {
	if m == nil {
		return
	}
	m.runtimeApproval = nil
	if top, ok := m.overlays.Top(); ok && strings.HasPrefix(top.ID, "runtime-approval:") {
		m.dismissOverlay()
	}
}

func runtimeApprovalBody(approval app.RuntimeApproval) string {
	lines := []string{
		"This is a Codex runtime request, not proposal approval.",
		"scope: " + safeText(approval.RequestedScope),
	}
	if approval.ExactCommandArgs != "" {
		lines = append(lines, "command: "+safeText(approval.ExactCommandArgs))
	}
	if approval.ToolName != "" {
		lines = append(lines, "tool: "+safeText(approval.ToolName))
	}
	if approval.NetworkTarget != "" {
		lines = append(lines, "network: "+safeText(approval.NetworkTarget), "network access is always denied in v1")
	}
	if !app.CanApproveRuntimeApproval(approval) {
		lines = append(lines, "this request cannot be allowed by Nudge; press n to deny")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) resolveRuntimeApproval(allow bool) tea.Cmd {
	if m == nil || m.runtimeApproval == nil {
		return nil
	}
	approval := *m.runtimeApproval
	decision := provider.ApprovalDeny
	if allow && app.CanApproveRuntimeApproval(approval) {
		decision = provider.ApprovalAllowOnce
	}
	response := provider.RuntimeApprovalResponse{RequestID: approval.ID, ThreadID: approval.ThreadID, OperationID: approval.OperationID, TurnRef: approval.ProviderTurnRef, CorrelationID: approval.CorrelationID, Scope: approval.RequestedScopeID, Decision: decision}
	m.clearRuntimeApproval()
	commands := make([]tea.Cmd, 0, 2)
	if m.client != nil {
		commands = append(commands, dispatchCommand(m.ctx, m.client, app.RespondToRuntimeApproval{Response: response, CorrelationID: app.CorrelationID(approval.CorrelationID)}))
	}
	if m.runtimeProvider != nil {
		providerPort := m.runtimeProvider
		commands = append(commands, func() tea.Msg {
			err := providerPort.RespondToRuntimeApproval(context.Background(), response)
			if err != nil && decision == provider.ApprovalDeny && err == app.ErrRuntimeApprovalPolicy {
				return nil
			}
			return DispatchResultMsg{Err: err}
		})
	} else if allow {
		m.lastError = fmt.Sprintf("%s unavailable", runtimeApprovalTitle)
	}
	return tea.Batch(commands...)
}
