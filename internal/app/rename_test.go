package app

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestAcceptedRenameEvidencePreservesPolicyAndMappings(t *testing.T) {
	oldPath := repository.RepoPath([]byte("old.txt"))
	newPath := repository.RepoPath([]byte("new.txt"))
	rename, err := repository.NewRenameEvidence(1, 80, repository.ChangeRenamed, oldPath, newPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := repository.CapturePolicyEvidence{
		MachineGitVersion: 1, RenameVersion: 1, RenameOutcome: "complete", RenameDeleteCandidates: 1, RenameAddCandidates: 1,
		RenameSimilarityPercent: 60, RenameMaxDeleteSources: 1000, RenameMaxAddTargets: 1000, RenameDetectChangedSourceCopies: true,
		RenameFlags: []string{"--find-renames=60%", "--find-copies=60%"}, RenameEvidenceHash: repository.RenamePolicyEvidenceHash(1, 60, 1000, 1000, true, false, "complete", 1, 1, []string{"--find-renames=60%", "--find-copies=60%"}),
		PatchFormatVersion: 1, ConversionPolicyVersion: 1, ConversionDecision: "byte_neutral", ConversionFingerprint: strings.Repeat("a", 64), ResourcePolicyVersion: 1,
	}
	evidence, err := AcceptedRenameEvidence(policy)
	if err != nil || !evidence.Complete() {
		t.Fatalf("policy evidence = %#v, error = %v", evidence, err)
	}
	mappings, err := AcceptedRenameMappings([]repository.ChangedFile{{OldPath: &oldPath, NewPath: &newPath, Kind: repository.ChangeRenamed, OldFileKind: repository.FileKindRegular, NewFileKind: repository.FileKindRegular, OldMode: 0o100644, NewMode: 0o100644, Rename: &rename}}, repository.DiffHead)
	if err != nil || len(mappings) != 1 || mappings[0].EvidenceHash != rename.EvidenceHash {
		t.Fatalf("mappings = %#v, error = %v", mappings, err)
	}
}
