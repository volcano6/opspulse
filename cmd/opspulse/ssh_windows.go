//go:build windows

package main

import "os/exec"

func setupTerminalResizeNotify(cmd *exec.Cmd) func() {
	return func() {}
}
