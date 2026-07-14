//go:build !windows

package process

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type unixProcessTree struct {
	mu        sync.Mutex
	pid       int
	canceled  bool
	closed    bool
	forceDone chan struct{}
}

func newProcessTree() processTree { return &unixProcessTree{} }

func (t *unixProcessTree) Prepare(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func (t *unixProcessTree) Attach(cmd *exec.Cmd) error {
	t.mu.Lock()
	t.pid = cmd.Process.Pid
	canceled := t.canceled
	t.mu.Unlock()
	if canceled {
		return t.Cancel()
	}
	return nil
}

func (t *unixProcessTree) Cancel() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	if t.canceled && t.forceDone != nil {
		t.mu.Unlock()
		return nil
	}
	t.canceled = true
	pid := t.pid
	if pid <= 0 {
		t.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	t.forceDone = done
	t.mu.Unlock()
	termErr := syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		close(done)
	}()
	if termErr == syscall.ESRCH {
		return nil
	}
	return termErr
}

func (t *unixProcessTree) Close() error {
	t.mu.Lock()
	done := t.forceDone
	t.closed = true
	t.pid = 0
	t.mu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}
