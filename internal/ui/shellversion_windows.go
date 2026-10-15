//go:build windows

package ui

import "github.com/YoanWai/agent-manager/internal/pwsh"

// paneShellWarning warns when the PowerShell panes run under predates 7.3.
// A missing PowerShell stays silent here: creating a session reports it.
func paneShellWarning() string {
	shell, err := pwsh.Path()
	if err != nil {
		return ""
	}
	return pwsh.VersionWarning(shell)
}
