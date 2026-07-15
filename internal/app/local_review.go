package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/highlight"
)

var (
	// ErrInvalidLocalReviewSource reports incomplete local-review composition.
	ErrInvalidLocalReviewSource = errors.New("invalid local review source")
	// ErrInvalidLocalReviewRequest reports an empty or unsafe launch path.
	ErrInvalidLocalReviewRequest = errors.New("invalid local review request")
)

// LocalReviewPhase identifies the bounded user-visible stage of one local
// review open. It is deliberately a small lifecycle, not canonical workflow
// state or a future target-mode enum.
type LocalReviewPhase string

const (
	LocalReviewResolvingRepository LocalReviewPhase = "resolving_repository"
	LocalReviewCapturing           LocalReviewPhase = "capturing_local_change"
	LocalReviewLoadingTree         LocalReviewPhase = "loading_tree"
	LocalReviewLoadingFile         LocalReviewPhase = "loading_file"
	LocalReviewClean               LocalReviewPhase = "clean"
	LocalReviewReady               LocalReviewPhase = "ready"
	LocalReviewCancelled           LocalReviewPhase = "cancelled"
	LocalReviewFailed              LocalReviewPhase = "failed"
)

// Highlighter is the application-owned consumer port for immutable source
// highlighting. The adapter performs tokenization outside Bubble Tea.
type Highlighter interface {
	Highlight(context.Context, repository.FileContent, string, string) (highlight.HighlightedFile, error)
}

// LocalReviewConfig supplies the explicit owner ports for one local runtime.
type LocalReviewConfig struct {
	Source              LocalReviewSource
	IDs                 IDSource
	Clock               Clock
	Persistence         PersistenceMode
	Sessions            *SessionManager
	PersistenceDegraded bool
	Branch              *BranchReviewConfig
	Commit              *CommitReviewConfig
}

// LocalReviewSnapshot is a bounded immutable projection of local-review
// progress and the first changed file. It contains no live filesystem path
// reads; local working-tree bytes come only from the adopted capture.
type LocalReviewSnapshot struct {
	Revision            uint64
	Persistence         PersistenceMode
	SessionID           domain.ReviewSessionID
	PersistenceDegraded bool
	StartPath           string
	Phase               LocalReviewPhase
	Repository          *RepositoryState
	Target              *repository.ResolvedTarget
	CaptureID           domain.CaptureID
	TreePage            TreePage
	ChangedFiles        []repository.ChangedFile
	ActiveFile          *repository.ChangedFile
	FileContent         *repository.FileContent
	FileDiff            *repository.FileDiff
	Displayed           *DisplayedContent
	DisplayedPage       *DisplayedContentPage
	Highlighted         *highlight.HighlightedFile
	Error               *AppError
}

// Clone returns a defensive snapshot copy for frontend projection.
func (s LocalReviewSnapshot) Clone() LocalReviewSnapshot {
	result := s
	if s.Repository != nil {
		value := cloneRepositoryState(s.Repository)
		result.Repository = value
	}
	if s.Target != nil {
		value := cloneResolvedTarget(s.Target)
		result.Target = value
	}
	result.TreePage = s.TreePage.Clone()
	result.ChangedFiles = cloneChangedFiles(s.ChangedFiles)
	if s.ActiveFile != nil {
		value := cloneChangedFile(*s.ActiveFile)
		result.ActiveFile = &value
	}
	if s.FileContent != nil {
		value := cloneFileContent(*s.FileContent)
		result.FileContent = &value
	}
	if s.FileDiff != nil {
		value := cloneFileDiff(*s.FileDiff)
		result.FileDiff = &value
	}
	if s.Displayed != nil {
		value := *s.Displayed
		if s.Displayed.BasePath != nil {
			path := repository.RepoPath(s.Displayed.BasePath.Bytes())
			value.BasePath = &path
		}
		if s.Displayed.HeadPath != nil {
			path := repository.RepoPath(s.Displayed.HeadPath.Bytes())
			value.HeadPath = &path
		}
		result.Displayed = &value
	}
	if s.DisplayedPage != nil {
		value := s.DisplayedPage.Clone()
		result.DisplayedPage = &value
	}
	if s.Highlighted != nil {
		value := cloneHighlighted(*s.Highlighted)
		result.Highlighted = &value
	}
	if s.Error != nil {
		value := *s.Error
		result.Error = &value
	}
	return result
}

// LocalReview runs one cancellable local open. A runtime owns one worker; a
// second Start call is rejected so stale result ordering cannot be ambiguous.
type LocalReview struct {
	source              LocalReviewSource
	ids                 IDSource
	clock               Clock
	persistence         PersistenceMode
	sessions            *SessionManager
	persistenceDegraded bool
	branch              *BranchReviewConfig
	commit              *CommitReviewConfig

	mu      sync.Mutex
	started bool
}

// NewLocalReview validates a local-review composition root.
func NewLocalReview(config LocalReviewConfig) (*LocalReview, error) {
	if err := config.Source.Validate(); err != nil {
		return nil, err
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	persistence := config.Persistence
	if persistence == "" {
		persistence = PersistenceDurable
	}
	if persistence != PersistenceDurable && persistence != PersistenceNoPersist {
		return nil, ErrInvalidLocalReviewSource
	}
	if config.Branch != nil {
		if err := config.Branch.validate(); err != nil || config.Source.Changed == nil || config.Source.Content == nil {
			return nil, ErrInvalidLocalReviewSource
		}
	}
	if config.Commit != nil {
		if err := config.Commit.validate(); err != nil || config.Source.Changed == nil || config.Source.Content == nil {
			return nil, ErrInvalidLocalReviewSource
		}
	}
	if config.Branch != nil && config.Commit != nil {
		return nil, ErrInvalidLocalReviewSource
	}
	return &LocalReview{
		source: config.Source, ids: config.IDs, clock: config.Clock, persistence: persistence,
		sessions: config.Sessions, persistenceDegraded: config.PersistenceDegraded, branch: config.Branch, commit: config.Commit,
	}, nil
}

// Start begins one asynchronous local-review open and returns its bounded
// projection stream. The first snapshot is published before repository I/O.
func (r *LocalReview) Start(ctx context.Context, startPath string) (<-chan LocalReviewSnapshot, error) {
	if r == nil || ctx == nil || strings.TrimSpace(startPath) == "" {
		return nil, ErrInvalidLocalReviewRequest
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil, ErrInvalidLocalReviewRequest
	}
	r.started = true
	r.mu.Unlock()

	stream := make(chan LocalReviewSnapshot, 12)
	go func() {
		defer close(stream)
		r.run(ctx, startPath, stream)
	}()
	return stream, nil
}

func (r *LocalReview) run(ctx context.Context, startPath string, stream chan<- LocalReviewSnapshot) {
	var revision uint64
	var sessionID domain.ReviewSessionID
	publish := func(snapshot LocalReviewSnapshot) bool {
		revision++
		snapshot.Revision = revision
		snapshot.StartPath = startPath
		if snapshot.Persistence == "" {
			snapshot.Persistence = r.persistence
		}
		snapshot.SessionID = sessionID
		snapshot.PersistenceDegraded = r.persistenceDegraded
		select {
		case stream <- snapshot.Clone():
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(phase LocalReviewPhase, operation string, cause error, repositoryState *RepositoryState, target *repository.ResolvedTarget, captureID domain.CaptureID, page TreePage, changes []repository.ChangedFile) {
		message := "Local review could not be opened."
		if phase == LocalReviewResolvingRepository {
			message = "The path is not inside a usable Git worktree."
		}
		_ = publish(LocalReviewSnapshot{
			Phase:        LocalReviewFailed,
			Repository:   repositoryState,
			Target:       target,
			CaptureID:    captureID,
			TreePage:     page,
			ChangedFiles: changes,
			Error:        &AppError{Code: CodeInternal, Operation: operation, UserMessage: message, Retryable: true, Cause: cause},
		})
	}

	if !publish(LocalReviewSnapshot{Phase: LocalReviewResolvingRepository}) {
		return
	}
	repo, worktree, err := r.source.Resolver.ResolveRepository(ctx, startPath)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled})
			return
		}
		fail(LocalReviewResolvingRepository, "resolve repository", err, nil, nil, "", TreePage{}, nil)
		return
	}
	repositoryState := &RepositoryState{Repository: repo, Worktree: &worktree}
	if err := repositoryState.Validate(); err != nil {
		fail(LocalReviewResolvingRepository, "validate repository", err, nil, nil, "", TreePage{}, nil)
		return
	}
	if !publish(LocalReviewSnapshot{Phase: LocalReviewCapturing, Repository: repositoryState}) {
		return
	}
	if r.branch != nil {
		r.runBranch(ctx, stream, publish, fail, repo, worktree, repositoryState)
		return
	}
	if r.commit != nil {
		r.runCommit(ctx, stream, publish, fail, repo, worktree, repositoryState)
		return
	}
	artifacts, err := r.source.Capture.Capture(ctx, repo, worktree)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled, Repository: repositoryState})
			return
		}
		fail(LocalReviewCapturing, "capture local change", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = artifacts.Abort(context.Background())
		}
	}()
	sessionID, err = domain.NewReviewSessionID(r.ids.NewID())
	if err != nil {
		fail(LocalReviewCapturing, "create review session", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	leaseID, err := domain.NewSessionLeaseID(r.ids.NewID())
	if err != nil {
		fail(LocalReviewCapturing, "create review lease", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	adoption, err := r.source.Store.Adopt(ctx, artifacts, CaptureSessionState{
		Guard:        CaptureSessionGuard{SessionID: sessionID, LeaseID: leaseID, WriterEpoch: 1, Revision: 1},
		RepositoryID: repo.ID,
		WorktreeID:   worktree.ID,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled, Repository: repositoryState})
			return
		}
		fail(LocalReviewCapturing, "adopt local capture", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	adopted = true
	spec, err := repository.NewLocalTargetSpec()
	if err != nil {
		fail(LocalReviewCapturing, "create local target", err, repositoryState, nil, adoption.Generation.CaptureID, TreePage{}, nil)
		return
	}
	baseKind := repository.SnapshotCommit
	if adoption.Generation.Base.Unborn {
		baseKind = repository.SnapshotTree
	}
	target, err := repository.NewResolvedTarget(repository.ResolvedTarget{
		Spec:       spec,
		Generation: adoption.Generation.Generation,
		Base:       repository.SnapshotRef{Kind: baseKind, ObjectID: adoption.Generation.Base.ObjectID},
		Head:       repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: adoption.Generation.Fingerprint},
		Editable:   true,
		EditDestination: func() *domain.WorktreeID {
			value := worktree.ID
			return &value
		}(),
		Fingerprint: adoption.Generation.Fingerprint,
		ResolvedAt:  r.clock.Now().UTC(),
	})
	if err != nil {
		fail(LocalReviewCapturing, "resolve local target", err, repositoryState, nil, adoption.Generation.CaptureID, TreePage{}, nil)
		return
	}
	var sessionHandle *SessionHandle
	if r.sessions != nil {
		handle, openErr := r.sessions.OpenSession(ctx, OpenSessionRequest{
			Repository:  repo,
			Worktree:    worktree,
			Target:      target,
			Mode:        SessionWritable,
			Persistence: r.persistence,
		})
		if openErr != nil {
			fail(LocalReviewCapturing, "open review session", openErr, repositoryState, &target, adoption.Generation.CaptureID, TreePage{}, nil)
			return
		}
		sessionHandle = &handle
		r.persistenceDegraded = r.persistenceDegraded || handle.PersistenceDegraded
		defer func() { _ = r.sessions.ReleaseSession(context.Background(), sessionHandle) }()
		if handle.Restored && handle.Session.Target.Fingerprint != target.Fingerprint {
			if err := r.sessions.RefreshTarget(ctx, sessionHandle, target); err != nil {
				fail(LocalReviewCapturing, "refresh review session target", err, repositoryState, &target, adoption.Generation.CaptureID, TreePage{}, nil)
				return
			}
		}
		target = sessionHandle.Session.Target
		sessionID = sessionHandle.Session.ID
	}
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingTree, Repository: repositoryState, Target: &target, CaptureID: adoption.Generation.CaptureID}) {
		return
	}
	query := TreeQuery{Filter: TreeFilterChanged}
	page, err := r.source.Tree.ListTree(ctx, target, query)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled, Repository: repositoryState, Target: &target, CaptureID: adoption.Generation.CaptureID})
			return
		}
		fail(LocalReviewLoadingTree, "load changed tree", err, repositoryState, &target, adoption.Generation.CaptureID, TreePage{}, nil)
		return
	}
	changes := changedFilesFromCandidate(artifacts.Candidate)
	if len(changes) == 0 {
		publish(LocalReviewSnapshot{Phase: LocalReviewClean, Repository: repositoryState, Target: &target, CaptureID: adoption.Generation.CaptureID, TreePage: page})
		return
	}
	active := firstChangedFile(changes)
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingFile, Repository: repositoryState, Target: &target, CaptureID: adoption.Generation.CaptureID, TreePage: page, ChangedFiles: changes, ActiveFile: &active}) {
		return
	}
	var diffValue repository.FileDiff
	if target.Spec.Kind == repository.TargetLocal {
		diffValue, err = r.source.Content.LoadDiff(ctx, adoption.Generation.CaptureID, active)
	} else if loader, ok := r.source.Content.(TargetContentLoader); ok {
		diffValue, err = loader.LoadTargetDiff(ctx, target, active)
	} else {
		err = errors.New("target content loader unavailable")
	}
	if err != nil {
		fail(LocalReviewLoadingFile, "load file diff", err, repositoryState, &target, adoption.Generation.CaptureID, page, changes)
		return
	}
	var content *repository.FileContent
	contentPath, contentSnapshot := active.NewPath, target.Head
	if contentPath == nil {
		contentPath, contentSnapshot = active.OldPath, target.Base
	}
	if contentPath != nil && contentSnapshot.Validate() == nil {
		loaded, loadErr := r.source.Content.LoadFile(ctx, adoption.Generation.CaptureID, contentSnapshot, *contentPath)
		if loadErr != nil {
			fail(LocalReviewLoadingFile, "load file content", loadErr, repositoryState, &target, adoption.Generation.CaptureID, page, changes)
			return
		}
		content = &loaded
	}
	displayed, displayedPage, displayedErr := displayedDiff(target, adoption.Generation.CaptureID, active, diffValue, content)
	if displayedErr != nil {
		fail(LocalReviewLoadingFile, "build displayed diff", displayedErr, repositoryState, &target, adoption.Generation.CaptureID, page, changes)
		return
	}
	var highlighted *highlight.HighlightedFile
	if content != nil && contentPath != nil {
		value, highlightErr := r.source.Highlighter.Highlight(ctx, *content, string(contentPath.Bytes()), "github")
		if highlightErr != nil {
			fail(LocalReviewLoadingFile, "highlight file", highlightErr, repositoryState, &target, adoption.Generation.CaptureID, page, changes)
			return
		}
		highlighted = &value
	}
	publish(LocalReviewSnapshot{Phase: LocalReviewReady, Repository: repositoryState, Target: &target, CaptureID: adoption.Generation.CaptureID, TreePage: page, ChangedFiles: changes, ActiveFile: &active, FileContent: content, FileDiff: &diffValue, Displayed: &displayed, DisplayedPage: &displayedPage, Highlighted: highlighted})
}

func (r *LocalReview) runBranch(ctx context.Context, _ chan<- LocalReviewSnapshot, publish func(LocalReviewSnapshot) bool, fail func(LocalReviewPhase, string, error, *RepositoryState, *repository.ResolvedTarget, domain.CaptureID, TreePage, []repository.ChangedFile), repo repository.Repository, worktree repository.WorktreeRef, repositoryState *RepositoryState) {
	target, err := OpenBranchTarget(ctx, OpenBranchTargetRequest{
		Repository:         repo,
		Worktree:           worktree,
		ExplicitExpression: r.branch.ExplicitBaseExpression,
		SessionExpression:  r.branch.SessionBaseExpression,
		Persistence:        r.persistence,
		Preferences:        r.branch.Preferences,
		Discover:           r.branch.Discover,
		Resolver:           r.branch.Resolver,
		Generation:         1,
	})
	if err != nil {
		fail(LocalReviewCapturing, "resolve branch target", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	var sessionHandle *SessionHandle
	if r.sessions != nil {
		handle, openErr := r.sessions.OpenSession(ctx, OpenSessionRequest{Repository: repo, Worktree: worktree, Target: target, Mode: SessionWritable, Persistence: r.persistence})
		if openErr != nil {
			fail(LocalReviewCapturing, "open review session", openErr, repositoryState, &target, "", TreePage{}, nil)
			return
		}
		sessionHandle = &handle
		r.persistenceDegraded = r.persistenceDegraded || handle.PersistenceDegraded
		defer func() { _ = r.sessions.ReleaseSession(context.Background(), sessionHandle) }()
		target = handle.Session.Target
	}
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingTree, Repository: repositoryState, Target: &target}) {
		return
	}
	page, err := r.source.Tree.ListTree(ctx, target, TreeQuery{Filter: TreeFilterChanged})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled, Repository: repositoryState, Target: &target})
			return
		}
		fail(LocalReviewLoadingTree, "load branch tree", err, repositoryState, &target, "", TreePage{}, nil)
		return
	}
	changes, err := r.source.Changed.ChangedFiles(ctx, target)
	if err != nil {
		fail(LocalReviewLoadingTree, "load branch changes", err, repositoryState, &target, "", page, nil)
		return
	}
	if len(changes) == 0 {
		publish(LocalReviewSnapshot{Phase: LocalReviewClean, Repository: repositoryState, Target: &target, TreePage: page})
		return
	}
	active := firstChangedFile(changes)
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingFile, Repository: repositoryState, Target: &target, TreePage: page, ChangedFiles: changes, ActiveFile: &active}) {
		return
	}
	loader, ok := r.source.Content.(TargetContentLoader)
	if !ok {
		fail(LocalReviewLoadingFile, "load branch diff", errors.New("target content loader unavailable"), repositoryState, &target, "", page, changes)
		return
	}
	diffValue, err := loader.LoadTargetDiff(ctx, target, active)
	if err != nil {
		fail(LocalReviewLoadingFile, "load branch diff", err, repositoryState, &target, "", page, changes)
		return
	}
	var content *repository.FileContent
	contentPath, contentSnapshot := active.NewPath, target.Head
	if contentPath == nil {
		contentPath, contentSnapshot = active.OldPath, target.Base
	}
	if contentPath != nil && contentSnapshot.Validate() == nil {
		loaded, loadErr := r.source.Content.LoadFile(ctx, "", contentSnapshot, *contentPath)
		if loadErr != nil {
			fail(LocalReviewLoadingFile, "load branch file", loadErr, repositoryState, &target, "", page, changes)
			return
		}
		content = &loaded
	}
	displayed, displayedPage, err := displayedDiff(target, "", active, diffValue, content)
	if err != nil {
		fail(LocalReviewLoadingFile, "build branch diff", err, repositoryState, &target, "", page, changes)
		return
	}
	var highlighted *highlight.HighlightedFile
	if content != nil && contentPath != nil {
		value, highlightErr := r.source.Highlighter.Highlight(ctx, *content, string(contentPath.Bytes()), "github")
		if highlightErr != nil {
			fail(LocalReviewLoadingFile, "highlight branch file", highlightErr, repositoryState, &target, "", page, changes)
			return
		}
		highlighted = &value
	}
	publish(LocalReviewSnapshot{Phase: LocalReviewReady, Repository: repositoryState, Target: &target, TreePage: page, ChangedFiles: changes, ActiveFile: &active, FileContent: content, FileDiff: &diffValue, Displayed: &displayed, DisplayedPage: &displayedPage, Highlighted: highlighted})
}

func (r *LocalReview) runCommit(ctx context.Context, _ chan<- LocalReviewSnapshot, publish func(LocalReviewSnapshot) bool, fail func(LocalReviewPhase, string, error, *RepositoryState, *repository.ResolvedTarget, domain.CaptureID, TreePage, []repository.ChangedFile), repo repository.Repository, worktree repository.WorktreeRef, repositoryState *RepositoryState) {
	target, err := OpenCommitTarget(ctx, OpenCommitTargetRequest{
		Repository: repo,
		Worktree:   worktree,
		Expression: r.commit.Expression,
		Resolver:   r.commit.Resolver,
		Generation: 1,
	})
	if err != nil {
		fail(LocalReviewCapturing, "resolve commit target", err, repositoryState, nil, "", TreePage{}, nil)
		return
	}
	var sessionHandle *SessionHandle
	if r.sessions != nil {
		handle, openErr := r.sessions.OpenSession(ctx, OpenSessionRequest{Repository: repo, Worktree: worktree, Target: target, Mode: SessionWritable, Persistence: r.persistence})
		if openErr != nil {
			fail(LocalReviewCapturing, "open review session", openErr, repositoryState, &target, "", TreePage{}, nil)
			return
		}
		sessionHandle = &handle
		r.persistenceDegraded = r.persistenceDegraded || handle.PersistenceDegraded
		defer func() { _ = r.sessions.ReleaseSession(context.Background(), sessionHandle) }()
		target = handle.Session.Target
	}
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingTree, Repository: repositoryState, Target: &target}) {
		return
	}
	page, err := r.source.Tree.ListTree(ctx, target, TreeQuery{Filter: TreeFilterChanged})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			publish(LocalReviewSnapshot{Phase: LocalReviewCancelled, Repository: repositoryState, Target: &target})
			return
		}
		fail(LocalReviewLoadingTree, "load commit tree", err, repositoryState, &target, "", TreePage{}, nil)
		return
	}
	changes, err := r.source.Changed.ChangedFiles(ctx, target)
	if err != nil {
		fail(LocalReviewLoadingTree, "load commit changes", err, repositoryState, &target, "", page, nil)
		return
	}
	if len(changes) == 0 {
		publish(LocalReviewSnapshot{Phase: LocalReviewClean, Repository: repositoryState, Target: &target, TreePage: page})
		return
	}
	active := firstChangedFile(changes)
	if !publish(LocalReviewSnapshot{Phase: LocalReviewLoadingFile, Repository: repositoryState, Target: &target, TreePage: page, ChangedFiles: changes, ActiveFile: &active}) {
		return
	}
	loader, ok := r.source.Content.(TargetContentLoader)
	if !ok {
		fail(LocalReviewLoadingFile, "load commit diff", errors.New("target content loader unavailable"), repositoryState, &target, "", page, changes)
		return
	}
	diffValue, err := loader.LoadTargetDiff(ctx, target, active)
	if err != nil {
		fail(LocalReviewLoadingFile, "load commit diff", err, repositoryState, &target, "", page, changes)
		return
	}
	var content *repository.FileContent
	contentPath, contentSnapshot := active.NewPath, target.Head
	if contentPath == nil {
		contentPath, contentSnapshot = active.OldPath, target.Base
	}
	if contentPath != nil && contentSnapshot.Validate() == nil {
		loaded, loadErr := r.source.Content.LoadFile(ctx, "", contentSnapshot, *contentPath)
		if loadErr != nil {
			fail(LocalReviewLoadingFile, "load commit file", loadErr, repositoryState, &target, "", page, changes)
			return
		}
		content = &loaded
	}
	displayed, displayedPage, err := displayedDiff(target, "", active, diffValue, content)
	if err != nil {
		fail(LocalReviewLoadingFile, "build commit diff", err, repositoryState, &target, "", page, changes)
		return
	}
	var highlighted *highlight.HighlightedFile
	if content != nil && contentPath != nil {
		value, highlightErr := r.source.Highlighter.Highlight(ctx, *content, string(contentPath.Bytes()), "github")
		if highlightErr != nil {
			fail(LocalReviewLoadingFile, "highlight commit file", highlightErr, repositoryState, &target, "", page, changes)
			return
		}
		highlighted = &value
	}
	publish(LocalReviewSnapshot{Phase: LocalReviewReady, Repository: repositoryState, Target: &target, TreePage: page, ChangedFiles: changes, ActiveFile: &active, FileContent: content, FileDiff: &diffValue, Displayed: &displayed, DisplayedPage: &displayedPage, Highlighted: highlighted})
}

func changedFilesFromCandidate(candidate repository.LocalCaptureCandidate) []repository.ChangedFile {
	changes := make([]repository.ChangedFile, 0, len(candidate.Entries))
	for _, entry := range candidate.Entries {
		changes = append(changes, cloneChangedFile(entry.Change))
	}
	sort.SliceStable(changes, func(i, j int) bool { return changePath(changes[i]) < changePath(changes[j]) })
	return changes
}

func firstChangedFile(changes []repository.ChangedFile) repository.ChangedFile {
	if len(changes) == 0 {
		return repository.ChangedFile{}
	}
	return cloneChangedFile(changes[0])
}

func changePath(file repository.ChangedFile) string {
	if file.NewPath != nil {
		return string(file.NewPath.Bytes())
	}
	if file.OldPath != nil {
		return string(file.OldPath.Bytes())
	}
	return ""
}

func displayedDiff(target repository.ResolvedTarget, captureID domain.CaptureID, file repository.ChangedFile, diffValue repository.FileDiff, content *repository.FileContent) (DisplayedContent, DisplayedContentPage, error) {
	encoded, err := json.Marshal(diffValue)
	if err != nil {
		return DisplayedContent{}, DisplayedContentPage{}, err
	}
	hash := sha256.Sum256(encoded)
	diffIdentity := hex.EncodeToString(hash[:])
	contentID, err := NewDisplayedContentID(DisplayedContentIdentity{TargetIdentity: target.Fingerprint, CaptureIdentity: string(captureID), Base: target.Base, Head: target.Head, DiffIdentity: diffIdentity, RowConstructionVersion: 1})
	if err != nil {
		return DisplayedContent{}, DisplayedContentPage{}, err
	}
	status := ContentReady
	reason := ""
	if file.Conflict != nil {
		status, reason = ContentUnmerged, "unmerged_index"
	} else if file.Binary {
		status, reason = ContentBinary, "binary"
	} else if content == nil {
		status, reason = ContentError, "content_unavailable"
	} else if content.Truncated {
		status, reason = ContentTooLarge, content.LimitReason
		if reason == "" {
			reason = "content_limit"
		}
	}
	displayed := DisplayedContent{ID: contentID, Mode: DisplayUnifiedDiff, Status: status, Reason: reason}
	if file.OldPath != nil {
		path := repository.RepoPath(file.OldPath.Bytes())
		displayed.BasePath = &path
	}
	if file.NewPath != nil {
		path := repository.RepoPath(file.NewPath.Bytes())
		displayed.HeadPath = &path
	}
	page := DisplayedContentPage{ContentID: contentID}
	ordinal := uint64(0)
	page.Rows = append(page.Rows, DisplayedRow{ID: CodeRowID{Content: contentID, Ordinal: ordinal}, Kind: DisplayedRowDiffHeader, Side: SideNone, Text: changePath(file)})
	for _, hunk := range diffValue.Hunks {
		ordinal++
		page.Rows = append(page.Rows, DisplayedRow{ID: CodeRowID{Content: contentID, Ordinal: ordinal}, Kind: DisplayedRowHunkHeader, Side: SideNone, HunkID: hunk.ID, Text: hunk.Header})
		for _, line := range hunk.Lines {
			ordinal++
			row := DisplayedRow{ID: CodeRowID{Content: contentID, Ordinal: ordinal}, HunkID: hunk.ID, Text: line.Text}
			switch line.Kind {
			case repository.DiffLineContext:
				row.Kind, row.Side, row.Selectable = DisplayedRowContext, SideBoth, true
				row.BaseLine, row.HeadLine = cloneInt(line.BaseLine), cloneInt(line.HeadLine)
				row.BaseText, row.HeadText = line.Text, line.Text
			case repository.DiffLineAdded:
				row.Kind, row.Side, row.Selectable = DisplayedRowAdded, SideHead, true
				row.HeadLine, row.HeadText = cloneInt(line.HeadLine), line.Text
			case repository.DiffLineDeleted:
				row.Kind, row.Side, row.Selectable = DisplayedRowDeleted, SideBase, true
				row.BaseLine, row.BaseText = cloneInt(line.BaseLine), line.Text
			case repository.DiffLineNoNewline:
				row.Kind, row.Side, row.Selectable = DisplayedRowNoNewline, SideNone, false
			}
			page.Rows = append(page.Rows, row)
		}
	}
	if status != ContentReady {
		page.Rows = []DisplayedRow{{ID: CodeRowID{Content: contentID, Ordinal: 0}, Kind: DisplayedRowPlaceholder, Side: SideNone, Text: reason, Placeholder: placeholderForStatus(status)}}
	}
	if err := page.Validate(); err != nil {
		return DisplayedContent{}, DisplayedContentPage{}, err
	}
	return displayed, page, nil
}

func placeholderForStatus(status ContentStatus) PlaceholderKind {
	switch status {
	case ContentBinary:
		return PlaceholderBinary
	case ContentUnmerged:
		return PlaceholderUnmerged
	case ContentTooLarge:
		return PlaceholderTooLarge
	default:
		return PlaceholderError
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneChangedFiles(values []repository.ChangedFile) []repository.ChangedFile {
	if len(values) == 0 {
		return nil
	}
	result := make([]repository.ChangedFile, len(values))
	for index, value := range values {
		result[index] = cloneChangedFile(value)
	}
	return result
}

func cloneChangedFile(value repository.ChangedFile) repository.ChangedFile {
	if value.OldPath != nil {
		path := repository.RepoPath(value.OldPath.Bytes())
		value.OldPath = &path
	}
	if value.NewPath != nil {
		path := repository.RepoPath(value.NewPath.Bytes())
		value.NewPath = &path
	}
	if value.OldObjectID != nil {
		objectID := *value.OldObjectID
		value.OldObjectID = &objectID
	}
	if value.NewObjectID != nil {
		objectID := *value.NewObjectID
		value.NewObjectID = &objectID
	}
	if value.Conflict != nil {
		conflict := *value.Conflict
		if value.Conflict.Stage1 != nil {
			stage := *value.Conflict.Stage1
			conflict.Stage1 = &stage
		}
		if value.Conflict.Stage2 != nil {
			stage := *value.Conflict.Stage2
			conflict.Stage2 = &stage
		}
		if value.Conflict.Stage3 != nil {
			stage := *value.Conflict.Stage3
			conflict.Stage3 = &stage
		}
		value.Conflict = &conflict
	}
	return value
}

func cloneFileContent(value repository.FileContent) repository.FileContent {
	value.Bytes = append([]byte(nil), value.Bytes...)
	value.Path = repository.RepoPath(value.Path.Bytes())
	return value
}

func cloneFileDiff(value repository.FileDiff) repository.FileDiff {
	value.File = cloneChangedFile(value.File)
	value.Hunks = append([]repository.DiffHunk(nil), value.Hunks...)
	for index := range value.Hunks {
		value.Hunks[index].Lines = append([]repository.DiffLine(nil), value.Hunks[index].Lines...)
		for lineIndex := range value.Hunks[index].Lines {
			line := &value.Hunks[index].Lines[lineIndex]
			line.BaseLine, line.HeadLine = cloneInt(line.BaseLine), cloneInt(line.HeadLine)
		}
	}
	if value.BinaryPatch != nil {
		patch := *value.BinaryPatch
		value.BinaryPatch = &patch
	}
	return value
}

func cloneHighlighted(value highlight.HighlightedFile) highlight.HighlightedFile {
	value.Lines = make([][]highlight.StyledSpan, len(value.Lines))
	for index, line := range value.Lines {
		value.Lines[index] = append([]highlight.StyledSpan(nil), line...)
	}
	return value
}
