//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkBatchShim refuses a tool that PowerShell would hand to cmd.exe. A
// .cmd or .bat file has its arguments parsed again by cmd.exe, which reads
// the escaped quote PowerShell 7.3 writes as the end of the argument, so a
// prompt holding a double quote splits there and runs as commands. A bare
// name is safe when a .ps1 sits beside the batch file, the pair npm
// installs: PowerShell runs the script, which passes its arguments on
// whole. A command naming the batch file itself runs it as named.
func checkBatchShim(binary, path string) error {
	ext := filepath.Ext(path)
	switch strings.ToLower(ext) {
	case ".cmd", ".bat":
	default:
		return nil
	}
	if !strings.ContainsRune(binary, filepath.Separator) {
		if _, err := os.Stat(strings.TrimSuffix(path, ext) + ".ps1"); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s is a batch file, and cmd.exe would run a prompt holding a double quote as commands; point the tool's command at the program's .exe or .ps1 instead", path)
}
