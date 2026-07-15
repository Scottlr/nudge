package gitcli

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

var (
	ErrInvalidProposalPatchRequest = errors.New("invalid proposal patch request")
	ErrProposalPatchUnavailable    = errors.New("proposal patch generation unavailable")
)

// ProposalPatchGenerator is the controlled Git boundary for T111. It creates
// trees only in the Nudge-owned admin root and streams the final diff to the
// caller-owned spool.
type ProposalPatchGenerator struct {
	executable process.ExecutableIdentity
	runner     process.Runner
	policy     MachineGitReadPolicyV1
	format     PatchFormatV1
	rename     RenamePolicyV1
	conversion ContentConversionPolicyV1
}

// ProposalPatchGeneratorConfig supplies the trusted executable and exact
// deterministic policy classes used for one patch derivation.
type ProposalPatchGeneratorConfig struct {
	Executable process.ExecutableIdentity
	Runner     process.Runner
	Policy     MachineGitReadPolicyV1
	Format     PatchFormatV1
	Rename     RenamePolicyV1
	Conversion ContentConversionPolicyV1
}

// NewProposalPatchGenerator validates the immutable Git policy selection.
func NewProposalPatchGenerator(config ProposalPatchGeneratorConfig) (*ProposalPatchGenerator, error) {
	if config.Executable.Validate() != nil || config.Runner == nil || config.Policy.validate() != nil || config.Format.Validate() != nil || config.Rename.Validate() != nil || config.Conversion.Validate() != nil {
		return nil, ErrInvalidProposalPatchRequest
	}
	return &ProposalPatchGenerator{executable: config.Executable, runner: config.Runner, policy: config.Policy, format: config.Format, rename: config.Rename, conversion: config.Conversion}, nil
}

// ProposalPatchRequest binds Git generation to the complete T110 manifest
// pair. The roots must already be proven Nudge-owned by T108.
type ProposalPatchRequest struct {
	AdminRoot          string
	BaselineRoot       string
	ResultRoot         string
	Baseline           app.WorkspaceManifest
	Result             app.ResultSnapshot
	ResourcePolicy     app.ResourcePolicy
	ConversionDecision string
}

func (r ProposalPatchRequest) validate() error {
	if r.AdminRoot == "" || r.BaselineRoot == "" || r.ResultRoot == "" || r.Baseline.Validate() != nil || r.Result.Validate() != nil || r.Result.State != app.ResultSnapshotReady || r.ResourcePolicy.Validate() != nil || r.ConversionDecision != "byte_neutral" {
		return ErrInvalidProposalPatchRequest
	}
	if r.Baseline.Hash != r.Result.Baseline.ManifestHash || len(r.Result.Delta.Entries) == 0 {
		return ErrInvalidProposalPatchRequest
	}
	for _, root := range []string{r.AdminRoot, r.BaselineRoot, r.ResultRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
			return ErrInvalidProposalPatchRequest
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return ErrInvalidProposalPatchRequest
		}
	}
	return nil
}

// Generate streams one complete PatchFormatV1 diff into output. The caller
// owns cleanup of output when this operation returns an error.
func (g *ProposalPatchGenerator) Generate(ctx context.Context, request ProposalPatchRequest, output io.Writer) (app.StreamIdentity, error) {
	if g == nil || ctx == nil || output == nil || request.validate() != nil {
		return app.StreamIdentity{}, ErrInvalidProposalPatchRequest
	}
	admin, err := filepath.Abs(request.AdminRoot)
	if err != nil {
		return app.StreamIdentity{}, ErrInvalidProposalPatchRequest
	}
	if err := g.initializeAdmin(ctx, admin); err != nil {
		return app.StreamIdentity{}, err
	}
	baseIndex := filepath.Join(admin, "nudge-baseline.index")
	resultIndex := filepath.Join(admin, "nudge-result.index")
	defer os.Remove(baseIndex)
	defer os.Remove(resultIndex)
	baseTree, err := g.materializeTree(ctx, admin, request.BaselineRoot, baseIndex)
	if err != nil {
		return app.StreamIdentity{}, err
	}
	resultTree, err := g.materializeTree(ctx, admin, request.ResultRoot, resultIndex)
	if err != nil {
		return app.StreamIdentity{}, err
	}
	configArgs, err := g.diffConfigArgs()
	if err != nil {
		return app.StreamIdentity{}, err
	}
	renameArgs, err := g.rename.DiffArgs()
	if err != nil {
		return app.StreamIdentity{}, err
	}
	args := []string{"--git-dir=" + admin}
	args = append(args, configArgs...)
	args = append(args, "diff-tree")
	args = append(args, "--patch", "--binary", "--full-index", "--unified=3", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers", "--no-indent-heuristic")
	args = append(args, renameArgs...)
	args = append(args, "--root", string(baseTree), string(resultTree), "--")
	stream, err := g.runStream(ctx, output, args...)
	if err != nil {
		return app.StreamIdentity{}, err
	}
	return app.StreamIdentity{Bytes: app.ByteSize(stream.StdoutBytes), SHA256: stream.StdoutHash}, nil
}

func (g *ProposalPatchGenerator) initializeAdmin(ctx context.Context, admin string) error {
	args := []string{"init", "--bare", "--quiet", "--template=", "--", admin}
	_, err := g.run(ctx, args...)
	if err != nil {
		return ErrProposalPatchUnavailable
	}
	return nil
}

func (g *ProposalPatchGenerator) materializeTree(ctx context.Context, admin, worktree, index string) (repository.ObjectID, error) {
	args := []string{"--git-dir=" + admin, "--work-tree=" + worktree}
	config, err := g.diffConfigArgs()
	if err != nil {
		return "", err
	}
	args = append(args, config...)
	args = append(args, "add", "--all", "--force", "--", ".")
	env := g.environment(index)
	result, err := g.runner.Run(ctx, process.Spec{Executable: g.executable, Args: args, Environment: env, Timeout: g.policy.Timeout, StdoutLimit: g.policy.StdoutLimit, StderrLimit: g.policy.StderrLimit})
	if err != nil {
		return "", classifyProcessError(err, result)
	}
	result, err = g.runner.Run(ctx, process.Spec{Executable: g.executable, Args: append([]string{"--git-dir=" + admin}, "write-tree"), Environment: env, Timeout: g.policy.Timeout, StdoutLimit: 4 * 1024, StderrLimit: g.policy.StderrLimit})
	if err != nil {
		return "", classifyProcessError(err, result)
	}
	tree := strings.TrimSpace(string(result.Stdout))
	if !validTreeID(tree) {
		return "", ErrProposalPatchUnavailable
	}
	return repository.ObjectID(tree), nil
}

func (g *ProposalPatchGenerator) diffConfigArgs() ([]string, error) {
	format, err := g.format.DiffConfigArgs()
	if err != nil {
		return nil, err
	}
	conversion, err := g.conversion.ConfigArgs()
	if err != nil {
		return nil, err
	}
	return append(format, conversion...), nil
}

func (g *ProposalPatchGenerator) environment(index string) process.EnvironmentPolicy {
	environment := g.policy.EnvironmentPolicy()
	if index != "" {
		if environment.Set == nil {
			environment.Set = make(map[string]string)
		}
		environment.Set["GIT_INDEX_FILE"] = index
	}
	return environment
}

func (g *ProposalPatchGenerator) run(ctx context.Context, args ...string) (process.Result, error) {
	environment := g.environment("")
	result, err := g.runner.Run(ctx, process.Spec{Executable: g.executable, Args: args, Environment: environment, Timeout: g.policy.Timeout, StdoutLimit: g.policy.StdoutLimit, StderrLimit: g.policy.StderrLimit})
	if err == nil {
		return result, nil
	}
	return result, classifyProcessError(err, result)
}

func (g *ProposalPatchGenerator) runStream(ctx context.Context, output io.Writer, args ...string) (process.StreamResult, error) {
	result, err := g.runner.RunStream(ctx, process.Spec{Executable: g.executable, Args: args, Environment: g.environment(""), Timeout: g.policy.Timeout, StdoutLimit: g.policy.StdoutLimit, StderrLimit: g.policy.StderrLimit}, output)
	if err == nil {
		return result, nil
	}
	return result, classifyProcessError(err, process.Result{ExitCode: result.ExitCode, Stderr: result.StderrTail})
}

func validTreeID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
