//go:build !windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureBashProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Stop the whole pipeline, including children holding stdout/stderr open.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
