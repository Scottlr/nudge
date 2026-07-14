package gitcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

const treeCursorVersion uint32 = 1

var (
	// ErrInvalidTreeReader reports incomplete tree-adapter composition.
	ErrInvalidTreeReader = errors.New("invalid tree reader")
	// ErrTreeCursor reports a cursor that is stale or bound to another query.
	ErrTreeCursor = errors.New("invalid tree cursor")
	// ErrTreeLimit reports a bounded tree metadata result that exceeds policy.
	ErrTreeLimit         = errors.New("tree metadata limit exceeded")
	errInvalidTreeOutput = errors.New("invalid tree output")
)

func malformedTree(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidTreeOutput, fmt.Sprintf(format, args...))
}

// TreeReaderConfig supplies the trusted Git process and bounded metadata
// policy used by GitTreeReader.
type TreeReaderConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	StartPath  string
	Policy     MachineGitReadPolicyV1
	Limits     app.ResourcePolicy
}

// GitTreeReader enumerates immutable Git trees and one local working-tree
// projection. It never opens or reads file content.
type GitTreeReader struct {
	builder *CommandBuilder
	root    string
	limits  app.ResourcePolicy
	cache   treePageCache
}

// NewTreeReader constructs a bounded Git-backed tree reader.
func NewTreeReader(config TreeReaderConfig) (*GitTreeReader, error) {
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
		return nil, ErrInvalidTreeReader
	}
	root, err := canonicalExistingDirectory(config.StartPath)
	if err != nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{
		Executable: config.Executable,
		Runner:     config.Runner,
		StartPath:  root,
		Policy:     policy,
	})
	if err != nil {
		return nil, err
	}
	return &GitTreeReader{builder: builder, root: root, limits: limits, cache: newTreePageCache(limits.MetadataCache)}, nil
}

var _ app.TreeReader = (*GitTreeReader)(nil)

// ListTree returns one immediate-child page bound to the target head and
// cursor query. The adapter never expands submodules or loads file bytes.
func (r *GitTreeReader) ListTree(ctx context.Context, target repository.ResolvedTarget, query app.TreeQuery) (app.TreePage, error) {
	if r == nil || ctx == nil {
		return app.TreePage{}, ErrInvalidTreeReader
	}
	if err := target.Validate(); err != nil {
		return app.TreePage{}, ErrInvalidTreeReader
	}
	query, err := query.Normalize(r.limits)
	if err != nil {
		return app.TreePage{}, err
	}
	lastPath, err := r.validateCursor(target, query)
	if err != nil {
		return app.TreePage{}, err
	}
	key := treeCacheKeyFor(target, query)
	if page, ok := r.cache.get(key); ok {
		return page, nil
	}
	var page app.TreePage
	if query.Filter == app.TreeFilterChanged {
		page, err = r.listChanged(ctx, target, query, lastPath)
	} else {
		page, err = r.listAll(ctx, target, query, lastPath)
	}
	if err != nil {
		return app.TreePage{}, err
	}
	if err := page.Validate(); err != nil {
		return app.TreePage{}, err
	}
	r.cache.put(key, page)
	return page, nil
}

func (r *GitTreeReader) validateCursor(target repository.ResolvedTarget, query app.TreeQuery) (*repository.RepoPath, error) {
	if query.Cursor == "" {
		return nil, nil
	}
	cursor, err := decodeTreeCursor(query.Cursor)
	if err != nil || cursor.Version != treeCursorVersion || cursor.Generation != uint64(target.Generation) || cursor.TargetFingerprint != target.Fingerprint || cursor.Filter != string(query.Filter) || cursor.Limit != query.Limit {
		return nil, ErrTreeCursor
	}
	if !sameSnapshot(cursor.Snapshot, target.Head) || cursor.Parent != encodedPath(query.ParentPath) || cursor.LastPath == "" {
		return nil, ErrTreeCursor
	}
	path, err := decodePath(cursor.LastPath)
	if err != nil {
		return nil, ErrTreeCursor
	}
	return &path, nil
}

func (r *GitTreeReader) listAll(ctx context.Context, target repository.ResolvedTarget, query app.TreeQuery, lastPath *repository.RepoPath) (app.TreePage, error) {
	acc := newTreePageAccumulator(query, lastPath, r.limits.MetadataCache)
	if target.Head.Kind == repository.SnapshotEmpty {
		return finishTreePage(target, query, acc)
	}
	switch target.Head.Kind {
	case repository.SnapshotCommit, repository.SnapshotTree:
		if target.Head.ObjectID == "" {
			return app.TreePage{}, ErrTreeLimit
		}
		var writer *nulRecordWriter
		writer = newNULRecordWriter(int(r.limits.Input.GitRecordBytes), func(record []byte) {
			tree, parseErr := parseTreeRecord(record)
			if parseErr != nil {
				writer.setError(parseErr)
				return
			}
			entry, direct, entryErr := treeEntryForRecord(tree, query.ParentPath, nil)
			if entryErr != nil {
				writer.setError(entryErr)
				return
			}
			if direct {
				acc.add(entry)
			}
		})
		args := []string{"ls-tree", "-z", "-r", "-t", "--full-tree", string(target.Head.ObjectID), "--"}
		if err := r.runNULStream(ctx, writer, args...); err != nil {
			return app.TreePage{}, err
		}
	case repository.SnapshotWorkingTree:
		return r.listWorkingTree(ctx, target, query, lastPath)
	default:
		return app.TreePage{}, ErrInvalidTreeReader
	}
	return finishTreePage(target, query, acc)
}

func (r *GitTreeReader) listChanged(ctx context.Context, target repository.ResolvedTarget, query app.TreeQuery, lastPath *repository.RepoPath) (app.TreePage, error) {
	changes, err := r.changedFiles(ctx, target)
	if err != nil {
		return app.TreePage{}, err
	}
	entries, err := changedTreeEntries(changes)
	if err != nil {
		return app.TreePage{}, err
	}
	acc := newTreePageAccumulator(query, lastPath, r.limits.MetadataCache)
	for _, entry := range entries {
		acc.add(entry)
	}
	return finishTreePage(target, query, acc)
}

func (r *GitTreeReader) changedFiles(ctx context.Context, target repository.ResolvedTarget) ([]repository.ChangedFile, error) {
	if target.Head.Kind == repository.SnapshotWorkingTree {
		status, err := r.builder.Run(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--renames", "--ignore-submodules=all")
		if err != nil {
			return nil, err
		}
		untracked, err := r.builder.Run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		records, _, err := parseStatusRecords(status.Stdout, untracked.Stdout)
		if err != nil {
			return nil, err
		}
		changes := make([]repository.ChangedFile, 0, len(records))
		for index := range records {
			change := &records[index].entry.Change
			if records[index].untracked && change.NewPath != nil {
				kind, mode, observeErr := observeWorkingPath(r.root, *change.NewPath)
				if observeErr != nil {
					return nil, observeErr
				}
				change.NewFileKind, change.NewMode = kind, mode
			}
			if err := change.Validate(); err != nil {
				return nil, fmt.Errorf("%w: %v", errInvalidTreeOutput, err)
			}
			changes = append(changes, *cloneChangedFile(change))
		}
		sort.SliceStable(changes, func(i, j int) bool { return changeDisplayPath(changes[i]) < changeDisplayPath(changes[j]) })
		return changes, nil
	}
	if target.Base.ObjectID == "" || target.Head.ObjectID == "" {
		return nil, ErrInvalidTreeReader
	}
	result, err := r.builder.Run(ctx, "diff", "--raw", "-z", "--full-index", "--find-renames", string(target.Base.ObjectID), string(target.Head.ObjectID), "--")
	if err != nil {
		return nil, err
	}
	return parseRawDiff(result.Stdout)
}

func (r *GitTreeReader) listWorkingTree(ctx context.Context, target repository.ResolvedTarget, query app.TreeQuery, lastPath *repository.RepoPath) (app.TreePage, error) {
	changes, err := r.changedFiles(ctx, target)
	if err != nil {
		return app.TreePage{}, err
	}
	changedByPath := make(map[string]*repository.ChangedFile, len(changes))
	deleted := make([]repository.ChangedFile, 0)
	for index := range changes {
		change := &changes[index]
		changedByPath[changeDisplayPath(*change)] = cloneChangedFile(change)
		if change.Kind == repository.ChangeDeleted {
			deleted = append(deleted, *cloneChangedFile(change))
		}
	}
	deletedEntries, err := changedTreeEntries(deleted)
	if err != nil {
		return app.TreePage{}, err
	}
	tracked := newTreePageAccumulator(query, lastPath, r.limits.MetadataCache)
	untracked := newTreePageAccumulator(query, lastPath, r.limits.MetadataCache)
	deletedPage := newTreePageAccumulator(query, lastPath, r.limits.MetadataCache)
	addRecord := func(acc *treePageAccumulator, parsed lsFilesRecord) error {
		mode := parsed.Mode
		kind := fileKindFromGitMode(mode)
		if mode == 0 {
			var observeErr error
			kind, mode, observeErr = observeWorkingPath(r.root, parsed.Path)
			if observeErr != nil {
				return observeErr
			}
		}
		changed := changedByPath[string(parsed.Path)]
		entry, entryErr := newTreeEntry(parsed.Path, kind, mode, parsed.ObjectID, changed)
		if entryErr != nil {
			return entryErr
		}
		acc.add(entry)
		pathValue := parsed.Path
		for start := 0; ; {
			slash := bytes.IndexByte(pathValue[start:], '/')
			if slash < 0 {
				break
			}
			end := start + slash
			ancestor, ancestorErr := newTreeEntry(repository.RepoPath(pathValue[:end]), repository.FileKindDirectory, 0o40000, nil, nil)
			if ancestorErr != nil {
				return ancestorErr
			}
			acc.add(ancestor)
			start = end + 1
		}
		return acc.err
	}
	var trackedWriter *nulRecordWriter
	trackedWriter = newNULRecordWriter(int(r.limits.Input.GitRecordBytes), func(record []byte) {
		parsed, parseErr := parseLSFilesRecord(record)
		if parseErr != nil {
			trackedWriter.setError(parseErr)
			return
		}
		if err := addRecord(tracked, parsed); err != nil {
			trackedWriter.setError(err)
		}
	})
	if err := r.runNULStream(ctx, trackedWriter, "ls-files", "-z", "--cached", "--stage"); err != nil {
		return app.TreePage{}, err
	}
	var untrackedWriter *nulRecordWriter
	untrackedWriter = newNULRecordWriter(int(r.limits.Input.GitRecordBytes), func(record []byte) {
		parsed, parseErr := parseLSFilesRecord(record)
		if parseErr != nil {
			untrackedWriter.setError(parseErr)
			return
		}
		if err := addRecord(untracked, parsed); err != nil {
			untrackedWriter.setError(err)
		}
	})
	if err := r.runNULStream(ctx, untrackedWriter, "ls-files", "--others", "--exclude-standard", "-z"); err != nil {
		return app.TreePage{}, err
	}
	for _, entry := range deletedEntries {
		deletedPage.add(entry)
	}
	merged, hasMore, err := mergeTreePageSources(query.Limit,
		treePageSource{entries: tracked.entries, more: len(tracked.entries) > query.Limit},
		treePageSource{entries: untracked.entries, more: len(untracked.entries) > query.Limit},
		treePageSource{entries: deletedPage.entries, more: len(deletedPage.entries) > query.Limit},
	)
	if err != nil {
		return app.TreePage{}, err
	}
	return finishTreeEntries(target, query, merged, hasMore)
}

func (r *GitTreeReader) runNULStream(ctx context.Context, writer *nulRecordWriter, args ...string) error {
	if _, err := r.builder.RunStream(ctx, writer, args...); err != nil {
		return err
	}
	if err := writer.finish(); err != nil {
		return err
	}
	return writer.err
}

func finishTreePage(target repository.ResolvedTarget, query app.TreeQuery, acc *treePageAccumulator) (app.TreePage, error) {
	entries, hasMore, err := acc.page()
	if err != nil {
		return app.TreePage{}, err
	}
	return finishTreeEntries(target, query, entries, hasMore)
}

func finishTreeEntries(target repository.ResolvedTarget, query app.TreeQuery, entries []repository.TreeEntry, hasMore bool) (app.TreePage, error) {
	page := app.TreePage{Entries: entries, Snapshot: target.Head}
	if hasMore {
		if len(entries) == 0 {
			return app.TreePage{}, ErrTreeLimit
		}
		last := entries[len(entries)-1].Path
		cursor, err := encodeTreeCursor(treeCursor{Version: treeCursorVersion, Generation: uint64(target.Generation), TargetFingerprint: target.Fingerprint, Snapshot: target.Head, Parent: encodedPath(query.ParentPath), Filter: string(query.Filter), Limit: query.Limit, LastPath: encodedPath(&last)})
		if err != nil {
			return app.TreePage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

type treePageSource struct {
	entries []repository.TreeEntry
	more    bool
}

func mergeTreePageSources(limit int, sources ...treePageSource) ([]repository.TreeEntry, bool, error) {
	if limit <= 0 {
		return nil, false, ErrTreeLimit
	}
	all := make([]repository.TreeEntry, 0)
	more := false
	for _, source := range sources {
		all = append(all, source.entries...)
		more = more || source.more
	}
	sort.SliceStable(all, func(i, j int) bool { return bytes.Compare(all[i].Path, all[j].Path) < 0 })
	merged := make([]repository.TreeEntry, 0, len(all))
	for _, entry := range all {
		if len(merged) > 0 && bytes.Equal(merged[len(merged)-1].Path, entry.Path) {
			merged[len(merged)-1] = mergeTreeEntry(merged[len(merged)-1], entry)
			continue
		}
		merged = append(merged, entry)
	}
	if len(merged) > limit {
		more = true
		merged = merged[:limit]
	}
	result := make([]repository.TreeEntry, len(merged))
	for index := range merged {
		result[index] = cloneGitTreeEntry(merged[index])
	}
	return result, more, nil
}

type treeCursor struct {
	Version           uint32
	Generation        uint64
	TargetFingerprint string
	Snapshot          repository.SnapshotRef
	Parent            string
	Filter            string
	Limit             int
	LastPath          string
}

func encodeTreeCursor(cursor treeCursor) (string, error) {
	if cursor.Version != treeCursorVersion || cursor.Generation == 0 || cursor.Filter == "" || cursor.Limit <= 0 || cursor.LastPath == "" {
		return "", ErrTreeCursor
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", ErrTreeCursor
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeTreeCursor(value string) (treeCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return treeCursor{}, ErrTreeCursor
	}
	var cursor treeCursor
	if json.Unmarshal(data, &cursor) != nil {
		return treeCursor{}, ErrTreeCursor
	}
	return cursor, nil
}

func encodedPath(path *repository.RepoPath) string {
	if path == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(path.Bytes())
}

func decodePath(value string) (repository.RepoPath, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return repository.NewRepoPath(data)
}

func sameSnapshot(left, right repository.SnapshotRef) bool {
	return left.Kind == right.Kind && left.ObjectID == right.ObjectID && left.WorktreeID == right.WorktreeID && left.Fingerprint == right.Fingerprint
}

type treePageAccumulator struct {
	query       app.TreeQuery
	lastPath    *repository.RepoPath
	entries     []repository.TreeEntry
	lastOrdered repository.RepoPath
	maxBytes    app.ByteSize
	bytes       app.ByteSize
	err         error
}

func newTreePageAccumulator(query app.TreeQuery, lastPath *repository.RepoPath, limits app.MetadataCacheLimits) *treePageAccumulator {
	var copyLast *repository.RepoPath
	if lastPath != nil {
		value := repository.RepoPath(lastPath.Bytes())
		copyLast = &value
	}
	return &treePageAccumulator{query: query, lastPath: copyLast, maxBytes: limits.MaxBytes}
}

func (a *treePageAccumulator) add(entry repository.TreeEntry) {
	if a.err != nil {
		return
	}
	projected, direct, synthetic := treeEntryForQuery(entry, a.query.ParentPath)
	if !direct {
		return
	}
	if synthetic {
		projected, _ = newTreeEntry(projected.Path, repository.FileKindDirectory, 0o40000, nil, nil)
	}
	if entryErr := projected.Validate(); entryErr != nil {
		a.err = entryErr
		return
	}
	if len(a.lastOrdered) > 0 && bytes.Compare(projected.Path, a.lastOrdered) < 0 {
		a.err = errInvalidTreeOutput
		return
	}
	if len(a.lastOrdered) == 0 || !bytes.Equal(projected.Path, a.lastOrdered) {
		a.lastOrdered = repository.RepoPath(projected.Path.Bytes())
		if a.lastPath != nil && bytes.Compare(projected.Path, *a.lastPath) <= 0 {
			return
		}
		if len(a.entries) < a.query.Limit+1 {
			a.bytes += treeEntryBytes(projected)
			if a.bytes > a.maxBytes {
				a.err = ErrTreeLimit
				return
			}
			a.entries = append(a.entries, projected)
		}
		return
	}
	if len(a.entries) == 0 {
		return
	}
	last := &a.entries[len(a.entries)-1]
	if bytes.Equal(last.Path, projected.Path) {
		*last = mergeTreeEntry(*last, projected)
	}
}

func (a *treePageAccumulator) page() ([]repository.TreeEntry, bool, error) {
	if a.err != nil {
		return nil, false, a.err
	}
	hasMore := len(a.entries) > a.query.Limit
	if hasMore {
		a.entries = a.entries[:a.query.Limit]
	}
	result := make([]repository.TreeEntry, len(a.entries))
	for index := range a.entries {
		result[index] = cloneGitTreeEntry(a.entries[index])
	}
	return result, hasMore, nil
}

func treeEntryForQuery(entry repository.TreeEntry, parent *repository.RepoPath) (repository.TreeEntry, bool, bool) {
	child, direct, synthetic := treeChildPath(entry.Path, parent)
	if !direct {
		return repository.TreeEntry{}, false, false
	}
	if synthetic {
		return repository.TreeEntry{Path: child, Kind: repository.FileKindDirectory, Mode: 0o40000, LazyChild: true}, true, true
	}
	entry.Path = child
	return entry, true, false
}

func mergeTreeEntry(left, right repository.TreeEntry) repository.TreeEntry {
	if left.ChangedSummary == nil && right.ChangedSummary != nil {
		left.ChangedSummary = cloneChangedFile(right.ChangedSummary)
	}
	if left.ObjectID == nil && right.ObjectID != nil {
		left.ObjectID = cloneObjectID(right.ObjectID)
	}
	if right.Kind == repository.FileKindDirectory {
		left.Kind, left.Mode, left.LazyChild = right.Kind, right.Mode, true
	}
	return left
}

func treeEntryBytes(entry repository.TreeEntry) app.ByteSize {
	bytes := len(entry.Path) + len(entry.Name) + len(entry.Parent) + 48
	if entry.ObjectID != nil {
		bytes += len(*entry.ObjectID)
	}
	if entry.ChangedSummary != nil {
		if entry.ChangedSummary.OldPath != nil {
			bytes += len(*entry.ChangedSummary.OldPath)
		}
		if entry.ChangedSummary.NewPath != nil {
			bytes += len(*entry.ChangedSummary.NewPath)
		}
	}
	return app.ByteSize(bytes)
}

type nulRecordWriter struct {
	max      int
	pending  []byte
	onRecord func([]byte)
	err      error
	finished bool
}

func newNULRecordWriter(max int, onRecord func([]byte)) *nulRecordWriter {
	return &nulRecordWriter{max: max, onRecord: onRecord}
}

func (w *nulRecordWriter) Write(value []byte) (int, error) {
	if w == nil || w.finished {
		return 0, errors.New("tree stream closed")
	}
	w.pending = append(w.pending, value...)
	if len(w.pending) > w.max && bytes.IndexByte(w.pending, 0) < 0 {
		w.err = ErrTreeLimit
		return len(value), nil
	}
	for {
		separator := bytes.IndexByte(w.pending, 0)
		if separator < 0 {
			break
		}
		record := append([]byte(nil), w.pending[:separator]...)
		w.pending = w.pending[separator+1:]
		if len(record) > w.max {
			w.err = ErrTreeLimit
			continue
		}
		if w.onRecord != nil && w.err == nil {
			w.onRecord(record)
		}
	}
	return len(value), nil
}

func (w *nulRecordWriter) setError(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *nulRecordWriter) finish() error {
	if w == nil {
		return errInvalidTreeOutput
	}
	w.finished = true
	if len(w.pending) != 0 && w.err == nil {
		w.err = fmt.Errorf("%w: unterminated NUL record", errInvalidTreeOutput)
	}
	return w.err
}

func changeDisplayPath(change repository.ChangedFile) string {
	if change.NewPath != nil {
		return string(*change.NewPath)
	}
	if change.OldPath != nil {
		return string(*change.OldPath)
	}
	return ""
}

func changedTreeEntries(changes []repository.ChangedFile) ([]repository.TreeEntry, error) {
	entries := make(map[string]repository.TreeEntry, len(changes)*2)
	for index := range changes {
		change := &changes[index]
		path := change.NewPath
		kind, mode, objectID := change.NewFileKind, change.NewMode, change.NewObjectID
		if path == nil {
			path = change.OldPath
			kind, mode, objectID = change.OldFileKind, change.OldMode, change.OldObjectID
		}
		if path == nil {
			return nil, errInvalidTreeOutput
		}
		entry, err := newTreeEntry(*path, kind, mode, objectID, change)
		if err != nil {
			return nil, err
		}
		entries[string(entry.Path)] = entry
		pathValue := *path
		for start := 0; ; {
			slash := bytes.IndexByte(pathValue[start:], '/')
			if slash < 0 {
				break
			}
			end := start + slash
			ancestor := repository.RepoPath(pathValue[:end])
			if _, exists := entries[string(ancestor)]; !exists {
				directory, dirErr := newTreeEntry(ancestor, repository.FileKindDirectory, 0o40000, nil, nil)
				if dirErr != nil {
					return nil, dirErr
				}
				entries[string(ancestor)] = directory
			}
			start = end + 1
		}
	}
	result := make([]repository.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i].Path, result[j].Path) < 0 })
	return result, nil
}

type treeCacheEntry struct {
	page  app.TreePage
	bytes app.ByteSize
	seq   uint64
}

type treePageCache struct {
	mu         sync.Mutex
	entries    map[string]treeCacheEntry
	limits     app.MetadataCacheLimits
	bytes      app.ByteSize
	entryCount app.Count
	sequence   uint64
}

func newTreePageCache(limits app.MetadataCacheLimits) treePageCache {
	return treePageCache{entries: make(map[string]treeCacheEntry), limits: limits}
}

func (c *treePageCache) get(key string) (app.TreePage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return app.TreePage{}, false
	}
	c.sequence++
	entry.seq = c.sequence
	c.entries[key] = entry
	return entry.page.Clone(), true
}

func (c *treePageCache) put(key string, page app.TreePage) {
	value := page.Clone()
	bytesUsed := app.ByteSize(0)
	for _, entry := range value.Entries {
		bytesUsed += treeEntryBytes(entry)
	}
	if bytesUsed > c.limits.MaxBytes || app.Count(len(value.Entries)) > c.limits.MaxEntries {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.bytes
		c.entryCount -= app.Count(len(previous.page.Entries))
	}
	c.sequence++
	c.entries[key] = treeCacheEntry{page: value, bytes: bytesUsed, seq: c.sequence}
	c.bytes += bytesUsed
	c.entryCount += app.Count(len(value.Entries))
	for c.bytes > c.limits.MaxBytes || c.entryCount > c.limits.MaxEntries {
		oldKey := ""
		var oldSeq uint64
		for candidateKey, candidate := range c.entries {
			if candidateKey == key && len(c.entries) == 1 {
				oldKey = ""
				break
			}
			if oldKey == "" || candidate.seq < oldSeq {
				oldKey, oldSeq = candidateKey, candidate.seq
			}
		}
		if oldKey == "" {
			break
		}
		old := c.entries[oldKey]
		delete(c.entries, oldKey)
		c.bytes -= old.bytes
		c.entryCount -= app.Count(len(old.page.Entries))
	}
}

func treeCacheKeyFor(target repository.ResolvedTarget, query app.TreeQuery) string {
	key := struct {
		Generation uint64
		Target     string
		Head       repository.SnapshotRef
		Parent     string
		Filter     app.TreeFilter
		Cursor     string
		Limit      int
	}{uint64(target.Generation), target.Fingerprint, target.Head, encodedPath(query.ParentPath), query.Filter, query.Cursor, query.Limit}
	data, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(data)
}
