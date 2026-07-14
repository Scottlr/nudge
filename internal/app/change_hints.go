package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidWatchedSet reports incomplete or non-absolute watch identity.
	ErrInvalidWatchedSet = errors.New("invalid watched set")
	// ErrInvalidWatchHint reports a hint without a bounded reason or identity.
	ErrInvalidWatchHint = errors.New("invalid watch hint")
	// ErrInvalidRefreshRequest reports an incomplete refresh request.
	ErrInvalidRefreshRequest = errors.New("invalid refresh request")
	// ErrInvalidRefreshScheduler reports invalid debounce timing.
	ErrInvalidRefreshScheduler = errors.New("invalid refresh scheduler")
	// ErrRefreshNotActive reports completion for a request that is not active.
	ErrRefreshNotActive = errors.New("refresh request is not active")
	// ErrRefreshAlreadyStarted reports a second coordinator start.
	ErrRefreshAlreadyStarted = errors.New("refresh coordinator already started")
	// ErrRefreshCoordinatorClosed reports an input after coordinator closure.
	ErrRefreshCoordinatorClosed = errors.New("refresh coordinator is closed")
	// ErrRefreshCoordinatorNotStarted reports an operation before coordinator start.
	ErrRefreshCoordinatorNotStarted = errors.New("refresh coordinator is not started")
	// ErrRefreshCompletionQueued reports that a completion is already waiting.
	ErrRefreshCompletionQueued = errors.New("refresh completion is already queued")
)

// WatchPathKind identifies a resolved directory root without exposing raw
// filesystem events to canonical application state.
type WatchPathKind string

const (
	WatchPathWorktreeRoot WatchPathKind = "worktree_root"
	WatchPathWorktreeGit  WatchPathKind = "worktree_git"
	WatchPathCommonGit    WatchPathKind = "common_git"
)

// WatchedPath is one resolved directory root in an immutable watched set.
type WatchedPath struct {
	Path string
	Kind WatchPathKind
}

func (p WatchedPath) validate() error {
	if p.Path == "" || !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path {
		return ErrInvalidWatchedSet
	}
	switch p.Kind {
	case WatchPathWorktreeRoot, WatchPathWorktreeGit, WatchPathCommonGit:
		return nil
	default:
		return ErrInvalidWatchedSet
	}
}

// WatchedSet binds one watcher subscription to repository/worktree identity
// and the complete resolved directory roots it covers.
type WatchedSet struct {
	RepositoryID   domain.RepositoryID
	WorktreeID     domain.WorktreeID
	WorktreeRoot   string
	WorktreeGitDir string
	CommonGitDir   string
	Paths          []WatchedPath
	WatchedSetID   string
}

// NewWatchedSet builds the v1 worktree and Git-administration watch roots.
// The roots are resolved by the repository adapter; this function does not
// touch the filesystem or execute Git.
func NewWatchedSet(repo repository.Repository, worktree repository.WorktreeRef) (WatchedSet, error) {
	if err := repo.Validate(); err != nil {
		return WatchedSet{}, fmt.Errorf("repository: %w", err)
	}
	if err := worktree.Validate(); err != nil {
		return WatchedSet{}, fmt.Errorf("worktree: %w", err)
	}
	if worktree.RepositoryID != repo.ID {
		return WatchedSet{}, ErrInvalidWatchedSet
	}
	set := WatchedSet{
		RepositoryID:   repo.ID,
		WorktreeID:     worktree.ID,
		WorktreeRoot:   worktree.RootPath,
		WorktreeGitDir: worktree.GitDir,
		CommonGitDir:   repo.CommonGitDir,
		Paths: []WatchedPath{
			{Path: worktree.RootPath, Kind: WatchPathWorktreeRoot},
			{Path: worktree.GitDir, Kind: WatchPathWorktreeGit},
			{Path: repo.CommonGitDir, Kind: WatchPathCommonGit},
		},
	}
	if err := set.finalize(); err != nil {
		return WatchedSet{}, err
	}
	return set, nil
}

func (s *WatchedSet) finalize() error {
	if s == nil || s.RepositoryID == "" || s.WorktreeID == "" || s.WorktreeRoot == "" || s.WorktreeGitDir == "" || s.CommonGitDir == "" {
		return ErrInvalidWatchedSet
	}
	for _, value := range []string{s.WorktreeRoot, s.WorktreeGitDir, s.CommonGitDir} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return ErrInvalidWatchedSet
		}
	}
	if len(s.Paths) == 0 {
		return ErrInvalidWatchedSet
	}
	s.Paths = append([]WatchedPath(nil), s.Paths...)
	sort.Slice(s.Paths, func(i, j int) bool {
		if s.Paths[i].Path == s.Paths[j].Path {
			return s.Paths[i].Kind < s.Paths[j].Kind
		}
		return s.Paths[i].Path < s.Paths[j].Path
	})
	seen := make(map[string]struct{}, len(s.Paths))
	for _, value := range s.Paths {
		if err := value.validate(); err != nil {
			return err
		}
		if _, exists := seen[value.Path]; exists {
			return ErrInvalidWatchedSet
		}
		seen[value.Path] = struct{}{}
	}
	s.WatchedSetID = watchedSetIdentity(*s)
	return nil
}

// Validate checks the immutable identity and path set.
func (s WatchedSet) Validate() error {
	copyValue := s
	if err := copyValue.finalize(); err != nil {
		return err
	}
	if s.WatchedSetID != copyValue.WatchedSetID {
		return ErrInvalidWatchedSet
	}
	return nil
}

// Clone returns a detached watched-set value for adapter ownership.
func (s WatchedSet) Clone() WatchedSet {
	s.Paths = append([]WatchedPath(nil), s.Paths...)
	return s
}

func watchedSetIdentity(s WatchedSet) string {
	h := sha256.New()
	writeIdentityPart := func(value string) {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	writeIdentityPart(string(s.RepositoryID))
	writeIdentityPart(string(s.WorktreeID))
	writeIdentityPart(s.WorktreeRoot)
	writeIdentityPart(s.WorktreeGitDir)
	writeIdentityPart(s.CommonGitDir)
	for _, path := range s.Paths {
		writeIdentityPart(path.Path)
		writeIdentityPart(string(path.Kind))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileWatcher is the application-owned port for lossy platform hints.
// Implementations never perform Git reconciliation or mutate application
// projections.
type FileWatcher interface {
	Start(context.Context, WatchedSet) error
	Hints() <-chan WatchHint
	Replace(context.Context, WatchedSet) error
	Close() error
}

// RefreshReason is a bounded explanation for one authoritative refresh hint.
type RefreshReason string

const (
	RefreshReasonFilesystemChange    RefreshReason = "filesystem_change"
	RefreshReasonExplicit            RefreshReason = "explicit_refresh"
	RefreshReasonAppResume           RefreshReason = "app_resume"
	RefreshReasonProviderTurn        RefreshReason = "provider_turn_completed"
	RefreshReasonProposalReset       RefreshReason = "proposal_reset"
	RefreshReasonProposalApplied     RefreshReason = "proposal_applied"
	RefreshReasonProposalRejected    RefreshReason = "proposal_rejected"
	RefreshReasonHeadChanged         RefreshReason = "head_or_ref_changed"
	RefreshReasonMaximumAge          RefreshReason = "maximum_age"
	RefreshReasonWatcherOverflow     RefreshReason = "watcher_overflow"
	RefreshReasonWatcherError        RefreshReason = "watcher_error"
	RefreshReasonWatcherClosed       RefreshReason = "watcher_closed"
	RefreshReasonWatchedRootReplaced RefreshReason = "watched_root_replaced"
	RefreshReasonWatchedSetChanged   RefreshReason = "watched_set_changed"
	RefreshReasonRescanRequired      RefreshReason = "rescan_required"
)

var refreshReasonOrder = [...]RefreshReason{
	RefreshReasonFilesystemChange,
	RefreshReasonExplicit,
	RefreshReasonAppResume,
	RefreshReasonProviderTurn,
	RefreshReasonProposalReset,
	RefreshReasonProposalApplied,
	RefreshReasonProposalRejected,
	RefreshReasonHeadChanged,
	RefreshReasonMaximumAge,
	RefreshReasonWatcherOverflow,
	RefreshReasonWatcherError,
	RefreshReasonWatcherClosed,
	RefreshReasonWatchedRootReplaced,
	RefreshReasonWatchedSetChanged,
	RefreshReasonRescanRequired,
}

func refreshReasonBit(reason RefreshReason) uint32 {
	for index, value := range refreshReasonOrder {
		if value == reason {
			return uint32(1) << index
		}
	}
	return 0
}

func validRefreshReason(reason RefreshReason) bool { return refreshReasonBit(reason) != 0 }

// WatchHint is a bounded, path-free filesystem or lifecycle hint.
type WatchHint struct {
	WatchedSet WatchedSet
	Reason     RefreshReason
	TruthLost  bool
}

// Validate checks that a hint is tied to one immutable watched-set identity.
func (h WatchHint) Validate() error {
	if err := h.WatchedSet.Validate(); err != nil || !validRefreshReason(h.Reason) {
		return ErrInvalidWatchHint
	}
	return nil
}

// RefreshRequest is the only output of the debounce boundary. It asks a
// later application operation to obtain authoritative Git state.
type RefreshRequest struct {
	WatchedSet  WatchedSet
	Reasons     []RefreshReason
	TruthLost   bool
	RequestedAt time.Time
	Sequence    uint64
}

// Validate checks reason bounds and immutable watched-set identity.
func (r RefreshRequest) Validate() error {
	if err := r.WatchedSet.Validate(); err != nil || r.RequestedAt.IsZero() || r.Sequence == 0 || len(r.Reasons) == 0 {
		return ErrInvalidRefreshRequest
	}
	previous := RefreshReason("")
	for _, reason := range r.Reasons {
		if !validRefreshReason(reason) || reason == previous {
			return ErrInvalidRefreshRequest
		}
		if previous != "" && refreshReasonBit(previous) > refreshReasonBit(reason) {
			return ErrInvalidRefreshRequest
		}
		previous = reason
	}
	return nil
}

// RefreshTicket identifies one emitted request for completion.
type RefreshTicket uint64

// RefreshSchedulerConfig controls quiet, maximum-delay, and focused-session
// refresh timing. FocusedMaximumAge is never allowed above the v1 30-second
// bound.
type RefreshSchedulerConfig struct {
	QuietDelay        time.Duration
	MaximumDelay      time.Duration
	FocusedMaximumAge time.Duration
}

// DefaultRefreshSchedulerConfig returns the bounded v1 timing defaults.
func DefaultRefreshSchedulerConfig() RefreshSchedulerConfig {
	return RefreshSchedulerConfig{QuietDelay: 200 * time.Millisecond, MaximumDelay: 2 * time.Second, FocusedMaximumAge: 30 * time.Second}
}

func (c RefreshSchedulerConfig) validate() error {
	if c.QuietDelay <= 0 || c.MaximumDelay < c.QuietDelay || c.FocusedMaximumAge <= 0 || c.FocusedMaximumAge > 30*time.Second {
		return ErrInvalidRefreshScheduler
	}
	return nil
}

// RefreshScheduler is a deterministic, single-owner debounce state machine.
// It has one active request and at most one coalesced follow-up.
type RefreshScheduler struct {
	config  RefreshSchedulerConfig
	set     WatchedSet
	haveSet bool

	focused      bool
	localSession bool

	pendingBits      uint32
	pendingTruthLost bool
	firstHintAt      time.Time
	lastHintAt       time.Time
	lastRefreshAt    time.Time

	active       bool
	activeTicket RefreshTicket
	sequence     uint64
}

// NewRefreshScheduler constructs the deterministic debounce state machine.
func NewRefreshScheduler(config RefreshSchedulerConfig) (*RefreshScheduler, error) {
	if config == (RefreshSchedulerConfig{}) {
		config = DefaultRefreshSchedulerConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &RefreshScheduler{config: config}, nil
}

// SetWatchedSet installs the authoritative resolved watch identity. A change
// is itself a truth-loss hint so coverage cannot silently narrow.
func (s *RefreshScheduler) SetWatchedSet(now time.Time, set WatchedSet) error {
	if s == nil || now.IsZero() {
		return ErrInvalidWatchedSet
	}
	if err := set.Validate(); err != nil {
		return err
	}
	if !s.haveSet {
		s.set = set.Clone()
		s.haveSet = true
		s.lastRefreshAt = now
		return nil
	}
	if s.set.WatchedSetID == set.WatchedSetID {
		return nil
	}
	s.set = set.Clone()
	s.add(now, RefreshReasonWatchedSetChanged, true)
	return nil
}

// SetFocused pauses or resumes focused-session maximum-age scheduling.
func (s *RefreshScheduler) SetFocused(now time.Time, focused, localSession bool) error {
	if s == nil || now.IsZero() || !s.haveSet {
		return ErrInvalidWatchedSet
	}
	s.focused = focused
	s.localSession = localSession
	if !focused || !localSession {
		return nil
	}
	if s.lastRefreshAt.IsZero() {
		s.lastRefreshAt = now
	}
	return nil
}

// SubmitHint merges one watcher or lifecycle hint into the pending request.
func (s *RefreshScheduler) SubmitHint(now time.Time, hint WatchHint) error {
	if s == nil || now.IsZero() || hint.Validate() != nil {
		return ErrInvalidWatchHint
	}
	if !s.haveSet {
		if err := s.SetWatchedSet(now, hint.WatchedSet); err != nil {
			return err
		}
	} else if s.set.WatchedSetID != hint.WatchedSet.WatchedSetID {
		// A stale adapter event cannot identify the current repository. Preserve
		// truth-loss against the current set instead of changing identity from a
		// raw event.
		s.add(now, RefreshReasonWatchedSetChanged, true)
		return nil
	}
	s.add(now, hint.Reason, hint.TruthLost)
	return nil
}

func (s *RefreshScheduler) add(now time.Time, reason RefreshReason, truthLost bool) {
	bit := refreshReasonBit(reason)
	if bit == 0 {
		return
	}
	if s.pendingBits == 0 {
		s.firstHintAt = now
	}
	s.lastHintAt = now
	s.pendingBits |= bit
	s.pendingTruthLost = s.pendingTruthLost || truthLost
}

// NextDue returns the next wall-clock instant at which a request may be
// emitted. A zero time means no request is due or the scheduler is active.
func (s *RefreshScheduler) NextDue(now time.Time) time.Time {
	if s == nil || !s.haveSet || s.active || now.IsZero() {
		return time.Time{}
	}
	var due time.Time
	if s.pendingBits != 0 {
		due = s.lastHintAt.Add(s.config.QuietDelay)
		maximum := s.firstHintAt.Add(s.config.MaximumDelay)
		if maximum.Before(due) {
			due = maximum
		}
	}
	if s.focused && s.localSession {
		ageDue := s.lastRefreshAt.Add(s.config.FocusedMaximumAge)
		if due.IsZero() || ageDue.Before(due) {
			due = ageDue
		}
	}
	return due
}

// Due emits one request when quiet, maximum-delay, or focused maximum-age
// semantics allow it.
func (s *RefreshScheduler) Due(now time.Time) (RefreshRequest, RefreshTicket, bool) {
	if s == nil || !s.haveSet || s.active || now.IsZero() {
		return RefreshRequest{}, 0, false
	}
	ageDue := s.focused && s.localSession && !now.Before(s.lastRefreshAt.Add(s.config.FocusedMaximumAge))
	if ageDue {
		s.pendingBits |= refreshReasonBit(RefreshReasonMaximumAge)
		if s.pendingBits == refreshReasonBit(RefreshReasonMaximumAge) {
			s.firstHintAt = now
		}
		s.lastHintAt = now
	}
	if s.pendingBits == 0 {
		return RefreshRequest{}, 0, false
	}
	quietDue := !now.Before(s.lastHintAt.Add(s.config.QuietDelay))
	maximumDue := !now.Before(s.firstHintAt.Add(s.config.MaximumDelay))
	if !ageDue && !quietDue && !maximumDue {
		return RefreshRequest{}, 0, false
	}
	s.sequence++
	s.activeTicket = RefreshTicket(s.sequence)
	s.active = true
	s.lastRefreshAt = now
	request := RefreshRequest{
		WatchedSet:  s.set.Clone(),
		Reasons:     reasonsFromBits(s.pendingBits),
		TruthLost:   s.pendingTruthLost,
		RequestedAt: now,
		Sequence:    s.sequence,
	}
	s.pendingBits = 0
	s.pendingTruthLost = false
	s.firstHintAt = time.Time{}
	s.lastHintAt = time.Time{}
	return request, s.activeTicket, true
}

func reasonsFromBits(bits uint32) []RefreshReason {
	result := make([]RefreshReason, 0, len(refreshReasonOrder))
	for index, reason := range refreshReasonOrder {
		if bits&(uint32(1)<<index) != 0 {
			result = append(result, reason)
		}
	}
	return result
}

// Complete marks the active request committed and releases the scheduler for
// one coalesced follow-up, if any.
func (s *RefreshScheduler) Complete(now time.Time, ticket RefreshTicket) error {
	if s == nil || now.IsZero() || !s.active || ticket == 0 || ticket != s.activeTicket {
		return ErrRefreshNotActive
	}
	s.active = false
	s.activeTicket = 0
	return nil
}

// Active reports whether a request is awaiting authoritative completion.
func (s *RefreshScheduler) Active() bool { return s != nil && s.active }

// WatchedSet returns the current detached identity.
func (s *RefreshScheduler) WatchedSet() (WatchedSet, bool) {
	if s == nil || !s.haveSet {
		return WatchedSet{}, false
	}
	return s.set.Clone(), true
}

type coordinatorInput struct {
	focusSet     bool
	focused      bool
	localSession bool
}

// RefreshCoordinator combines one FileWatcher with the deterministic
// scheduler and emits a bounded request stream. It does not execute Git.
type RefreshCoordinator struct {
	watcher   FileWatcher
	scheduler *RefreshScheduler
	clock     Clock

	mu             sync.Mutex
	started        bool
	closed         bool
	cancel         context.CancelFunc
	done           chan struct{}
	wake           chan struct{}
	input          coordinatorInput
	inputSet       *WatchedSet
	inputHint      *WatchHint
	inputBits      uint32
	inputTruthLost bool
	complete       chan RefreshTicket
	requests       chan RefreshRequest
}

// RefreshCoordinatorConfig supplies the watcher and debounce policy.
type RefreshCoordinatorConfig struct {
	Watcher   FileWatcher
	Clock     Clock
	Scheduler RefreshSchedulerConfig
}

// NewRefreshCoordinator validates an application hint coordinator.
func NewRefreshCoordinator(config RefreshCoordinatorConfig) (*RefreshCoordinator, error) {
	if config.Watcher == nil {
		return nil, ErrInvalidWatchHint
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	scheduler, err := NewRefreshScheduler(config.Scheduler)
	if err != nil {
		return nil, err
	}
	return &RefreshCoordinator{
		watcher: config.Watcher, scheduler: scheduler, clock: config.Clock,
		wake: make(chan struct{}, 1), complete: make(chan RefreshTicket, 1), requests: make(chan RefreshRequest, 1),
	}, nil
}

// Start installs the initial immutable watch identity and begins scheduling.
func (c *RefreshCoordinator) Start(ctx context.Context, set WatchedSet) error {
	if c == nil || ctx == nil {
		return ErrRefreshCoordinatorNotStarted
	}
	if err := set.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrRefreshCoordinatorClosed
	}
	if c.started {
		c.mu.Unlock()
		return ErrRefreshAlreadyStarted
	}
	if err := c.watcher.Start(ctx, set); err != nil {
		c.mu.Unlock()
		return err
	}
	if err := c.scheduler.SetWatchedSet(c.clock.Now(), set); err != nil {
		_ = c.watcher.Close()
		c.mu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})
	c.started = true
	c.mu.Unlock()
	go c.run(runCtx, c.watcher.Hints())
	return nil
}

// Requests returns the capacity-one request stream.
func (c *RefreshCoordinator) Requests() <-chan RefreshRequest { return c.requests }

func (c *RefreshCoordinator) enqueue(reason RefreshReason, truthLost bool) error {
	if !validRefreshReason(reason) {
		return ErrInvalidWatchHint
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrRefreshCoordinatorNotStarted
	}
	if c.closed {
		return ErrRefreshCoordinatorClosed
	}
	c.inputBits |= refreshReasonBit(reason)
	c.inputTruthLost = c.inputTruthLost || truthLost
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

// Hint submits one path-free watcher hint for coalescing.
func (c *RefreshCoordinator) Hint(hint WatchHint) error {
	if err := hint.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrRefreshCoordinatorNotStarted
	}
	if c.closed {
		return ErrRefreshCoordinatorClosed
	}
	if c.inputHint == nil {
		value := hint
		value.WatchedSet = hint.WatchedSet.Clone()
		c.inputHint = &value
	} else if c.inputHint.WatchedSet.WatchedSetID != hint.WatchedSet.WatchedSetID {
		value := hint
		value.WatchedSet = hint.WatchedSet.Clone()
		c.inputHint = &value
		c.inputBits |= refreshReasonBit(RefreshReasonWatchedSetChanged)
		c.inputTruthLost = true
	}
	c.inputBits |= refreshReasonBit(hint.Reason)
	c.inputTruthLost = c.inputTruthLost || hint.TruthLost
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

// ExplicitRefresh requests an authoritative refresh through the common path.
func (c *RefreshCoordinator) ExplicitRefresh() error { return c.enqueue(RefreshReasonExplicit, false) }

// AppResumed records the lifecycle trigger and resumes focused scheduling.
func (c *RefreshCoordinator) AppResumed() error { return c.enqueue(RefreshReasonAppResume, false) }

// ProviderTurnCompleted records only settled provider lifecycle completion.
// Token/file notifications intentionally have no coordinator entrypoint.
func (c *RefreshCoordinator) ProviderTurnCompleted() error {
	return c.enqueue(RefreshReasonProviderTurn, false)
}

// ProposalReset records a settled proposal lifecycle outcome.
func (c *RefreshCoordinator) ProposalReset() error {
	return c.enqueue(RefreshReasonProposalReset, false)
}

// ProposalApplied records a settled proposal lifecycle outcome.
func (c *RefreshCoordinator) ProposalApplied() error {
	return c.enqueue(RefreshReasonProposalApplied, false)
}

// ProposalRejected records a settled proposal lifecycle outcome.
func (c *RefreshCoordinator) ProposalRejected() error {
	return c.enqueue(RefreshReasonProposalRejected, false)
}

// SetFocused changes focused local-session maximum-age scheduling.
func (c *RefreshCoordinator) SetFocused(focused, localSession bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrRefreshCoordinatorNotStarted
	}
	if c.closed {
		return ErrRefreshCoordinatorClosed
	}
	c.input.focusSet = true
	c.input.focused = focused
	c.input.localSession = localSession
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

// ReplaceWatchedSet replaces adapter coverage after authoritative resolution.
func (c *RefreshCoordinator) ReplaceWatchedSet(ctx context.Context, set WatchedSet) error {
	if c == nil || ctx == nil {
		return ErrRefreshCoordinatorNotStarted
	}
	if err := set.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return ErrRefreshCoordinatorNotStarted
	}
	if c.closed {
		c.mu.Unlock()
		return ErrRefreshCoordinatorClosed
	}
	c.mu.Unlock()
	err := c.watcher.Replace(ctx, set)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrRefreshCoordinatorClosed
	}
	c.inputBits |= refreshReasonBit(RefreshReasonWatchedSetChanged)
	value := set.Clone()
	c.inputSet = &value
	c.inputTruthLost = true
	select {
	case c.wake <- struct{}{}:
	default:
	}
	c.mu.Unlock()
	return err
}

// Complete releases one emitted request for its coalesced follow-up.
func (c *RefreshCoordinator) Complete(ticket RefreshTicket) error {
	if ticket == 0 {
		return ErrRefreshNotActive
	}
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return ErrRefreshCoordinatorNotStarted
	}
	if c.closed {
		c.mu.Unlock()
		return ErrRefreshCoordinatorClosed
	}
	select {
	case c.complete <- ticket:
		c.mu.Unlock()
		select {
		case c.wake <- struct{}{}:
		default:
		}
		return nil
	default:
		c.mu.Unlock()
		return ErrRefreshCompletionQueued
	}
}

func (c *RefreshCoordinator) drainInput(now time.Time) {
	c.mu.Lock()
	bits := c.inputBits
	truthLost := c.inputTruthLost
	input := c.input
	inputSet := c.inputSet
	hint := c.inputHint
	c.inputBits = 0
	c.inputTruthLost = false
	c.input = coordinatorInput{}
	c.inputSet = nil
	c.inputHint = nil
	c.mu.Unlock()
	if input.focusSet {
		_ = c.scheduler.SetFocused(now, input.focused, input.localSession)
	}
	watchedSet := mustWatchedSet(c.scheduler)
	if inputSet != nil {
		_ = c.scheduler.SetWatchedSet(now, *inputSet)
		watchedSet = inputSet.Clone()
	} else if hint != nil {
		watchedSet = hint.WatchedSet
	}
	for _, reason := range reasonsFromBits(bits) {
		hintValue := WatchHint{WatchedSet: watchedSet, Reason: reason, TruthLost: truthLost}
		if hint != nil && reason == hint.Reason {
			hintValue.TruthLost = hint.TruthLost || truthLost
		}
		_ = c.scheduler.SubmitHint(now, hintValue)
	}
}

func mustWatchedSet(s *RefreshScheduler) WatchedSet {
	set, _ := s.WatchedSet()
	return set
}

func (c *RefreshCoordinator) emitDue(now time.Time, ctx context.Context) time.Duration {
	request, _, due := c.scheduler.Due(now)
	if due {
		select {
		case c.requests <- request:
		case <-ctx.Done():
			return 0
		}
	}
	next := c.scheduler.NextDue(c.clock.Now())
	if next.IsZero() {
		return 24 * time.Hour
	}
	delay := next.Sub(c.clock.Now())
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

func (c *RefreshCoordinator) run(ctx context.Context, hints <-chan WatchHint) {
	defer close(c.done)
	defer close(c.requests)
	timer := time.NewTimer(24 * time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	resetTimer := func(delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case hint, ok := <-hints:
			if !ok {
				_ = c.scheduler.SubmitHint(c.clock.Now(), WatchHint{WatchedSet: mustWatchedSet(c.scheduler), Reason: RefreshReasonWatcherClosed, TruthLost: true})
				delay := c.emitDue(c.clock.Now(), ctx)
				resetTimer(delay)
				hints = nil
				continue
			}
			_ = c.scheduler.SubmitHint(c.clock.Now(), hint)
			resetTimer(c.emitDue(c.clock.Now(), ctx))
		case <-c.wake:
			c.drainInput(c.clock.Now())
			resetTimer(c.emitDue(c.clock.Now(), ctx))
		case ticket := <-c.complete:
			_ = c.scheduler.Complete(c.clock.Now(), ticket)
			resetTimer(c.emitDue(c.clock.Now(), ctx))
		case <-timer.C:
			resetTimer(c.emitDue(c.clock.Now(), ctx))
		}
	}
}

// Close stops watcher, timer, and coordinator goroutines deterministically.
func (c *RefreshCoordinator) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if !c.started {
		if c.closed {
			c.mu.Unlock()
			return nil
		}
		c.closed = true
		close(c.requests)
		c.mu.Unlock()
		return nil
	}
	if c.closed {
		done := c.done
		c.mu.Unlock()
		<-done
		return nil
	}
	c.closed = true
	c.cancel()
	done := c.done
	c.mu.Unlock()
	_ = c.watcher.Close()
	<-done
	return nil
}
