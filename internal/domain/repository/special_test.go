package repository

import (
	"io/fs"
	"testing"
	"time"
)

func TestSpecialFileKindAndReviewOnlyEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		mode fs.FileMode
		want SpecialFileKind
	}{
		{name: "socket", mode: fs.ModeSocket, want: SpecialSocket},
		{name: "fifo", mode: fs.ModeNamedPipe, want: SpecialFIFO},
		{name: "character device", mode: fs.ModeDevice | fs.ModeCharDevice, want: SpecialCharDevice},
		{name: "block device", mode: fs.ModeDevice, want: SpecialBlockDevice},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := SpecialFileKindFromMode(test.mode)
			if !ok || kind != test.want {
				t.Fatalf("SpecialFileKindFromMode() = %q, %v; want %q, true", kind, ok, test.want)
			}
			evidence := NewCompleteReviewOnlyEntryEvidence(kind, test.mode, 0, time.Unix(10, 20))
			if err := evidence.Validate(); err != nil {
				t.Fatalf("complete evidence rejected: %v", err)
			}
		})
	}

	pathOnly := NewPathOnlyReviewOnlyEntryEvidence()
	if err := pathOnly.Validate(); err != nil {
		t.Fatalf("path-only evidence rejected: %v", err)
	}
	if complete := NewCompleteReviewOnlyEntryEvidence(SpecialJunction, 0, 0, time.Unix(1, 0)); complete.Validate() != nil {
		t.Fatal("junction evidence rejected")
	}
}
