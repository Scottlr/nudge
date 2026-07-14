// Package capacityprobe provides platform volume/free-space observations.
// It never claims a hard quota when the operating system only reports free
// bytes.
package capacityprobe

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
)

var ErrInvalidPath = errors.New("invalid capacity probe path")

// Probe is the native volume evidence adapter.
type Probe struct{}

// New returns the native probe. It has no mutable process-wide state.
func New() Probe { return Probe{} }

// Observe returns a path-free volume identity and free-space observation.
func (Probe) Observe(ctx context.Context, path string) (app.VolumeEvidence, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return app.VolumeEvidence{}, ErrInvalidPath
	}
	select {
	case <-ctx.Done():
		return app.VolumeEvidence{}, ctx.Err()
	default:
	}
	return observeNative(path)
}
