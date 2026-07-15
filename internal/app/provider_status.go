package app

import (
	"context"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/privacy"
)

var (
	ErrProviderDataDisclosureRequired = errors.New("provider data disclosure acknowledgement required")
	ErrProviderUnavailable            = errors.New("provider is unavailable")
	ErrProviderAccountRequired        = errors.New("provider account authentication required")
)

// ProviderDisclosureVersion identifies the exact disclosure text and scope
// acknowledged by the user.
type ProviderDisclosureVersion = privacy.PolicyVersion

const ProviderDisclosureVersionV1 ProviderDisclosureVersion = privacy.PolicyVersionV1

// DisclosurePersistence identifies whether an acknowledgement is process-only
// or written by the protected-settings owner from T005.
type DisclosurePersistence = privacy.DisclosurePersistence

const (
	DisclosureProcessOnly       DisclosurePersistence = "process_only"
	DisclosureProtectedSettings DisclosurePersistence = "protected_settings"
)

// ProviderDataDisclosureGate is the application gate for selected code,
// anchor context, transcript, concern, and proposal intent leaving Nudge.
// Persistence is metadata only; T005 owns the protected settings write.
type ProviderDataDisclosureGate struct {
	CurrentVersion      ProviderDisclosureVersion
	AcknowledgedVersion ProviderDisclosureVersion
	AcknowledgedAt      time.Time
	Persistence         DisclosurePersistence
}

// NewProviderDataDisclosureGate creates an unacknowledged current-version
// gate. An unacknowledged gate never permits provider data dispatch.
func NewProviderDataDisclosureGate() ProviderDataDisclosureGate {
	return ProviderDataDisclosureGate{CurrentVersion: ProviderDisclosureVersionV1}
}

// Acknowledged reports whether the current disclosure version was explicitly
// accepted with a valid timestamp.
func (g ProviderDataDisclosureGate) Acknowledged() bool {
	return g.CurrentVersion != "" && g.AcknowledgedVersion == g.CurrentVersion && !g.AcknowledgedAt.IsZero()
}

// Acknowledge records a user action. It does not itself write protected
// settings; callers choose process-only or T005-owned protected persistence.
func (g *ProviderDataDisclosureGate) Acknowledge(version ProviderDisclosureVersion, at time.Time, persistence DisclosurePersistence) error {
	if g == nil || version == "" || version != g.CurrentVersion || at.IsZero() || (persistence != DisclosureProcessOnly && persistence != DisclosureProtectedSettings) {
		return ErrProviderDataDisclosureRequired
	}
	g.AcknowledgedVersion = version
	g.AcknowledgedAt = at.UTC()
	g.Persistence = persistence
	return nil
}

// Decline clears the acknowledgement while leaving local review state intact.
func (g *ProviderDataDisclosureGate) Decline() {
	if g == nil {
		return
	}
	g.AcknowledgedVersion = ""
	g.AcknowledgedAt = time.Time{}
	g.Persistence = ""
}

// CheckProviderDataDisclosure is the mandatory application-side dispatch gate.
func CheckProviderDataDisclosure(status ProviderStatus) error {
	if !status.Disclosure.Acknowledged() {
		return ErrProviderDataDisclosureRequired
	}
	return nil
}

// CheckProviderTurn is shared by discussion and proposal dispatch paths.
// Authentication and provider compatibility remain separate from disclosure.
func CheckProviderTurn(status ProviderStatus) error {
	if err := CheckProviderDataDisclosure(status); err != nil {
		return err
	}
	if status.Connection != ProviderConnected {
		return ErrProviderUnavailable
	}
	if status.Account.State == ProviderAccountAuthRequired {
		return ErrProviderAccountRequired
	}
	return nil
}

// ProviderStatusResult is the asynchronous application connection outcome.
type ProviderStatusResult struct {
	Status ProviderStatus
	Err    error
}

// ProviderStatusPort is the application-owned subset used by the status
// controller. Connect is deliberately run away from the local review actor.
type ProviderStatusPort interface {
	Probe(context.Context) (ProviderStatus, error)
	Connect(context.Context) error
}

// ProviderStatusUseCase keeps provider startup asynchronous and exposes only
// status transitions to the reducer/frontend integration layer.
type ProviderStatusUseCase struct {
	port ProviderStatusPort
}

func NewProviderStatusUseCase(port ProviderStatusPort) *ProviderStatusUseCase {
	return &ProviderStatusUseCase{port: port}
}

// Connect begins a bounded, joinable provider startup operation. The caller's
// local repository/tree/diff client remains independent and usable.
func (u *ProviderStatusUseCase) Connect(ctx context.Context, initial ProviderStatus) <-chan ProviderStatusResult {
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan ProviderStatusResult, 1)
	go func() {
		defer close(result)
		if u == nil || u.port == nil {
			result <- ProviderStatusResult{Status: degradedProviderStatus(initial, ProviderUnavailable), Err: ErrProviderUnavailable}
			return
		}
		if err := u.port.Connect(ctx); err != nil {
			result <- ProviderStatusResult{Status: degradedProviderStatus(initial, ProviderUnavailable), Err: err}
			return
		}
		status, err := u.port.Probe(ctx)
		if err != nil {
			result <- ProviderStatusResult{Status: degradedProviderStatus(initial, ProviderUnavailable), Err: err}
			return
		}
		status.Disclosure = initial.Disclosure
		result <- ProviderStatusResult{Status: status}
	}()
	return result
}

func degradedProviderStatus(initial ProviderStatus, state ProviderConnectionState) ProviderStatus {
	initial.Connection = state
	return initial
}
