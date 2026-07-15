package theme

// Role identifies a semantic visual meaning. Components select roles instead
// of embedding product colors in their rendering code.
type Role string

const (
	RoleForeground         Role = "foreground"
	RoleBackground         Role = "background"
	RoleBorder             Role = "border"
	RoleBorderInactive     Role = "border_inactive"
	RoleBorderFocus        Role = "border_focus"
	RoleMuted              Role = "muted"
	RoleFocus              Role = "focus"
	RoleSelection          Role = "selection"
	RoleDisabled           Role = "disabled"
	RoleHelp               Role = "help"
	RoleWarning            Role = "warning"
	RoleError              Role = "error"
	RoleOverlay            Role = "overlay"
	RoleOverlayTitle       Role = "overlay_title"
	RoleCursor             Role = "cursor"
	RoleSearch             Role = "search"
	RoleSearchMatch        Role = "search_match"
	RoleBadge              Role = "badge"
	RoleBadgeAdded         Role = "badge_added"
	RoleBadgeDeleted       Role = "badge_deleted"
	RoleBadgeModified      Role = "badge_modified"
	RoleBadgeConflict      Role = "badge_conflict"
	RoleMarker             Role = "marker"
	RoleDiffAdded          Role = "diff_added"
	RoleDiffDeleted        Role = "diff_deleted"
	RoleDiffModified       Role = "diff_modified"
	RoleDiffHunk           Role = "diff_hunk"
	RoleDiffContext        Role = "diff_context"
	RoleDiffIndicator      Role = "diff_indicator"
	RoleThreadOpen         Role = "thread_open"
	RoleThreadBusy         Role = "thread_busy"
	RoleThreadProposal     Role = "thread_proposal"
	RoleThreadResolved     Role = "thread_resolved"
	RoleThreadError        Role = "thread_error"
	RoleThreadOrphaned     Role = "thread_orphaned"
	RoleStatus             Role = "status"
	RoleStatusConnected    Role = "status_connected"
	RoleStatusDisconnected Role = "status_disconnected"
	RoleStatusBusy         Role = "status_busy"
	RoleStatusWarning      Role = "status_warning"
	RoleStatusError        Role = "status_error"
	RoleProposal           Role = "proposal"
	RoleProposalGenerating Role = "proposal_generating"
	RoleProposalReady      Role = "proposal_ready"
	RoleProposalStale      Role = "proposal_stale"
	RoleProposalApplying   Role = "proposal_applying"
	RoleProposalApplied    Role = "proposal_applied"
	RoleProposalRejected   Role = "proposal_rejected"
	RoleProposalFailed     Role = "proposal_failed"
	RoleSyntax             Role = "syntax"
	RoleSyntaxComment      Role = "syntax_comment"
	RoleSyntaxKeyword      Role = "syntax_keyword"
	RoleSyntaxString       Role = "syntax_string"
	RoleSyntaxNumber       Role = "syntax_number"
	RoleSyntaxType         Role = "syntax_type"
	RoleSyntaxOperator     Role = "syntax_operator"
	RoleSyntaxPunct        Role = "syntax_punctuation"
)

// Short semantic names mirror the language used by the product design while
// the Role-prefixed names remain convenient for callers that prefer explicit
// constants.
const (
	Foreground         = RoleForeground
	Background         = RoleBackground
	Border             = RoleBorder
	BorderInactive     = RoleBorderInactive
	BorderFocus        = RoleBorderFocus
	TextMuted          = RoleMuted
	Focus              = RoleFocus
	Selection          = RoleSelection
	Disabled           = RoleDisabled
	Help               = RoleHelp
	Warning            = RoleWarning
	Error              = RoleError
	Overlay            = RoleOverlay
	OverlayTitle       = RoleOverlayTitle
	Cursor             = RoleCursor
	Search             = RoleSearch
	SearchMatch        = RoleSearchMatch
	Badge              = RoleBadge
	BadgeAdded         = RoleBadgeAdded
	BadgeDeleted       = RoleBadgeDeleted
	BadgeModified      = RoleBadgeModified
	BadgeConflict      = RoleBadgeConflict
	Marker             = RoleMarker
	DiffAdded          = RoleDiffAdded
	DiffDeleted        = RoleDiffDeleted
	DiffModified       = RoleDiffModified
	DiffHunk           = RoleDiffHunk
	DiffContext        = RoleDiffContext
	DiffIndicator      = RoleDiffIndicator
	ThreadOpen         = RoleThreadOpen
	ThreadBusy         = RoleThreadBusy
	ThreadProposal     = RoleThreadProposal
	ThreadResolved     = RoleThreadResolved
	ThreadError        = RoleThreadError
	ThreadOrphaned     = RoleThreadOrphaned
	Status             = RoleStatus
	StatusConnected    = RoleStatusConnected
	StatusDisconnected = RoleStatusDisconnected
	StatusBusy         = RoleStatusBusy
	StatusWarning      = RoleStatusWarning
	StatusError        = RoleStatusError
	Proposal           = RoleProposal
	ProposalGenerating = RoleProposalGenerating
	ProposalReady      = RoleProposalReady
	ProposalStale      = RoleProposalStale
	ProposalApplying   = RoleProposalApplying
	ProposalApplied    = RoleProposalApplied
	ProposalRejected   = RoleProposalRejected
	ProposalFailed     = RoleProposalFailed
	Syntax             = RoleSyntax
	SyntaxComment      = RoleSyntaxComment
	SyntaxKeyword      = RoleSyntaxKeyword
	SyntaxString       = RoleSyntaxString
	SyntaxNumber       = RoleSyntaxNumber
	SyntaxType         = RoleSyntaxType
	SyntaxOperator     = RoleSyntaxOperator
	SyntaxPunct        = RoleSyntaxPunct
)

const (
	RoleTextMuted         = RoleMuted
	RoleBorderFocused     = RoleBorderFocus
	RoleSyntaxPunctuation = RoleSyntaxPunct
)

var requiredRoles = [...]Role{
	RoleForeground,
	RoleBackground,
	RoleBorder,
	RoleBorderInactive,
	RoleBorderFocus,
	RoleMuted,
	RoleFocus,
	RoleSelection,
	RoleDisabled,
	RoleHelp,
	RoleWarning,
	RoleError,
	RoleOverlay,
	RoleOverlayTitle,
	RoleCursor,
	RoleSearch,
	RoleSearchMatch,
	RoleBadge,
	RoleBadgeAdded,
	RoleBadgeDeleted,
	RoleBadgeModified,
	RoleBadgeConflict,
	RoleMarker,
	RoleDiffAdded,
	RoleDiffDeleted,
	RoleDiffModified,
	RoleDiffHunk,
	RoleDiffContext,
	RoleDiffIndicator,
	RoleThreadOpen,
	RoleThreadBusy,
	RoleThreadProposal,
	RoleThreadResolved,
	RoleThreadError,
	RoleThreadOrphaned,
	RoleStatus,
	RoleStatusConnected,
	RoleStatusDisconnected,
	RoleStatusBusy,
	RoleStatusWarning,
	RoleStatusError,
	RoleProposal,
	RoleProposalGenerating,
	RoleProposalReady,
	RoleProposalStale,
	RoleProposalApplying,
	RoleProposalApplied,
	RoleProposalRejected,
	RoleProposalFailed,
	RoleSyntax,
	RoleSyntaxComment,
	RoleSyntaxKeyword,
	RoleSyntaxString,
	RoleSyntaxNumber,
	RoleSyntaxType,
	RoleSyntaxOperator,
	RoleSyntaxPunct,
}

// RequiredRoles returns the semantic roles every built-in theme must cover.
func RequiredRoles() []Role {
	roles := make([]Role, len(requiredRoles))
	copy(roles, requiredRoles[:])
	return roles
}
