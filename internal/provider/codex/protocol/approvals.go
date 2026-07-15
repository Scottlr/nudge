package protocol

import "encoding/json"

// CommandExecutionRequestApprovalParams is the pinned v0.144 approval
// request shape. Additive provider fields remain outside this DTO.
type CommandExecutionRequestApprovalParams struct {
	ThreadID                        string          `json:"threadId"`
	TurnID                          string          `json:"turnId"`
	ApprovalID                      *string         `json:"approvalId"`
	ItemID                          string          `json:"itemId"`
	Command                         *string         `json:"command"`
	CommandActions                  json.RawMessage `json:"commandActions"`
	CWD                             *string         `json:"cwd"`
	EnvironmentID                   *string         `json:"environmentId"`
	NetworkApprovalContext          json.RawMessage `json:"networkApprovalContext"`
	ProposedExecpolicyAmendment     []string        `json:"proposedExecpolicyAmendment"`
	ProposedNetworkPolicyAmendments json.RawMessage `json:"proposedNetworkPolicyAmendments"`
	Reason                          *string         `json:"reason"`
	StartedAtMS                     int64           `json:"startedAtMs"`
}

// FileChangeRequestApprovalParams is the pinned file-change approval shape.
type FileChangeRequestApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	GrantRoot   *string `json:"grantRoot"`
	Reason      *string `json:"reason"`
	StartedAtMS int64   `json:"startedAtMs"`
}

// PermissionsRequestApprovalParams retains the permission profile as opaque
// JSON. The adapter only needs to detect requested filesystem/network scope;
// it never forwards the profile into durable application state.
type PermissionsRequestApprovalParams struct {
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
	ItemID      string          `json:"itemId"`
	CWD         string          `json:"cwd"`
	Permissions json.RawMessage `json:"permissions"`
	Reason      *string         `json:"reason"`
	StartedAtMS int64           `json:"startedAtMs"`
}

// DynamicToolCallParams identifies a provider tool request without retaining
// its arguments in normalized or durable state.
type DynamicToolCallParams struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolRequestUserInputParams is parsed only to fail closed for unsupported
// interactive provider requests.
type ToolRequestUserInputParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
}

// ExecCommandApprovalParams is the legacy app-server command approval shape.
type ExecCommandApprovalParams struct {
	CallID         string   `json:"callId"`
	ConversationID string   `json:"conversationId"`
	ApprovalID     *string  `json:"approvalId"`
	Command        []string `json:"command"`
	CWD            string   `json:"cwd"`
	Reason         *string  `json:"reason"`
}

// ApplyPatchApprovalParams is the legacy app-server patch approval shape.
type ApplyPatchApprovalParams struct {
	CallID         string          `json:"callId"`
	ConversationID string          `json:"conversationId"`
	GrantRoot      *string         `json:"grantRoot"`
	FileChanges    json.RawMessage `json:"fileChanges"`
	Reason         *string         `json:"reason"`
}
