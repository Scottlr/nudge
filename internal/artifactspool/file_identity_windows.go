//go:build windows

package artifactspool

import (
	"os"

	"github.com/Scottlr/nudge/internal/app"
	"golang.org/x/sys/windows"
)

func validateRegularNoHardLink(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil || !info.Mode().IsRegular() {
		return app.ErrSpoolResidueAmbiguous
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &data); err != nil {
		return err
	}
	if data.NumberOfLinks != 1 {
		return app.ErrSpoolResidueAmbiguous
	}
	return nil
}
