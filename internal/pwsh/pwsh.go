// Package pwsh renders text for PowerShell: quoted literals, encoded
// commands, and script files, plus the lookup of the PowerShell binary.
package pwsh

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

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

// versionCommand runs pwsh to retrieve the PSVersionTable.
// It uses -NoProfile -NonInteractive to avoid profile side effects.
func versionCommand() (string, error) {
	cmd := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion")
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pwsh version probe: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}

// Version holds a parsed PowerShell version.
type Version struct {
	Major, Minor int
}

// Path finds PowerShell 7.3+, refusing Windows PowerShell 5.1.
func Path() (string, error) {
	if path, err := exec.LookPath("pwsh"); err == nil {
		ver, err := versionCommand()
		if err != nil {
			return "", fmt.Errorf("PowerShell 7.3 or newer required: %w; install with winget install Microsoft.PowerShell", err)
		}
		m := versionRe.FindStringSubmatch(ver)
		if m == nil {
			return "", fmt.Errorf("cannot parse version from %q; install with winget install Microsoft.PowerShell", ver)
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if major < 7 || (major == 7 && minor < 3) {
			return "", fmt.Errorf("PowerShell %d.%d is too old; 7.3 or newer required; install with winget install Microsoft.PowerShell", major, minor)
		}
		return path, nil
	}
	// Windows PowerShell 5.1 is not supported; reject with a clear message.
	return "", errors.New("PowerShell 7.3 or newer required; install with winget install Microsoft.PowerShell")
}

// ScriptFile renders a script body as file bytes led by a UTF-8 byte order
// mark, without which Windows PowerShell 5.1 reads the file as ANSI and
// garbles non-ASCII text.
func ScriptFile(body string) []byte {
	return []byte("\ufeff" + body)
}
