package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Create used to type the launch line with send-keys, which silently
// truncates around 1024 bytes and left long first prompts as a broken
// shell command. paste-buffer must deliver the full line.
func TestCreateDeliversLongCommand(t *testing.T) {
	driver := requireTmux(t)
	id := "long" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	marker := "/tmp/am-long-" + id
	t.Cleanup(func() { os.Remove(marker) })

	payload := strings.Repeat("x", 1500)
	command := "printf '%s' '" + payload + "' > " + marker
	if len(command) < 1024 {
		t.Fatalf("test command must exceed the old 1024-byte send-keys limit, got %d", len(command))
	}
	if err := driver.Create(id, "/tmp", command, nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil && string(data) == payload {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	pane, _ := driver.CapturePane(id)
	got, _ := os.ReadFile(marker)
	t.Fatalf("long launch command truncated or failed; wrote %d bytes, want %d; pane:\n%s", len(got), len(payload), pane)
}

// An agent that auto-detects its palette asks the terminal for its
// foreground and background with OSC 10 and 11. Nothing answers that on this
// server — the only client is in control mode and has no tty — unless the
// pane carries explicit colors of its own, which is what the pane theme sets.
func TestCreateAnswersColorQueries(t *testing.T) {
	driver := requireTmux(t)
	t.Cleanup(func() { clearPaneTheme(t) })
	driver.PublishPaneTheme(PaneTheme{Foreground: "#cdd6f4", Background: "#1e1e2e", ColorFgBg: "15;0"})

	id := "osc" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	reply := t.TempDir() + "/reply"
	// tmux delivers the answer on the pane's input, so the query and the
	// read both happen inside the pane. Raw mode keeps the line discipline
	// from holding a reply that ends in ST rather than a newline.
	command := "stty raw; printf '\\033]10;?\\033\\\\\\033]11;?\\033\\\\'; cat > " + reply
	if err := driver.Create(id, "/tmp", command, nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })

	got := waitForFile(t, driver, id, reply)
	for _, want := range []string{"]10;rgb:cdcd/d6d6/f4f4", "]11;rgb:1e1e/1e1e/2e2e"} {
		if !strings.Contains(got, want) {
			t.Fatalf("OSC 10/11 replies = %q, want one carrying %q", got, want)
		}
	}
}

// COLORFGBG is the fallback for agents that read the environment instead of
// querying, and nothing in a pane's environment carries it otherwise.
func TestCreateExportsColorFgBg(t *testing.T) {
	driver := requireTmux(t)
	t.Cleanup(func() { clearPaneTheme(t) })
	driver.PublishPaneTheme(PaneTheme{Foreground: "#cdd6f4", Background: "#1e1e2e", ColorFgBg: "15;0"})

	id := "fgbg" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	marker := t.TempDir() + "/env"
	if err := driver.Create(id, "/tmp", "printenv COLORFGBG > "+marker, nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })

	if got := strings.TrimSpace(waitForFile(t, driver, id, marker)); got != "15;0" {
		t.Fatalf("COLORFGBG in pane = %q, want %q", got, "15;0")
	}
}

// The session environment has to outlive the launch command. Quitting the
// agent drops the pane onto the shell the script execs, and an agent
// started again from that shell belongs to this managed session only if it
// inherits these values: the rename subcommand and the MCP server both
// identify the session by AGENT_MANAGER_SESSION_ID alone.
func TestCreateExportsSessionEnvIntoTheShell(t *testing.T) {
	driver := requireTmux(t)
	id := "senv" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	fixture := t.TempDir()
	marker := fixture + "/env"
	shell := fixture + "/shell"
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nPS1='am-shell-ready> ' exec /bin/sh -i\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	env := map[string]string{
		"AGENT_MANAGER_SESSION_ID":  "abc123",
		"AGENT_MANAGER_STATUS_FILE": "/tmp/status-abc123",
	}
	// A launch command that returns at once stands in for the user quitting
	// the agent back to the pane's shell.
	if err := driver.Create(id, "/tmp", "printf 'agent ran\\n'", env, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })
	waitForPane(t, driver, id, "am-shell-ready>")

	// Written whole and moved into place, so the read cannot land between
	// the two values.
	report := `printf '%s %s\n' "$AGENT_MANAGER_SESSION_ID" "$AGENT_MANAGER_STATUS_FILE" > ` +
		marker + `.part && mv ` + marker + `.part ` + marker
	if err := driver.SendKeys(id, report, "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	got := strings.Fields(waitForFile(t, driver, id, marker))
	want := []string{"abc123", "/tmp/status-abc123"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment left to the shell = %v, want %v", got, want)
	}
}

// The pane is where the user is looking when the agent exits, so the way
// back to a wired agent is named there.
func TestCreateNamesTheWayBackWhenTheAgentExits(t *testing.T) {
	driver := requireTmux(t)
	id := "hint" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if err := driver.Create(id, "/tmp", "printf 'agent ran\\n'", nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })
	waitForPane(t, driver, id, relaunchHint)
}

func TestExportEnvPrefixesTheCommand(t *testing.T) {
	env := map[string]string{"B": "second", "A": "fir st"}
	want := `export A='fir st'; export B='second'; claude --resume 7`
	if got := exportEnv(env, "claude --resume 7"); got != want {
		t.Fatalf("exportEnv = %q, want %q", got, want)
	}
}

func TestCreateExecutesLaunchScriptAsPaneRoot(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	stub := filepath.Join(dir, "tmux")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+ShellQuote(calls)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	driver := &Driver{bin: stub, socket: testSocket}
	id := "launch-root"
	t.Cleanup(func() { _ = os.Remove(launchScriptPath(id)) })
	if err := driver.Create(id, dir, "cat", nil, 80, 24); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range strings.Split(string(args), "\n") {
		if strings.Contains(arg, launchScriptPath(id)) {
			if !strings.HasPrefix(arg, "exec sh ") {
				t.Fatalf("launch leaves an extra shell above the pane script: %q", arg)
			}
			return
		}
	}
	t.Fatal("launch command absent")
}

// tmux hands the launch to its default-shell with -c, and Debian's dash or
// fish stays on above a command it was not told to exec. A default-shell that
// always waits on its command stands in for them on every platform. The launch
// script has to be the pane's process, so the agent it starts is the pane's
// only child.
func TestCreateRunsTheLaunchScriptAsThePaneProcess(t *testing.T) {
	driver := requireTmux(t)
	waitingShell := t.TempDir() + "/waiting-sh"
	if err := os.WriteFile(waitingShell, []byte("#!/bin/sh\neval \"$2\"\nexit $?\n"), 0o700); err != nil {
		t.Fatalf("waiting shell: %v", err)
	}
	stamp := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if err := driver.Create("hold"+stamp, "/tmp", "", nil, 80, 24); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill("hold" + stamp) })
	out, err := tmuxCmd("show-options", "-gv", "default-shell").CombinedOutput()
	if err != nil {
		t.Fatalf("default-shell: %v: %s", err, out)
	}
	previous := strings.TrimSpace(string(out))
	if out, err := tmuxCmd("set-option", "-g", "default-shell", waitingShell).CombinedOutput(); err != nil {
		t.Fatalf("set default-shell: %v: %s", err, out)
	}
	t.Cleanup(func() { tmuxCmd("set-option", "-g", "default-shell", previous).Run() })

	id := "launch" + stamp
	if err := driver.Create(id, "/tmp", "sleep 61", nil, 80, 24); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })
	panePID, err := driver.PanePID(id)
	if err != nil {
		t.Fatalf("PanePID: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if exec.Command("pgrep", "-P", strconv.Itoa(panePID), "sleep").Run() == nil {
			return
		}
	}
	tree, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=,args=").Output()
	t.Fatalf("no sleep directly under pane pid %d:\n%s", panePID, tree)
}

// A relaunch line typed whole is dropped by a shell without line editing
// once it passes 1024 bytes, and a Codex relaunch with its hooks comes
// close on its own.
func TestRunInPaneDeliversALongCommandToAShellWithoutLineEditing(t *testing.T) {
	driver := requireTmux(t)
	id := "rerun" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if err := driver.Create(id, "/tmp", "exec /bin/dash", nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })
	marker := filepath.Join(t.TempDir(), "ran")
	payload := strings.Repeat("x", 1500)
	if err := driver.RunInPane(id, map[string]string{"AGENT_MANAGER_SESSION_ID": "abc123"}, "printf '%s %s' \"$AGENT_MANAGER_SESSION_ID\" '"+payload+"' > "+ShellQuote(marker+".part")+" && mv "+ShellQuote(marker+".part")+" "+ShellQuote(marker)); err != nil {
		t.Fatalf("RunInPane: %v", err)
	}
	if got := waitForFile(t, driver, id, marker); got != "abc123 "+payload {
		t.Fatalf("the pane ran %d bytes, want the session env and the %d-byte payload", len(got), len(payload))
	}
}

// A relaunched agent is the pane's foreground command, as a launched one is,
// and its script leaves nothing behind in the shared temp dir to be planted
// or reused.
func TestRunInPaneLeavesTheAgentInFrontAndNoScript(t *testing.T) {
	driver := requireTmux(t)
	id := "front" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if err := driver.Create(id, "/tmp", "exec /bin/dash", nil, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { driver.Kill(id) })
	if err := driver.RunInPane(id, nil, "sleep 30"); err != nil {
		t.Fatalf("RunInPane: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := tmuxCmd("display-message", "-p", "-t", "am_"+id+":.0", "#{pane_current_command}").Output()
		if err == nil && strings.TrimSpace(string(out)) == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane command = %q, want the relaunched agent", strings.TrimSpace(string(out)))
		}
		time.Sleep(50 * time.Millisecond)
	}
	left, err := filepath.Glob(filepath.Join(os.TempDir(), "am-relaunch-"+id+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("relaunch scripts left behind: %v", left)
	}
}
