//go:build !windows

package artifactspool

import (
	"os"
	"syscall"

	"github.com/Scottlr/nudge/internal/app"
)

func validateRegularNoHardLink(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.Mode().IsRegular() {
		return app.ErrSpoolResidueAmbiguous
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return app.ErrSpoolResidueAmbiguous
	}
	return nil
}
