package app

import (
	"fmt"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// State is the canonical application state. Only Reducer.Handle may mutate
// the reducer-owned instance; frontends receive AppSnapshot values instead.
type State struct {
	Revision      uint64
	Repository    *RepositoryState
	SessionID     *domain.ReviewSessionID
	Target        *repository.ResolvedTarget
	Tree          TreeProjection
	ChangedFiles  []ChangedFileSummary
	ActiveFile    *repository.RepoPath
	ActiveThread  *domain.ReviewThreadID
	Provider      ProviderStatus
	Operations    map[domain.OperationID]OperationState
	Notifications []Notification
}

// RepositoryState contains the repository and selected worktree loaded by the
// application. Git adapters populate it; the application does not execute Git.
type RepositoryState struct {
	Repository repository.Repository
	Worktree   *repository.WorktreeRef
}

// Validate checks the repository/worktree relationship without touching the
// filesystem or executing Git.
func (s RepositoryState) Validate() error {
	if err := s.Repository.Validate(); err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	if s.Worktree != nil {
		if err := s.Worktree.Validate(); err != nil {
			return fmt.Errorf("worktree: %w", err)
		}
		if s.Worktree.RepositoryID != s.Repository.ID {
			return fmt.Errorf("worktree repository mismatch")
		}
	}
	return nil
}

// ProviderConnectionState describes provider availability without exposing
// provider protocol values.
type ProviderConnectionState string

const (
	ProviderDisconnected ProviderConnectionState = "disconnected"
	ProviderConnecting   ProviderConnectionState = "connecting"
	ProviderConnected    ProviderConnectionState = "connected"
	ProviderUnavailable  ProviderConnectionState = "unavailable"
)

// ProviderStatus is the frontend-neutral provider summary.
type ProviderStatus struct {
	Connection ProviderConnectionState
	Message    string
}

// NewState returns an empty canonical state with initialized collections.
func NewState() State {
	return State{
		Operations: make(map[domain.OperationID]OperationState),
		Provider:   ProviderStatus{Connection: ProviderDisconnected},
	}
}

func (s State) clone() State {
	copyState := s
	copyState.Repository = cloneRepositoryState(s.Repository)
	copyState.SessionID = cloneReviewSessionID(s.SessionID)
	copyState.Target = cloneResolvedTarget(s.Target)
	copyState.Tree = s.Tree.clone()
	copyState.ChangedFiles = cloneChangedFileSummaries(s.ChangedFiles)
	copyState.ActiveFile = cloneRepoPath(s.ActiveFile)
	copyState.ActiveThread = cloneReviewThreadID(s.ActiveThread)
	copyState.Operations = make(map[domain.OperationID]OperationState, len(s.Operations))
	for id, operation := range s.Operations {
		copyState.Operations[id] = operation
	}
	copyState.Notifications = cloneNotifications(s.Notifications)
	return copyState
}

func cloneRepositoryState(value *RepositoryState) *RepositoryState {
	if value == nil {
		return nil
	}
	copyValue := *value
	copyValue.Repository.Remotes = cloneRemotes(value.Repository.Remotes)
	if value.Worktree != nil {
		worktree := *value.Worktree
		if value.Worktree.Upstream != nil {
			upstream := *value.Worktree.Upstream
			worktree.Upstream = &upstream
		}
		copyValue.Worktree = &worktree
	}
	return &copyValue
}

func cloneRemotes(values []repository.Remote) []repository.Remote {
	if len(values) == 0 {
		return nil
	}
	copyValues := make([]repository.Remote, len(values))
	for i, value := range values {
		copyValues[i] = value
		copyValues[i].FetchURLs = append([]string(nil), value.FetchURLs...)
		copyValues[i].PushURLs = append([]string(nil), value.PushURLs...)
	}
	return copyValues
}

func cloneReviewSessionID(value *domain.ReviewSessionID) *domain.ReviewSessionID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneReviewThreadID(value *domain.ReviewThreadID) *domain.ReviewThreadID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneResolvedTarget(value *repository.ResolvedTarget) *repository.ResolvedTarget {
	if value == nil {
		return nil
	}
	copyValue := *value
	if value.EditDestination != nil {
		worktreeID := *value.EditDestination
		copyValue.EditDestination = &worktreeID
	}
	return &copyValue
}

func cloneRepoPath(value *repository.RepoPath) *repository.RepoPath {
	if value == nil {
		return nil
	}
	path := repository.RepoPath(value.Bytes())
	return &path
}
