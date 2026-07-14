package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

// ResolverConfig supplies the trusted Git executable, process boundary, ID
// source, and versioned machine-read policy used by a Resolver.
type ResolverConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	IDs        app.IDSource
	Policy     MachineGitReadPolicyV1
}

type repositoryKey struct {
	objectFormat string
	commonDir    string
	identity     repository.NativeIdentity
}

type worktreeKey struct {
	repositoryID domain.RepositoryID
	objectFormat string
	rootPath     string
	gitDir       string
	rootIdentity repository.NativeIdentity
	gitIdentity  repository.NativeIdentity
}

type repositoryBinding struct {
	id        domain.RepositoryID
	createdAt time.Time
	evidence  repository.RepositoryBindingEvidence
}

type worktreeBinding struct {
	id       domain.WorktreeID
	evidence repository.WorktreeBindingEvidence
}

// Resolver resolves the enclosing repository and current worktree using only
// installed Git read commands. Its identity registry is process-local and is
// deliberately protected independently from Git I/O.
type Resolver struct {
	executable process.ExecutableIdentity
	runner     process.Runner
	ids        app.IDSource
	policy     MachineGitReadPolicyV1

	mu            sync.Mutex
	repositories  map[repositoryKey]repositoryBinding
	repositoryIDs map[domain.RepositoryID]repositoryKey
	worktrees     map[worktreeKey]worktreeBinding
	worktreeIDs   map[domain.WorktreeID]worktreeKey
}

// NewResolver constructs a repository resolver with a trusted Git identity.
func NewResolver(config ResolverConfig) (*Resolver, error) {
	policy := config.Policy
	if policy == (MachineGitReadPolicyV1{}) {
		policy = DefaultMachineGitReadPolicyV1()
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	if config.IDs == nil {
		config.IDs = app.RandomIDSource{}
	}
	if err := config.Executable.Validate(); err != nil {
		return nil, &GitError{Code: ErrorGitUnavailable, Cause: err}
	}
	return &Resolver{
		executable:    config.Executable,
		runner:        config.Runner,
		ids:           config.IDs,
		policy:        policy,
		repositories:  make(map[repositoryKey]repositoryBinding),
		repositoryIDs: make(map[domain.RepositoryID]repositoryKey),
		worktrees:     make(map[worktreeKey]worktreeBinding),
		worktreeIDs:   make(map[domain.WorktreeID]worktreeKey),
	}, nil
}

// ResolveRepository resolves the repository containing startPath and the
// exact checked-out worktree selected by Git for that path.
func (r *Resolver) ResolveRepository(ctx context.Context, startPath string) (repository.Repository, repository.WorktreeRef, error) {
	if ctx == nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorInvalidInput}
	}
	if err := ctx.Err(); err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorCanceled, Cause: err}
	}
	start, err := canonicalExistingDirectory(startPath)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{
		Executable: r.executable,
		Runner:     r.runner,
		StartPath:  start,
		Policy:     r.policy,
	})
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}

	bare, err := r.requiredBoolean(ctx, builder, "is-bare-repository", "rev-parse", "--is-bare-repository")
	if err != nil {
		if isCommandFailure(err) {
			return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorOutsideRepository, Cause: err}
		}
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	if bare {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorBareRepository}
	}
	inside, err := r.requiredBoolean(ctx, builder, "is-inside-work-tree", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if isCommandFailure(err) {
			return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorOutsideRepository, Cause: err}
		}
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	if !inside {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorOutsideRepository}
	}

	topLevel, err := r.requiredLine(ctx, builder, "top level", "rev-parse", "--show-toplevel")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	commonDir, err := r.requiredLine(ctx, builder, "common Git directory", "rev-parse", "--git-common-dir")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	gitDir, err := r.requiredLine(ctx, builder, "worktree Git directory", "rev-parse", "--git-dir")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	prefix, err := r.requiredLineAllowEmpty(ctx, builder, "worktree prefix", "rev-parse", "--show-prefix")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	objectFormat, err := r.requiredLine(ctx, builder, "object format", "rev-parse", "--show-object-format")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}

	root, err := resolveGitDirectory(topLevel, start)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorMalformedOutput, Cause: err}
	}
	common, err := resolveGitDirectory(commonDir, start)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorMalformedOutput, Cause: err}
	}
	worktreeGitDir, err := resolveGitDirectory(gitDir, start)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, &GitError{Code: ErrorMalformedOutput, Cause: err}
	}
	rootIdentity, err := nativePathIdentity(root)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	commonIdentity, err := nativePathIdentity(common)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	gitIdentity, err := nativePathIdentity(worktreeGitDir)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	launchFocus, err := normalizeLaunchFocus(prefix)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, malformed("invalid worktree prefix")
	}

	repositoryBindingEvidence := repository.RepositoryBindingEvidence{
		Version:              MachineGitReadPolicyVersion,
		ObjectFormat:         objectFormat,
		CommonGitDir:         common,
		CommonGitDirIdentity: commonIdentity,
	}
	worktreeBindingEvidence := repository.WorktreeBindingEvidence{
		Version:        MachineGitReadPolicyVersion,
		ObjectFormat:   objectFormat,
		RootPath:       root,
		GitDir:         worktreeGitDir,
		RootIdentity:   rootIdentity,
		GitDirIdentity: gitIdentity,
	}
	repositoryID, createdAt, err := r.repositoryIdentity(repositoryKey{
		objectFormat: objectFormat,
		commonDir:    common,
		identity:     commonIdentity,
	}, repositoryBindingEvidence)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	worktreeID, err := r.worktreeIdentity(worktreeKey{
		repositoryID: repositoryID,
		objectFormat: objectFormat,
		rootPath:     root,
		gitDir:       worktreeGitDir,
		rootIdentity: rootIdentity,
		gitIdentity:  gitIdentity,
	}, worktreeBindingEvidence)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}

	currentObjectID, err := r.optionalObjectID(ctx, builder, "HEAD")
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	branchName, detached, err := r.branch(ctx, builder)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	remotes, err := r.remotes(ctx, builder)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	upstream, err := r.upstream(ctx, builder, remotes)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}
	defaultBranch, err := r.defaultBranch(ctx, builder, remotes, upstream)
	if err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, err
	}

	now := time.Now().UTC()
	repositoryValue := repository.Repository{
		ID:            repositoryID,
		CommonGitDir:  common,
		Binding:       repositoryBindingEvidence,
		DisplayName:   filepath.Base(root),
		Remotes:       remotes,
		DefaultBranch: defaultBranch,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
	}
	worktreeValue := repository.WorktreeRef{
		ID:              worktreeID,
		RepositoryID:    repositoryID,
		RootPath:        root,
		GitDir:          worktreeGitDir,
		Binding:         worktreeBindingEvidence,
		CurrentObjectID: currentObjectID,
		BranchName:      branchName,
		Detached:        detached,
		LaunchFocus:     launchFocus,
		Upstream:        upstream,
	}
	if err := repositoryValue.Validate(); err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, fmt.Errorf("resolved repository: %w", err)
	}
	if err := worktreeValue.Validate(); err != nil {
		return repository.Repository{}, repository.WorktreeRef{}, fmt.Errorf("resolved worktree: %w", err)
	}
	return repositoryValue, worktreeValue, nil
}

func (r *Resolver) requiredBoolean(ctx context.Context, builder *CommandBuilder, name string, command ...string) (bool, error) {
	result, err := r.run(ctx, builder, command...)
	if err != nil {
		return false, err
	}
	return parseBooleanOutput(name, result.Stdout)
}

func (r *Resolver) requiredLine(ctx context.Context, builder *CommandBuilder, name string, command ...string) (string, error) {
	result, err := r.run(ctx, builder, command...)
	if err != nil {
		return "", err
	}
	return parseOutputLine(name, result.Stdout, false)
}

func (r *Resolver) requiredLineAllowEmpty(ctx context.Context, builder *CommandBuilder, name string, command ...string) (string, error) {
	result, err := r.run(ctx, builder, command...)
	if err != nil {
		return "", err
	}
	return parseOutputLine(name, result.Stdout, true)
}

func (r *Resolver) run(ctx context.Context, builder *CommandBuilder, command ...string) (process.Result, error) {
	result, err := builder.Run(ctx, command...)
	if err == nil {
		return result, nil
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) && errors.Is(gitErr, os.ErrPermission) {
		return result, &GitError{Code: ErrorPermission, Cause: err, ExitCode: gitErr.ExitCode, Stderr: gitErr.Stderr}
	}
	return result, err
}

func (r *Resolver) optionalLine(ctx context.Context, builder *CommandBuilder, name string, command ...string) (string, bool, error) {
	result, err := r.run(ctx, builder, command...)
	if err != nil {
		if isOptionalAbsence(err) {
			return "", false, nil
		}
		return "", false, err
	}
	value, err := parseOutputLine(name, result.Stdout, true)
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (r *Resolver) optionalObjectID(ctx context.Context, builder *CommandBuilder, ref string) (repository.ObjectID, error) {
	value, present, err := r.optionalLine(ctx, builder, "object ID", "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed && gitErr.ExitCode == 128 {
			return "", &GitError{Code: ErrorObjectUnavailableNoFetch, Cause: err, ExitCode: gitErr.ExitCode, Stderr: gitErr.Stderr}
		}
		return "", err
	}
	if !present || value == "" {
		return "", err
	}
	objectID, err := repository.NewObjectID(value)
	if err != nil {
		return "", malformed("invalid object ID")
	}
	return objectID, nil
}

func (r *Resolver) branch(ctx context.Context, builder *CommandBuilder) (string, bool, error) {
	value, present, err := r.optionalLine(ctx, builder, "branch", "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", false, err
	}
	if !present {
		return "", true, nil
	}
	return value, false, nil
}

func (r *Resolver) remotes(ctx context.Context, builder *CommandBuilder) ([]repository.Remote, error) {
	result, err := r.run(ctx, builder, "remote")
	if err != nil {
		return nil, err
	}
	names, err := parseOutputLines("remote", result.Stdout)
	if err != nil {
		return nil, err
	}
	remotes := make([]repository.Remote, 0, len(names))
	for _, name := range names {
		fetchURLs, err := r.urls(ctx, builder, name, false)
		if err != nil {
			return nil, err
		}
		pushURLs, err := r.urls(ctx, builder, name, true)
		if err != nil {
			pushURLs = append([]string(nil), fetchURLs...)
		}
		remote := repository.Remote{Name: name, FetchURLs: fetchURLs, PushURLs: pushURLs}
		if err := remote.Validate(); err != nil {
			return nil, malformed("invalid remote metadata")
		}
		remotes = append(remotes, remote)
	}
	return remotes, nil
}

func (r *Resolver) urls(ctx context.Context, builder *CommandBuilder, name string, push bool) ([]string, error) {
	command := []string{"remote", "get-url", "--all"}
	if push {
		command = append(command, "--push")
	}
	command = append(command, "--", name)
	result, err := r.run(ctx, builder, command...)
	if err != nil {
		return nil, err
	}
	return parseOutputLines("remote URL", result.Stdout)
}

func (r *Resolver) upstream(ctx context.Context, builder *CommandBuilder, remotes []repository.Remote) (*repository.UpstreamRef, error) {
	result, err := r.run(ctx, builder, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		var gitErr *GitError
		if errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed && (gitErr.ExitCode == 1 || gitErr.ExitCode == 128) {
			return nil, nil
		}
		return nil, err
	}
	value, err := parseOutputLine("upstream", result.Stdout, false)
	if err != nil {
		return nil, err
	}
	remoteNames := make([]string, 0, len(remotes))
	for _, remote := range remotes {
		remoteNames = append(remoteNames, remote.Name)
	}
	sort.Slice(remoteNames, func(i, j int) bool { return len(remoteNames[i]) > len(remoteNames[j]) })
	var remoteName string
	var branchName string
	for _, name := range remoteNames {
		if strings.HasPrefix(value, name+"/") {
			remoteName = name
			branchName = strings.TrimPrefix(value, name+"/")
			break
		}
	}
	if remoteName == "" {
		separator := strings.IndexByte(value, '/')
		if separator <= 0 || separator == len(value)-1 {
			return nil, malformed("invalid upstream")
		}
		remoteName = value[:separator]
		branchName = value[separator+1:]
	}
	upstream := &repository.UpstreamRef{
		RemoteName: remoteName,
		BranchName: branchName,
		RefName:    "refs/remotes/" + remoteName + "/" + branchName,
	}
	if err := upstream.Validate(); err != nil {
		return nil, malformed("invalid upstream")
	}
	return upstream, nil
}

func (r *Resolver) defaultBranch(ctx context.Context, builder *CommandBuilder, remotes []repository.Remote, upstream *repository.UpstreamRef) (string, error) {
	if _, origin := findRemote(remotes, "origin"); origin {
		value, present, err := r.optionalLine(ctx, builder, "origin default branch", "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", err
		}
		if present && strings.HasPrefix(value, "origin/") && len(value) > len("origin/") {
			return strings.TrimPrefix(value, "origin/"), nil
		}
	}
	if upstream != nil {
		return upstream.BranchName, nil
	}
	candidates := []struct {
		ref    string
		branch string
	}{
		{ref: "refs/heads/main^{commit}", branch: "main"},
		{ref: "refs/remotes/origin/main^{commit}", branch: "main"},
		{ref: "refs/heads/master^{commit}", branch: "master"},
		{ref: "refs/remotes/origin/master^{commit}", branch: "master"},
	}
	for _, candidate := range candidates {
		if _, present, err := r.optionalLine(ctx, builder, "default branch ref", "rev-parse", "--verify", "--quiet", candidate.ref); err != nil {
			return "", err
		} else if present {
			return candidate.branch, nil
		}
	}
	return "", nil
}

func findRemote(remotes []repository.Remote, name string) (repository.Remote, bool) {
	for _, remote := range remotes {
		if remote.Name == name {
			return remote, true
		}
	}
	return repository.Remote{}, false
}

func (r *Resolver) repositoryIdentity(key repositoryKey, evidence repository.RepositoryBindingEvidence) (domain.RepositoryID, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.repositories[key]; ok {
		return existing.id, existing.createdAt, nil
	}
	value := r.ids.NewID()
	id, err := domain.NewRepositoryID(value)
	if err != nil {
		return "", time.Time{}, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if _, exists := r.repositoryIDs[id]; exists {
		return "", time.Time{}, &GitError{Code: ErrorInvalidInput, Cause: errors.New("ID source collision")}
	}
	createdAt := time.Now().UTC()
	r.repositories[key] = repositoryBinding{id: id, createdAt: createdAt, evidence: evidence}
	r.repositoryIDs[id] = key
	return id, createdAt, nil
}

func (r *Resolver) worktreeIdentity(key worktreeKey, evidence repository.WorktreeBindingEvidence) (domain.WorktreeID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.worktrees[key]; ok {
		return existing.id, nil
	}
	value := r.ids.NewID()
	id, err := domain.NewWorktreeID(value)
	if err != nil {
		return "", &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if _, exists := r.worktreeIDs[id]; exists {
		return "", &GitError{Code: ErrorInvalidInput, Cause: errors.New("ID source collision")}
	}
	r.worktrees[key] = worktreeBinding{id: id, evidence: evidence}
	r.worktreeIDs[id] = key
	return id, nil
}

func resolveGitDirectory(value, base string) (string, error) {
	value = filepath.FromSlash(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return canonicalExistingDirectory(value)
}

func normalizeLaunchFocus(value string) (string, error) {
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return "", nil
	}
	cleaned := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", errors.New("invalid launch focus")
	}
	return cleaned, nil
}

func isCommandFailure(err error) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed
}

func isOptionalAbsence(err error) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed && gitErr.ExitCode == 1
}
