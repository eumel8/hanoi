package main

import "os/exec"

func configureBashProcess(cmd *exec.Cmd) {
	// CommandContext kills the shell; WaitDelay also bounds inherited pipes.
}
