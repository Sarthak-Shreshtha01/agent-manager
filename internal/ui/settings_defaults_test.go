package ui

import "testing"

func TestDefaultToolFallsBackWhenSettingStale(t *testing.T) {
	m := buildModel(t)
	if err := m.services.store.SetDefaultTool("deleted-tool"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	m.applyTestMsg(t, m.openForm()())
	if got := m.form.toolNames[m.form.toolIndex]; got != "claude" {
		t.Fatalf("form default tool = %q want claude (alphabetical fallback)", got)
	}
}

func TestDefaultSplitLayout(t *testing.T) {
	m := buildModel(t)
	if !m.defaultSplitLayout() {
		t.Fatal("split should be the default layout")
	}
	if err := m.services.store.SetSetting(diffLayoutSetting, "unified"); err != nil {
		t.Fatal(err)
	}
	if m.defaultSplitLayout() {
		t.Fatal("stored unified choice should opt out of split")
	}
}
