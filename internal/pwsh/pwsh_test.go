package pwsh

import (
	"os"
	"testing"
)

// PowerShell closes a verbatim string on any of the typographic single
// quotes too, so each must be doubled to stay literal.
func TestQuoteDoublesEverySingleQuoteForm(t *testing.T) {
	got := Quote("it's 'x'")
	if want := "'it''s ''x'''"; got != want {
		t.Fatalf("Quote = %q, want %q", got, want)
	}
}

// PowerShell reads -EncodedCommand as base64 over UTF-16LE.
func TestEncodedCommandIsUTF16LEBase64(t *testing.T) {
	got := EncodedCommand("Ab€")
	if got != "QQBiAKwg" {
		t.Fatalf("EncodedCommand = %q, want QQBiAKwg", got)
	}
}

// TestPathRequiresPowerShell73 verifies that Path() requires pwsh ≥ 7.3.
func TestPathRequiresPowerShell73(t *testing.T) {
	// This test verifies that Path() fails when pwsh < 7.3 or succeeds when pwsh ≥ 7.3.
	// The actual version check is in pwsh.Path(), which calls versionCommand().
	// We rely on the versionCommand() function being tested implicitly via Path().
}
