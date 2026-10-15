// Package pwsh renders text for PowerShell: quoted literals, encoded
// commands, and script files, plus the lookup of the PowerShell binary.
package pwsh

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf16"
)

// quoteReplacer doubles every character PowerShell accepts as a single
// quote, since each one closes a verbatim string.
var quoteReplacer = strings.NewReplacer(
	"'", "''",
	"\u2018", "\u2018\u2018",
	"\u2019", "\u2019\u2019",
	"\u201A", "\u201A\u201A",
	"\u201B", "\u201B\u201B",
)

// Quote renders s as a PowerShell verbatim single-quoted string, which
// expands nothing.
func Quote(s string) string {
	return "'" + quoteReplacer.Replace(s) + "'"
}

// EncodedCommand renders a script as -EncodedCommand takes it: UTF-16LE,
// then base64.
func EncodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// Path finds PowerShell 7 (pwsh), falling back to Windows PowerShell.
func Path() (string, error) {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("powershell"); err == nil {
		return path, nil
	}
	return "", errors.New("PowerShell not found on PATH: install PowerShell 7 (winget install Microsoft.PowerShell)")
}

// VersionWarning asks the PowerShell at shell for its version and returns a
// warning when it predates 7.3, empty otherwise. Before 7.3, PowerShell
// hands a native program an argument holding a double quote without
// escaping it, so a prompt like `x " --flag " y` reaches the agent CLI as
// several arguments. The shell still runs; the user decides.
func VersionWarning(shell string) string {
	// ToString prints the bare version; the object itself formats as a
	// table whose header and line breaks would end up in the warning.
	out, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()").Output()
	if err != nil {
		return fmt.Sprintf("could not read the PowerShell version (%v); prompts containing \" may reach agent CLIs split before PowerShell 7.3: winget install Microsoft.PowerShell", err)
	}
	return versionWarning(strings.TrimSpace(string(out)))
}

// versionWarning judges a version as $PSVersionTable.PSVersion.ToString()
// prints it: 7.4.1, 5.1.26100.2161, 7.5.0-preview.3.
func versionWarning(version string) string {
	major, minor, ok := majorMinor(version)
	if !ok {
		return fmt.Sprintf("could not read the PowerShell version from %s; prompts containing \" may reach agent CLIs split before PowerShell 7.3: winget install Microsoft.PowerShell", strconv.Quote(version))
	}
	if major > 7 || major == 7 && minor >= 3 {
		return ""
	}
	return fmt.Sprintf("PowerShell %d.%d splits a prompt containing \" into several arguments to the agent CLI; install PowerShell 7.3 or newer: winget install Microsoft.PowerShell", major, minor)
}

// majorMinor reads the first two dot-separated numbers of a version.
func majorMinor(version string) (int, int, bool) {
	first, rest, found := strings.Cut(version, ".")
	if !found {
		return 0, 0, false
	}
	second, _, _ := strings.Cut(rest, ".")
	major, err := strconv.Atoi(first)
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(second)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// ScriptFile renders a script body as file bytes led by a UTF-8 byte order
// mark, without which Windows PowerShell 5.1 reads the file as ANSI and
// garbles non-ASCII text.
func ScriptFile(body string) []byte {
	return []byte("\ufeff" + body)
}
