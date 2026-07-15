package proposal

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestReviewRequiresExactEvidenceBeforeApprove(t *testing.T) {
	model := NewModel()
	projection := proposalProjection()
	intents := model.Update(SetProjectionMsg{Projection: projection})
	if len(intents) != 1 || intents[0].EntryPage == nil {
		t.Fatalf("initial entry request = %#v", intents)
	}
	request := *intents[0].EntryPage
	entry := proposalEntry()
	staleRequest := request
	staleRequest.PatchSHA256 = strings.Repeat("f", 64)
	model.Update(EntryPageResultMsg{Page: EntryPage{Request: staleRequest, Entries: []Entry{entry}, Total: 1}})
	if model.SelectedEntry() != "" {
		t.Fatal("stale entry page changed selection")
	}

	model.Update(EntryPageResultMsg{Page: EntryPage{Request: request, Entries: []Entry{entry}, Total: 1}})
	if model.SelectedEntry() != entry.ID || model.CanApprove() {
		t.Fatalf("entry evidence = selected %q canApprove %v", model.SelectedEntry(), model.CanApprove())
	}
	hunkIntents := model.InitialHunkRequest()
	if len(hunkIntents) != 1 || hunkIntents[0].HunkPage == nil {
		t.Fatalf("initial hunk request = %#v", hunkIntents)
	}
	hunkRequest := *hunkIntents[0].HunkPage
	model.Update(HunkPageResultMsg{Page: HunkPage{Request: hunkRequest, Hunks: []Hunk{{ID: "hunk-1", Ordinal: 0, Offset: 0, Length: 1, Rows: 1, SHA256: strings.Repeat("b", 64)}}, Total: 1}})
	rangeIntents := model.InitialRangeRequest()
	if len(rangeIntents) != 1 || rangeIntents[0].Range == nil {
		t.Fatalf("initial range request = %#v", rangeIntents)
	}
	rangeRequest := *rangeIntents[0].Range
	rangeBytes := []byte("diff\n")
	model.Update(PatchRangeResultMsg{Range: PatchRange{Request: rangeRequest, Bytes: rangeBytes, SHA256: hashBytes(rangeBytes), Complete: true}})
	if model.CanApprove() {
		t.Fatal("approval enabled before disclosure acknowledgement")
	}
	model.Update(AcknowledgeDisclosureMsg{})
	if !model.CanApprove() {
		t.Fatalf("approval remained disabled: %s", model.LastError())
	}
	model.Update(BeginApproveMsg{})
	if model.Confirmation() != string(confirmationApprove) {
		t.Fatalf("confirmation = %q", model.Confirmation())
	}
	approve := model.Update(ConfirmApproveMsg{})
	if len(approve) != 1 || approve[0].Approve == nil || approve[0].Approve.Identity.Version != projection.Version || approve[0].Approve.Identity.PatchSHA256 != projection.PatchSHA256 || approve[0].Approve.Identity.IndexHash != projection.IndexHash {
		t.Fatalf("approve intent = %#v", approve)
	}
}

func TestProposalIdentityAndStatusChangesInvalidateReviewState(t *testing.T) {
	model := NewModel()
	projection := proposalProjection()
	model.Update(SetProjectionMsg{Projection: projection})
	model.Update(BeginApproveMsg{})
	if model.Confirmation() != "" {
		t.Fatal("confirmation opened without complete evidence")
	}
	model.Update(SetProjectionMsg{Projection: func() Projection {
		value := projection
		value.Status = review.ProposalVersionStale
		value.StatusReason = "destination changed"
		value.Revision++
		return value
	}()})
	if model.Confirmation() != "" || model.CanApprove() {
		t.Fatalf("stale status retained approval state: confirmation=%q canApprove=%v", model.Confirmation(), model.CanApprove())
	}
	model.Update(SetProjectionMsg{Projection: func() Projection { value := projection; value.Version = 2; value.Revision += 2; return value }()})
	if model.SelectedEntry() != "" || model.SelectedHunk() != "" {
		t.Fatalf("identity change retained selection: entry=%q hunk=%q", model.SelectedEntry(), model.SelectedHunk())
	}
}

func TestNoChangesHasNoProposalActions(t *testing.T) {
	model := NewModel()
	model.Update(SetProjectionMsg{Projection: Projection{Revision: 1, ProposalID: "proposal-1", NoChanges: true}})
	if model.Mode() != ModeDiscussion || model.CanApprove() || model.Confirmation() != "" {
		t.Fatalf("no-change state = mode %q canApprove=%v confirmation=%q", model.Mode(), model.CanApprove(), model.Confirmation())
	}
	if actions := model.Update(BeginApproveMsg{}); len(actions) != 0 {
		t.Fatalf("no-change approve actions = %#v", actions)
	}
	if actions := model.Update(ReturnToDiscussionMsg{}); len(actions) != 1 || actions[0].Mode == nil || actions[0].Mode.Mode != ModeDiscussion {
		t.Fatalf("no-change discussion action = %#v", actions)
	}
	view := model.View()
	if !strings.Contains(view, "No proposed changes") || strings.Contains(view, "Approve proposal") || strings.Contains(view, "Reject proposal") {
		t.Fatalf("no-change view = %q", view)
	}
}

func proposalProjection() Projection {
	return Projection{
		Revision: 1, ProposalID: "proposal-1", Version: 1, PatchSHA256: strings.Repeat("a", 64), IndexHash: strings.Repeat("c", 64), ArtifactID: "artifact-1", PatchBytes: 5, FileCount: 1, HunkCount: 1, RowCount: 1,
		Scope: review.ProposalScopeFocused, Status: review.ProposalVersionReady, StatusReason: "complete", Applicability: ApplicabilityReady, ApplicabilityReason: "destination matches", Destination: "working tree",
	}
}

func proposalEntry() Entry {
	path := repository.RepoPath("main.go")
	oldPath := repository.RepoPath("main.go")
	return Entry{ID: "entry-1", Ordinal: 0, Path: path, OldPath: &oldPath, Kind: repository.ChangeModified, OldKind: repository.FileKindRegular, NewKind: repository.FileKindRegular, OldMode: 0o100644, NewMode: 0o100644, Offset: 0, Length: 5, HunkCount: 1, Bytes: 5, SHA256: strings.Repeat("d", 64)}
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
