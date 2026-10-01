//go:build windows

package catalog

import (
	"os"
	"os/exec"
)

// startProcessGroup is a no-op on Windows: there is no process group to
// detach into, and the manager's own job already covers the tree.
func startProcessGroup(*exec.Cmd) {}

// termGroup has no group to signal on Windows; stop closes the process's
// stdin, which the server and any child it launched read.
func termGroup(int) {}

// killGroup kills the server itself, which Windows has no group signal for.
func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
