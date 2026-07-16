package logging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

type ownerMarker struct {
	Version       uint32              `json:"version"`
	ProcessID     string              `json:"process_id"`
	RepositoryID  domain.RepositoryID `json:"repository_id,omitempty"`
	State         string              `json:"state"`
	CreatedAt     string              `json:"created_at"`
	ClosedAt      string              `json:"closed_at,omitempty"`
	Files         []string            `json:"files"`
	ReservationID string              `json:"reservation_id,omitempty"`
	PlanDigest    string              `json:"plan_digest,omitempty"`
	VolumeID      string              `json:"volume_id,omitempty"`
}

func (w *Writer) rotateLocked() error {
	if w.file == nil {
		return errLogClosed
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	w.generation++
	rotatedName := logName(w.processID, w.generation)
	if _, err := os.Lstat(filepath.Join(w.root, rotatedName)); !errors.Is(err, os.ErrNotExist) {
		return errLogDisabled
	}
	if err := linkAndRemove(filepath.Join(w.root, w.activeName), filepath.Join(w.root, rotatedName)); err != nil {
		return err
	}
	rotated := append([]string(nil), w.files[1:]...)
	w.files = append([]string{w.activeName, rotatedName}, rotated...)
	file, err := paths.OpenProtectedFile(w.root, w.activeName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w.file = file
	w.bytes = 0
	if err := w.updateMarker(false); err != nil {
		return err
	}
	return w.enforceFileLimitLocked()
}

func (w *Writer) enforceFileLimitLocked() error {
	for uint64(len(w.files)) > w.maxFiles {
		if len(w.files) <= 1 {
			return nil
		}
		oldest := w.files[len(w.files)-1]
		if oldest == w.activeName {
			return errLogDisabled
		}
		if err := removeOwnedFile(w.root, oldest); err != nil {
			return err
		}
		w.files = w.files[:len(w.files)-1]
	}
	return w.updateMarker(false)
}

func (w *Writer) reapClosed() error {
	entries, err := os.ReadDir(w.ownerRoot)
	if err != nil {
		return err
	}
	type candidate struct {
		name   string
		marker ownerMarker
		closed time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	cutoff := w.now().UTC().Add(-w.retention)
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" || name == w.markerName {
			continue
		}
		data, err := paths.ReadProtectedFileBounded(w.ownerRoot, name, maxOwnerMarkerBytes)
		if err != nil {
			continue
		}
		var marker ownerMarker
		if json.Unmarshal(data, &marker) != nil || marker.Version != logSchemaVersion || marker.State != "closed" || !validOwnerToken(marker.ProcessID) || len(marker.Files) == 0 {
			continue
		}
		closed, err := time.Parse(time.RFC3339Nano, marker.ClosedAt)
		if err != nil || closed.After(cutoff) {
			continue
		}
		candidates = append(candidates, candidate{name: name, marker: marker, closed: closed})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].closed.Before(candidates[j].closed) })
	for _, item := range candidates {
		if err := w.reapCandidate(item.marker); err != nil {
			if errors.Is(err, filelock.ErrBusy) {
				continue
			}
			return err
		}
		if err := removeOwnedFile(w.ownerRoot, item.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		lockName := strings.TrimSuffix(item.name, ".json") + ".lock"
		if err := removeOwnedFile(w.ownerRoot, lockName); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (w *Writer) reapCandidate(marker ownerMarker) error {
	lock, err := filelock.TryAcquire(context.Background(), filepath.Join(w.ownerRoot, marker.ProcessID+".lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	for _, name := range marker.Files {
		if !validOwnedLogName(marker.ProcessID, name) {
			return app.ErrInvalidLogField
		}
		if err := removeOwnedFile(w.root, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func linkAndRemove(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

func validOwnedLogName(processID, name string) bool {
	if !validOwnerToken(processID) || filepath.Base(name) != name || filepath.Ext(name) != ".jsonl" {
		return false
	}
	prefix := "log-" + processID + "-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	generation, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl"), 10, 64)
	return err == nil && logName(processID, generation) == name
}
