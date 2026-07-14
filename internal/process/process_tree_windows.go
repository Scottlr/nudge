//go:build windows

package process

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessTree struct {
	mu       sync.Mutex
	job      windows.Handle
	attached bool
	canceled bool
}

func newProcessTree() processTree { return &windowsProcessTree{} }

func (t *windowsProcessTree) Prepare(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	t.mu.Lock()
	t.job = job
	t.mu.Unlock()
	return nil
}

func (t *windowsProcessTree) Attach(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return syscall.EINVAL
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(processHandle)
	t.mu.Lock()
	job := t.job
	t.mu.Unlock()
	attachErr := windows.AssignProcessToJobObject(job, processHandle)
	if attachErr != nil {
		return attachErr
	}
	t.mu.Lock()
	t.attached = true
	canceled := t.canceled
	t.mu.Unlock()
	if canceled {
		return t.terminate()
	}
	return nil
}

func (t *windowsProcessTree) Cancel() error {
	t.mu.Lock()
	if t.canceled && t.attached {
		t.mu.Unlock()
		return nil
	}
	t.canceled = true
	attached := t.attached
	job := t.job
	t.mu.Unlock()
	if !attached || job == 0 {
		return nil
	}
	return windows.TerminateJobObject(job, 1)
}

func (t *windowsProcessTree) terminate() error {
	t.mu.Lock()
	job := t.job
	t.mu.Unlock()
	if job == 0 {
		return nil
	}
	return windows.TerminateJobObject(job, 1)
}

func (t *windowsProcessTree) Close() error {
	t.mu.Lock()
	job := t.job
	t.job = 0
	t.mu.Unlock()
	if job == 0 {
		return nil
	}
	return windows.CloseHandle(job)
}
