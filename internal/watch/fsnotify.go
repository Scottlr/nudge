// Package watch adapts fsnotify into Nudge's bounded, path-free watcher port.
// It reports lossy reasons only; Git remains the source of repository truth.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/fsnotify/fsnotify"
)

var (
	// ErrInvalidConfig reports an invalid adapter bound or ignored root.
	ErrInvalidConfig = errors.New("invalid filesystem watcher config")
	// ErrWatcherNotStarted reports replacement before Start.
	ErrWatcherNotStarted = errors.New("filesystem watcher is not started")
	// ErrWatcherStarted reports a second Start call.
	ErrWatcherStarted = errors.New("filesystem watcher is already started")
	// ErrWatcherClosed reports use after Close.
	ErrWatcherClosed = errors.New("filesystem watcher is closed")
	// ErrWatchedSetIncomplete reports that one or more required roots could not
	// be watched. The adapter still emits a truth-loss hint for reconstruction.
	ErrWatchedSetIncomplete = errors.New("filesystem watcher set is incomplete")
)

const (
	defaultHintBuffer          = 128
	defaultMaxWatchedDirectory = 100_000
)

// Config bounds adapter delivery and directory descriptors. IgnoredRoots are
// explicit Nudge-owned roots; no Git ignore walk is performed.
type Config struct {
	HintBuffer            int
	MaxWatchedDirectories int
	IgnoredRoots          []string
}

func (c Config) normalized() (Config, error) {
	if c.HintBuffer == 0 {
		c.HintBuffer = defaultHintBuffer
	}
	if c.MaxWatchedDirectories == 0 {
		c.MaxWatchedDirectories = defaultMaxWatchedDirectory
	}
	if c.HintBuffer < 1 || c.HintBuffer > 4096 || c.MaxWatchedDirectories < 1 || c.MaxWatchedDirectories > 1_000_000 {
		return Config{}, ErrInvalidConfig
	}
	roots := make([]string, 0, len(c.IgnoredRoots))
	for _, root := range c.IgnoredRoots {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return Config{}, ErrInvalidConfig
		}
		roots = append(roots, root)
	}
	c.IgnoredRoots = roots
	return c, nil
}

// FileWatcher owns one fsnotify watcher and one bounded hint channel.
type FileWatcher struct {
	config  Config
	backend *fsnotify.Watcher
	hints   chan app.WatchHint

	mu          sync.RWMutex
	sendMu      sync.Mutex
	set         app.WatchedSet
	watchedDirs map[string]struct{}
	started     bool
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewFileWatcher constructs an unstarted fsnotify adapter.
func NewFileWatcher(config Config) (*FileWatcher, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	// Keep one reserved delivery slot for truth-loss/control hints. Ordinary
	// filesystem bursts are lossy and may be dropped before that slot is used.
	return &FileWatcher{config: config, hints: make(chan app.WatchHint, config.HintBuffer+1), watchedDirs: make(map[string]struct{})}, nil
}

// Hints returns the bounded path-free hint stream. The adapter closes it
// after Close or an unexpected backend shutdown.
func (w *FileWatcher) Hints() <-chan app.WatchHint { return w.hints }

// Start installs and recursively watches the complete resolved roots.
func (w *FileWatcher) Start(ctx context.Context, set app.WatchedSet) error {
	if w == nil || ctx == nil {
		return ErrInvalidConfig
	}
	if err := set.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWatcherClosed
	}
	if w.started {
		w.mu.Unlock()
		return ErrWatcherStarted
	}
	backend, err := fsnotify.NewWatcher()
	if err != nil {
		w.mu.Unlock()
		return err
	}
	w.backend = backend
	w.set = set.Clone()
	rebuildErr := w.rebuildLocked()
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	w.mu.Unlock()
	if rebuildErr != nil {
		w.emit(app.WatchHint{WatchedSet: set.Clone(), Reason: app.RefreshReasonWatcherError, TruthLost: true})
	}
	go w.run(runCtx, backend)
	return nil
}

// Replace rebuilds coverage for a newly authoritative resolved set. A
// partial rebuild is never silent: the returned error and truth-loss hint both
// require a later authoritative refresh/reconstruction.
func (w *FileWatcher) Replace(ctx context.Context, set app.WatchedSet) error {
	if w == nil || ctx == nil {
		return ErrInvalidConfig
	}
	if err := set.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrWatcherClosed
	}
	if !w.started {
		w.mu.Unlock()
		return ErrWatcherNotStarted
	}
	select {
	case <-ctx.Done():
		w.mu.Unlock()
		return ctx.Err()
	default:
	}
	w.set = set.Clone()
	err := w.rebuildLocked()
	hint := app.WatchHint{WatchedSet: w.set.Clone(), Reason: app.RefreshReasonWatcherError, TruthLost: true}
	w.mu.Unlock()
	if err != nil {
		w.emit(hint)
	}
	return err
}

func (w *FileWatcher) rebuildLocked() error {
	for path := range w.watchedDirs {
		_ = w.backend.Remove(path)
		delete(w.watchedDirs, path)
	}
	var firstErr error
	for _, root := range w.set.Paths {
		if err := w.addTreeLocked(root); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("%w: %v", ErrWatchedSetIncomplete, firstErr)
	}
	return nil
}

func (w *FileWatcher) addTreeLocked(root app.WatchedPath) error {
	info, err := os.Stat(root.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root.Path)
	}
	count := 0
	err = filepath.WalkDir(root.Path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if w.skipPath(root, path, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		count++
		if len(w.watchedDirs) >= w.config.MaxWatchedDirectories {
			return ErrWatchedSetIncomplete
		}
		if err := w.backend.Add(path); err != nil {
			return err
		}
		w.watchedDirs[filepath.Clean(path)] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrWatchedSetIncomplete
	}
	return nil
}

func (w *FileWatcher) skipPath(root app.WatchedPath, path string, entry fs.DirEntry) bool {
	clean := filepath.Clean(path)
	for _, ignored := range w.config.IgnoredRoots {
		if pathWithin(ignored, clean) {
			return true
		}
	}
	if root.Kind == app.WatchPathWorktreeRoot {
		rel, err := filepath.Rel(root.Path, clean)
		if err == nil && (rel == ".git" || len(rel) > len(".git") && rel[:len(".git")+1] == ".git"+string(filepath.Separator)) {
			return true
		}
	}
	return entry.Type()&os.ModeSymlink != 0
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && (len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator)))
}

func (w *FileWatcher) run(ctx context.Context, backend *fsnotify.Watcher) {
	defer close(w.done)
	defer func() {
		w.sendMu.Lock()
		close(w.hints)
		w.sendMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-backend.Events:
			if !ok {
				if ctx.Err() == nil {
					w.emit(app.WatchHint{WatchedSet: w.currentSet(), Reason: app.RefreshReasonWatcherClosed, TruthLost: true})
				}
				return
			}
			w.handleEvent(event)
		case err, ok := <-backend.Errors:
			if !ok {
				if ctx.Err() == nil {
					w.emit(app.WatchHint{WatchedSet: w.currentSet(), Reason: app.RefreshReasonWatcherClosed, TruthLost: true})
				}
				return
			}
			reason := app.RefreshReasonWatcherError
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				reason = app.RefreshReasonWatcherOverflow
			}
			w.emit(app.WatchHint{WatchedSet: w.currentSet(), Reason: reason, TruthLost: true})
		}
	}
}

func (w *FileWatcher) handleEvent(event fsnotify.Event) {
	reason, truthLost, relevant := classifyEvent(event, w.currentSet(), w.isTracked(event.Name))
	if !relevant {
		return
	}
	path := filepath.Clean(event.Name)
	w.mu.RLock()
	set := w.set.Clone()
	w.mu.RUnlock()
	if w.ignored(path) {
		return
	}
	if truthLost {
		w.emit(app.WatchHint{WatchedSet: set, Reason: reason, TruthLost: true})
		return
	}
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			w.mu.Lock()
			err := w.addTreeLocked(app.WatchedPath{Path: path, Kind: app.WatchPathWorktreeRoot})
			w.mu.Unlock()
			if err != nil {
				w.emit(app.WatchHint{WatchedSet: set, Reason: app.RefreshReasonWatcherError, TruthLost: true})
				return
			}
		}
	}
	w.emit(app.WatchHint{WatchedSet: set, Reason: reason})
}

func (w *FileWatcher) isTracked(path string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.watchedDirs[filepath.Clean(path)]
	return ok
}

func classifyEvent(event fsnotify.Event, set app.WatchedSet, tracked bool) (app.RefreshReason, bool, bool) {
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
		return "", false, false
	}
	if tracked && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		return app.RefreshReasonWatchedRootReplaced, true, true
	}
	path := filepath.Clean(event.Name)
	if pathWithin(set.WorktreeGitDir, path) || pathWithin(set.CommonGitDir, path) {
		return app.RefreshReasonHeadChanged, false, true
	}
	return app.RefreshReasonFilesystemChange, false, true
}

func (w *FileWatcher) ignored(path string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, root := range w.config.IgnoredRoots {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

func (w *FileWatcher) currentSet() app.WatchedSet {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.set.Clone()
}

func (w *FileWatcher) emit(hint app.WatchHint) {
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return
	}
	hints := w.hints
	w.mu.RUnlock()
	if hint.TruthLost {
		select {
		case hints <- hint:
		default:
		}
		return
	}
	if len(hints) >= cap(hints)-1 {
		return
	}
	select {
	case hints <- hint:
	default:
	}
}

// Close cancels the backend, wakes the reader, and joins the owner goroutine.
func (w *FileWatcher) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		done := w.done
		w.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	w.closed = true
	cancel := w.cancel
	backend := w.backend
	done := w.done
	w.mu.Unlock()
	if done == nil {
		w.sendMu.Lock()
		close(w.hints)
		w.sendMu.Unlock()
		return nil
	}
	if cancel != nil {
		cancel()
	}
	if backend != nil {
		_ = backend.Close()
	}
	if done != nil {
		<-done
	}
	return nil
}

var _ app.FileWatcher = (*FileWatcher)(nil)
