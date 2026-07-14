package artifactspool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

type markerRecord struct {
	Version          uint32          `json:"version"`
	SpoolID          string          `json:"spool_id"`
	OperationID      string          `json:"operation_id"`
	OwnerKind        string          `json:"owner_kind"`
	ReservationID    string          `json:"reservation_id"`
	RootNonce        string          `json:"root_nonce"`
	Limits           app.SpoolLimits `json:"limits"`
	State            string          `json:"state"`
	CreatedUnixNano  int64           `json:"created_unix_nano"`
	ManifestHash     string          `json:"manifest_hash,omitempty"`
	ManifestBytes    uint64          `json:"manifest_bytes,omitempty"`
	ManifestEntries  uint64          `json:"manifest_entries,omitempty"`
	VerifiedUnixNano int64           `json:"verified_unix_nano,omitempty"`
	TargetDigest     string          `json:"target_digest,omitempty"`
}

func markerRecordFromDescriptor(descriptor app.ArtifactSpool, now time.Time) markerRecord {
	return markerRecord{
		Version:         markerVersion,
		SpoolID:         descriptor.SpoolID,
		OperationID:     string(descriptor.OperationID),
		OwnerKind:       string(descriptor.OwnerKind),
		ReservationID:   descriptor.ReservationID,
		RootNonce:       descriptor.RootNonce,
		Limits:          descriptor.Limits,
		State:           string(descriptor.State),
		CreatedUnixNano: now.UnixNano(),
	}
}

func artifactIdentityFromMarker(record markerRecord) app.ArtifactIdentity {
	return app.ArtifactIdentity{
		SpoolID:      record.SpoolID,
		ManifestHash: record.ManifestHash,
		Bytes:        app.ByteSize(record.ManifestBytes),
		Entries:      app.Count(record.ManifestEntries),
		Complete:     record.ManifestHash != "" && record.VerifiedUnixNano > 0,
		VerifiedAt:   time.Unix(0, record.VerifiedUnixNano).UTC(),
	}
}

func writeNewMarker(root string, record markerRecord) error {
	if err := validateMarker(record); err != nil {
		return err
	}
	file, err := paths.OpenProtectedFile(root, markerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(filepath.Join(root, markerName))
		}
	}()
	if err := encodeMarker(file, record); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	keep = true
	return nil
}

func readMarker(root string) (markerRecord, error) {
	data, err := paths.ReadProtectedFile(root, markerName)
	if err != nil {
		return markerRecord{}, app.ErrSpoolResidueAmbiguous
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record markerRecord
	if err := decoder.Decode(&record); err != nil {
		return markerRecord{}, app.ErrSpoolResidueAmbiguous
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return markerRecord{}, app.ErrSpoolResidueAmbiguous
	}
	if err := validateMarker(record); err != nil {
		return markerRecord{}, app.ErrSpoolResidueAmbiguous
	}
	return record, nil
}

func writeMarker(root string, record markerRecord) error {
	if err := validateMarker(record); err != nil {
		return err
	}
	file, err := paths.OpenExistingProtectedFileForUpdate(root, markerName, os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if err := encodeMarker(file, record); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDirectory(root)
}

func encodeMarker(file *os.File, record markerRecord) error {
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(record)
}

func validateMarker(record markerRecord) error {
	if record.Version != markerVersion || record.SpoolID == "" || record.OperationID == "" || !appOwnerKind(record.OwnerKind) || record.ReservationID == "" || record.RootNonce == "" || record.CreatedUnixNano <= 0 || !validState(record.State) {
		return ErrMarkerUnknown
	}
	if err := record.Limits.Validate(); err != nil {
		return ErrMarkerUnknown
	}
	if record.ManifestHash != "" && !validHash(record.ManifestHash) {
		return ErrMarkerUnknown
	}
	if record.VerifiedUnixNano < 0 || record.ManifestHash == "" && (record.ManifestBytes != 0 || record.ManifestEntries != 0 || record.VerifiedUnixNano != 0) {
		return ErrMarkerUnknown
	}
	return nil
}

func verifyMarkerDescriptor(record markerRecord, descriptor app.ArtifactSpool) error {
	if err := validateMarker(record); err != nil {
		return ErrMarkerUnknown
	}
	if record.SpoolID != descriptor.SpoolID || record.OperationID != string(descriptor.OperationID) || record.OwnerKind != string(descriptor.OwnerKind) || record.ReservationID != descriptor.ReservationID || record.RootNonce != descriptor.RootNonce || record.Limits != descriptor.Limits {
		return ErrMarkerMismatch
	}
	return nil
}

func validState(value string) bool {
	switch app.SpoolState(value) {
	case app.SpoolOpen, app.SpoolVerifying, app.SpoolVerified, app.SpoolPublishing, app.SpoolPublished, app.SpoolRecoveryRequired:
		return true
	default:
		return false
	}
}

func appOwnerKind(value string) bool {
	return app.OwnerKind(value) != "" && len(value) <= 64
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}
