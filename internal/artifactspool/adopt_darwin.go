//go:build darwin

package artifactspool

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		return ErrDestinationExists
	}
	return err
}
