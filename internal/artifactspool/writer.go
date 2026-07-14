package artifactspool

import (
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/Scottlr/nudge/internal/app"
)

type fileWriter struct {
	spool  *Handle
	file   *os.File
	hash   hash.Hash
	bytes  app.ByteSize
	mu     sync.Mutex
	closed bool
}

func (w *fileWriter) Write(data []byte) (int, error) {
	if w == nil || w.spool == nil || w.file == nil {
		return 0, app.ErrInvalidArtifactSpool
	}
	if len(data) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return 0, app.ErrSpoolNotReady
	}
	w.spool.stateMu.Lock()
	state := w.spool.descriptor.State
	limit := w.spool.descriptor.Limits.MaxBytes
	w.spool.stateMu.Unlock()
	if state != app.SpoolOpen {
		return 0, app.ErrSpoolNotReady
	}
	if err := reserveBytes(&w.spool.reserved, limit, len(data)); err != nil {
		w.spool.markRecoveryLocal()
		return 0, err
	}
	read, err := w.file.Write(data)
	if read < len(data) {
		releaseBytes(&w.spool.reserved, len(data)-read)
	}
	if read > 0 {
		_, _ = w.hash.Write(data[:read])
		w.bytes += app.ByteSize(read)
	}
	if err != nil {
		w.spool.markRecoveryLocal()
		return read, err
	}
	if read != len(data) {
		w.spool.markRecoveryLocal()
		return read, io.ErrShortWrite
	}
	return read, nil
}

func (w *fileWriter) Close() error {
	if w == nil || w.file == nil {
		return app.ErrInvalidArtifactSpool
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	syncErr := w.file.Sync()
	closeErr := w.file.Close()
	w.closed = true
	w.mu.Unlock()
	w.spool.stateMu.Lock()
	if w.spool.active > 0 {
		w.spool.active--
	}
	w.spool.stateMu.Unlock()
	return errors.Join(syncErr, closeErr)
}

func (w *fileWriter) identity() app.StreamIdentity {
	if w == nil {
		return app.StreamIdentity{}
	}
	return app.StreamIdentity{Bytes: w.bytes, SHA256: hex.EncodeToString(w.hash.Sum(nil))}
}

func reserveBytes(counter *atomic.Uint64, limit app.ByteSize, requested int) error {
	if requested < 0 {
		return app.ErrInvalidArtifactSpool
	}
	value := uint64(requested)
	for {
		current := counter.Load()
		if current > uint64(limit) || value > uint64(limit)-current {
			return app.ErrSpoolLimitExceeded
		}
		if counter.CompareAndSwap(current, current+value) {
			return nil
		}
	}
}

func releaseBytes(counter *atomic.Uint64, count int) {
	if count > 0 {
		counter.Add(^uint64(count - 1))
	}
}
