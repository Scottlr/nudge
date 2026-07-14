package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidTreeSearchQuery reports an invalid or over-limit search request.
	ErrInvalidTreeSearchQuery = errors.New("invalid tree search query")
	// ErrInvalidTreeSearchPage reports a result that cannot be safely projected.
	ErrInvalidTreeSearchPage = errors.New("invalid tree search page")
	// ErrTreeSearchUnavailable reports that the composed runtime has no search source.
	ErrTreeSearchUnavailable = errors.New("tree search unavailable")
	// ErrTreeSearchIncomplete is the stable class for cancellation or deadline
	// before a deterministic page was completely enumerated.
	ErrTreeSearchIncomplete = errors.New("tree search incomplete")
	// ErrTreeSearchStale reports a result or cursor from a different snapshot.
	ErrTreeSearchStale = errors.New("tree search snapshot is stale")
)

// TreeSearchRankingVersion identifies the ordering and matching semantics
// encoded by search cursors.
const TreeSearchRankingVersion uint32 = 1

// SearchTreeQuery requests one bounded repository-wide path search over one
// immutable snapshot. It never requests file contents.
type SearchTreeQuery struct {
	Snapshot repository.SnapshotRef
	Query    string
	Cursor   string
	Limit    int
}

func (SearchTreeQuery) isQuery() {}

// Normalize applies the versioned search limits and copies no mutable path
// data because the request contains only immutable scalar identity fields.
func (q SearchTreeQuery) Normalize(policy ResourcePolicy) (SearchTreeQuery, error) {
	if policy == (ResourcePolicy{}) {
		policy = DefaultResourcePolicy()
	}
	if policy.Validate() != nil || q.Snapshot.Validate() != nil || len(q.Query) == 0 || ByteSize(len(q.Query)) > policy.TreeSearch.QueryBytes || bytes.IndexByte([]byte(q.Query), 0) >= 0 || !utf8.ValidString(q.Query) {
		return SearchTreeQuery{}, ErrInvalidTreeSearchQuery
	}
	if q.Cursor != "" && (ByteSize(len(q.Cursor)) > policy.TreeSearch.CursorBytes || !utf8.ValidString(q.Cursor) || !safeSearchCursor(q.Cursor)) {
		return SearchTreeQuery{}, ErrInvalidTreeSearchQuery
	}
	if q.Limit == 0 {
		q.Limit = int(policy.TreeSearch.Page.Default)
	}
	if q.Limit < 1 || q.Limit > int(policy.TreeSearch.Page.Hard) {
		return SearchTreeQuery{}, ErrInvalidTreeSearchQuery
	}
	return q, nil
}

func safeSearchCursor(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

// QueryHash returns the stable query identity used by opaque cursors.
func (q SearchTreeQuery) QueryHash() string {
	hash := sha256.Sum256([]byte(q.Query))
	return hex.EncodeToString(hash[:])
}

// TreeSearchRank is the deterministic rank tuple for one matched path.
type TreeSearchRank struct {
	Class     uint8
	GapBytes  uint32
	PathBytes uint32
}

// TreeSearchMatch is one metadata-only path result with raw-byte match ranges.
type TreeSearchMatch struct {
	Entry       repository.TreeEntry
	Rank        TreeSearchRank
	MatchRanges []repository.ByteRange
}

// SearchTreePage is one complete bounded result page. A partial enumeration is
// returned as an error and is never represented as Complete=true.
type SearchTreePage struct {
	Snapshot       repository.SnapshotRef
	Matches        []TreeSearchMatch
	NextCursor     string
	ScannedEntries uint64
	Complete       bool
}

// TreeSearcher is the consumer-owned application boundary for full-tree path
// search. Implementations must enumerate immutable metadata and never content.
type TreeSearcher interface {
	SearchTree(context.Context, SearchTreeQuery) (SearchTreePage, error)
}

// ValidateSearchTreeResult verifies that a returned page belongs to the exact
// immutable request that produced it. Consumers use ErrTreeSearchStale to
// discard an old result without presenting it as current.
func ValidateSearchTreeResult(query SearchTreeQuery, page SearchTreePage, policy ResourcePolicy) error {
	normalized, err := query.Normalize(policy)
	if err != nil {
		return err
	}
	if page.Snapshot.Kind != normalized.Snapshot.Kind || page.Snapshot.ObjectID != normalized.Snapshot.ObjectID || page.Snapshot.WorktreeID != normalized.Snapshot.WorktreeID || page.Snapshot.Fingerprint != normalized.Snapshot.Fingerprint {
		return ErrTreeSearchStale
	}
	return page.Validate(policy)
}

// TreeSearchIncompleteError preserves the cancellation/deadline cause and
// the amount of immutable metadata examined without claiming completeness.
type TreeSearchIncompleteError struct {
	Scanned uint64
	Cause   error
}

func (e *TreeSearchIncompleteError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrTreeSearchIncomplete.Error()
	}
	return ErrTreeSearchIncomplete.Error() + ": " + e.Cause.Error()
}

func (e *TreeSearchIncompleteError) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrTreeSearchIncomplete}
	}
	return []error{ErrTreeSearchIncomplete, e.Cause}
}

// Validate checks the page identity, complete marker, ordering, and ranges.
func (p SearchTreePage) Validate(policy ResourcePolicy) error {
	if policy == (ResourcePolicy{}) {
		policy = DefaultResourcePolicy()
	}
	if policy.Validate() != nil || p.Snapshot.Validate() != nil || !p.Complete || len(p.Matches) > int(policy.TreeSearch.Page.Hard) || p.ScannedEntries > uint64(policy.TreeSearch.EntryCeiling) || (p.NextCursor != "" && (!utf8.ValidString(p.NextCursor) || !safeSearchCursor(p.NextCursor))) {
		return ErrInvalidTreeSearchPage
	}
	for index := range p.Matches {
		match := p.Matches[index]
		if match.Entry.Validate() != nil || match.Rank.PathBytes != uint32(len(match.Entry.Path)) || match.Rank.Class > 5 || len(match.MatchRanges) == 0 || Count(len(match.MatchRanges)) > policy.TreeSearch.MatchRanges {
			return ErrInvalidTreeSearchPage
		}
		previousEnd := uint32(0)
		for rangeIndex, matchRange := range match.MatchRanges {
			if matchRange.Validate(len(match.Entry.Path)) != nil || rangeIndex > 0 && matchRange.Start < previousEnd {
				return ErrInvalidTreeSearchPage
			}
			previousEnd = matchRange.End
		}
		if index > 0 && CompareTreeSearchMatch(p.Matches[index-1], match) >= 0 {
			return ErrInvalidTreeSearchPage
		}
	}
	return nil
}

// Clone returns a defensive page copy for the application/frontend boundary.
func (p SearchTreePage) Clone() SearchTreePage {
	result := p
	result.Matches = make([]TreeSearchMatch, len(p.Matches))
	for index, match := range p.Matches {
		result.Matches[index] = cloneTreeSearchMatch(match)
	}
	return result
}

func cloneTreeSearchMatch(match TreeSearchMatch) TreeSearchMatch {
	match.Entry.Path = repository.RepoPath(match.Entry.Path.Bytes())
	match.Entry.Name = repository.RepoPath(match.Entry.Name.Bytes())
	match.Entry.Parent = repository.RepoPath(match.Entry.Parent.Bytes())
	match.MatchRanges = append([]repository.ByteRange(nil), match.MatchRanges...)
	return match
}

// CompareTreeSearchMatch orders results by the product ranking tuple and raw
// path bytes. It is independent of terminal presentation or locale.
func CompareTreeSearchMatch(left, right TreeSearchMatch) int {
	if left.Rank.Class != right.Rank.Class {
		return compareUint8(left.Rank.Class, right.Rank.Class)
	}
	if left.Rank.GapBytes != right.Rank.GapBytes {
		return compareUint32(left.Rank.GapBytes, right.Rank.GapBytes)
	}
	if left.Rank.PathBytes != right.Rank.PathBytes {
		return compareUint32(left.Rank.PathBytes, right.Rank.PathBytes)
	}
	return bytes.Compare(left.Entry.Path, right.Entry.Path)
}

func compareUint8(left, right uint8) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareUint32(left, right uint32) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

// MatchTreeEntry applies the v1 path matcher without Unicode normalization.
// ASCII letters fold for comparison; every other byte remains exact.
func MatchTreeEntry(entry repository.TreeEntry, query string) (TreeSearchMatch, bool) {
	path := entry.Path.Bytes()
	needle := []byte(query)
	if len(path) == 0 || len(needle) == 0 {
		return TreeSearchMatch{}, false
	}
	if rank, ranges, ok := exactPath(path, needle); ok {
		return TreeSearchMatch{Entry: cloneTreeSearchEntry(entry), Rank: rank, MatchRanges: ranges}, true
	}
	baseStart := bytes.LastIndexByte(path, '/') + 1
	base := path[baseStart:]
	if len(base) == len(needle) {
		if ranges, ok := contiguousFold(base, needle, baseStart); ok {
			return matched(entry, 1, 0, ranges), true
		}
	}
	if ranges, ok := prefixFold(base, needle, baseStart); ok {
		return matched(entry, 2, 0, ranges), true
	}
	for start := 0; start < len(path); {
		end := bytes.IndexByte(path[start:], '/')
		if end < 0 {
			end = len(path)
		} else {
			end += start
		}
		if ranges, ok := prefixFold(path[start:end], needle, start); ok {
			return matched(entry, 3, 0, ranges), true
		}
		if end == len(path) {
			break
		}
		start = end + 1
	}
	if ranges, ok := contiguousFold(path, needle, 0); ok {
		return matched(entry, 4, 0, ranges), true
	}
	if ranges, gap, ok := subsequenceFold(path, needle); ok {
		return matched(entry, 5, gap, ranges), true
	}
	return TreeSearchMatch{}, false
}

func matched(entry repository.TreeEntry, class uint8, gap uint32, ranges []repository.ByteRange) TreeSearchMatch {
	return TreeSearchMatch{Entry: cloneTreeSearchEntry(entry), Rank: TreeSearchRank{Class: class, GapBytes: gap, PathBytes: uint32(len(entry.Path))}, MatchRanges: ranges}
}

func exactPath(path, needle []byte) (TreeSearchRank, []repository.ByteRange, bool) {
	if len(path) != len(needle) {
		return TreeSearchRank{}, nil, false
	}
	for index := range path {
		if foldASCII(path[index]) != foldASCII(needle[index]) {
			return TreeSearchRank{}, nil, false
		}
	}
	return TreeSearchRank{Class: 0, PathBytes: uint32(len(path))}, []repository.ByteRange{{Start: 0, End: uint32(len(path))}}, true
}

func contiguousFold(haystack, needle []byte, offset int) ([]repository.ByteRange, bool) {
	if len(needle) > len(haystack) {
		return nil, false
	}
	for start := 0; start <= len(haystack)-len(needle); start++ {
		matched := true
		for index := range needle {
			if foldASCII(haystack[start+index]) != foldASCII(needle[index]) {
				matched = false
				break
			}
		}
		if matched {
			return []repository.ByteRange{{Start: uint32(offset + start), End: uint32(offset + start + len(needle))}}, true
		}
	}
	return nil, false
}

func prefixFold(haystack, needle []byte, offset int) ([]repository.ByteRange, bool) {
	if len(needle) > len(haystack) {
		return nil, false
	}
	for index := range needle {
		if foldASCII(haystack[index]) != foldASCII(needle[index]) {
			return nil, false
		}
	}
	return []repository.ByteRange{{Start: uint32(offset), End: uint32(offset + len(needle))}}, true
}

func subsequenceFold(path, needle []byte) ([]repository.ByteRange, uint32, bool) {
	bestGap := ^uint32(0)
	bestStart, bestEnd := -1, -1
	for start := 0; start < len(path); start++ {
		if foldASCII(path[start]) != foldASCII(needle[0]) {
			continue
		}
		pathIndex := start
		end := start
		matched := true
		for _, wanted := range needle[1:] {
			pathIndex++
			for pathIndex < len(path) && foldASCII(path[pathIndex]) != foldASCII(wanted) {
				pathIndex++
			}
			if pathIndex == len(path) {
				matched = false
				break
			}
			end = pathIndex
		}
		if !matched {
			continue
		}
		gap := uint32(end - start + 1 - len(needle))
		if gap < bestGap || gap == bestGap && (bestStart < 0 || start < bestStart) {
			bestGap, bestStart, bestEnd = gap, start, end
			if bestGap == 0 {
				break
			}
		}
	}
	if bestStart < 0 {
		return nil, 0, false
	}
	positions := make([]uint32, 0, len(needle))
	pathIndex := bestStart
	positions = append(positions, uint32(pathIndex))
	for _, wanted := range needle[1:] {
		pathIndex++
		for pathIndex <= bestEnd && foldASCII(path[pathIndex]) != foldASCII(wanted) {
			pathIndex++
		}
		positions = append(positions, uint32(pathIndex))
	}
	ranges := make([]repository.ByteRange, 0, len(positions))
	start := positions[0]
	previous := positions[0]
	for _, position := range positions[1:] {
		if position != previous+1 {
			ranges = append(ranges, repository.ByteRange{Start: start, End: previous + 1})
			start = position
		}
		previous = position
	}
	ranges = append(ranges, repository.ByteRange{Start: start, End: previous + 1})
	return ranges, bestGap, true
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func cloneTreeSearchEntry(entry repository.TreeEntry) repository.TreeEntry {
	entry.Path = repository.RepoPath(entry.Path.Bytes())
	entry.Name = repository.RepoPath(entry.Name.Bytes())
	entry.Parent = repository.RepoPath(entry.Parent.Bytes())
	if entry.ObjectID != nil {
		objectID := *entry.ObjectID
		entry.ObjectID = &objectID
	}
	return entry
}

// SortTreeSearchMatches provides the canonical stable order for an in-memory
// bounded result page. Callers should retain only their configured page size.
func SortTreeSearchMatches(matches []TreeSearchMatch) {
	sort.SliceStable(matches, func(left, right int) bool { return CompareTreeSearchMatch(matches[left], matches[right]) < 0 })
}
