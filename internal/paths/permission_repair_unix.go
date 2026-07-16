//go:build !windows && (linux || darwin)

package paths

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"golang.org/x/sys/unix"
)

const unixProtectedPermission = uint32(0o700)

func inspectProtectedPermission(path string) (protectedPermissionObservation, error) {
	if err := validatePermissionPath(path); err != nil {
		return protectedPermissionObservation{}, err
	}
	file, err := openProtectedPermissionDirectory(path)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	defer file.Close()
	return observeProtectedPermission(file)
}

func repairProtectedPermission(path, expectedIdentityHash, desiredPermissionHash string) (protectedPermissionObservation, error) {
	if err := validatePermissionPath(path); err != nil {
		return protectedPermissionObservation{}, err
	}
	file, err := openProtectedPermissionDirectory(path)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	defer file.Close()

	before, err := observeProtectedPermission(file)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	if nativeIdentityHash(before.identity) != expectedIdentityHash {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	if before.desiredHash != desiredPermissionHash {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	if before.currentHash != desiredPermissionHash {
		if err := unix.Fchmod(int(file.Fd()), uint32(unixProtectedPermission)); err != nil {
			return protectedPermissionObservation{}, err
		}
	}
	after, err := observeProtectedPermission(file)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	if nativeIdentityHash(after.identity) != expectedIdentityHash || after.currentHash != desiredPermissionHash {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	return after, nil
}

func validatePermissionPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrProtectedPermissionAlias
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return mapPermissionPathError(err)
	}
	if canonical != path {
		return ErrProtectedPermissionAlias
	}
	return nil
}

func openProtectedPermissionDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapPermissionPathError(err)
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return nil, mapPermissionPathError(openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		file.Close()
		return nil, ErrProtectedPath
	}
	if uint64(stat.Uid) != uint64(os.Getuid()) {
		file.Close()
		return nil, ErrProtectedPermissionOwnership
	}
	return file, nil
}

func observeProtectedPermission(file *os.File) (protectedPermissionObservation, error) {
	if file == nil {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	info, err := file.Stat()
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	if !info.IsDir() {
		return protectedPermissionObservation{}, ErrProtectedPath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	if uint64(stat.Uid) != uint64(os.Getuid()) {
		return protectedPermissionObservation{}, ErrProtectedPermissionOwnership
	}
	identity := unixPermissionIdentity(stat)
	mode := uint32(stat.Mode) & 0o7777
	return protectedPermissionObservation{
		identity:    identity,
		currentHash: unixPermissionHash(mode),
		desiredHash: unixPermissionHash(unixProtectedPermission),
	}, nil
}

func unixPermissionIdentity(stat *syscall.Stat_t) repository.NativeIdentity {
	identity := make([]byte, 16)
	binary.LittleEndian.PutUint64(identity[0:8], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(identity[8:16], uint64(stat.Ino))
	return repository.NativeIdentity(string(identity))
}

func unixPermissionHash(mode uint32) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("unix-mode:%04o", mode&0o7777)))
	return fmt.Sprintf("%x", digest[:])
}

func mapPermissionPathError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return ErrProtectedPermissionMissing
	}
	if errors.Is(err, syscall.ELOOP) {
		return ErrProtectedPermissionAlias
	}
	return err
}
