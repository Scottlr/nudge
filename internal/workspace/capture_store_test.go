package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestCaptureStoreReusesIdenticalAndAdvancesAtoBtoA(t *testing.T) {
	committer := &captureCommitter{manifests: make(map[domain.CaptureID]app.CaptureManifest)}
	reader := &captureReader{data: map[string][]byte{"blob-00000000": []byte("A")}}
	store, err := NewCaptureStore(CaptureStoreConfig{Committer: committer, Manifests: committer, Reader: reader, Releaser: &captureReleaser{}})
	if err != nil {
		t.Fatal(err)
	}
	state := testCaptureSessionState()

	first := testCaptureArtifacts(t, "A", "patch-1", "blob-1")
	adoption, err := store.Adopt(context.Background(), first, state)
	if err != nil {
		t.Fatal(err)
	}
	if adoption.Generation.Generation != 1 || adoption.Reused {
		t.Fatalf("first adoption = %+v", adoption)
	}
	state.Current = &adoption.Generation
	state.Guard.Revision++

	identical := testCaptureArtifacts(t, "A", "patch-2", "blob-2")
	reused, err := store.Adopt(context.Background(), identical, state)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.Generation.Generation != 1 {
		t.Fatalf("identical adoption = %+v", reused)
	}
	if identical.PatchSpool.(*captureSpool).aborted != 1 || identical.BlobSpool.(*captureSpool).aborted != 1 {
		t.Fatal("identical candidate was not aborted through both typed handles")
	}

	state.Guard.Revision++
	changed := testCaptureArtifacts(t, "B", "patch-3", "blob-3")
	second, err := store.Adopt(context.Background(), changed, state)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation.Generation != 2 {
		t.Fatalf("changed adoption generation = %d, want 2", second.Generation.Generation)
	}
	state.Current = &second.Generation
	state.Guard.Revision++

	back := testCaptureArtifacts(t, "A", "patch-4", "blob-4")
	third, err := store.Adopt(context.Background(), back, state)
	if err != nil {
		t.Fatal(err)
	}
	if third.Generation.Generation != 3 {
		t.Fatalf("A after B generation = %d, want 3", third.Generation.Generation)
	}

	request := app.CaptureBlobRead{
		CaptureID:    third.Generation.CaptureID,
		ManifestHash: third.Manifest.ManifestHash,
		RelativePath: "blob-00000000",
		Expected:     app.StreamIdentity{Bytes: 1, SHA256: sha256Hex("A")},
		MaxBytes:     1,
	}
	value, err := store.ReadBlobRange(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "A" {
		t.Fatalf("accepted blob = %q", value)
	}
}

func TestCaptureStoreRemovesNewArtifactsWhenGuardedCommitFails(t *testing.T) {
	releaser := &captureReleaser{}
	committer := &captureCommitter{manifests: make(map[domain.CaptureID]app.CaptureManifest), err: errors.New("commit rejected")}
	store, err := NewCaptureStore(CaptureStoreConfig{Committer: committer, Manifests: committer, Reader: &captureReader{data: map[string][]byte{}}, Releaser: releaser})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := testCaptureArtifacts(t, "A", "patch-fail", "blob-fail")
	_, err = store.Adopt(context.Background(), artifacts, testCaptureSessionState())
	if !errors.Is(err, committer.err) || releaser.removed != 2 || artifacts.Capacity.(*captureCapacity).released != 1 {
		t.Fatalf("failed adoption = %v, removed=%d released=%d", err, releaser.removed, artifacts.Capacity.(*captureCapacity).released)
	}
}

type captureCommitter struct {
	manifests map[domain.CaptureID]app.CaptureManifest
	err       error
}

func (c *captureCommitter) CommitLocalCapture(_ context.Context, _ app.CaptureSessionState, generation app.CaptureGeneration, manifest app.CaptureManifest, _ app.CapacityReservation, _ app.CapacityPlan) error {
	if c.err != nil {
		return c.err
	}
	c.manifests[generation.CaptureID] = manifest
	return nil
}

func (c *captureCommitter) OpenCaptureManifest(_ context.Context, captureID domain.CaptureID) (app.CaptureManifest, error) {
	manifest, ok := c.manifests[captureID]
	if !ok {
		return app.CaptureManifest{}, app.ErrCaptureNotFound
	}
	return manifest, nil
}

type captureReader struct {
	data map[string][]byte
}

func (r *captureReader) ReadPublishedRange(_ context.Context, _ app.PublishTarget, relative string, expected app.StreamIdentity, offset, max app.ByteSize) ([]byte, error) {
	value, ok := r.data[relative]
	if !ok || uint64(len(value)) != uint64(expected.Bytes) || sha256Hex(string(value)) != expected.SHA256 || offset > expected.Bytes {
		return nil, app.ErrCaptureCorrupt
	}
	end := offset + max
	if end > expected.Bytes {
		end = expected.Bytes
	}
	return append([]byte(nil), value[offset:end]...), nil
}

type captureReleaser struct {
	removed int
}

func (r *captureReleaser) RemovePublished(_ context.Context, _ app.PublishedArtifact) error {
	r.removed++
	return nil
}

type captureCapacity struct {
	released int
}

func (c *captureCapacity) Reserve(context.Context, app.CapacityPlan, app.ResourcePolicy, []app.VolumeEvidence) (app.CapacityReservation, error) {
	return app.CapacityReservation{}, nil
}

func (c *captureCapacity) Recheck(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.RecheckBounds, []app.VolumeEvidence) (app.CapacityCheck, error) {
	return app.CapacityCheck{}, nil
}

func (c *captureCapacity) Release(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy) error {
	c.released++
	return nil
}

func (c *captureCapacity) Reconcile(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.ReconciliationProof) error {
	return nil
}

type captureSpool struct {
	descriptor app.ArtifactSpool
	aborted    int
}

func (s *captureSpool) Descriptor() app.ArtifactSpool { return s.descriptor }

func (s *captureSpool) CreateFile(context.Context, string) (app.ArtifactSpoolFile, error) {
	return nil, io.ErrClosedPipe
}

func (s *captureSpool) WriteFrom(context.Context, string, io.Reader) (app.StreamIdentity, error) {
	return app.StreamIdentity{}, io.ErrClosedPipe
}

func (s *captureSpool) CloseAndVerify(context.Context) (app.ArtifactIdentity, error) {
	return app.ArtifactIdentity{}, app.ErrSpoolNotReady
}

func (s *captureSpool) Publish(_ context.Context, expected app.ArtifactIdentity, target app.PublishTarget) (app.PublishedArtifact, error) {
	return app.PublishedArtifact{Identity: expected, Target: target, Limits: s.descriptor.Limits}, nil
}

func (s *captureSpool) Abort(context.Context) error {
	s.aborted++
	return nil
}

func (s *captureSpool) Recover(context.Context, app.SpoolRecoveryProof) error { return nil }

func testCaptureSessionState() app.CaptureSessionState {
	return app.CaptureSessionState{
		Guard:        app.CaptureSessionGuard{SessionID: "session", LeaseID: "lease", WriterEpoch: 1, Revision: 1},
		RepositoryID: "repository",
		WorktreeID:   "worktree",
	}
}

func testCaptureArtifacts(t *testing.T, value, patchID, blobID string) app.LocalCaptureArtifacts {
	t.Helper()
	policy := app.DefaultResourcePolicy()
	when := time.Unix(1, 0).UTC()
	path, err := repository.NewRepoPath([]byte("file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	base, err := repository.NewObjectID("base-object")
	if err != nil {
		t.Fatal(err)
	}
	patchHash := sha256Hex("patch-" + value)
	blobHash := sha256Hex(value)
	patchManifest := sha256Hex("patch-manifest-" + value)
	blobManifest := sha256Hex("blob-manifest-" + value)
	patch := repository.CaptureArtifact{Kind: repository.CaptureArtifactPatch, SpoolID: patchID, ManifestHash: patchManifest, RelativePath: "patch", Bytes: 1, Entries: 1, ContentSHA256: patchHash, VerifiedAt: when}
	blobs := repository.CaptureArtifact{Kind: repository.CaptureArtifactBlobs, SpoolID: blobID, ManifestHash: blobManifest, RelativePath: "payload", Bytes: uint64(len(value)), Entries: 1, VerifiedAt: when}
	entry := repository.LocalCaptureEntry{Change: repository.ChangedFile{NewPath: &path, Kind: repository.ChangeUntracked, NewFileKind: repository.FileKindRegular, NewMode: 0o100644, Unstaged: true}, Blobs: []repository.CaptureBlobRef{{Side: repository.CaptureBlobWorkingTree, Path: path, Artifact: repository.CaptureArtifact{Kind: repository.CaptureArtifactBlobs, SpoolID: blobID, ManifestHash: blobManifest, RelativePath: "blob-00000000", Bytes: uint64(len(value)), Entries: 1, ContentSHA256: blobHash, VerifiedAt: when}}}}
	candidate := repository.LocalCaptureCandidate{Version: repository.LocalCaptureCandidateVersion, RepositoryID: "repository", WorktreeID: "worktree", Base: repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: base}, Index: repository.LocalCaptureIndexEvidence{}, Entries: []repository.LocalCaptureEntry{entry}, Patch: patch, BlobSpool: blobs, Policy: repository.CapturePolicyEvidence{MachineGitVersion: 1, RenameVersion: 1, RenameOutcome: "complete", PatchFormatVersion: 1, ConversionPolicyVersion: 1, ConversionDecision: "byte_neutral", ConversionFingerprint: sha256Hex("conversion"), ResourcePolicyVersion: uint32(policy.Version)}, Consistency: repository.CaptureConsistencyEvidence{HeadToken: sha256Hex("head"), IndexToken: sha256Hex("index"), StatusToken: sha256Hex("status"), FlagsToken: sha256Hex("flags"), FilesystemToken: sha256Hex("filesystem"), AggregateToken: sha256Hex("aggregate")}, EntryCount: 1, TotalBytes: 1 + uint64(len(value)), CapturedAt: when}
	fingerprint, err := candidate.FingerprintValue()
	if err != nil {
		t.Fatal(err)
	}
	candidate.Fingerprint = fingerprint
	candidate.Consistency.AggregateToken = fingerprint
	if err := candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	operation := domain.OperationID("operation")
	reservation, err := app.NewCapacityReservation("reservation", operation, "repository", "plan", policy.Version)
	if err != nil {
		t.Fatal(err)
	}
	limits := app.SpoolLimits{MaxBytes: 1024, MaxEntries: 16, MaxPathBytes: 128, MaxManifestBytes: 4096, BufferBytes: 1, CheckEveryBytes: 1}
	capacity := &captureCapacity{}
	return app.LocalCaptureArtifacts{Candidate: candidate, PatchSpool: &captureSpool{descriptor: app.ArtifactSpool{SpoolID: patchID, OperationID: operation, OwnerKind: app.OwnerCapture, ReservationID: reservation.Marker(), RootNonce: strings.Repeat("a", 32), Limits: limits, State: app.SpoolVerified}}, BlobSpool: &captureSpool{descriptor: app.ArtifactSpool{SpoolID: blobID, OperationID: operation, OwnerKind: app.OwnerCapture, ReservationID: reservation.Marker(), RootNonce: strings.Repeat("b", 32), Limits: limits, State: app.SpoolVerified}}, Reservation: reservation, Plan: app.CapacityPlan{OperationID: operation, PolicyVersion: policy.Version, VolumePeaks: []app.VolumePeak{{ID: "volume", Reserve: 1}}}, Policy: policy, Capacity: capacity}
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
