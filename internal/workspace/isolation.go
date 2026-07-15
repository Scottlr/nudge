package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

// Isolation roots are deliberately four separate filesystem domains. The
// provider receives only Result; Nudge owns the other three roots.
type IsolationRoots struct {
	Baseline    string
	Admin       string
	Result      string
	Destination string
}

// IsolationCapability is native evidence supplied by a platform sandbox
// adapter. Codex permission prompts do not satisfy any of these fields.
type IsolationCapability struct {
	FilesystemBoundary         bool
	DescendantsContained       bool
	NetworkDisabled            bool
	EnvironmentSanitized       bool
	NoInheritedWritableHandles bool
	NoSymlinkEscape            bool
	NoJunctionEscape           bool
	NoMountEscape              bool
	NoHardLinkAlias            bool
	NoSharedCloneAlias         bool
}

// GrowthPolicy records the enforcement level for provider result-root growth.
// V1 uses monitored cancellation unless a platform adapter proves a native
// hard quota.
type GrowthPolicy struct {
	Mode                 app.VolumeCapacityMode
	LimitBytes           app.ByteSize
	ReserveBytes         app.ByteSize
	RecoveryReserveBytes app.ByteSize
	RecheckBytes         app.ByteSize
	RecheckInterval      time.Duration
	CancelOnLimit        bool
	NativeHardQuota      bool
}

// IsolationContract is the accepted, immutable proposal-mode boundary. It
// is a capability record, not a workspace manager or a path-deletion helper.
type IsolationContract struct {
	Roots      IsolationRoots
	Capability IsolationCapability
	Growth     GrowthPolicy
}

var (
	ErrInvalidIsolationRoots = errors.New("invalid proposal isolation roots")
	ErrIsolationUnavailable  = errors.New("proposal_isolation_unavailable")
	ErrQuiescenceUnproven    = errors.New("proposal_quiescence_unproven")
	ErrWorkspaceGrowthLimit  = errors.New("workspace_growth_limit")
	ErrInvalidGrowthPolicy   = errors.New("invalid workspace growth policy")
)

// IsolationErrorCode is a stable, user-visible proposal capability reason.
type IsolationErrorCode string

const (
	IsolationUnavailableCode IsolationErrorCode = "proposal_isolation_unavailable"
	QuiescenceUnprovenCode   IsolationErrorCode = "proposal_quiescence_unproven"
	GrowthLimitCode          IsolationErrorCode = "workspace_growth_limit"
)

// IsolationError retains a stable reason without exposing filesystem details.
type IsolationError struct {
	Code  IsolationErrorCode
	Cause error
}

func (e *IsolationError) Error() string {
	if e == nil || e.Code == "" {
		return string(IsolationUnavailableCode)
	}
	return string(e.Code)
}

func (e *IsolationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewIsolationContract canonicalizes and validates all four roots before a
// provider turn can be admitted. Every root must already exist as a directory
// so that a later path alias cannot silently change the boundary.
func NewIsolationContract(roots IsolationRoots, capability IsolationCapability, growth GrowthPolicy) (IsolationContract, error) {
	canonical, err := canonicalRoots(roots)
	if err != nil {
		return IsolationContract{}, err
	}
	if err := growth.Validate(); err != nil {
		return IsolationContract{}, err
	}
	contract := IsolationContract{Roots: canonical, Capability: capability, Growth: growth}
	if err := contract.validateBoundary(); err != nil {
		return IsolationContract{}, err
	}
	return contract, nil
}

// ProposalTurnAvailable reports whether the platform can safely grant the
// result root to a mutating provider turn.
func (c IsolationContract) ProposalTurnAvailable() bool {
	return c.validateBoundary() == nil
}

// RequireProposalTurn returns a typed unavailable result when any native
// containment evidence is missing.
func (c IsolationContract) RequireProposalTurn() error {
	if err := c.validateBoundary(); err != nil {
		return err
	}
	return nil
}

func (c IsolationContract) validateBoundary() error {
	if err := c.Roots.Validate(); err != nil {
		return err
	}
	if !c.Capability.FilesystemBoundary || !c.Capability.DescendantsContained || !c.Capability.NetworkDisabled || !c.Capability.EnvironmentSanitized || !c.Capability.NoInheritedWritableHandles || !c.Capability.NoSymlinkEscape || !c.Capability.NoJunctionEscape || !c.Capability.NoMountEscape || !c.Capability.NoHardLinkAlias || !c.Capability.NoSharedCloneAlias {
		return &IsolationError{Code: IsolationUnavailableCode, Cause: ErrIsolationUnavailable}
	}
	return nil
}

// VerifyQuiescence must pass before Nudge snapshots or deletes a result root.
// A directory permission change alone is not a quiescence proof.
func (c IsolationContract) VerifyQuiescence(proof QuiescenceProof) error {
	if err := c.RequireProposalTurn(); err != nil {
		return err
	}
	if !proof.DescendantsEmpty || !proof.WritableHandlesClosed || !proof.ResultRootStable {
		return &IsolationError{Code: QuiescenceUnprovenCode, Cause: ErrQuiescenceUnproven}
	}
	return nil
}

// QuiescenceProof is emitted by the process/sandbox owner after a mutating
// turn. It must cover detached descendants and inherited writable handles.
type QuiescenceProof struct {
	DescendantsEmpty      bool
	WritableHandlesClosed bool
	ResultRootStable      bool
}

// GrowthExceeded returns the stable workspace_growth_limit result when the
// monitored result charge crosses its admitted limit. A hard quota is valid
// only when the native adapter independently proves it.
func (c IsolationContract) GrowthExceeded(observed app.ByteSize) error {
	if observed <= c.Growth.LimitBytes {
		return nil
	}
	return &IsolationError{Code: GrowthLimitCode, Cause: ErrWorkspaceGrowthLimit}
}

func (p GrowthPolicy) Validate() error {
	if p.LimitBytes == 0 || p.ReserveBytes == 0 || p.RecoveryReserveBytes == 0 || p.RecoveryReserveBytes > p.ReserveBytes || p.RecheckBytes == 0 || p.RecheckInterval <= 0 || !p.CancelOnLimit {
		return ErrInvalidGrowthPolicy
	}
	switch p.Mode {
	case app.VolumeCapacityMonitored:
		if p.NativeHardQuota {
			return ErrInvalidGrowthPolicy
		}
	case app.VolumeCapacityHard:
		if !p.NativeHardQuota {
			return ErrInvalidGrowthPolicy
		}
	default:
		return ErrInvalidGrowthPolicy
	}
	return nil
}

func canonicalRoots(roots IsolationRoots) (IsolationRoots, error) {
	values := []string{roots.Baseline, roots.Admin, roots.Result, roots.Destination}
	canonical := make([]string, len(values))
	for index, value := range values {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.IndexByte(value, 0) >= 0 {
			return IsolationRoots{}, ErrInvalidIsolationRoots
		}
		info, err := os.Stat(value)
		if err != nil || !info.IsDir() {
			return IsolationRoots{}, ErrInvalidIsolationRoots
		}
		resolved, err := filepath.EvalSymlinks(value)
		if err != nil || !filepath.IsAbs(resolved) {
			return IsolationRoots{}, ErrInvalidIsolationRoots
		}
		canonical[index] = filepath.Clean(resolved)
	}
	result := IsolationRoots{Baseline: canonical[0], Admin: canonical[1], Result: canonical[2], Destination: canonical[3]}
	if err := result.Validate(); err != nil {
		return IsolationRoots{}, err
	}
	return result, nil
}

// Validate checks canonical, non-overlapping roots. Native alias and boundary
// checks remain explicit capability evidence because path strings cannot prove
// inode, reparse-point, mount, or clone separation.
func (r IsolationRoots) Validate() error {
	values := []string{r.Baseline, r.Admin, r.Result, r.Destination}
	for _, value := range values {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.IndexByte(value, 0) >= 0 {
			return ErrInvalidIsolationRoots
		}
	}
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if sameOrDescendant(values[left], values[right]) || sameOrDescendant(values[right], values[left]) {
				return fmt.Errorf("%w: overlapping roots", ErrInvalidIsolationRoots)
			}
		}
	}
	return nil
}

func sameOrDescendant(root, path string) bool {
	root = comparisonPath(root)
	path = comparisonPath(path)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." {
		return false
	}
	return relative == "." || (relative != "" && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func comparisonPath(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
