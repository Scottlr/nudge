package process

import "os/exec"

type processTree interface {
	Prepare(*exec.Cmd) error
	Attach(*exec.Cmd) error
	Cancel() error
	Close() error
}
