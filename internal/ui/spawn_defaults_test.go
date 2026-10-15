package ui

import "testing"

func TestDefaultToolFallsBackWhenSettingStale(t *testing.T) {
	m := buildModel(t)
	if err := m.services.store.SetDefaultTool("deleted-tool"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	m.applyTestMsg(t, m.openForm()())
	if got := m.form.Tool(); got != "claude" {
		t.Fatalf("form default tool = %q want claude (alphabetical fallback)", got)
	}
}
