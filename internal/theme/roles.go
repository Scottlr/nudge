package theme

// Role identifies a semantic visual meaning. Components select roles instead
// of embedding product colors in their rendering code.
type Role string

const (
	RoleForeground Role = "foreground"
	RoleBackground Role = "background"
	RoleBorder     Role = "border"
	RoleMuted      Role = "muted"
	RoleFocus      Role = "focus"
	RoleSelection  Role = "selection"

	RoleDiffAdded    Role = "diff_added"
	RoleDiffDeleted  Role = "diff_deleted"
	RoleDiffModified Role = "diff_modified"
	RoleDiffHunk     Role = "diff_hunk"
	RoleDiffContext  Role = "diff_context"

	RoleThreadOpen     Role = "thread_open"
	RoleThreadBusy     Role = "thread_busy"
	RoleThreadProposal Role = "thread_proposal"
	RoleThreadResolved Role = "thread_resolved"
	RoleThreadError    Role = "thread_error"
	RoleThreadOrphaned Role = "thread_orphaned"

	RoleCursor Role = "cursor"
	RoleSearch Role = "search"
)

// Short semantic names mirror the language used by the product design while
// the Role-prefixed names remain convenient for callers that prefer explicit
// constants.
const (
	Foreground     = RoleForeground
	Background     = RoleBackground
	Border         = RoleBorder
	TextMuted      = RoleMuted
	Focus          = RoleFocus
	Selection      = RoleSelection
	DiffAdded      = RoleDiffAdded
	DiffDeleted    = RoleDiffDeleted
	DiffModified   = RoleDiffModified
	DiffHunk       = RoleDiffHunk
	DiffContext    = RoleDiffContext
	ThreadOpen     = RoleThreadOpen
	ThreadBusy     = RoleThreadBusy
	ThreadProposal = RoleThreadProposal
	ThreadResolved = RoleThreadResolved
	ThreadError    = RoleThreadError
	ThreadOrphaned = RoleThreadOrphaned
	Cursor         = RoleCursor
	Search         = RoleSearch
)

var requiredRoles = [...]Role{
	RoleForeground,
	RoleBackground,
	RoleBorder,
	RoleMuted,
	RoleFocus,
	RoleSelection,
	RoleDiffAdded,
	RoleDiffDeleted,
	RoleDiffModified,
	RoleDiffHunk,
	RoleDiffContext,
	RoleThreadOpen,
	RoleThreadBusy,
	RoleThreadProposal,
	RoleThreadResolved,
	RoleThreadError,
	RoleThreadOrphaned,
	RoleCursor,
	RoleSearch,
}

// RequiredRoles returns the semantic roles every built-in theme must cover.
func RequiredRoles() []Role {
	roles := make([]Role, len(requiredRoles))
	copy(roles, requiredRoles[:])
	return roles
}
