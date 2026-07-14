package app

import (
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestDisplayedContentIDChangesWithImmutableInputs(t *testing.T) {
	identity := DisplayedContentIdentity{
		TargetIdentity:         "target-1",
		CaptureIdentity:        "capture-1",
		Base:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		DiffIdentity:           "patch-1",
		RowConstructionVersion: 1,
	}
	first, err := NewDisplayedContentID(identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.DiffIdentity = "patch-2"
	second, err := NewDisplayedContentID(identity)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.Validate() != nil || second.Validate() != nil {
		t.Fatalf("content IDs = %q/%q", first, second)
	}
}

func TestDisplayedRowPageRejectsCrossContentRows(t *testing.T) {
	identity := DisplayedContentIdentity{
		TargetIdentity:         "target-1",
		Base:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:                   repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		DiffIdentity:           "patch-1",
		RowConstructionVersion: 1,
	}
	content, err := NewDisplayedContentID(identity)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity := identity
	otherIdentity.DiffIdentity = "patch-2"
	other, err := NewDisplayedContentID(otherIdentity)
	if err != nil {
		t.Fatal(err)
	}
	line := 1
	page := DisplayedContentPage{
		ContentID: content,
		Rows: []DisplayedRow{{
			ID:         CodeRowID{Content: other, Ordinal: 0},
			Kind:       DisplayedRowSource,
			Side:       SideHead,
			Selectable: true,
			HeadLine:   &line,
			Text:       "main",
		}},
	}
	if page.Validate() == nil {
		t.Fatal("cross-content row was accepted")
	}
}
