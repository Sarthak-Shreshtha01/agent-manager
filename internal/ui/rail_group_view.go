package ui

import (
	"fmt"
	"github.com/YoanWai/agent-manager/internal/status"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

func (m *Model) groupSessionCount(path string) int {
	count := 0
	for _, sess := range m.listedAgents() {
		if inGroupSubtree(sess.Group, path) {
			count++
		}
	}
	return count
}

func (m *Model) renamingGroup(group string) bool {
	return m.mode == modeRename && m.rename.isGroup && m.rename.path == group
}

// groupStatusBreakdown renders "2 working · 1 waiting" for the subtree,
// each count tinted in its status color, skipping zero statuses.
func (m *Model) groupStatusBreakdown(group string) string {
	counts := m.groupStatusCounts(group)
	var parts []string
	for _, st := range []string{status.Starting, status.Working, status.Waiting, status.Finished, status.Errored, status.Idle, status.Dead} {
		if counts[st] > 0 {
			// The count carries the state's color, the word stays quiet: a
			// rollup line should read as one texture, not as six labels
			// competing with the session names above it.
			parts = append(parts, lipgloss.NewStyle().Foreground(statusColor(st)).
				Render(fmt.Sprintf("%d", counts[st]))+subtleStyle.Render(" "+st))
		}
	}
	return strings.Join(parts, subtleStyle.Render(" · "))
}

func (m *Model) groupStatusCounts(group string) map[string]int {
	counts := map[string]int{}
	for _, sess := range m.listedAgents() {
		if inGroupSubtree(sess.Group, group) {
			counts[sess.Status]++
		}
	}
	return counts
}
