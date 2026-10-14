//go:build windows

package catalog

import (
	"os/exec"
	"strconv"
)

// startProcessGroup is a no-op on Windows: killGroup finds a server's
// children by their parent pid, so they need no group of their own.
func startProcessGroup(*exec.Cmd) {}

// termGroup has no signal to send on Windows; the stdin stop closes is
// the polite ask.
func termGroup(int) {}

// killGroup kills the server and every process under it.
func killGroup(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
