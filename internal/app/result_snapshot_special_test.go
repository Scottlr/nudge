package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestResultSnapshotEntryRetainsSpecialEvidenceAsNonReady(t *testing.T) {
	path := []byte("named.pipe")
	evidence := repository.NewCompleteReviewOnlyEntryEvidence(repository.SpecialFIFO, 0o010000, 0, time.Unix(1, 0))
	entry := ResultSnapshotEntry{Path: path, Kind: repository.FileKindUnknown, Reason: ResultReasonUnsupportedEntry, ReviewOnly: &evidence}
	if err := entry.Validate(); err != nil {
		t.Fatalf("special result entry rejected: %v", err)
	}
	bad := entry
	bad.Complete = true
	if bad.Validate() == nil {
		t.Fatal("special result entry became complete")
	}
}

func TestCompareResultManifestKeepsSpecialReplacementVisible(t *testing.T) {
	baseline, err := NewWorkspaceManifest([]WorkspaceManifestEntry{{Path: []byte("main.go"), Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: 1, SHA256: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	evidence := repository.NewCompleteReviewOnlyEntryEvidence(repository.SpecialFIFO, 0o010000, 0, time.Unix(1, 0))
	result, err := NewResultManifest([]ResultSnapshotEntry{{Path: []byte("main.go"), Kind: repository.FileKindUnknown, Reason: ResultReasonUnsupportedEntry, ReviewOnly: &evidence}}, DefaultResourcePolicy().Version, true, ResultReasonUnsupportedEntry)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	delta, err := CompareResultManifest(baseline, result)
	if err != nil {
		t.Fatalf("CompareResultManifest: %v", err)
	}
	if len(delta.Entries) != 1 || delta.Entries[0].Result == nil || delta.Entries[0].Result.ReviewOnly == nil || delta.Entries[0].Complete != result.Complete {
		t.Fatalf("special replacement delta = %#v", delta)
	}
}
