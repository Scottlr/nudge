package app

import (
	"sort"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// RepositorySummary is the bounded repository projection exposed to clients.
type RepositorySummary struct {
	ID          domain.RepositoryID
	DisplayName string
	WorktreeID  domain.WorktreeID
	BranchName  string
	Detached    bool
}

// TargetSummary is the bounded target projection exposed to clients.
type TargetSummary struct {
	Present          bool
	Spec             repository.ReviewTargetSpec
	Generation       repository.TargetGeneration
	Editable         bool
	EditDestination  *domain.WorktreeID
	Fingerprint      string
	DirtyWorktree    bool
	NoFetchWarning   bool
	BaseBranchSource string
	BranchRef        string
	BaseBranchRef    string
	BaseObjectID     repository.ObjectID
	ParentLabel      string
	MergeBase        repository.ObjectID
	HeadObjectID     repository.ObjectID
}

// TreeEntrySummary is metadata for one visible repository tree entry.
type TreeEntrySummary struct {
	Path      repository.RepoPath
	Kind      repository.FileKind
	ModeClass repository.GitModeClass
	Changed   bool
	LazyChild bool
}

// TreeProjection is a bounded tree view. Large trees are paged by later
// application queries rather than eagerly loaded into canonical state.
type TreeProjection struct {
	Entries []TreeEntrySummary
}

// ChangedFileSummary is the frontend-neutral summary of one changed path.
type ChangedFileSummary struct {
	OldPath        repository.RepoPath
	NewPath        repository.RepoPath
	Kind           repository.ChangeKind
	OldKind        repository.FileKind
	NewKind        repository.FileKind
	OldMode        uint32
	NewMode        uint32
	ModeTransition *repository.ModeTransition
	Binary         bool
}

// ThreadSummary reserves the stable client projection for the thread slices.
// Thread entities are introduced by the later thread tasks.
type ThreadSummary struct {
	ID                     domain.ReviewThreadID
	SessionID              domain.ReviewSessionID
	Title                  string
	Status                 string
	Resolution             review.ResolutionState
	Conversation           review.ConversationState
	Proposal               review.ProposalState
	Anchor                 review.AnchorState
	AnchorSide             repository.DiffSide
	AnchorStartLine        int
	AnchorEndLine          int
	Read                   review.ReadState
	FailurePhase           review.FailurePhase
	ErrorCode              review.ErrorCode
	AnchorPath             repository.RepoPath
	Unread                 bool
	ProviderConversationID *domain.ProviderConversationID
	LatestProposalID       *domain.ProposalID
	UpdatedAt              time.Time
}

// ThreadDetail is the bounded active-thread projection.
type ThreadDetail struct {
	Summary      ThreadSummary
	MessageCount uint64
}

// ThreadWindow is the bounded canonical thread projection. It contains only
// complete summaries and a revision-bound continuation cursor.
type ThreadWindow struct {
	Items      []ThreadSummary
	Query      ThreadPage
	NextCursor *ThreadCursor
	TotalCount uint64
	Revision   uint64
}

// FileView identifies the active file content request without embedding large
// content in the application snapshot.
type FileView struct {
	Path             repository.RepoPath
	TargetGeneration repository.TargetGeneration
}

// Notification is a bounded safe message for frontend presentation.
type Notification struct {
	Level   string
	Code    ErrorCode
	Message string
}

// AppSnapshot is a complete immutable projection of canonical application
// state at one revision. Mutable slices and pointers are copied at creation.
type AppSnapshot struct {
	Revision      uint64
	Repository    RepositorySummary
	SessionID     *domain.ReviewSessionID
	Target        TargetSummary
	Tree          TreeProjection
	ChangedFiles  []ChangedFileSummary
	Threads       []ThreadSummary
	ThreadWindow  ThreadWindow
	ActiveThread  *ThreadDetail
	ActiveFile    *FileView
	Provider      ProviderStatus
	Operations    []OperationState
	Notifications []Notification
}

// Clone returns an independent snapshot copy suitable for another consumer.
func (s AppSnapshot) Clone() AppSnapshot {
	copySnapshot := s
	copySnapshot.SessionID = cloneReviewSessionID(s.SessionID)
	copySnapshot.Target.EditDestination = cloneWorktreeID(s.Target.EditDestination)
	copySnapshot.Tree = s.Tree.clone()
	copySnapshot.ChangedFiles = cloneChangedFileSummaries(s.ChangedFiles)
	copySnapshot.Threads = cloneThreadSummaries(s.Threads)
	copySnapshot.ThreadWindow = s.ThreadWindow.clone()
	if s.ActiveThread != nil {
		activeThread := *s.ActiveThread
		activeThread.Summary = cloneThreadSummary(s.ActiveThread.Summary)
		copySnapshot.ActiveThread = &activeThread
	}
	if s.ActiveFile != nil {
		activeFile := *s.ActiveFile
		activeFile.Path = repository.RepoPath(s.ActiveFile.Path.Bytes())
		copySnapshot.ActiveFile = &activeFile
	}
	copySnapshot.Operations = append([]OperationState(nil), s.Operations...)
	copySnapshot.Notifications = cloneNotifications(s.Notifications)
	return copySnapshot
}

func snapshotFromState(state State) AppSnapshot {
	snapshot := AppSnapshot{
		Revision:      state.Revision,
		SessionID:     cloneReviewSessionID(state.SessionID),
		Tree:          state.Tree.clone(),
		ChangedFiles:  cloneChangedFileSummaries(state.ChangedFiles),
		Provider:      state.Provider,
		Threads:       cloneThreadSummaries(state.ThreadWindow.Items),
		ThreadWindow:  state.ThreadWindow.clone(),
		Operations:    make([]OperationState, 0, len(state.Operations)),
		Notifications: cloneNotifications(state.Notifications),
	}

	if state.Repository != nil {
		snapshot.Repository = RepositorySummary{
			ID:          state.Repository.Repository.ID,
			DisplayName: state.Repository.Repository.DisplayName,
		}
		if state.Repository.Worktree != nil {
			snapshot.Repository.WorktreeID = state.Repository.Worktree.ID
			snapshot.Repository.BranchName = state.Repository.Worktree.BranchName
			snapshot.Repository.Detached = state.Repository.Worktree.Detached
		}
	}

	if state.Target != nil {
		baseObjectID := state.Target.ResolvedBaseRef
		if state.Target.Spec.Kind == repository.TargetCommit {
			baseObjectID = state.Target.Base.ObjectID
		}
		snapshot.Target = TargetSummary{
			Present:          true,
			Spec:             state.Target.Spec,
			Generation:       state.Target.Generation,
			Editable:         state.Target.Editable,
			EditDestination:  cloneWorktreeID(state.Target.EditDestination),
			Fingerprint:      state.Target.Fingerprint,
			DirtyWorktree:    state.Target.DirtyWorktree,
			NoFetchWarning:   state.Target.NoFetchWarning,
			BaseBranchSource: string(state.Target.BaseBranchSource),
			BranchRef:        state.Target.BranchRef,
			BaseBranchRef:    state.Target.BaseBranchRef,
			BaseObjectID:     baseObjectID,
			ParentLabel:      state.Target.ParentLabel,
			MergeBase:        state.Target.MergeBase,
			HeadObjectID:     state.Target.ResolvedCommit,
		}
	}
	if state.ActiveThreadDetail != nil {
		activeThread := *state.ActiveThreadDetail
		activeThread.Summary = cloneThreadSummary(state.ActiveThreadDetail.Summary)
		snapshot.ActiveThread = &activeThread
	} else if state.ActiveThread != nil {
		activeThread := ThreadDetail{Summary: ThreadSummary{ID: *state.ActiveThread}}
		snapshot.ActiveThread = &activeThread
	}
	if state.ActiveFile != nil {
		snapshot.ActiveFile = &FileView{Path: repository.RepoPath(state.ActiveFile.Bytes()), TargetGeneration: targetGeneration(state.Target)}
	}

	for _, operation := range state.Operations {
		snapshot.Operations = append(snapshot.Operations, operation)
	}
	sort.Slice(snapshot.Operations, func(i, j int) bool {
		return snapshot.Operations[i].ID < snapshot.Operations[j].ID
	})
	return snapshot
}

func (w ThreadWindow) clone() ThreadWindow {
	copyWindow := w
	copyWindow.Items = cloneThreadSummaries(w.Items)
	if w.Query.Cursor != nil {
		cursor := *w.Query.Cursor
		copyWindow.Query.Cursor = &cursor
	}
	if w.NextCursor != nil {
		cursor := *w.NextCursor
		copyWindow.NextCursor = &cursor
	}
	return copyWindow
}

func targetGeneration(target *repository.ResolvedTarget) repository.TargetGeneration {
	if target == nil {
		return 0
	}
	return target.Generation
}

func cloneWorktreeID(value *domain.WorktreeID) *domain.WorktreeID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (p TreeProjection) clone() TreeProjection {
	copyProjection := p
	copyProjection.Entries = make([]TreeEntrySummary, len(p.Entries))
	for i, entry := range p.Entries {
		copyProjection.Entries[i] = entry
		copyProjection.Entries[i].Path = repository.RepoPath(entry.Path.Bytes())
	}
	return copyProjection
}

func cloneChangedFileSummaries(values []ChangedFileSummary) []ChangedFileSummary {
	if len(values) == 0 {
		return nil
	}
	copyValues := make([]ChangedFileSummary, len(values))
	for i, value := range values {
		copyValues[i] = value
		copyValues[i].OldPath = repository.RepoPath(value.OldPath.Bytes())
		copyValues[i].NewPath = repository.RepoPath(value.NewPath.Bytes())
		if value.ModeTransition != nil {
			transition := *value.ModeTransition
			copyValues[i].ModeTransition = &transition
		}
	}
	return copyValues
}

func cloneThreadSummaries(values []ThreadSummary) []ThreadSummary {
	if len(values) == 0 {
		return nil
	}
	copyValues := make([]ThreadSummary, len(values))
	for i, value := range values {
		copyValues[i] = cloneThreadSummary(value)
	}
	return copyValues
}

func cloneThreadSummary(value ThreadSummary) ThreadSummary {
	value.AnchorPath = repository.RepoPath(value.AnchorPath.Bytes())
	value.ProviderConversationID = cloneProviderConversationID(value.ProviderConversationID)
	value.LatestProposalID = cloneProposalID(value.LatestProposalID)
	return value
}

func cloneProviderConversationID(value *domain.ProviderConversationID) *domain.ProviderConversationID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneProposalID(value *domain.ProposalID) *domain.ProposalID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneNotifications(values []Notification) []Notification {
	return append([]Notification(nil), values...)
}
