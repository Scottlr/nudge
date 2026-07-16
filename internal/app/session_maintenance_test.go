package app

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestOpenSessionHonorsRepositoryMaintenanceGate(t *testing.T) {
	store := newFakeSessionStore()
	gate := &rejectingMaintenanceGate{}
	manager, err := NewSessionManager(SessionManagerConfig{
		Store:       store,
		Leases:      fakeLeaseManager{},
		Maintenance: gate,
		IDs:         &sequenceIDs{values: []string{"session-1", "lease-1"}},
		Clock:       fixedClock{when: testTime},
	})
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}

	_, err = manager.OpenSession(context.Background(), testSessionRequest(t, "fingerprint-1", "base-object"))
	if !errors.Is(err, ErrRepositoryMaintenance) {
		t.Fatalf("open session error = %v, want maintenance error", err)
	}
	if gate.calls != 1 {
		t.Fatalf("maintenance gate calls = %d, want 1", gate.calls)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("sessions created while maintenance was active: %d", len(store.sessions))
	}
}

type rejectingMaintenanceGate struct {
	calls int
}

func (g *rejectingMaintenanceGate) Acquire(context.Context, domain.RepositoryID) (io.Closer, error) {
	g.calls++
	return nil, ErrRepositoryMaintenance
}
