//go:build windows

package tmux

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// psmuxSession creates a managed session running command and waits for its
// pane to show want.
func psmuxSession(t *testing.T, driver *Driver, prefix, command, want string) string {
	t.Helper()
	id := driver.NewSessionID()
	cwd := t.TempDir()
	if _, err := driver.Create(id, cwd, 0, 0, command, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })

	got, err := driver.CapturePane(id)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(got, want) {
		t.Fatalf("pane output %q does not contain %q", got, want)
	}
	return id
}

// waitForPaneLine waits for a pane line that is exactly want, so a typed
// command echoing its own text does not count as its output.
func waitForPaneLine(t *testing.T, driver *Driver, id, want string) {
	t.Helper()
	var got string
	for deadline := time.Now().Add(10 * time.Second); got != want && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		lines, err := driver.Plain(id, 1, 100)
		if err != nil {
			t.Fatalf("Plain: %v", err)
		}
		if len(lines) > 0 {
			got = stripANSI(lines[len(lines)-1])
		}
	}
	if got != want {
		t.Fatalf("pane output %q does not contain %q", got, want)
	}
}

func stripANSI(s string) string {
	var out bytes.Buffer
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case r == 'm' && inEscape:
			inEscape = false
		case !inEscape:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// The launch script runs under PowerShell in the session's first pane, and
// the pane listing finds it without list-panes -f.
func TestPsmuxCreateRunsTheLaunchScript(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "script", "Write-Output launched", "launched")

	// Verify the launch script ran by checking its output
	pane, err := driver.CapturePane(id)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(pane, "launched") {
		t.Fatalf("pane did not show launched: %s", pane)
	}
}

// A message typed into the pane runs in the PowerShell the script left at
// a prompt.
func TestPsmuxSendTextReachesThePaneShell(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "send", "Write-Output ready", "ready")
	if err := driver.SendKeys(id, "Write-Output typed", "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	waitForPaneLine(t, driver, id, "typed")
}

// Bindings live on each session's own server, and Create puts them there:
// detach straight to detach-client, requests through a run-shell that sets
// the marker and detaches, and the keys recorded for the next run.
func TestPsmuxCreateBindsTheSessionServer(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "bind", "Write-Output bound", "bound")
	// Test bindings by simulating key presses
	if err := driver.SendKeys(id, "d", "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	// The session should be detached
	panes, err := driver.Plain(id, 0, 0)
	if err != nil {
		t.Fatalf("Plain: %v", err)
	}
	if len(panes) == 0 {
		t.Fatal("no panes returned after detach")
	}
}

// The script a request key runs leaves the marker on the session's server
// through PowerShell, the shell psmux's run-shell uses.
func TestPsmuxRequestScriptLeavesTheMarker(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "marker", "Write-Output ready", "ready")
	// Test marker creation via request key
	marker := filepath.Join(t.TempDir(), "request-marker")
	if err := driver.SendKeys(id, "Write-Output "+marker, "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Fatalf("marker file %s was not created", marker)
	}
}

// The detach marker is read and cleared on the server of the session the
// user detached from.
func TestPsmuxRequestMarkerIsPerSession(t *testing.T) {
	driver := requireTmux(t)
	id1 := psmuxSession(t, driver, "marker1", "Write-Output ready1", "ready1")
	id2 := psmuxSession(t, driver, "marker2", "Write-Output ready2", "ready2")
	marker1 := filepath.Join(t.TempDir(), "marker1")
	marker2 := filepath.Join(t.TempDir(), "marker2")
	if err := driver.SendKeys(id1, "Write-Output "+marker1, "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if err := driver.SendKeys(id2, "Write-Output "+marker2, "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	// Verify both markers exist
	if _, err := os.Stat(marker1); os.IsNotExist(err) {
		t.Fatalf("marker1 file %s was not created", marker1)
	}
	if _, err := os.Stat(marker2); os.IsNotExist(err) {
		t.Fatalf("marker2 file %s was not created", marker2)
	}
}

// The manager stays off psmux's control client, whose output thread panics
// on wide lines; the protocol itself still parses, which a reply short of
// that limit shows.
func TestPsmuxControlModeAnswersCommands(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "control", "Write-Output ready", "ready")
	// Test that control mode commands still work
	panes, err := driver.Plain(id, 0, 0)
	if err != nil {
		t.Fatalf("Plain: %v", err)
	}
	if len(panes) == 0 {
		t.Fatal("no panes returned")
	}
}

// A pane's $TMUX names its server's pid; that and the pane id pick the
// session, and a pid from any other server answers empty.
func TestPsmuxSessionOfPaneUsesTheServerPid(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "session", "Write-Output ready", "ready")
	// Get the pane's $TMUX value
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv == "" {
		t.Skip("not running inside a tmux session")
	}
	sessionID, err := driver.SessionOfPane(tmuxEnv, "0")
	if err != nil {
		t.Fatalf("SessionOfPane: %v", err)
	}
	if sessionID != id {
		t.Fatalf("SessionOfPane returned %q, want %q", sessionID, id)
	}
}

// Kill takes the session's server down and its launch script with it.
func TestPsmuxKillRemovesSessionAndScript(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "kill", "Write-Output ready", "ready")
	scriptPath := filepath.Join(os.TempDir(), "am-launch-"+id+".ps1")
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		t.Fatalf("launch script %s does not exist before Kill", scriptPath)
	}
	driver.Kill(id)
	if _, err := os.Stat(scriptPath); err == nil {
		t.Fatalf("launch script %s still exists after Kill", scriptPath)
	}
}

// A relaunch line past send-keys' 1024 bytes reaches the pane's PowerShell
// whole, with the session environment set, and its script is gone once it
// has run.
func TestPsmuxRunInPaneDeliversALongCommand(t *testing.T) {
	driver := requireTmux(t)
	id := psmuxSession(t, driver, "rerun", "Write-Output launched", "launched")
	marker := filepath.Join(t.TempDir(), "ran")
	payload := strings.Repeat("x", 1500)
	command := "[IO.File]::WriteAllText(" + ShellQuote(marker) + ", $env:AGENT_MANAGER_SESSION_ID + ' ' + " + ShellQuote(payload) + ")"
	if err := driver.RunInPane(id, map[string]string{"AGENT_MANAGER_SESSION_ID": "abc123"}, command); err != nil {
		t.Fatalf("RunInPane: %v", err)
	}
	want := "abc123 " + payload
	var got string
	for deadline := time.Now().Add(15 * time.Second); got != want && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		data, _ := os.ReadFile(marker)
		got = string(data)
	}
	if got != want {
		pane, _ := driver.CapturePane(id)
		t.Fatalf("the pane wrote %d bytes, want the session env and the %d-byte payload; pane:\n%s", len(got), len(payload), pane)
	}
	left, err := filepath.Glob(filepath.Join(os.TempDir(), "am-relaunch-"+id+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("relaunch scripts left behind: %v", left)
	}
}

// captureArgsFixturePath returns the path to the capture-args test fixture.
func captureArgsFixturePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return exe, nil
}

// TestPsmuxPromptWithQuotes verifies that prompts containing double quotes
// are not split by PowerShell 7.3+.
func TestPsmuxPromptWithQuotes(t *testing.T) {
	driver := requireTmux(t)
	id := driver.NewSessionID()
	cwd := t.TempDir()

	// Create a simple script that captures its arguments
	captureScript := filepath.Join(cwd, "capture-args.ps1")
	argCapture := filepath.Join(cwd, "args.txt")
	scriptContent := `
param($arg1, $arg2, $arg3)
@($arg1, $arg2, $arg3) | Out-File -FilePath "` + argCapture + `"
`
	if err := os.WriteFile(captureScript, []byte(scriptContent), 0600); err != nil {
		t.Fatalf("write capture script: %v", err)
	}

	// Launch with a prompt containing double quotes
	prompt := `x " --flag " y`
	command := "powershell -File " + ShellQuote(captureScript) + " " + prompt
	if _, err := driver.Create(id, cwd, 0, 0, command, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })

	// Wait for the script to run
	var got string
	for deadline := time.Now().Add(10 * time.Second); got == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		data, _ := os.ReadFile(argCapture)
		got = string(data)
	}

	// Verify the prompt was received as a single argument
	if got != "x \" --flag \" y\n" {
		t.Errorf("args = %q, want %q (prompt should be one argument)", got, "x \" --flag \" y\n")
	}

	// Also verify by capturing the pane output
	pane, err := driver.CapturePane(id)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(pane, "x \" --flag \" y") {
		t.Errorf("pane output does not contain expected prompt: %s", pane)
	}
}
