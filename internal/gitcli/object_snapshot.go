package gitcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

// ObjectSnapshotSourceConfig supplies the trusted Git process and bounds for
// enumerating one pinned commit tree.
type ObjectSnapshotSourceConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	StartPath  string
	Policy     MachineGitReadPolicyV1
	MaxEntries app.Count
}

// GitObjectSnapshotSource reads only the object tree named by LocalCaptureBase
// and never falls back to a worktree path, index, ref, or network fetch.
type GitObjectSnapshotSource struct {
	builder    *CommandBuilder
	maxEntries app.Count
}

// NewObjectSnapshotSource constructs a pinned object-tree source for review
// snapshot materialization.
func NewObjectSnapshotSource(config ObjectSnapshotSourceConfig) (*GitObjectSnapshotSource, error) {
	policy := config.Policy
	if policy == (MachineGitReadPolicyV1{}) {
		policy = DefaultMachineGitReadPolicyV1()
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{Executable: config.Executable, Runner: config.Runner, StartPath: config.StartPath, Policy: policy})
	if err != nil {
		return nil, err
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = app.DefaultResourcePolicy().Artifact.SnapshotEntries
	}
	return &GitObjectSnapshotSource{builder: builder, maxEntries: config.MaxEntries}, nil
}

var _ app.ReviewSnapshotBaseSource = (*GitObjectSnapshotSource)(nil)

// ListBase enumerates the complete pinned tree, preserving file-kind and mode
// evidence for the workspace owner to qualify before publication.
func (s *GitObjectSnapshotSource) ListBase(ctx context.Context, base repository.LocalCaptureBase) ([]repository.TreeEntry, error) {
	if s == nil || ctx == nil || base.Validate() != nil {
		return nil, &GitError{Code: ErrorInvalidInput}
	}
	result, err := s.builder.Run(ctx, "ls-tree", "-z", "-r", "-t", "--full-tree", string(base.ObjectID), "--")
	if err != nil {
		return nil, objectUnavailable(err)
	}
	entries := make([]repository.TreeEntry, 0)
	for _, record := range bytes.Split(result.Stdout, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parsed, parseErr := parseTreeRecord(record)
		if parseErr != nil {
			return nil, parseErr
		}
		entry, err := newTreeEntry(parsed.Path, parsed.Kind, parsed.Mode, parsed.ObjectID, nil)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
		if app.Count(len(entries)) > s.maxEntries {
			return nil, app.ErrReviewSnapshotLimit
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return bytes.Compare(entries[i].Path, entries[j].Path) < 0 })
	return entries, nil
}

// OpenBase streams one regular-file or symlink blob from the pinned tree.
func (s *GitObjectSnapshotSource) OpenBase(ctx context.Context, base repository.LocalCaptureBase, entry repository.TreeEntry) (io.ReadCloser, error) {
	if s == nil || ctx == nil || base.Validate() != nil || entry.Validate() != nil || entry.Kind != repository.FileKindRegular && entry.Kind != repository.FileKindSymlink {
		return nil, &GitError{Code: ErrorInvalidInput}
	}
	var data bytes.Buffer
	if _, err := s.builder.RunStream(ctx, &data, "cat-file", "blob", string(base.ObjectID)+":"+string(entry.Path.Bytes())); err != nil {
		return nil, objectUnavailable(err)
	}
	return io.NopCloser(bytes.NewReader(data.Bytes())), nil
}

func objectUnavailable(err error) error {
	var gitErr *GitError
	if errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed {
		return &GitError{Code: ErrorObjectUnavailableNoFetch, Cause: err, ExitCode: gitErr.ExitCode, Stderr: gitErr.Stderr}
	}
	return err
}
