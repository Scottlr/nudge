package artifactspool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
)

func TestPublishedRangeIsIdentityBoundAndNewArtifactCanBeRemoved(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteFrom(context.Background(), "data", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	identity, err := handle.CloseAndVerify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	published, err := handle.Publish(context.Background(), identity, app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "capture/patch", SourceRelativePath: "data"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("hello"))
	value, err := manager.ReadPublishedRange(context.Background(), published.Target, "", app.StreamIdentity{Bytes: 5, SHA256: hex.EncodeToString(digest[:])}, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "ell" {
		t.Fatalf("range = %q, want ell", value)
	}
	if _, err := manager.ReadPublishedRange(context.Background(), published.Target, "", app.StreamIdentity{Bytes: 5, SHA256: strings.Repeat("0", 64)}, 0, 5); !errors.Is(err, app.ErrCaptureCorrupt) {
		t.Fatalf("wrong identity error = %v, want capture corrupt", err)
	}
	if err := manager.RemovePublished(context.Background(), published); err != nil {
		t.Fatal(err)
	}
}
