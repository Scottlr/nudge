package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

const (
	// MaxBaseBranchExpressionBytes is the T070 bounded raw revision-expression
	// size. Expressions are stored exactly as accepted; they are never trimmed
	// or rewritten into a canonical ref name.
	MaxBaseBranchExpressionBytes = 4 << 10
)

var (
	// ErrInvalidBaseBranchPreference reports an unsafe or incomplete preference.
	ErrInvalidBaseBranchPreference = errors.New("invalid base branch preference")
	// ErrPreferencePersistenceDisabled reports a mutation attempted in
	// no-persist mode.
	ErrPreferencePersistenceDisabled = errors.New("base branch preference persistence is disabled")
	// ErrPreferenceRevisionConflict reports an optimistic preference CAS miss.
	ErrPreferenceRevisionConflict = errors.New("base branch preference revision conflict")
	// ErrSavedBaseUnavailable identifies a saved expression that cannot be used
	// for the current repository generation. Callers must offer choose/clear;
	// they must not silently select discovery instead.
	ErrSavedBaseUnavailable = errors.New("saved base branch is unavailable")
	// ErrBaseBranchDiscoveryUnavailable reports that no usable base was chosen.
	ErrBaseBranchDiscoveryUnavailable = errors.New("base branch discovery unavailable")
)

// BaseBranchPreference is the one explicitly saved raw base expression for a
// replacement-safe repository binding.
type BaseBranchPreference struct {
	RepositoryID domain.RepositoryID
	Expression   string
	Revision     uint64
	UpdatedAt    time.Time
}

// Validate checks the repository identity, raw expression, revision, and
// timestamp without resolving Git or touching a ref.
func (p BaseBranchPreference) Validate() error {
	if p.RepositoryID == "" || p.Revision == 0 || p.UpdatedAt.IsZero() {
		return ErrInvalidBaseBranchPreference
	}
	if err := ValidateBaseBranchExpression(p.Expression); err != nil {
		return err
	}
	return nil
}

// ValidateBaseBranchExpression validates one raw expression before storage or
// Git invocation. Accepted bytes are deliberately preserved exactly.
func ValidateBaseBranchExpression(expression string) error {
	if expression == "" || len(expression) > MaxBaseBranchExpressionBytes || !utf8.ValidString(expression) || strings.HasPrefix(expression, "-") {
		return ErrInvalidBaseBranchPreference
	}
	if strings.TrimSpace(expression) == "" || strings.ContainsRune(expression, '\x00') || !stableText(expression) {
		return ErrInvalidBaseBranchPreference
	}
	return nil
}

// BaseBranchSource identifies the policy layer that supplied a raw base
// expression. It is persisted with a resolved target generation, not with the
// reusable preference row.
type BaseBranchSource string

const (
	BaseFromExplicitFlag  BaseBranchSource = "explicit_branch_flag"
	BaseFromSessionChoice BaseBranchSource = "session_choice"
	BaseFromPreference    BaseBranchSource = "repository_preference"
	BaseFromDiscovery     BaseBranchSource = "discovery"
)

// Validate checks that the source is known and the expression is usable.
func (s BaseBranchSource) Validate(expression string) error {
	switch s {
	case BaseFromExplicitFlag, BaseFromSessionChoice, BaseFromPreference, BaseFromDiscovery:
	default:
		return ErrInvalidBaseBranchPreference
	}
	return ValidateBaseBranchExpression(expression)
}

// BaseBranchSelection is the source-tagged raw expression selected before Git
// resolves and freezes a target generation.
type BaseBranchSelection struct {
	Expression string
	Source     BaseBranchSource
}

// Validate checks the selection without resolving Git.
func (s BaseBranchSelection) Validate() error {
	return s.Source.Validate(s.Expression)
}

// RepositoryPreferenceStore is the application-owned persistence contract for
// repository-scoped base preferences. Implementations must key rows by the
// replacement-safe RepositoryID, not by a display path.
type RepositoryPreferenceStore interface {
	LoadBaseBranchPreference(context.Context, domain.RepositoryID) (*BaseBranchPreference, error)
	SaveBaseBranchPreference(context.Context, BaseBranchPreference, uint64) error
	ClearBaseBranchPreference(context.Context, domain.RepositoryID, uint64) error
}

// BaseBranchSelectionRequest supplies the already-known ephemeral choices and
// the deferred local discovery operation. Discovery is called only after all
// higher-precedence choices and a saved preference have been considered.
type BaseBranchSelectionRequest struct {
	RepositoryID       domain.RepositoryID
	ExplicitExpression string
	SessionExpression  string
	Persistence        PersistenceMode
	Store              RepositoryPreferenceStore
	Discover           func(context.Context) (string, error)
}

// Validate checks the request without invoking persistence or discovery.
func (r BaseBranchSelectionRequest) Validate() error {
	if r.RepositoryID == "" {
		return ErrInvalidBaseBranchPreference
	}
	if r.Persistence == "" {
		return nil
	}
	if r.Persistence != PersistenceDurable && r.Persistence != PersistenceNoPersist {
		return ErrInvalidBaseBranchPreference
	}
	if r.ExplicitExpression != "" {
		if err := ValidateBaseBranchExpression(r.ExplicitExpression); err != nil {
			return err
		}
	}
	if r.SessionExpression != "" {
		if err := ValidateBaseBranchExpression(r.SessionExpression); err != nil {
			return err
		}
	}
	return nil
}

// SelectBaseBranch applies the exact T083 precedence policy. A storage read
// failure is non-fatal to the current review and falls back to discovery; the
// resulting source makes clear that no saved preference was claimed. A loaded
// saved expression is never silently bypassed.
func SelectBaseBranch(ctx context.Context, request BaseBranchSelectionRequest) (BaseBranchSelection, error) {
	if ctx == nil {
		return BaseBranchSelection{}, ErrInvalidBaseBranchPreference
	}
	if err := request.Validate(); err != nil {
		return BaseBranchSelection{}, err
	}
	if err := ctx.Err(); err != nil {
		return BaseBranchSelection{}, err
	}
	if request.ExplicitExpression != "" {
		return BaseBranchSelection{Expression: request.ExplicitExpression, Source: BaseFromExplicitFlag}, nil
	}
	if request.SessionExpression != "" {
		return BaseBranchSelection{Expression: request.SessionExpression, Source: BaseFromSessionChoice}, nil
	}
	if request.Persistence != PersistenceNoPersist && request.Store != nil {
		preference, err := request.Store.LoadBaseBranchPreference(ctx, request.RepositoryID)
		if err == nil && preference != nil {
			if err := preference.Validate(); err != nil {
				return BaseBranchSelection{}, &SavedBaseUnavailableError{Expression: preference.Expression, Cause: err}
			}
			return BaseBranchSelection{Expression: preference.Expression, Source: BaseFromPreference}, nil
		}
		var unavailable *SavedBaseUnavailableError
		if errors.As(err, &unavailable) {
			return BaseBranchSelection{}, unavailable
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return BaseBranchSelection{}, err
		}
		// Missing preferences and transient storage failures leave the current
		// review usable. Only an actual saved row can block discovery, and a
		// syntactically valid row reaches Git resolution in T043.
	}
	if request.Discover == nil {
		return BaseBranchSelection{}, ErrBaseBranchDiscoveryUnavailable
	}
	expression, err := request.Discover(ctx)
	if err != nil {
		return BaseBranchSelection{}, err
	}
	if err := ValidateBaseBranchExpression(expression); err != nil {
		return BaseBranchSelection{}, fmt.Errorf("%w: discovery: %v", ErrBaseBranchDiscoveryUnavailable, err)
	}
	return BaseBranchSelection{Expression: expression, Source: BaseFromDiscovery}, nil
}

// SavedBaseUnavailableError retains the accepted expression for a bounded UI
// action model while keeping the default error text free of repository data.
type SavedBaseUnavailableError struct {
	Expression string
	Cause      error
}

func (e *SavedBaseUnavailableError) Error() string { return ErrSavedBaseUnavailable.Error() }

func (e *SavedBaseUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is allows callers to classify the safe unavailable category without
// exposing the stored expression or its private cause.
func (e *SavedBaseUnavailableError) Is(target error) bool {
	return target == ErrSavedBaseUnavailable
}

// SaveBaseBranchPreference is an explicit user action. Selection and
// discovery never construct this command implicitly.
type SaveBaseBranchPreference struct {
	RepositoryID     domain.RepositoryID
	Expression       string
	ExpectedRevision uint64
}

// Validate checks the command before persistence.
func (c SaveBaseBranchPreference) Validate() error {
	if c.RepositoryID == "" {
		return ErrInvalidBaseBranchPreference
	}
	return ValidateBaseBranchExpression(c.Expression)
}

// ClearBaseBranchPreference is an explicit user action that removes only the
// current repository binding's preference.
type ClearBaseBranchPreference struct {
	RepositoryID     domain.RepositoryID
	ExpectedRevision uint64
}

// Validate checks the command before persistence.
func (c ClearBaseBranchPreference) Validate() error {
	if c.RepositoryID == "" {
		return ErrInvalidBaseBranchPreference
	}
	return nil
}

// RepositoryPreferenceActions executes the two explicit persistence actions.
// It is intentionally separate from selection: choosing or discovering a
// base never writes a preference implicitly.
type RepositoryPreferenceActions struct {
	Store       RepositoryPreferenceStore
	Clock       Clock
	Persistence PersistenceMode
}

// Validate checks the action composition.
func (a RepositoryPreferenceActions) Validate() error {
	if a.Store == nil {
		return ErrReviewStoreClosed
	}
	if a.Persistence != "" && a.Persistence != PersistenceDurable && a.Persistence != PersistenceNoPersist {
		return ErrInvalidBaseBranchPreference
	}
	return nil
}

// Save applies one explicit save command and advances its optimistic revision.
func (a RepositoryPreferenceActions) Save(ctx context.Context, command SaveBaseBranchPreference) error {
	if a.Persistence == PersistenceNoPersist {
		return ErrPreferencePersistenceDisabled
	}
	if err := a.Validate(); err != nil || ctx == nil {
		if err != nil {
			return err
		}
		return ErrInvalidBaseBranchPreference
	}
	if command.ExpectedRevision == ^uint64(0) {
		return ErrPreferenceRevisionConflict
	}
	clock := a.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	preference := BaseBranchPreference{
		RepositoryID: command.RepositoryID,
		Expression:   command.Expression,
		Revision:     command.ExpectedRevision + 1,
		UpdatedAt:    clock.Now().UTC(),
	}
	if err := preference.Validate(); err != nil || command.Validate() != nil {
		return ErrInvalidBaseBranchPreference
	}
	return a.Store.SaveBaseBranchPreference(ctx, preference, command.ExpectedRevision)
}

// Clear applies one explicit clear command. A missing row with revision zero
// is idempotently accepted by the durable store.
func (a RepositoryPreferenceActions) Clear(ctx context.Context, command ClearBaseBranchPreference) error {
	if a.Persistence == PersistenceNoPersist {
		return ErrPreferencePersistenceDisabled
	}
	if err := a.Validate(); err != nil || ctx == nil {
		if err != nil {
			return err
		}
		return ErrInvalidBaseBranchPreference
	}
	if err := command.Validate(); err != nil {
		return err
	}
	return a.Store.ClearBaseBranchPreference(ctx, command.RepositoryID, command.ExpectedRevision)
}
