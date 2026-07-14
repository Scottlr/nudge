//go:build !windows

package capacityprobe

import (
	"fmt"
	"math"
	"os"
	"syscall"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"golang.org/x/sys/unix"
)

func observeNative(path string) (app.VolumeEvidence, error) {
	info, err := os.Stat(path)
	if err != nil {
		return app.VolumeEvidence{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return app.VolumeEvidence{}, errorsNative()
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(path, &filesystem); err != nil {
		return app.VolumeEvidence{}, err
	}
	if filesystem.Bsize <= 0 {
		return app.VolumeEvidence{}, errorsNative()
	}
	blockSize := uint64(filesystem.Bsize)
	available := uint64(filesystem.Bavail)
	if blockSize != 0 && available > math.MaxUint64/blockSize {
		return app.VolumeEvidence{}, errorsNative()
	}
	return app.VolumeEvidence{
		ID:       fmt.Sprintf("unix:%d", stat.Dev),
		Free:     app.ByteSize(available * blockSize),
		Mode:     app.VolumeCapacityMonitored,
		Stable:   true,
		Observed: time.Now().UTC(),
	}, nil
}

func errorsNative() error { return fmt.Errorf("native volume evidence unavailable") }
