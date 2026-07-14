//go:build windows

package artifactspool

import (
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"golang.org/x/sys/windows"
)

func renameNoReplace(from, to string) error {
	from16, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	to16, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from16, to16, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return ErrDestinationExists
		}
		return err
	}
	return nil
}

var _ = app.ErrSpoolPublicationUnsupported
