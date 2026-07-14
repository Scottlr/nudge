//go:build windows

package capacityprobe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"golang.org/x/sys/windows"
)

func observeNative(path string) (app.VolumeEvidence, error) {
	root := filepath.VolumeName(path)
	if root == "" {
		return app.VolumeEvidence{}, fmt.Errorf("volume identity unavailable")
	}
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	root16, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return app.VolumeEvidence{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(root16, &free, &total, &totalFree); err != nil {
		return app.VolumeEvidence{}, err
	}
	var name [windows.MAX_PATH + 1]uint16
	var filesystem [windows.MAX_PATH + 1]uint16
	var serial, maximumComponentLength, flags uint32
	if err := windows.GetVolumeInformation(root16, &name[0], uint32(len(name)), &serial, &maximumComponentLength, &flags, &filesystem[0], uint32(len(filesystem))); err != nil {
		return app.VolumeEvidence{}, err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", strings.ToUpper(root), serial)))
	return app.VolumeEvidence{
		ID:       "windows:" + hex.EncodeToString(digest[:]),
		Free:     app.ByteSize(free),
		Mode:     app.VolumeCapacityMonitored,
		Stable:   true,
		Observed: time.Now().UTC(),
	}, nil
}
