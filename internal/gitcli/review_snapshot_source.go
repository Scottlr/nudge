package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

// ReviewSnapshotSourceConfig supplies the trusted Git process used to read a
// pinned base object tree. It never includes a user-worktree reader.
type ReviewSnapshotSourceConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	StartPath  string
	Policy     MachineGitReadPolicyV1
	Limits     app.ResourcePolicy
}

// GitReviewSnapshotSource implements app.ReviewSnapshotBaseSource using
// ls-tree and cat-file against the accepted immutable base object.
type GitReviewSnapshotSource struct {
	builder    *CommandBuilder
	maxEntries app.Count
	maxRecord  int
	maxFile    app.ByteSize
}

// NewReviewSnapshotSource constructs the pinned-object source.
func NewReviewSnapshotSource(config ReviewSnapshotSourceConfig) (*GitReviewSnapshotSource, error) {
	resourcePolicy := config.Limits
	if resourcePolicy == (app.ResourcePolicy{}) {
		resourcePolicy = app.DefaultResourcePolicy()
	}
	if err := resourcePolicy.Validate(); err != nil {
		return nil, ErrInvalidTreeReader
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	if config.Policy == (MachineGitReadPolicyV1{}) {
		config.Policy = DefaultMachineGitReadPolicyV1()
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{Executable: config.Executable, Runner: config.Runner, StartPath: config.StartPath, Policy: config.Policy})
	if err != nil {
		return nil, err
	}
	return &GitReviewSnapshotSource{builder: builder, maxEntries: resourcePolicy.Artifact.SnapshotEntries, maxRecord: int(resourcePolicy.Input.GitRecordBytes), maxFile: resourcePolicy.Symlink.TrackedBlobBytes}, nil
}

var _ app.ReviewSnapshotBaseSource = (*GitReviewSnapshotSource)(nil)

// ListBase enumerates the pinned tree without consulting the working tree.
func (s *GitReviewSnapshotSource) ListBase(ctx context.Context, base repository.LocalCaptureBase) ([]repository.TreeEntry, error) {
	if s == nil || ctx == nil || base.Validate() != nil || base.ObjectID == "" {
		return nil, ErrInvalidTreeReader
	}
	writer := &snapshotTreeWriter{maxEntries: s.maxEntries, maxRecord: s.maxRecord}
	_, err := s.builder.RunStream(ctx, writer, "ls-tree", "-z", "-r", "-t", "--full-tree", string(base.ObjectID), "--")
	if err != nil {
		return nil, err
	}
	if err := writer.finish(); err != nil {
		return nil, err
	}
	return writer.entries, nil
}

// OpenBase opens one exact blob object from the pinned tree.
func (s *GitReviewSnapshotSource) OpenBase(ctx context.Context, base repository.LocalCaptureBase, entry repository.TreeEntry) (io.ReadCloser, error) {
	if s == nil || ctx == nil || base.Validate() != nil || entry.Validate() != nil || entry.ObjectID == nil || (entry.Kind != repository.FileKindRegular && entry.Kind != repository.FileKindSymlink) {
		return nil, ErrInvalidContentLoader
	}
	writer := newLimitedContentWriter(s.maxFile)
	_, err := s.builder.RunStream(ctx, writer, "cat-file", "blob", string(*entry.ObjectID))
	if err != nil {
		if writer.limited {
			return nil, app.ErrReviewSnapshotLimit
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(writer.Bytes())), nil
}

type snapshotTreeWriter struct {
	pending    []byte
	entries    []repository.TreeEntry
	seen       map[string]struct{}
	maxEntries app.Count
	maxRecord  int
	err        error
}

func (w *snapshotTreeWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.seen == nil {
		w.seen = make(map[string]struct{})
	}
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, 0)
		if index < 0 {
			if len(w.pending) > w.maxRecord {
				w.err = ErrTreeLimit
			}
			break
		}
		record := append([]byte(nil), w.pending[:index]...)
		w.pending = w.pending[index+1:]
		if len(record) == 0 {
			continue
		}
		if len(record) > w.maxRecord {
			w.err = ErrTreeLimit
			break
		}
		parsed, err := parseTreeRecord(record)
		if err != nil {
			w.err = err
			break
		}
		entry, err := newTreeEntry(parsed.Path, parsed.Kind, parsed.Mode, parsed.ObjectID, nil)
		if err != nil {
			w.err = err
			break
		}
		key := string(entry.Path.Bytes())
		if _, ok := w.seen[key]; ok {
			w.err = errors.New("duplicate pinned tree path")
			break
		}
		if app.Count(len(w.entries)) >= w.maxEntries {
			w.err = ErrTreeLimit
			break
		}
		w.seen[key] = struct{}{}
		w.entries = append(w.entries, entry)
	}
	if w.err != nil {
		return len(data), w.err
	}
	return len(data), nil
}

func (w *snapshotTreeWriter) finish() error {
	if w.err != nil {
		return w.err
	}
	if len(w.pending) != 0 {
		return errInvalidTreeOutput
	}
	return nil
}
