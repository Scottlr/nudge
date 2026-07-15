package repository

import "testing"

func TestContentClassV1UsesOneDeterministicRuleAcrossChunks(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		explicit bool
		want     ContentClassV1
	}{
		{name: "utf8", data: []byte("hello\n"), want: ContentClassRegularTextUTF8},
		{name: "bom", data: []byte{0xef, 0xbb, 0xbf, 'h'}, want: ContentClassRegularTextUTF8},
		{name: "nul", data: []byte{'a', 0, 'b'}, want: ContentClassRegularBinary},
		{name: "invalid utf8", data: []byte{'a', 0xff, 'b'}, want: ContentClassOpaqueBytes},
		{name: "explicit patch", data: []byte("text"), explicit: true, want: ContentClassRegularBinary},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classifier := NewContentClassifierV1(test.explicit)
			for index := range test.data {
				if _, err := classifier.Write(test.data[index : index+1]); err != nil {
					t.Fatal(err)
				}
			}
			if got := classifier.Classify(); got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
			if got := ClassifyContentV1(test.data, test.explicit); got != test.want {
				t.Fatalf("whole-value class = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFileContentAllowsMetadataOnlyBinaryIdentity(t *testing.T) {
	content := FileContent{
		Snapshot:     SnapshotRef{Kind: SnapshotEmpty},
		Path:         RepoPath("image.bin"),
		Kind:         FileKindRegular,
		Mode:         0o100644,
		ByteLength:   4096,
		ContentHash:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ContentClass: ContentClassRegularBinary,
		MetadataOnly: true,
		Binary:       true,
	}
	if err := content.Validate(); err != nil {
		t.Fatal(err)
	}
}
