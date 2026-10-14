//go:build windows

package catalog

import "golang.org/x/sys/windows"

// stillActive is the exit code GetExitCodeProcess reports for a process
// that has not exited.
const stillActive = 259

// processAlive reports whether a pid still has a running process behind it.
func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == stillActive
}
