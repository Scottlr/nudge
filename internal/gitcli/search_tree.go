package gitcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

const treeSearchCursorVersion uint32 = 1

var (
	// ErrInvalidTreeSearcher reports incomplete trusted-Git composition.
	ErrInvalidTreeSearcher = errors.New("invalid tree searcher")
	// ErrTreeSearchCursor reports a cursor bound to another search contract.
	ErrTreeSearchCursor = errors.New("invalid tree search cursor")
	// ErrTreeSearchLimit reports an immutable enumeration beyond the accepted
	// T070 tree-search ceiling.
	ErrTreeSearchLimit = errors.New("tree search entry limit exceeded")
	// ErrTreeSearchSnapshotUnavailable reports that no immutable source was
	// supplied for a working-tree snapshot.
	ErrTreeSearchSnapshotUnavailable = errors.New("immutable working-tree search source unavailable")
)

// ImmutableTreeEnumerator is the capture-backed seam for working-tree search.
// The callback must enumerate accepted immutable metadata, never live paths or
// file contents. Git object snapshots do not need this callback.
type ImmutableTreeEnumerator func(context.Context, repository.SnapshotRef, func(repository.TreeEntry) error) error

// TreeSearcherConfig supplies trusted Git and the immutable working-tree
// source used by GitTreeSearcher.
type TreeSearcherConfig struct {
	Executable  process.ExecutableIdentity
	Runner      process.Runner
	StartPath   string
	Policy      MachineGitReadPolicyV1
	Limits      app.ResourcePolicy
	WorkingTree ImmutableTreeEnumerator
}

// GitTreeSearcher streams one immutable tree source, retaining only the
// bounded result page and its cursor candidate.
type GitTreeSearcher struct {
	builder     *CommandBuilder
	limits      app.ResourcePolicy
	workingTree ImmutableTreeEnumerator
}

// NewTreeSearcher constructs a bounded Git-backed tree searcher.
func NewTreeSearcher(config TreeSearcherConfig) (*GitTreeSearcher, error) {
	policy := config.Policy
	if policy == (MachineGitReadPolicyV1{}) {
		policy = DefaultMachineGitReadPolicyV1()
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	limits := config.Limits
	if limits == (app.ResourcePolicy{}) {
		limits = app.DefaultResourcePolicy()
	}
	if limits.Validate() != nil || config.StartPath == "" {
		return nil, ErrInvalidTreeSearcher
	}
	root, err := canonicalExistingDirectory(config.StartPath)
	if err != nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{Executable: config.Executable, Runner: config.Runner, StartPath: root, Policy: policy})
	if err != nil {
		return nil, err
	}
	return &GitTreeSearcher{builder: builder, limits: limits, workingTree: config.WorkingTree}, nil
}

var _ app.TreeSearcher = (*GitTreeSearcher)(nil)

// SearchTree searches every accepted metadata entry in one immutable source.
// It completes the bounded scan before publishing a page so cancellation or
// deadline can never be mistaken for a complete deterministic result.
func (s *GitTreeSearcher) SearchTree(ctx context.Context, request app.SearchTreeQuery) (app.SearchTreePage, error) {
	if s == nil || ctx == nil {
		return app.SearchTreePage{}, ErrInvalidTreeSearcher
	}
	query, err := request.Normalize(s.limits)
	if err != nil {
		return app.SearchTreePage{}, err
	}
	searchCtx, cancel := context.WithTimeout(ctx, s.limits.TreeSearch.Deadline)
	defer cancel()
	cursor, err := decodeSearchCursor(query, s.limits)
	if err != nil {
		return app.SearchTreePage{}, err
	}
	matches := make([]app.TreeSearchMatch, 0, query.Limit+1)
	var scanned uint64
	add := func(entry repository.TreeEntry) error {
		if err := searchCtx.Err(); err != nil {
			return &app.TreeSearchIncompleteError{Scanned: scanned, Cause: err}
		}
		if scanned >= uint64(s.limits.TreeSearch.EntryCeiling) {
			return ErrTreeSearchLimit
		}
		scanned++
		match, ok := app.MatchTreeEntry(entry, query.Query)
		if !ok || cursor != nil && compareToCursor(match, cursor) <= 0 {
			return nil
		}
		matches = append(matches, match)
		app.SortTreeSearchMatches(matches)
		if len(matches) > query.Limit+1 {
			matches = matches[:query.Limit+1]
		}
		return nil
	}
	if err := s.enumerate(searchCtx, query.Snapshot, add); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return app.SearchTreePage{}, &app.TreeSearchIncompleteError{Scanned: scanned, Cause: err}
		}
		return app.SearchTreePage{}, err
	}
	if err := searchCtx.Err(); err != nil {
		return app.SearchTreePage{}, &app.TreeSearchIncompleteError{Scanned: scanned, Cause: err}
	}
	app.SortTreeSearchMatches(matches)
	page := app.SearchTreePage{Snapshot: query.Snapshot, ScannedEntries: scanned, Complete: true}
	if len(matches) > query.Limit {
		page.Matches = append(page.Matches, matches[:query.Limit]...)
		last := page.Matches[len(page.Matches)-1]
		page.NextCursor, err = encodeSearchCursor(query, last, s.limits)
		if err != nil {
			return app.SearchTreePage{}, err
		}
	} else {
		page.Matches = append(page.Matches, matches...)
	}
	if err := app.ValidateSearchTreeResult(query, page, s.limits); err != nil {
		return app.SearchTreePage{}, err
	}
	return page, nil
}

func (s *GitTreeSearcher) enumerate(ctx context.Context, snapshot repository.SnapshotRef, add func(repository.TreeEntry) error) error {
	switch snapshot.Kind {
	case repository.SnapshotEmpty:
		return nil
	case repository.SnapshotWorkingTree:
		if s.workingTree == nil {
			return ErrTreeSearchSnapshotUnavailable
		}
		return s.workingTree(ctx, snapshot, add)
	case repository.SnapshotCommit, repository.SnapshotTree:
		if snapshot.ObjectID == "" {
			return ErrInvalidTreeSearcher
		}
		var writer *nulRecordWriter
		writer = newNULRecordWriter(int(s.limits.Input.GitRecordBytes), func(record []byte) {
			tree, parseErr := parseTreeRecord(record)
			if parseErr != nil {
				writer.setError(parseErr)
				return
			}
			entry, entryErr := newTreeEntry(tree.Path, tree.Kind, tree.Mode, tree.ObjectID, nil)
			if entryErr != nil {
				writer.setError(entryErr)
				return
			}
			if addErr := add(entry); addErr != nil {
				writer.setError(addErr)
			}
		})
		if err := s.runNULStream(ctx, writer, "ls-tree", "-z", "-r", "-t", "--full-tree", string(snapshot.ObjectID), "--"); err != nil {
			return err
		}
		return nil
	default:
		return ErrInvalidTreeSearcher
	}
}

func (s *GitTreeSearcher) runNULStream(ctx context.Context, writer *nulRecordWriter, args ...string) error {
	if _, err := s.builder.RunStream(ctx, writer, args...); err != nil {
		return err
	}
	if err := writer.finish(); err != nil {
		return err
	}
	return writer.err
}

type treeSearchCursor struct {
	Version        uint32
	PolicyVersion  app.ResourcePolicyVersion
	RankingVersion uint32
	Snapshot       repository.SnapshotRef
	QueryHash      string
	Limit          int
	Last           app.TreeSearchRank
	LastPath       []byte
}

func encodeSearchCursor(query app.SearchTreeQuery, last app.TreeSearchMatch, policy app.ResourcePolicy) (string, error) {
	value := treeSearchCursor{Version: treeSearchCursorVersion, PolicyVersion: policy.Version, RankingVersion: app.TreeSearchRankingVersion, Snapshot: query.Snapshot, QueryHash: query.QueryHash(), Limit: query.Limit, Last: last.Rank, LastPath: last.Entry.Path.Bytes()}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	result := base64.RawURLEncoding.EncodeToString(encoded)
	if app.ByteSize(len(result)) > policy.TreeSearch.CursorBytes {
		return "", app.ErrLimitExceeded
	}
	return result, nil
}

func decodeSearchCursor(query app.SearchTreeQuery, policy app.ResourcePolicy) (*treeSearchCursor, error) {
	if query.Cursor == "" {
		return nil, nil
	}
	if app.ByteSize(len(query.Cursor)) > policy.TreeSearch.CursorBytes {
		return nil, ErrTreeSearchCursor
	}
	encoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return nil, ErrTreeSearchCursor
	}
	var cursor treeSearchCursor
	if json.Unmarshal(encoded, &cursor) != nil || cursor.Version != treeSearchCursorVersion || cursor.PolicyVersion != policy.Version || cursor.RankingVersion != app.TreeSearchRankingVersion || cursor.QueryHash != query.QueryHash() || cursor.Limit != query.Limit || !sameSnapshot(cursor.Snapshot, query.Snapshot) || len(cursor.LastPath) == 0 {
		return nil, ErrTreeSearchCursor
	}
	if cursor.Last.PathBytes != uint32(len(cursor.LastPath)) || cursor.Last.Class > 5 {
		return nil, ErrTreeSearchCursor
	}
	return &cursor, nil
}

func compareToCursor(match app.TreeSearchMatch, cursor *treeSearchCursor) int {
	return compareSearchRankPath(match.Rank, match.Entry.Path.Bytes(), cursor.Last, cursor.LastPath)
}

func compareSearchRankPath(leftRank app.TreeSearchRank, leftPath []byte, rightRank app.TreeSearchRank, rightPath []byte) int {
	if leftRank.Class != rightRank.Class {
		return compareUint8Search(leftRank.Class, rightRank.Class)
	}
	if leftRank.GapBytes != rightRank.GapBytes {
		return compareUint32Search(leftRank.GapBytes, rightRank.GapBytes)
	}
	if leftRank.PathBytes != rightRank.PathBytes {
		return compareUint32Search(leftRank.PathBytes, rightRank.PathBytes)
	}
	return bytesCompareSearch(leftPath, rightPath)
}

func compareUint8Search(left, right uint8) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareUint32Search(left, right uint32) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func bytesCompareSearch(left, right []byte) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}
