package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var ErrInvalidConversationResponse = errors.New("invalid codex conversation response")
var ErrInvalidTurnResponse = errors.New("invalid codex turn response")

var ErrInvalidNotification = errors.New("invalid codex notification")

// NotificationContext carries local Nudge ownership for one provider
// conversation/turn. Raw Codex IDs never become Nudge IDs; the connection
// supplies this context after its local journal has been fenced.
type NotificationContext struct {
	Sequence        uint64
	ThreadID        domain.ReviewThreadID
	OperationID     domain.OperationID
	CorrelationID   provider.CorrelationID
	ConversationID  domain.ProviderConversationID
	ConversationRef provider.ProviderConversationRef
	TurnID          domain.ProviderTurnID
	TurnRef         provider.ProviderTurnRef
}

const (
	notificationError         = "error"
	notificationThreadStarted = "thread/started"
	notificationTurnStarted   = "turn/started"
	notificationTurnCompleted = "turn/completed"
	notificationItemStarted   = "item/started"
	notificationItemCompleted = "item/completed"
	notificationMessageDelta  = "item/agentMessage/delta"
	notificationThreadStatus  = "thread/status/changed"
)

const (
	serverRequestCommandApproval     = "item/commandExecution/requestApproval"
	serverRequestFileApproval        = "item/fileChange/requestApproval"
	serverRequestPermissionsApproval = "item/permissions/requestApproval"
	serverRequestToolCall            = "item/tool/call"
	serverRequestToolInput           = "item/tool/requestUserInput"
	serverRequestLegacyExec          = "execCommandApproval"
	serverRequestLegacyPatch         = "applyPatchApproval"
)

// MappedRuntimeApproval is the adapter-owned ephemeral approval projection.
// The provider-neutral event retains the exact display details only in
// memory; durable callers use MappedRuntimeApproval.Request.Scope instead.
type MappedRuntimeApproval struct {
	Approval provider.RuntimeApproval
	Method   string
}

// MapRuntimeApprovalRequest converts a pinned Codex server request into a
// bounded one-shot approval. Unknown, unbound, network, and unsupported
// requests fail closed so the caller can send a deny response.
func MapRuntimeApprovalRequest(request protocol.ServerRequest, context NotificationContext, now time.Time) (MappedRuntimeApproval, error) {
	if now.IsZero() || context.ThreadID == "" || context.OperationID == "" || context.CorrelationID.Validate() != nil || context.TurnID == "" || context.TurnRef.Validate() != nil {
		return MappedRuntimeApproval{}, provider.ErrInvalidApproval
	}
	requestID := provider.ProviderRequestID("codex-" + request.Method + "-" + request.ID.String())
	base := provider.RuntimeApprovalRequest{RequestID: requestID, ThreadID: context.ThreadID, OperationID: context.OperationID, CorrelationID: context.CorrelationID, TurnRef: context.TurnRef, ExpiresAt: now.Add(2 * time.Minute)}
	details := provider.RuntimeApprovalDetails{RequestedScope: request.Method}
	switch request.Method {
	case serverRequestCommandApproval:
		var params protocol.CommandExecutionRequestApprovalParams
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" || params.TurnID == "" || params.Command == nil || strings.TrimSpace(*params.Command) == "" {
			return MappedRuntimeApproval{}, provider.ErrInvalidApproval
		}
		command := strings.TrimSpace(*params.Command)
		cwd := ""
		if params.CWD != nil {
			cwd = strings.TrimSpace(*params.CWD)
		}
		executable := commandExecutable(command)
		if !filepath.IsAbs(executable) {
			if !filepath.IsAbs(cwd) {
				return MappedRuntimeApproval{}, provider.ErrInvalidApproval
			}
			executable = filepath.Join(cwd, executable)
		}
		executable = filepath.Clean(executable)
		digest := sha256.Sum256([]byte(command))
		base.Scope = provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalCommand, Executable: executable, ArgumentsDigest: hex.EncodeToString(digest[:])}
		details.ExactCommandArgs = command
		if len(params.NetworkApprovalContext) > 0 && string(params.NetworkApprovalContext) != "null" {
			details.NetworkTarget = networkTarget(params.NetworkApprovalContext)
			details.RequestedScope = "network access"
		}
	case serverRequestFileApproval:
		var params protocol.FileChangeRequestApprovalParams
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" || params.TurnID == "" || params.GrantRoot == nil || !filepath.IsAbs(strings.TrimSpace(*params.GrantRoot)) {
			return MappedRuntimeApproval{}, provider.ErrInvalidApproval
		}
		base.Scope = provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalFile, Path: provider.PermissionRoot{Path: filepath.Clean(strings.TrimSpace(*params.GrantRoot))}}
	case serverRequestPermissionsApproval:
		var params protocol.PermissionsRequestApprovalParams
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" || params.TurnID == "" || !filepath.IsAbs(strings.TrimSpace(params.CWD)) {
			return MappedRuntimeApproval{}, provider.ErrInvalidApproval
		}
		base.Scope = provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalFile, Path: provider.PermissionRoot{Path: filepath.Clean(strings.TrimSpace(params.CWD))}}
		details.RequestedScope = "filesystem or network permission expansion"
	case serverRequestToolCall:
		var params protocol.DynamicToolCallParams
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID == "" || params.TurnID == "" || strings.TrimSpace(params.Tool) == "" {
			return MappedRuntimeApproval{}, provider.ErrInvalidApproval
		}
		base.Scope = provider.RuntimeApprovalScope{Kind: provider.RuntimeApprovalTool, Tool: strings.TrimSpace(params.Tool)}
		details.ToolName = strings.TrimSpace(params.Tool)
	case serverRequestToolInput:
		return MappedRuntimeApproval{}, provider.ErrInvalidApproval
	case serverRequestLegacyExec, serverRequestLegacyPatch:
		return MappedRuntimeApproval{}, provider.ErrInvalidApproval
	default:
		return MappedRuntimeApproval{}, provider.ErrInvalidApproval
	}
	if err := base.ValidateAt(now); err != nil {
		return MappedRuntimeApproval{}, err
	}
	approval, err := provider.NewRuntimeApproval(base, now)
	if err != nil {
		return MappedRuntimeApproval{}, err
	}
	approval.Details = details
	return MappedRuntimeApproval{Approval: approval, Method: request.Method}, nil
}

func commandExecutable(command string) string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, `"`) {
		if end := strings.Index(command[1:], `"`); end >= 0 {
			return command[1 : end+1]
		}
	}
	if end := strings.Index(strings.ToLower(command), ".exe "); end >= 0 {
		return command[:end+4]
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func networkTarget(data json.RawMessage) string {
	var value struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(data, &value) == nil {
		return strings.TrimSpace(value.Host)
	}
	return ""
}

// MapNotification converts only pinned, supported app-server notifications
// into bounded neutral events. Additive or unknown methods return no event.
func MapNotification(notification protocol.Notification, context NotificationContext) ([]provider.ProviderEvent, error) {
	base := provider.ProviderEvent{
		Sequence:        context.Sequence,
		ThreadID:        context.ThreadID,
		OperationID:     context.OperationID,
		CorrelationID:   context.CorrelationID,
		ConversationID:  context.ConversationID,
		ConversationRef: context.ConversationRef,
		TurnID:          context.TurnID,
		TurnRef:         context.TurnRef,
	}
	switch notification.Method {
	case notificationThreadStarted:
		var value protocol.ThreadStartedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.Thread.ID == "" {
			return nil, fmt.Errorf("%w: thread started", ErrInvalidNotification)
		}
		base.Kind = provider.EventConversationStarted
		base.ConversationRef = provider.ProviderConversationRef(value.Thread.ID)
		base.Status = "started"
		return []provider.ProviderEvent{base}, nil
	case notificationTurnStarted:
		var value protocol.TurnStartedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.Turn.ID == "" || value.Turn.Status == "" {
			return nil, fmt.Errorf("%w: turn started", ErrInvalidNotification)
		}
		base.Kind = provider.EventTurnStarted
		base.TurnRef = provider.ProviderTurnRef(value.Turn.ID)
		base.Status = value.Turn.Status
		return []provider.ProviderEvent{base}, nil
	case notificationTurnCompleted:
		var value protocol.TurnCompletedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.Turn.ID == "" || value.Turn.Status == "" {
			return nil, fmt.Errorf("%w: turn completed", ErrInvalidNotification)
		}
		base.TurnRef = provider.ProviderTurnRef(value.Turn.ID)
		base.Status = value.Turn.Status
		if value.Turn.Status == "completed" {
			base.Kind = provider.EventTurnCompleted
		} else {
			base.Kind = provider.EventTurnFailed
			base.Error = turnErrorMessage(value.Turn.Error, value.Turn.Status)
		}
		return []provider.ProviderEvent{base}, nil
	case notificationMessageDelta:
		var value protocol.AgentMessageDeltaNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.TurnID == "" || value.ItemID == "" || value.Delta == "" {
			return nil, fmt.Errorf("%w: message delta", ErrInvalidNotification)
		}
		base.Kind = provider.EventMessageDelta
		base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		base.ItemRef = value.ItemID
		base.Text = value.Delta
		base.CoalescingKey = value.ItemID
		base.Coalescible = true
		return []provider.ProviderEvent{base}, nil
	case notificationItemStarted, notificationItemCompleted:
		var value struct {
			Item     json.RawMessage `json:"item"`
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
		}
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.TurnID == "" {
			return nil, fmt.Errorf("%w: item lifecycle", ErrInvalidNotification)
		}
		item, err := mapItem(value.Item)
		if err != nil {
			return nil, err
		}
		base.ItemRef = item.ID
		base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		base.Status = item.Status
		switch item.Type {
		case "agentMessage":
			if notification.Method == notificationItemStarted {
				base.Kind = provider.EventMessageStarted
			} else {
				base.Kind = provider.EventMessageCompleted
			}
		case "commandExecution":
			if notification.Method == notificationItemStarted {
				base.Kind = provider.EventCommandStarted
			} else {
				base.Kind = provider.EventCommandCompleted
			}
		case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch":
			base.Kind = provider.EventToolActivity
		case "fileChange":
			base.Kind = provider.EventFileActivity
		default:
			return nil, nil
		}
		return []provider.ProviderEvent{base}, nil
	case notificationError:
		var value protocol.ErrorNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || len(value.Error) == 0 {
			return nil, fmt.Errorf("%w: error", ErrInvalidNotification)
		}
		base.Kind = provider.EventProviderError
		base.Error = turnErrorMessage(value.Error, "provider_error")
		if value.TurnID != "" {
			base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		}
		return []provider.ProviderEvent{base}, nil
	case notificationThreadStatus:
		var value protocol.ThreadStatusChangedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" {
			return nil, fmt.Errorf("%w: thread status", ErrInvalidNotification)
		}
		base.Kind = provider.EventConnectionChanged
		base.Status = "thread_status_changed"
		return []provider.ProviderEvent{base}, nil
	default:
		return nil, nil
	}
}

type mappedItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

func mapItem(data json.RawMessage) (mappedItem, error) {
	var item mappedItem
	if err := json.Unmarshal(data, &item); err != nil || item.ID == "" || item.Type == "" {
		return mappedItem{}, fmt.Errorf("%w: item", ErrInvalidNotification)
	}
	return item, nil
}

func turnErrorMessage(data json.RawMessage, fallback string) string {
	var object struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &object) == nil && strings.TrimSpace(object.Message) != "" {
		return object.Message
	}
	var text string
	if json.Unmarshal(data, &text) == nil && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func mapConversationResponse(response protocol.ThreadStartResponse) (provider.ProviderConversationRef, error) {
	ref := provider.ProviderConversationRef(response.Thread.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidConversationResponse
	}
	return ref, nil
}

func mapTurnResponse(response protocol.TurnStartResponse) (provider.ProviderTurnRef, error) {
	ref := provider.ProviderTurnRef(response.Turn.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidTurnResponse
	}
	return ref, nil
}
