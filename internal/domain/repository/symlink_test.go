package repository

import (
	"bytes"
	"testing"
)

func TestClassifySymlinkTargetUsesLexicalNoFollowPolicy(t *testing.T) {
	path := RepoPath("link")
	tests := []struct {
		name   string
		target []byte
		want   SymlinkTargetClass
	}{
		{name: "contained", target: []byte("target"), want: SymlinkRelativeContained},
		{name: "contained parent", target: []byte("dir/../target"), want: SymlinkRelativeContained},
		{name: "escaping", target: []byte("../outside"), want: SymlinkLexicallyEscaping},
		{name: "absolute", target: []byte("/outside"), want: SymlinkAbsolute},
		{name: "git admin", target: []byte("dir/.git/config"), want: SymlinkGitAdminAlias},
		{name: "empty component", target: []byte("dir//target"), want: SymlinkUnrepresentable},
		{name: "native separator", target: []byte(`dir\target`), want: SymlinkUnrepresentable},
		{name: "empty", target: nil, want: SymlinkUnrepresentable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifySymlinkTarget(path, test.target, SymlinkMaxComponents, SymlinkMaxDepth); got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
	if got := ClassifySymlinkTarget(path, []byte("a/b/c"), 2, SymlinkMaxDepth); got != SymlinkUnrepresentable {
		t.Fatalf("component limit class = %q", got)
	}
}

func TestSymlinkTargetRefAndEvidenceAreBoundedAndImmutable(t *testing.T) {
	target := bytes.Repeat([]byte{'x'}, SymlinkInlinePreviewMax+10)
	ref := NewSymlinkTargetRef(target)
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	if ref.Length != uint64(len(target)) || len(ref.InlinePreview) != SymlinkInlinePreviewMax || !bytes.Equal(ref.InlinePreview, target[:SymlinkInlinePreviewMax]) {
		t.Fatalf("target ref = %+v", ref)
	}
	target[0] = 'y'
	if ref.InlinePreview[0] != 'x' {
		t.Fatal("target preview aliases input")
	}

	path := RepoPath("link")
	evidence, err := NewSymlinkEvidence(path, []byte("target"), "root", "parents", "linux", SymlinkPrimitiveVersion)
	if err != nil || evidence.Validate() != nil || !evidence.IsActionable() {
		t.Fatalf("safe evidence = %+v, %v", evidence, err)
	}
	unsafe, err := NewSymlinkEvidence(path, []byte("../outside"), "root", "parents", "linux", SymlinkPrimitiveVersion)
	if err != nil || unsafe.Validate() != nil || unsafe.IsActionable() || unsafe.ReasonCode != "symlink_lexically_escaping" {
		t.Fatalf("unsafe evidence = %+v, %v", unsafe, err)
	}
}
