package gitcli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestRenameAndPatchPoliciesAreBoundedAndExact(t *testing.T) {
	rename := DefaultRenamePolicyV1()
	evidence, err := NewRenamePolicyEvidence(rename, RenameDetectionComplete, 4, 3)
	if err != nil || !evidence.AnchorMappingAllowed() {
		t.Fatalf("complete rename evidence = %#v, error = %v", evidence, err)
	}
	limited, err := NewRenamePolicyEvidence(rename, RenameDetectionLimited, rename.MaxDeleteSources, 2)
	if err != nil || limited.AnchorMappingAllowed() {
		t.Fatalf("limited rename evidence = %#v, error = %v", limited, err)
	}
	if !containsString(limited.Flags, "--find-renames=60%") || !containsString(limited.Flags, "--find-copies=60%") || containsString(limited.Flags, "--find-copies-harder") {
		t.Fatalf("rename flags = %#v", limited.Flags)
	}

	format := DefaultPatchFormatV1()
	args, err := format.DiffArgs(rename)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"core.quotePath=true", "diff.algorithm=myers", "diff.indentHeuristic=false", "diff.orderFile=" + os.DevNull, "--patch", "--binary", "--full-index", "--unified=3", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"} {
		if !containsString(args, expected) {
			t.Fatalf("patch args %#v do not contain %q", args, expected)
		}
	}
	if containsString(args, "--find-copies-harder") {
		t.Fatal("patch policy enabled harder copy detection")
	}
	if containsString(EmptyTreeArgs(), "-w") {
		t.Fatal("empty-tree command writes an object")
	}
}

func TestContentConversionPolicyRequiresByteNeutralEvidence(t *testing.T) {
	path, err := repository.NewRepoPath([]byte("src/file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultContentConversionPolicyV1()
	neutral, err := policy.Evaluate([]AttributeObservation{
		{Path: path, Name: AttributeText, State: AttributeUnspecified, Source: AttributeSourceRepository},
	}, false)
	if err != nil || neutral.Decision != ConversionByteNeutral || neutral.Reason != ConversionReasonNone || neutral.Validate() != nil {
		t.Fatalf("neutral conversion evidence = %#v, error = %v", neutral, err)
	}
	reordered, err := policy.Evaluate([]AttributeObservation{
		{Path: path, Name: AttributeFilter, State: AttributeUnset, Source: AttributeSourceRepository},
		{Path: path, Name: AttributeText, State: AttributeUnspecified, Source: AttributeSourceRepository},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := policy.Evaluate([]AttributeObservation{
		{Path: path, Name: AttributeText, State: AttributeUnspecified, Source: AttributeSourceRepository},
		{Path: path, Name: AttributeFilter, State: AttributeUnset, Source: AttributeSourceRepository},
	}, false)
	if err != nil || reordered.Fingerprint != sorted.Fingerprint {
		t.Fatalf("attribute ordering changed fingerprint: reordered=%#v sorted=%#v error=%v", reordered, sorted, err)
	}
	unsupported, err := policy.Evaluate([]AttributeObservation{
		{Path: path, Name: AttributeFilter, State: AttributeValue, Value: "custom-filter", Source: AttributeSourceRepository},
	}, false)
	if err != nil || unsupported.Decision != ConversionReviewOnly || unsupported.Reason != ConversionReasonAttributeUnsupported || unsupported.Validate() != nil {
		t.Fatalf("unsupported conversion evidence = %#v, error = %v", unsupported, err)
	}
	forged := unsupported
	forged.Decision = ConversionByteNeutral
	forged.Reason = ConversionReasonNone
	forged.Fingerprint = conversionFingerprint(forged)
	if forged.Validate() == nil {
		t.Fatal("non-neutral attribute evidence was accepted as byte-neutral")
	}
	changed, err := policy.Evaluate(nil, true)
	if err != nil || changed.Decision != ConversionReviewOnly || changed.Reason != ConversionReasonAttributeUnsupported {
		t.Fatalf("changed attributes evidence = %#v, error = %v", changed, err)
	}
}

func TestApplyPolicyKeepsCheckAndMutationArgumentsInParity(t *testing.T) {
	policy := DefaultApplyPolicyV1()
	check, err := policy.Args(ApplyCheckPhase)
	if err != nil {
		t.Fatal(err)
	}
	mutation, err := policy.Args(ApplyMutationPhase)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(check, "--check") || containsString(mutation, "--check") {
		t.Fatalf("check args = %#v, mutation args = %#v", check, mutation)
	}
	if err := ValidateApplyArgs(append(append([]string(nil), mutation...), "--3way")); !errors.Is(err, ErrInvalidGitPolicy) {
		t.Fatalf("unsafe apply args error = %v", err)
	}
	if strings.Join(check[:len(check)-1], "\x00") != strings.Join(mutation, "\x00") {
		t.Fatalf("check and mutation shared args differ: check=%#v mutation=%#v", check, mutation)
	}
}

func TestConformanceEvidenceRegistryRequiresExactRows(t *testing.T) {
	row := ConformanceEvidenceV1{
		Version:           currentGitPolicyVersion,
		Key:               PolicyEvidenceKey{GitVersion: "2.50.0", ObjectFormat: "sha256", Platform: "windows-amd64", FixtureSet: "v1"},
		MachineRead:       Selection(GitPolicyMachineRead),
		PatchDerivation:   Selection(GitPolicyPatchDerivation),
		ContentConversion: Selection(GitPolicyContentConversion),
		ApplyCheck:        Selection(GitPolicyApplyCheck),
		ApplyMutation:     Selection(GitPolicyApplyMutation),
		RenamePolicy:      DefaultRenamePolicyV1(),
		PatchFormat:       DefaultPatchFormatV1(),
		ConversionPolicy:  DefaultContentConversionPolicyV1(),
		ApplyPolicy:       DefaultApplyPolicyV1(),
	}
	if err := row.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := row.Digest()
	if err != nil || digest == "" {
		t.Fatalf("evidence digest = %q, error = %v", digest, err)
	}
	registry, err := NewConformanceEvidenceRegistry([]ConformanceEvidenceV1{row})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(row.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(PolicyEvidenceKey{GitVersion: "2.50.1", ObjectFormat: "sha256", Platform: "windows-amd64", FixtureSet: "v1"}); !errors.Is(err, ErrUnknownPolicyEvidence) {
		t.Fatalf("unknown evidence lookup error = %v", err)
	}
	if _, err := NewConformanceEvidenceRegistry([]ConformanceEvidenceV1{row, row}); !errors.Is(err, ErrInvalidPolicyEvidence) {
		t.Fatalf("duplicate evidence error = %v", err)
	}
}
