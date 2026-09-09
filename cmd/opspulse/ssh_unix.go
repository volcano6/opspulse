//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func setupTerminalResizeNotify(cmd *exec.Cmd) func() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		for range sigChan {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGWINCH)
			}
		}
	}()
	return func() {
		signal.Stop(sigChan)
	}
}
