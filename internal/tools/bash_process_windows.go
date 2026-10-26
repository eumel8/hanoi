package tools

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func configureBashProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// Kill the whole Windows process tree, including Git Bash children.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		kill := exec.CommandContext(ctx, "taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
