// Package workspace owns the application-facing adoption boundary for local
// captures. It never executes Git or reads the user worktree.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// CaptureStoreConfig supplies the durable session/accounting and protected
// artifact ports required to adopt one T106 result.
type CaptureStoreConfig struct {
	IDs       app.IDSource
	Clock     app.Clock
	Committer app.LocalCaptureCommitter
	Manifests app.CaptureManifestReader
	Reader    app.PublishedArtifactReader
	Releaser  app.PublishedArtifactReleaser
}

// CaptureStore adopts complete candidates and exposes only identity-bound
// accepted-capture reads.
type CaptureStore struct {
	ids       app.IDSource
	clock     app.Clock
	committer app.LocalCaptureCommitter
	manifests app.CaptureManifestReader
	reader    app.PublishedArtifactReader
	releaser  app.PublishedArtifactReleaser
}

// NewCaptureStore validates the application-owned ports before any candidate
// handle can be published.
func NewCaptureStore(config CaptureStoreConfig) (*CaptureStore, error) {
	if config.IDs == nil {
		config.IDs = app.RandomIDSource{}
	}
	if config.Clock == nil {
		config.Clock = app.SystemClock{}
	}
	if config.Committer == nil || config.Manifests == nil || config.Reader == nil || config.Releaser == nil {
		return nil, app.ErrInvalidLocalCaptureManifest
	}
	return &CaptureStore{
		ids:       config.IDs,
		clock:     config.Clock,
		committer: config.Committer,
		manifests: config.Manifests,
		reader:    config.Reader,
		releaser:  config.Releaser,
	}, nil
}

var _ app.LocalCaptureStore = (*CaptureStore)(nil)

// Adopt verifies and publishes one candidate, then asks the fenced durable
// committer to make its generation visible. A failed known commit is cleaned
// up only through the exact published-artifact capability.
func (s *CaptureStore) Adopt(ctx context.Context, artifacts app.LocalCaptureArtifacts, state app.CaptureSessionState) (adoption app.CaptureAdoption, err error) {
	if s == nil || ctx == nil {
		return app.CaptureAdoption{}, app.ErrInvalidLocalCaptureManifest
	}
	if err := state.Validate(); err != nil {
		return app.CaptureAdoption{}, err
	}
	if err := artifacts.Validate(); err != nil {
		return app.CaptureAdoption{}, err
	}
	if artifacts.Candidate.RepositoryID != state.RepositoryID || artifacts.Candidate.WorktreeID != state.WorktreeID {
		return app.CaptureAdoption{}, app.ErrCaptureGenerationConflict
	}
	if state.Current != nil && state.Current.Fingerprint == artifacts.Candidate.Fingerprint {
		if err := artifacts.Abort(ctx); err != nil {
			return app.CaptureAdoption{}, err
		}
		return app.CaptureAdoption{Generation: *state.Current, Reused: true}, nil
	}

	generation := repository.TargetGeneration(1)
	if state.Current != nil {
		if uint64(state.Current.Generation) == math.MaxUint64 {
			return app.CaptureAdoption{}, app.ErrCaptureGenerationConflict
		}
		generation = state.Current.Generation + 1
	}
	captureID, err := domain.NewCaptureID(s.ids.NewID())
	if err != nil {
		return app.CaptureAdoption{}, fmt.Errorf("capture ID: %w", err)
	}
	patchIdentity, err := artifacts.PatchIdentity()
	if err != nil {
		return app.CaptureAdoption{}, err
	}
	blobIdentity, err := artifacts.BlobIdentity()
	if err != nil {
		return app.CaptureAdoption{}, err
	}
	patchTarget := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: capturePath(captureID, "patch"), SourceRelativePath: artifacts.Candidate.Patch.RelativePath}
	blobTarget := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: capturePath(captureID, "blobs")}
	manifestHash, err := app.CaptureManifestHash(artifacts.Candidate, patchIdentity, blobIdentity)
	if err != nil {
		return app.CaptureAdoption{}, err
	}
	now := s.clock.Now().UTC()
	manifest := app.CaptureManifest{
		Version:      app.LocalCaptureManifestVersion,
		CaptureID:    captureID,
		RepositoryID: artifacts.Candidate.RepositoryID,
		WorktreeID:   artifacts.Candidate.WorktreeID,
		Candidate:    artifacts.Candidate,
		Patch: app.CaptureArtifactRef{
			Kind:     repositoryCapturePatch,
			Identity: patchIdentity,
			Target:   patchTarget,
			Limits:   artifacts.PatchSpool.Descriptor().Limits,
		},
		Blobs: app.CaptureArtifactRef{
			Kind:         repositoryCaptureBlobs,
			Identity:     blobIdentity,
			Target:       blobTarget,
			Limits:       artifacts.BlobSpool.Descriptor().Limits,
			RelativePath: "payload",
		},
		ManifestHash: manifestHash,
		CreatedAt:    now,
	}
	if err := manifest.Validate(); err != nil {
		return app.CaptureAdoption{}, err
	}
	accepted := app.CaptureGeneration{
		CaptureID:    captureID,
		Generation:   generation,
		RepositoryID: artifacts.Candidate.RepositoryID,
		WorktreeID:   artifacts.Candidate.WorktreeID,
		Fingerprint:  artifacts.Candidate.Fingerprint,
		ManifestHash: manifestHash,
		Base:         artifacts.Candidate.Base,
		CreatedAt:    now,
	}
	if err := accepted.Validate(); err != nil {
		return app.CaptureAdoption{}, err
	}

	patchPublished, err := artifacts.PatchSpool.Publish(ctx, patchIdentity, patchTarget)
	if err != nil {
		return app.CaptureAdoption{}, err
	}
	blobsPublished, err := artifacts.BlobSpool.Publish(ctx, blobIdentity, blobTarget)
	if err != nil {
		return app.CaptureAdoption{}, err
	}
	if err := s.committer.CommitLocalCapture(ctx, state, accepted, manifest, artifacts.Reservation, artifacts.Plan); err != nil {
		if errors.Is(err, app.ErrCaptureCommitUnknown) {
			return app.CaptureAdoption{}, err
		}
		cleanupErr := s.cleanupPublished(ctx, artifacts, patchPublished, blobsPublished)
		if cleanupErr != nil {
			return app.CaptureAdoption{}, errors.Join(err, cleanupErr)
		}
		return app.CaptureAdoption{}, err
	}
	return app.CaptureAdoption{Generation: accepted, Manifest: manifest}, nil
}

const (
	repositoryCapturePatch = "patch"
	repositoryCaptureBlobs = "blobs"
)

func (s *CaptureStore) cleanupPublished(ctx context.Context, artifacts app.LocalCaptureArtifacts, published ...app.PublishedArtifact) error {
	var cleanupErr error
	for index := len(published) - 1; index >= 0; index-- {
		if err := s.releaser.RemovePublished(ctx, published[index]); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if err := artifacts.Capacity.Release(ctx, artifacts.Reservation, artifacts.Plan, artifacts.Policy); err != nil {
		return err
	}
	return nil
}

// OpenCaptureManifest loads and validates one durable manifest. The adapter
// remains the source of truth after a process restart.
func (s *CaptureStore) OpenCaptureManifest(ctx context.Context, captureID domain.CaptureID) (app.CaptureManifest, error) {
	if s == nil || ctx == nil || captureID == "" {
		return app.CaptureManifest{}, app.ErrInvalidLocalCaptureManifest
	}
	manifest, err := s.manifests.OpenCaptureManifest(ctx, captureID)
	if err != nil {
		return app.CaptureManifest{}, err
	}
	if manifest.CaptureID != captureID {
		return app.CaptureManifest{}, app.ErrCaptureCorrupt
	}
	if err := manifest.Validate(); err != nil {
		return app.CaptureManifest{}, app.ErrCaptureCorrupt
	}
	return manifest, nil
}

// ReadBlobRange resolves a blob only through the accepted manifest and then
// delegates the bounded identity check to the protected artifact adapter.
func (s *CaptureStore) ReadBlobRange(ctx context.Context, request app.CaptureBlobRead) ([]byte, error) {
	if s == nil || ctx == nil || request.CaptureID == "" || request.ManifestHash == "" || request.RelativePath == "" || request.Expected.Bytes == 0 || request.Expected.SHA256 == "" || request.MaxBytes == 0 {
		return nil, app.ErrInvalidLocalCaptureManifest
	}
	manifest, err := s.OpenCaptureManifest(ctx, request.CaptureID)
	if err != nil {
		return nil, err
	}
	if manifest.ManifestHash != request.ManifestHash {
		return nil, app.ErrCaptureCorrupt
	}
	for _, entry := range manifest.Candidate.Entries {
		for _, blob := range entry.Blobs {
			if blob.Artifact.RelativePath != request.RelativePath || blob.Artifact.Bytes != uint64(request.Expected.Bytes) || blob.Artifact.ContentSHA256 != request.Expected.SHA256 {
				continue
			}
			return s.reader.ReadPublishedRange(ctx, manifest.Blobs.Target, request.RelativePath, request.Expected, request.Offset, request.MaxBytes)
		}
	}
	return nil, app.ErrCaptureNotFound
}

func capturePath(captureID domain.CaptureID, name string) string {
	digest := sha256.Sum256([]byte(captureID))
	return "captures/" + hex.EncodeToString(digest[:]) + "/" + name
}
