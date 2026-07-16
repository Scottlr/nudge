package app

import (
	"context"
	"errors"
	"io"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var ErrRepositoryMaintenance = errors.New("repository is under maintenance")

// RepositoryMaintenanceGate serializes repository-scoped maintenance with
// durable session creation and writer claims. The caller holds the returned
// closer across the complete claim sequence.
type RepositoryMaintenanceGate interface {
	Acquire(context.Context, domain.RepositoryID) (io.Closer, error)
}

// PersistenceMode identifies whether review state may survive this process.
type PersistenceMode string

const (
	// PersistenceDurable uses the local ReviewStore and a writable session lease.
	PersistenceDurable PersistenceMode = "durable"
	// PersistenceNoPersist keeps review identity in memory and never claims a
	// durable session lease or writes review state.
	PersistenceNoPersist PersistenceMode = "no_persist"
)

// SessionOpenMode describes the explicit ownership choice for a durable open.
type SessionOpenMode string

const (
	// SessionWritable restores or creates the compatible writable session.
	SessionWritable SessionOpenMode = "writable"
	// SessionReadOnly opens existing history without a durable writer lease.
	SessionReadOnly SessionOpenMode = "read_only"
	// SessionDistinct creates a new session even when compatible unfinished
	// history exists. This mode is an explicit user choice.
	SessionDistinct SessionOpenMode = "distinct"
)

// SessionLease is the application-facing lifetime of a native session lock.
// Closing it releases ownership; timestamps and process observations never do.
type SessionLease interface {
	LeaseID() domain.SessionLeaseID
	Close() error
}

// SessionLeaseRequest binds a native lock to a verified compatibility key and
// the durable lease identity that will be fenced in SQLite.
type SessionLeaseRequest struct {
	Key       review.SessionKey
	SessionID domain.ReviewSessionID
	LeaseID   domain.SessionLeaseID
	Distinct  bool
}

// Validate checks all lock/fence identities before an adapter sees them.
func (r SessionLeaseRequest) Validate() error {
	if err := r.Key.Validate(); err != nil || r.SessionID == "" || r.LeaseID == "" {
		return ErrReviewStoreInput
	}
	return nil
}

// SessionLeaseManager acquires the protected OS lock for a writable review
// session. Implementations must return ErrSessionBusy instead of waiting or
// stealing a live lock when ownership is unavailable.
type SessionLeaseManager interface {
	Acquire(context.Context, SessionLeaseRequest) (SessionLease, error)
}

// SessionLeaseIdentityProvider exposes the canonical, path-free identity of
// the lock used by SessionLeaseManager. Repair code uses this seam instead of
// duplicating native lock-key derivation.
type SessionLeaseIdentityProvider interface {
	LockIdentity(SessionLeaseRequest) (string, error)
}

// SessionLeaseIdentityStore records the lock identity selected at writable
// session open. Older sessions without this evidence are not repairable.
type SessionLeaseIdentityStore interface {
	SaveSessionLeaseIdentity(context.Context, SessionWriteGuard, string, bool) (SessionWriteGuard, error)
}

// OpenSessionRequest contains already-resolved repository and target evidence.
// The manager never resolves Git or derives a replacement binding itself.
type OpenSessionRequest struct {
	Repository  repository.Repository
	Worktree    repository.WorktreeRef
	Target      repository.ResolvedTarget
	Mode        SessionOpenMode
	Persistence PersistenceMode
}

// Validate checks repository/worktree ownership and target meaning.
func (r OpenSessionRequest) Validate() error {
	if err := r.Repository.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if err := r.Worktree.Validate(); err != nil || r.Worktree.RepositoryID != r.Repository.ID {
		return ErrReviewStoreInput
	}
	if err := r.Target.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if r.Mode == "" {
		return ErrReviewStoreInput
	}
	if r.Mode != SessionWritable && r.Mode != SessionReadOnly && r.Mode != SessionDistinct {
		return ErrReviewStoreInput
	}
	if r.Persistence == "" {
		return nil
	}
	if r.Persistence != PersistenceDurable && r.Persistence != PersistenceNoPersist {
		return ErrReviewStoreInput
	}
	return nil
}

// SessionHandle is the immutable session identity plus the current durable
// writer fence. A read-only or no-persist handle has no valid Guard.
type SessionHandle struct {
	Session             review.ReviewSession
	Key                 review.SessionKey
	Guard               SessionWriteGuard
	Lease               SessionLease
	Mode                SessionOpenMode
	Persistence         PersistenceMode
	Restored            bool
	ReadOnly            bool
	PersistenceDegraded bool
}

// Validate checks the handle before a lifecycle operation uses it.
func (h SessionHandle) Validate() error {
	if err := h.Session.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if err := h.Key.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if h.Persistence == PersistenceDurable && !h.ReadOnly && !h.PersistenceDegraded {
		if err := h.Guard.Validate(); err != nil || h.Lease == nil || h.Lease.LeaseID() != h.Guard.LeaseID {
			return ErrReviewStoreInput
		}
	}
	return nil
}

// Release closes only the native lease. It deliberately leaves the durable
// session unfinished so a normal quit remains eligible for restoration.
func (h *SessionHandle) Release() error {
	if h == nil || h.Lease == nil {
		return nil
	}
	err := h.Lease.Close()
	h.Lease = nil
	return err
}

// SessionManager coordinates exact session compatibility, durable fencing,
// and native lock lifetime. It does not own provider, workspace, or proposal
// state.
type SessionManager struct {
	store                  ReviewStore
	leases                 SessionLeaseManager
	maintenance            RepositoryMaintenanceGate
	ids                    IDSource
	clock                  Clock
	allowEphemeralFallback bool
}

// SessionManagerConfig composes the application port with its adapters.
type SessionManagerConfig struct {
	Store                  ReviewStore
	Leases                 SessionLeaseManager
	Maintenance            RepositoryMaintenanceGate
	IDs                    IDSource
	Clock                  Clock
	AllowEphemeralFallback bool
}

// NewSessionManager validates the durable-session composition root.
func NewSessionManager(config SessionManagerConfig) (*SessionManager, error) {
	if (config.Store == nil) != (config.Leases == nil) {
		return nil, ErrReviewStoreInput
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &SessionManager{
		store:                  config.Store,
		leases:                 config.Leases,
		maintenance:            config.Maintenance,
		ids:                    config.IDs,
		clock:                  config.Clock,
		allowEphemeralFallback: config.AllowEphemeralFallback,
	}, nil
}

// OpenSession restores the newest exact compatible unfinished session, or
// creates one after the caller's explicit ownership choice. No-persist skips
// every durable adapter and returns a process-scoped in-memory identity.
func (m *SessionManager) OpenSession(ctx context.Context, request OpenSessionRequest) (SessionHandle, error) {
	if m == nil || ctx == nil || request.Validate() != nil {
		return SessionHandle{}, ErrReviewStoreInput
	}
	if request.Persistence == "" {
		request.Persistence = PersistenceDurable
	}
	if request.Persistence == PersistenceNoPersist {
		return m.newEphemeral(request, false)
	}
	if m.store == nil || m.leases == nil {
		if m.allowEphemeralFallback {
			return m.newEphemeral(request, true)
		}
		return SessionHandle{}, ErrReviewStoreClosed
	}
	var maintenance io.Closer
	if m.maintenance != nil {
		var err error
		maintenance, err = m.maintenance.Acquire(ctx, request.Repository.ID)
		if err != nil {
			return SessionHandle{}, err
		}
		defer func() { _ = maintenance.Close() }()
	}

	if err := m.store.UpsertRepository(ctx, request.Repository, request.Worktree); err != nil {
		if m.allowEphemeralFallback && errors.Is(err, ErrReviewStoreClosed) {
			return m.newEphemeral(request, true)
		}
		return SessionHandle{}, err
	}
	provisionalID, err := m.newReviewSessionID()
	if err != nil {
		return SessionHandle{}, err
	}
	provisional, err := review.NewOpenReviewSession(provisionalID, request.Repository.ID, request.Target.Spec, request.Target, m.clock.Now().UTC())
	if err != nil {
		return SessionHandle{}, ErrReviewStoreInput
	}
	key, err := review.SessionKeyFor(provisional)
	if err != nil {
		return SessionHandle{}, ErrReviewStoreInput
	}

	var existing *review.ReviewSession
	if request.Mode != SessionDistinct {
		existing, err = m.store.FindCompatibleSession(ctx, key)
		if err != nil && !errors.Is(err, ErrReviewStoreNotFound) {
			if m.allowEphemeralFallback && errors.Is(err, ErrReviewStoreClosed) {
				return m.newEphemeral(request, true)
			}
			return SessionHandle{}, err
		}
	}
	if existing != nil && request.Mode == SessionReadOnly {
		return SessionHandle{Session: *existing, Key: key, Mode: request.Mode, Persistence: PersistenceDurable, Restored: true, ReadOnly: true}, nil
	}
	if request.Mode == SessionReadOnly {
		return SessionHandle{}, ErrReviewStoreNotFound
	}

	leaseID, err := m.newLeaseID()
	if err != nil {
		return SessionHandle{}, err
	}
	lockSessionID := provisional.ID
	if existing != nil {
		lockSessionID = existing.ID
	}
	lease, err := m.leases.Acquire(ctx, SessionLeaseRequest{Key: key, SessionID: lockSessionID, LeaseID: leaseID, Distinct: request.Mode == SessionDistinct})
	if err != nil {
		return SessionHandle{}, err
	}

	if request.Mode != SessionDistinct {
		// The first lookup can race a clean release. Re-read while holding the
		// native lock so a later open restores rather than forks history.
		latest, lookupErr := m.store.FindCompatibleSession(ctx, key)
		if lookupErr == nil {
			existing = latest
		} else if !errors.Is(lookupErr, ErrReviewStoreNotFound) {
			_ = lease.Close()
			return SessionHandle{}, lookupErr
		} else {
			existing = nil
		}
	}
	if existing != nil {
		guard, claimErr := m.store.ClaimSessionWriter(ctx, existing.ID, leaseID)
		if claimErr != nil {
			_ = lease.Close()
			return SessionHandle{}, claimErr
		}
		guard, claimErr = m.persistLeaseIdentity(ctx, guard, SessionLeaseRequest{Key: key, SessionID: existing.ID, LeaseID: leaseID, Distinct: false})
		if claimErr != nil {
			_ = lease.Close()
			return SessionHandle{}, claimErr
		}
		return SessionHandle{Session: *existing, Key: key, Guard: guard, Lease: lease, Mode: request.Mode, Persistence: PersistenceDurable, Restored: true}, nil
	}

	if request.Mode == SessionDistinct {
		key, err = review.SessionKeyFor(provisional)
		if err != nil {
			_ = lease.Close()
			return SessionHandle{}, ErrReviewStoreInput
		}
	}
	guard, err := m.store.CreateSession(ctx, provisional, leaseID)
	if err != nil {
		_ = lease.Close()
		return SessionHandle{}, err
	}
	guard, err = m.persistLeaseIdentity(ctx, guard, SessionLeaseRequest{Key: key, SessionID: provisional.ID, LeaseID: leaseID, Distinct: request.Mode == SessionDistinct})
	if err != nil {
		_ = lease.Close()
		return SessionHandle{}, err
	}
	return SessionHandle{Session: provisional, Key: key, Guard: guard, Lease: lease, Mode: request.Mode, Persistence: PersistenceDurable}, nil
}

func (m *SessionManager) persistLeaseIdentity(ctx context.Context, guard SessionWriteGuard, request SessionLeaseRequest) (SessionWriteGuard, error) {
	identityProvider, providerOK := m.leases.(SessionLeaseIdentityProvider)
	identityStore, storeOK := m.store.(SessionLeaseIdentityStore)
	if !providerOK || !storeOK {
		return guard, nil
	}
	identity, err := identityProvider.LockIdentity(request)
	if err != nil {
		return SessionWriteGuard{}, err
	}
	return identityStore.SaveSessionLeaseIdentity(ctx, guard, identity, request.Distinct)
}

func (m *SessionManager) newEphemeral(request OpenSessionRequest, degraded bool) (SessionHandle, error) {
	id, err := m.newReviewSessionID()
	if err != nil {
		return SessionHandle{}, err
	}
	now := m.clock.Now().UTC()
	session, err := review.NewOpenReviewSession(id, request.Repository.ID, request.Target.Spec, request.Target, now)
	if err != nil {
		return SessionHandle{}, ErrReviewStoreInput
	}
	key, err := review.SessionKeyFor(session)
	if err != nil {
		return SessionHandle{}, ErrReviewStoreInput
	}
	return SessionHandle{Session: session, Key: key, Mode: request.Mode, Persistence: PersistenceNoPersist, Restored: false, PersistenceDegraded: degraded}, nil
}

func (m *SessionManager) newReviewSessionID() (domain.ReviewSessionID, error) {
	return domain.NewReviewSessionID(m.ids.NewID())
}

func (m *SessionManager) newLeaseID() (domain.SessionLeaseID, error) {
	return domain.NewSessionLeaseID(m.ids.NewID())
}

// ReleaseSession records a bounded normal-quit timestamp, keeps the session
// unfinished, and then releases the native lock.
func (m *SessionManager) ReleaseSession(ctx context.Context, handle *SessionHandle) error {
	if m == nil || ctx == nil || handle == nil {
		return ErrReviewStoreInput
	}
	if handle.Persistence == PersistenceNoPersist || handle.PersistenceDegraded || handle.ReadOnly {
		return handle.Release()
	}
	if err := handle.Validate(); err != nil {
		return err
	}
	now := m.clock.Now().UTC()
	handle.Session.UpdatedAt = now
	handle.Session.ClosedAt = nil
	next, err := m.store.WithSessionTx(ctx, handle.Guard, func(tx ReviewStoreTx) error {
		return tx.SaveSession(ctx, handle.Session)
	})
	if err != nil {
		return err
	}
	handle.Guard = next
	return handle.Release()
}

// CloseSession explicitly excludes a session from automatic restore.
func (m *SessionManager) CloseSession(ctx context.Context, handle *SessionHandle) error {
	if m == nil || ctx == nil || handle == nil {
		return ErrReviewStoreInput
	}
	if handle.Persistence == PersistenceNoPersist || handle.PersistenceDegraded || handle.ReadOnly {
		return ErrSessionReadOnly
	}
	if err := handle.Validate(); err != nil {
		return err
	}
	now := m.clock.Now().UTC()
	if err := handle.Session.Close(now); err != nil {
		return err
	}
	next, err := m.store.WithSessionTx(ctx, handle.Guard, func(tx ReviewStoreTx) error {
		return tx.SaveSession(ctx, handle.Session)
	})
	if err != nil {
		return err
	}
	handle.Guard = next
	return handle.Release()
}

// RefreshTarget advances only a compatible session's target generation. A
// changed target kind/spec or base identity is rejected rather than silently
// mutating the session's meaning.
func (m *SessionManager) RefreshTarget(ctx context.Context, handle *SessionHandle, target repository.ResolvedTarget) error {
	if m == nil || ctx == nil || handle == nil {
		return ErrReviewStoreInput
	}
	if handle.Persistence == PersistenceNoPersist || handle.PersistenceDegraded || handle.ReadOnly {
		return ErrSessionReadOnly
	}
	if err := handle.Validate(); err != nil || target.Validate() != nil {
		return ErrReviewStoreInput
	}
	if target.Spec != handle.Session.TargetSpec {
		return ErrSessionTargetConflict
	}
	if target.Fingerprint == handle.Session.Target.Fingerprint {
		return nil
	}
	target.Generation = handle.Session.Target.Generation + 1
	updated := handle.Session
	updated.Target = target
	updated.UpdatedAt = m.clock.Now().UTC()
	next, err := m.store.WithSessionTx(ctx, handle.Guard, func(tx ReviewStoreTx) error {
		return tx.SaveSession(ctx, updated)
	})
	if err != nil {
		return err
	}
	handle.Guard = next
	handle.Session = updated
	return nil
}
