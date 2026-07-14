package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestMatchTreeEntryUsesStableRankClassesAndRawRanges(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		query string
		class uint8
	}{
		{name: "exact path", path: "main.go", query: "MAIN.GO", class: 0},
		{name: "exact basename", path: "src/main.go", query: "main.go", class: 1},
		{name: "basename prefix", path: "src/main_test.go", query: "main", class: 2},
		{name: "component prefix", path: "srcdir/file.go", query: "src", class: 3},
		{name: "substring", path: "docs/contains-main.md", query: "main", class: 4},
		{name: "subsequence", path: "m_a_i_n.go", query: "main", class: 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			entry := searchTestEntry(t, testCase.path)
			match, ok := MatchTreeEntry(entry, testCase.query)
			if !ok {
				t.Fatalf("MatchTreeEntry() did not match %q against %q", testCase.query, testCase.path)
			}
			if match.Rank.Class != testCase.class {
				t.Fatalf("rank class = %d, want %d", match.Rank.Class, testCase.class)
			}
			if len(match.MatchRanges) == 0 || match.MatchRanges[0].Validate(len(testCase.path)) != nil {
				t.Fatalf("match ranges = %#v", match.MatchRanges)
			}
		})
	}

	invalidPath := repository.RepoPath([]byte("bad\xff.go"))
	invalidEntry := searchTestEntry(t, "placeholder.go")
	invalidEntry.Path = invalidPath
	invalidEntry.Name = repository.RepoPath([]byte("bad\xff.go"))
	if match, ok := MatchTreeEntry(invalidEntry, "bad\xff"); !ok || match.Rank.Class != 2 {
		t.Fatalf("invalid UTF-8 raw match = %#v, %v", match, ok)
	}
}

func TestSearchTreeQueryNormalizesPolicyAndRejectsUnsafeCursor(t *testing.T) {
	policy := DefaultResourcePolicy()
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotEmpty}
	query, err := (SearchTreeQuery{Snapshot: snapshot, Query: "src"}).Normalize(policy)
	if err != nil {
		t.Fatal(err)
	}
	if query.Limit != int(policy.TreeSearch.Page.Default) {
		t.Fatalf("default search limit = %d, want %d", query.Limit, policy.TreeSearch.Page.Default)
	}
	if _, err := (SearchTreeQuery{Snapshot: snapshot, Query: strings.Repeat("x", int(policy.TreeSearch.QueryBytes)+1)}).Normalize(policy); !errors.Is(err, ErrInvalidTreeSearchQuery) {
		t.Fatalf("oversize query error = %v", err)
	}
	if _, err := (SearchTreeQuery{Snapshot: snapshot, Query: "src", Cursor: "\x01"}).Normalize(policy); !errors.Is(err, ErrInvalidTreeSearchQuery) {
		t.Fatalf("unsafe cursor error = %v", err)
	}
}

func TestValidateSearchTreeResultRejectsDifferentSnapshot(t *testing.T) {
	query := SearchTreeQuery{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Query: "src", Limit: 1}
	page := SearchTreePage{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: repository.ObjectID("commit")}, Complete: true}
	if !errors.Is(ValidateSearchTreeResult(query, page, DefaultResourcePolicy()), ErrTreeSearchStale) {
		t.Fatalf("stale result error = %v", ValidateSearchTreeResult(query, page, DefaultResourcePolicy()))
	}
}

func TestSearchTreePageAcceptsBoundedEntryCeilingEvidence(t *testing.T) {
	policy := DefaultResourcePolicy()
	entries := make([]TreeSearchMatch, 0, int(policy.TreeSearch.Page.Default))
	for index := 0; index < int(policy.TreeSearch.Page.Default); index++ {
		entry := searchTestEntry(t, fmt.Sprintf("generated/file-%05d.go", index))
		match, ok := MatchTreeEntry(entry, "file")
		if !ok {
			t.Fatal("expected generated path to match")
		}
		entries = append(entries, match)
	}
	SortTreeSearchMatches(entries)
	page := SearchTreePage{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Matches: entries, ScannedEntries: uint64(policy.TreeSearch.EntryCeiling), Complete: true}
	if err := page.Validate(policy); err != nil {
		t.Fatal(err)
	}
}

func searchTestEntry(t *testing.T, path string) repository.TreeEntry {
	t.Helper()
	value, err := repository.NewRepoPath([]byte(path))
	if err != nil {
		t.Fatal(err)
	}
	name := value
	parent := repository.RepoPath(nil)
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		parent, err = repository.NewRepoPath([]byte(path[:slash]))
		if err != nil {
			t.Fatal(err)
		}
		name, err = repository.NewRepoPath([]byte(path[slash+1:]))
		if err != nil {
			t.Fatal(err)
		}
	}
	entry := repository.TreeEntry{Path: value, Name: name, Parent: parent, Kind: repository.FileKindRegular, Mode: 0o100644}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	return entry
}
