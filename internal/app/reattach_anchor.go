package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrAnchorReattachmentUnavailable reports an application composition that
	// has no persistence-backed manual reattachment service.
	ErrAnchorReattachmentUnavailable = errors.New("anchor reattachment unavailable")
	// ErrAnchorReattachmentNotEligible reports a thread that is not currently
	// ambiguous or orphaned.
	ErrAnchorReattachmentNotEligible = errors.New("anchor is not eligible for manual reattachment")
	// ErrAnchorCandidateStale reports a candidate from an older target
	// generation or a changed candidate identity.
	ErrAnchorCandidateStale = errors.New("anchor candidate is stale")
	// ErrAnchorCandidateUnavailable reports a candidate that is not part of the
	// bounded, current-generation evidence set.
	ErrAnchorCandidateUnavailable = errors.New("anchor candidate is unavailable")
)

const (
	// MaxAnchorReattachmentActorBytes bounds the safe actor label persisted
	// alongside manual history evidence.
	MaxAnchorReattachmentActorBytes = 128
	manualAnchorReattachmentReason  = "manual_reattach"
)

// AnchorReattachmentInput is the immutable capture-owned input used to build
// the candidates shown by the manual reattachment surface.
type AnchorReattachmentInput struct {
	ThreadID          domain.ReviewThreadID
	Anchor            review.CodeAnchor
	CurrentGeneration repository.TargetGeneration
	Content           review.CapturedFile
	PreviousContent   *review.CapturedFile
	RenameMappings    []review.RenameMapping
	RenameEvidence    review.RenamePolicyEvidence
}

func (i AnchorReattachmentInput) Validate() error {
	if i.ThreadID == "" || i.Anchor.Validate() != nil || i.CurrentGeneration == 0 || i.Anchor.TargetGeneration != i.CurrentGeneration || i.Content.Validate() != nil || i.Content.Side != i.Anchor.Side {
		return ErrReviewStoreInput
	}
	if i.PreviousContent != nil && (i.PreviousContent.Validate() != nil || i.PreviousContent.Side != i.Anchor.Side) {
		return ErrReviewStoreInput
	}
	for _, mapping := range i.RenameMappings {
		if mapping.Validate() != nil {
			return ErrReviewStoreInput
		}
	}
	if len(i.RenameMappings) > 0 && !i.RenameEvidence.Complete() {
		return ErrReviewStoreInput
	}
	return nil
}

// AnchorReattachmentSet is the bounded immutable projection consumed by the
// TUI. Candidates are ranked in the same first-successful-tier order as T023.
type AnchorReattachmentSet struct {
	ThreadID          domain.ReviewThreadID
	CurrentGeneration repository.TargetGeneration
	Original          review.CodeAnchor
	State             review.AnchorState
	Reason            string
	Candidates        []review.AnchorCandidate
	CandidateOverflow bool
}

func (s AnchorReattachmentSet) Validate() error {
	if s.ThreadID == "" || s.CurrentGeneration == 0 || s.Original.Validate() != nil || s.Original.TargetGeneration != s.CurrentGeneration || (s.State != review.AnchorAmbiguous && s.State != review.AnchorOrphaned) || s.Reason == "" || len(s.Candidates) > review.MaxAnchorReconciliationCandidates {
		return ErrReviewStoreInput
	}
	for _, candidate := range s.Candidates {
		if candidate.Validate() != nil || candidate.Generation != s.CurrentGeneration || candidate.Side != s.Original.Side {
			return ErrReviewStoreInput
		}
	}
	return nil
}

// GenerateAnchorReattachmentCandidates produces the deterministic bounded
// candidate set for one current authoritative capture. It never chooses a
// destination and never reads a live path.
func GenerateAnchorReattachmentCandidates(input AnchorReattachmentInput) (AnchorReattachmentSet, error) {
	if err := input.Validate(); err != nil {
		return AnchorReattachmentSet{}, err
	}
	if !manualPathAllowed(input) {
		return AnchorReattachmentSet{
			ThreadID:          input.ThreadID,
			CurrentGeneration: input.CurrentGeneration,
			Original:          input.Anchor,
			State:             review.AnchorOrphaned,
			Reason:            "scoped_path_unavailable",
		}, nil
	}
	count := input.Anchor.EndLine - input.Anchor.StartLine + 1
	if count <= 0 || count > len(input.Content.Lines) {
		return AnchorReattachmentSet{ThreadID: input.ThreadID, CurrentGeneration: input.CurrentGeneration, Original: input.Anchor, State: review.AnchorOrphaned, Reason: "selected_range_not_present"}, nil
	}
	synthetic := review.ReconcileInput{Anchor: input.Anchor, Transition: review.GenerationTransition{ToGeneration: input.CurrentGeneration}, NewContent: input.Content, PreviousContent: input.PreviousContent}
	if candidates, found, overflow := firstCandidates(synthetic, input.Content, count, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && start == input.Anchor.StartLine && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, input.Content.Lines, start, start+count-1)
	}, review.EvidenceTierExactContextAtLine, "selected_and_context_at_original_line"); found {
		return reattachmentSet(input, candidates, overflow, "selected_and_context_at_original_line"), nil
	}
	if candidates, found, overflow := firstCandidates(synthetic, input.Content, count, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && withinAnchorWindow(input.Anchor.StartLine, start) && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, input.Content.Lines, start, start+count-1)
	}, review.EvidenceTierContextWindow, "selected_and_context_within_window"); found {
		return reattachmentSet(input, candidates, overflow, "selected_and_context_within_window"), nil
	}
	if candidates, found, overflow := firstCandidates(synthetic, input.Content, count, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, input.Content.Lines, start, start+count-1)
	}, review.EvidenceTierContextFile, "selected_and_context_in_file"); found {
		return reattachmentSet(input, candidates, overflow, "selected_and_context_in_file"), nil
	}
	if candidates, found, overflow := firstCandidates(synthetic, input.Content, count, func(start int, selected string) bool {
		return withinAnchorWindow(input.Anchor.StartLine, start) && selectionMatches(input.Anchor, selected)
	}, review.EvidenceTierSelectionWindow, "unique_selected_range_within_window"); found {
		return reattachmentSet(input, candidates, overflow, "unique_selected_range_within_window"), nil
	}
	if candidates, found, overflow := firstCandidates(synthetic, input.Content, count, func(_ int, selected string) bool {
		return selectionMatches(input.Anchor, selected)
	}, review.EvidenceTierSelectionFile, "unique_selected_range_in_file"); found {
		return reattachmentSet(input, candidates, overflow, "unique_selected_range_in_file"), nil
	}
	if candidates, found := lineDiffCandidate(synthetic, input.Content, string(input.Content.Path), count); found {
		return reattachmentSet(input, candidates, false, "versioned_line_diff"), nil
	}
	return AnchorReattachmentSet{ThreadID: input.ThreadID, CurrentGeneration: input.CurrentGeneration, Original: input.Anchor, State: review.AnchorOrphaned, Reason: "anchor_evidence_not_found"}, nil
}

func reattachmentSet(input AnchorReattachmentInput, candidates []review.AnchorCandidate, overflow bool, reason string) AnchorReattachmentSet {
	state := review.AnchorOrphaned
	if len(candidates) > 0 {
		state = review.AnchorAmbiguous
	}
	return AnchorReattachmentSet{ThreadID: input.ThreadID, CurrentGeneration: input.CurrentGeneration, Original: input.Anchor, State: state, Reason: reason, Candidates: candidates, CandidateOverflow: overflow}
}

func manualPathAllowed(input AnchorReattachmentInput) bool {
	if string(input.Content.Path) == string(input.Anchor.Path) {
		return true
	}
	if len(input.RenameMappings) != 1 || !input.RenameEvidence.Complete() {
		return false
	}
	mapping := input.RenameMappings[0]
	return string(mapping.OldPath) == string(input.Anchor.Path) && string(mapping.NewPath) == string(input.Content.Path) && mapping.Side == input.Anchor.Side
}

// AnchorVersionRecord is the durable history projection for one immutable
// anchor row. Legacy direct-anchor rows are returned with Method=legacy.
type AnchorVersionRecord struct {
	ThreadID        domain.ReviewThreadID
	Version         uint64
	Anchor          review.CodeAnchor
	Method          review.AnchorVersionMethod
	PreviousVersion uint64
	Candidate       *review.AnchorCandidate
	Actor           string
	CreatedAt       time.Time
}

func (r AnchorVersionRecord) Validate() error {
	if r.ThreadID == "" || r.Version == 0 || r.Anchor.Validate() != nil || r.Method.Validate() != nil || r.CreatedAt.IsZero() || r.PreviousVersion >= r.Version && r.PreviousVersion != 0 || r.Method == review.AnchorVersionMethodManual && (r.PreviousVersion == 0 || r.Candidate == nil || r.Actor == "") {
		return ErrReviewStoreInput
	}
	if r.Candidate != nil && (r.Candidate.Validate() != nil || r.Candidate.Generation != r.Anchor.TargetGeneration || review.AnchorCandidateFingerprint(*r.Candidate) == "") {
		return ErrReviewStoreInput
	}
	if r.Actor != "" && !validAnchorReattachmentActor(r.Actor) {
		return ErrReviewStoreInput
	}
	return nil
}

// AnchorVersionWrite is the application-to-store append request. The store
// resolves the next version and checks the expected previous anchor while it
// holds the session writer transaction.
type AnchorVersionWrite struct {
	ThreadID             domain.ReviewThreadID
	CurrentGeneration    repository.TargetGeneration
	PreviousAnchor       review.CodeAnchor
	Anchor               review.CodeAnchor
	Method               review.AnchorVersionMethod
	Candidate            review.AnchorCandidate
	CandidateFingerprint string
	Actor                string
	CreatedAt            time.Time
}

func (w AnchorVersionWrite) Validate() error {
	if w.ThreadID == "" || w.CurrentGeneration == 0 || w.PreviousAnchor.Validate() != nil || w.Anchor.Validate() != nil || w.Anchor.TargetGeneration != w.CurrentGeneration || w.Method != review.AnchorVersionMethodManual || w.Candidate.Validate() != nil || w.Candidate.Generation != w.CurrentGeneration || w.CandidateFingerprint != review.AnchorCandidateFingerprint(w.Candidate) || w.CreatedAt.IsZero() || !validAnchorReattachmentActor(w.Actor) {
		return ErrReviewStoreInput
	}
	return nil
}

// ReattachAnchor is the explicit confirmation command emitted by the TUI.
type ReattachAnchor struct {
	Guard                SessionWriteGuard
	ThreadID             domain.ReviewThreadID
	CurrentGeneration    repository.TargetGeneration
	Candidate            review.AnchorCandidate
	CandidateFingerprint string
	Actor                string
	CorrelationID        CorrelationID
}

func (ReattachAnchor) isReducerInput() {}
func (ReattachAnchor) isCommand()      {}

// AnchorReattachmentCommit is returned only after the new version has been
// committed. The thread and all non-anchor status axes are preserved.
type AnchorReattachmentCommit struct {
	Guard   SessionWriteGuard
	Thread  review.ReviewThread
	Version AnchorVersionRecord
	Events  []Event
}

// AnchorReattachmentServiceConfig composes manual reattachment with the
// fenced review store and deterministic clock.
type AnchorReattachmentServiceConfig struct {
	Store ReviewStore
	Clock Clock
}

// AnchorReattachmentService owns persistence-first manual anchor changes.
type AnchorReattachmentService struct {
	store ReviewStore
	clock Clock
}

func NewAnchorReattachmentService(config AnchorReattachmentServiceConfig) (*AnchorReattachmentService, error) {
	if config.Store == nil {
		return nil, ErrAnchorReattachmentUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &AnchorReattachmentService{store: config.Store, clock: config.Clock}, nil
}

// ReattachAnchor validates the current thread and appends one manual anchor
// version without changing the thread identity, conversation, messages, or
// resolution/proposal/read axes.
func (s *AnchorReattachmentService) ReattachAnchor(ctx context.Context, command ReattachAnchor) (AnchorReattachmentCommit, error) {
	if s == nil || s.store == nil || ctx == nil {
		return AnchorReattachmentCommit{}, ErrAnchorReattachmentUnavailable
	}
	if err := command.Guard.Validate(); err != nil || command.ThreadID == "" || command.CurrentGeneration == 0 || command.Candidate.Validate() != nil || command.Candidate.Generation != command.CurrentGeneration || command.CandidateFingerprint != review.AnchorCandidateFingerprint(command.Candidate) || !validAnchorReattachmentActor(command.Actor) {
		return AnchorReattachmentCommit{}, ErrReviewStoreInput
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return AnchorReattachmentCommit{}, err
	}
	if thread.SessionID != command.Guard.SessionID {
		return AnchorReattachmentCommit{}, ErrThreadNotOwned
	}
	if thread.Anchor.State != review.AnchorAmbiguous && thread.Anchor.State != review.AnchorOrphaned {
		return AnchorReattachmentCommit{}, ErrAnchorReattachmentNotEligible
	}
	if thread.Anchor.TargetGeneration != command.CurrentGeneration || command.Candidate.Side != thread.Anchor.Side {
		return AnchorReattachmentCommit{}, ErrAnchorCandidateStale
	}
	if string(command.Candidate.SourcePath) != "" && string(command.Candidate.SourcePath) != string(thread.Anchor.Path) {
		return AnchorReattachmentCommit{}, ErrAnchorCandidateUnavailable
	}
	if string(command.Candidate.Path) != string(thread.Anchor.Path) && string(command.Candidate.SourcePath) == "" {
		return AnchorReattachmentCommit{}, ErrAnchorCandidateUnavailable
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return AnchorReattachmentCommit{}, ErrReviewStoreInput
	}
	anchor, err := manualAnchor(thread.Anchor, command.Candidate, now)
	if err != nil {
		return AnchorReattachmentCommit{}, err
	}
	write := AnchorVersionWrite{ThreadID: thread.ID, CurrentGeneration: command.CurrentGeneration, PreviousAnchor: thread.Anchor, Anchor: anchor, Method: review.AnchorVersionMethodManual, Candidate: command.Candidate, CandidateFingerprint: command.CandidateFingerprint, Actor: command.Actor, CreatedAt: now}
	if err := write.Validate(); err != nil {
		return AnchorReattachmentCommit{}, err
	}
	var version AnchorVersionRecord
	nextGuard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
		var err error
		version, err = tx.AppendAnchorVersion(ctx, write)
		return err
	})
	if err != nil {
		return AnchorReattachmentCommit{}, err
	}
	thread.Anchor = anchor
	thread.UpdatedAt = now
	if err := thread.Validate(); err != nil {
		return AnchorReattachmentCommit{}, fmt.Errorf("reattached thread: %w", err)
	}
	return AnchorReattachmentCommit{Guard: nextGuard, Thread: thread, Version: version, Events: []Event{AnchorReattached{CorrelationID: command.CorrelationID, TargetGeneration: command.CurrentGeneration, SessionID: thread.SessionID, ThreadID: thread.ID, AnchorVersion: version.Version, Path: anchor.Path, StartLine: anchor.StartLine, EndLine: anchor.EndLine, CandidateFingerprint: command.CandidateFingerprint}}}, nil
}

func manualAnchor(previous review.CodeAnchor, candidate review.AnchorCandidate, now time.Time) (review.CodeAnchor, error) {
	if now.IsZero() || candidate.Validate() != nil {
		return review.CodeAnchor{}, ErrReviewStoreInput
	}
	anchor := previous
	anchor.Path = append([]byte(nil), candidate.Path...)
	anchor.Side = candidate.Side
	anchor.StartLine = candidate.StartLine
	anchor.EndLine = candidate.EndLine
	anchor.TargetGeneration = candidate.Generation
	anchor.State = review.AnchorValid
	anchor.SelectedText = candidate.SelectedText
	anchor.SelectionHash = review.FingerprintSelection(candidate.SelectedText)
	anchor.BeforeContextHash = review.FingerprintContext(candidate.BeforeContext)
	anchor.AfterContextHash = review.FingerprintContext(candidate.AfterContext)
	anchor.Relocation = &review.RelocationMetadata{PreviousPath: append([]byte(nil), previous.Path...), PreviousStartLine: previous.StartLine, PreviousEndLine: previous.EndLine, Reason: manualAnchorReattachmentReason, ReconciledAt: now.UTC()}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		return review.CodeAnchor{}, fmt.Errorf("manual anchor: %w", err)
	}
	return validated, nil
}

func candidatePointer(candidate review.AnchorCandidate) *review.AnchorCandidate {
	copyValue := candidate
	copyValue.SourcePath = append([]byte(nil), candidate.SourcePath...)
	copyValue.Path = append([]byte(nil), candidate.Path...)
	copyValue.BeforeContext = append([]string(nil), candidate.BeforeContext...)
	copyValue.AfterContext = append([]string(nil), candidate.AfterContext...)
	return &copyValue
}

func validAnchorReattachmentActor(actor string) bool {
	return actor != "" && len([]byte(actor)) <= MaxAnchorReattachmentActorBytes && !strings.ContainsRune(actor, 0)
}
