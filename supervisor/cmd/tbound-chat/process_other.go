//go:build !windows

package main

import "os/exec"

func configureWorkerProcess(_ *exec.Cmd) {}

func killWorkerProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
