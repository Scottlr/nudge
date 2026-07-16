package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrExportInput reports an incomplete or contradictory export request.
	ErrExportInput = errors.New("invalid export request")
	// ErrExportNotFound reports an absent thread or proposal version.
	ErrExportNotFound = errors.New("export selection not found")
	// ErrExportActiveMessage reports a message that has not reached a terminal
	// state and therefore is not a stable export source.
	ErrExportActiveMessage = errors.New("export message is still active")
	// ErrExportSource reports a missing or mismatched immutable source.
	ErrExportSource = errors.New("export source is unavailable or mismatched")
)

const (
	// ExportThreadKind selects a review-thread Markdown export.
	ExportThreadKind ExportKind = "thread"
	// ExportProposalKind selects one immutable proposal-version Markdown export.
	ExportProposalKind ExportKind = "proposal"

	// MaxExportMessages bounds the metadata retained before slow output begins.
	MaxExportMessages = 1000
	// MaxExportAnchorVersions bounds the immutable anchor history retained for
	// one export selection.
	MaxExportAnchorVersions = 2000
)

// ExportKind identifies the selected human-readable export.
type ExportKind string

// ExportSource is the application-owned read boundary used by the Markdown
// encoder. Implementations must return immutable identities and never expose
// provider protocol, workspace files, or operational data.
type ExportSource interface {
	LoadThread(context.Context, domain.ReviewThreadID) (review.ReviewThread, error)
	ListAnchorVersions(context.Context, domain.ReviewThreadID) ([]AnchorVersionRecord, error)
	ListMessages(context.Context, domain.ReviewThreadID, MessagePage) (MessagePageResult, error)
	ReadMessageBody(context.Context, BodyRange) (MessageBodyChunk, error)
	LoadProposalAggregate(context.Context, domain.ProposalID) (review.ProposalAggregate, error)
	LoadProposalPatchArtifactForAttempt(context.Context, domain.OperationID) (ProposalPatchArtifact, error)
}

// ExportSelection is the closed, bounded selection captured before output
// begins. Message and patch bodies remain identity-bound streams.
type ExportSelection struct {
	Kind     ExportKind
	Thread   *ExportThread
	Proposal *ExportProposal
}

// ExportThread contains stable thread metadata, anchor history, and terminal
// message identities. It intentionally does not retain message bodies.
type ExportThread struct {
	Thread   review.ReviewThread
	Anchors  []AnchorVersionRecord
	Messages []MessageSummary
}

// ExportProposal contains one immutable proposal version and its optional
// adopted patch artifact identity. Patch bytes are read separately by range.
type ExportProposal struct {
	Aggregate review.ProposalAggregate
	Version   review.ProposedPatch
	Artifact  *ProposalPatchArtifact
}

// SelectThread captures a bounded thread selection before Markdown output.
func SelectThread(ctx context.Context, source ExportSource, id domain.ReviewThreadID) (ExportSelection, error) {
	if ctx == nil || source == nil || id == "" {
		return ExportSelection{}, ErrExportInput
	}
	thread, err := source.LoadThread(ctx, id)
	if err != nil {
		return ExportSelection{}, err
	}
	if thread.Validate() != nil {
		return ExportSelection{}, ErrExportSource
	}
	anchors, err := source.ListAnchorVersions(ctx, id)
	if err != nil {
		return ExportSelection{}, err
	}
	if len(anchors) == 0 || len(anchors) > MaxExportAnchorVersions {
		return ExportSelection{}, ErrExportSource
	}
	for _, anchor := range anchors {
		if anchor.Validate() != nil || anchor.ThreadID != id {
			return ExportSelection{}, ErrExportSource
		}
	}
	messages, err := selectMessages(ctx, source, id, thread.Messages)
	if err != nil {
		return ExportSelection{}, err
	}
	selection := ExportSelection{Kind: ExportThreadKind, Thread: &ExportThread{Thread: thread, Anchors: anchors, Messages: messages}}
	if err := selection.Validate(); err != nil {
		return ExportSelection{}, err
	}
	return selection, nil
}

// SelectProposal captures one immutable proposal version before Markdown
// output. A patch artifact is required when the version uses external bytes.
func SelectProposal(ctx context.Context, source ExportSource, id domain.ProposalID, version review.ProposalVersionNumber) (ExportSelection, error) {
	if ctx == nil || source == nil || id == "" {
		return ExportSelection{}, ErrExportInput
	}
	aggregate, err := source.LoadProposalAggregate(ctx, id)
	if err != nil {
		return ExportSelection{}, err
	}
	if aggregate.Validate() != nil || aggregate.Proposal.ID != id {
		return ExportSelection{}, ErrExportSource
	}
	if version == 0 {
		if aggregate.Proposal.CurrentVersion == nil {
			return ExportSelection{}, ErrExportNotFound
		}
		version = *aggregate.Proposal.CurrentVersion
	}
	var selected *review.ProposedPatch
	for index := range aggregate.Versions {
		if aggregate.Versions[index].Version == version {
			value := aggregate.Versions[index]
			selected = &value
			break
		}
	}
	if selected == nil {
		return ExportSelection{}, ErrExportNotFound
	}
	result := ExportProposal{Aggregate: aggregate, Version: *selected}
	if selected.Artifact != (review.ProposedPatchArtifactReference{}) {
		artifact, loadErr := source.LoadProposalPatchArtifactForAttempt(ctx, selected.AttemptID)
		if loadErr != nil {
			return ExportSelection{}, loadErr
		}
		if artifact.Validate() != nil || artifact.ID != selected.Artifact.ArtifactID || artifact.PatchSHA256 != selected.PatchSHA256 || artifact.Published.Identity.Bytes != ByteSize(selected.Artifact.PatchBytes) || artifact.Index.Hash != selected.Artifact.IndexHash {
			return ExportSelection{}, ErrExportSource
		}
		result.Artifact = &artifact
	}
	selection := ExportSelection{Kind: ExportProposalKind, Proposal: &result}
	if err := selection.Validate(); err != nil {
		return ExportSelection{}, err
	}
	return selection, nil
}

// Validate checks the selected export variant and all bounded metadata.
func (s ExportSelection) Validate() error {
	switch s.Kind {
	case ExportThreadKind:
		if s.Thread == nil || s.Proposal != nil || s.Thread.Thread.Validate() != nil || len(s.Thread.Messages) > MaxExportMessages {
			return ErrExportInput
		}
		for _, anchor := range s.Thread.Anchors {
			if anchor.Validate() != nil || anchor.ThreadID != s.Thread.Thread.ID {
				return ErrExportInput
			}
		}
		for _, message := range s.Thread.Messages {
			if err := validateExportMessage(message, s.Thread.Thread.ID); err != nil {
				return err
			}
		}
	case ExportProposalKind:
		if s.Proposal == nil || s.Thread != nil || s.Proposal.Aggregate.Validate() != nil || s.Proposal.Version.Validate() != nil || s.Proposal.Version.ProposalID != s.Proposal.Aggregate.Proposal.ID {
			return ErrExportInput
		}
		if s.Proposal.Artifact != nil {
			if s.Proposal.Artifact.Validate() != nil || s.Proposal.Artifact.PatchSHA256 != s.Proposal.Version.PatchSHA256 {
				return ErrExportInput
			}
		} else if len(s.Proposal.Version.PatchBytes) == 0 {
			return ErrExportSource
		}
	default:
		return ErrExportInput
	}
	return nil
}

func selectMessages(ctx context.Context, source ExportSource, threadID domain.ReviewThreadID, expected []domain.MessageID) ([]MessageSummary, error) {
	messages := make([]MessageSummary, 0, len(expected))
	page := MessagePage{ThreadID: threadID, Limit: DefaultPageLimit}
	for {
		result, err := source.ListMessages(ctx, threadID, page)
		if err != nil {
			return nil, err
		}
		if result.Revision == 0 || len(messages)+len(result.Items) > MaxExportMessages {
			return nil, ErrExportSource
		}
		for _, message := range result.Items {
			if err := validateExportMessage(message, threadID); err != nil {
				return nil, err
			}
			messages = append(messages, message)
		}
		if !result.HasMore {
			break
		}
		if result.Next == nil {
			return nil, ErrExportSource
		}
		page.Cursor = result.Next
	}
	if len(messages) != len(expected) {
		return nil, ErrExportSource
	}
	for index, message := range messages {
		if message.ID != expected[index] {
			return nil, ErrExportSource
		}
	}
	return messages, nil
}

func validateExportMessage(message MessageSummary, threadID domain.ReviewThreadID) error {
	if message.ID == "" || message.ThreadID != threadID || message.Ordinal == 0 || message.ByteLength > MaxStreamedMessageBytes || len(message.SHA256) != sha256.Size*2 {
		return ErrExportSource
	}
	if _, err := hex.DecodeString(message.SHA256); err != nil {
		return ErrExportSource
	}
	if message.Status != review.MessageCompleted && message.Status != review.MessageFailed && message.Status != review.MessageCancelled {
		return ErrExportActiveMessage
	}
	return nil
}
