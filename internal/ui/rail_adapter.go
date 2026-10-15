package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/YoanWai/agent-manager/internal/clipboard"
	"github.com/YoanWai/agent-manager/internal/keybind"
	"github.com/YoanWai/agent-manager/internal/launch"
	"github.com/YoanWai/agent-manager/internal/status"
	"github.com/YoanWai/agent-manager/internal/store"
	"github.com/YoanWai/agent-manager/internal/sysstat"
	uirail "github.com/YoanWai/agent-manager/internal/ui/rail"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	collapsedSetting       = "collapsed_groups"
	namePlaceholder        = "…"
	placeholderPromptWidth = 12
	renameGrace            = time.Minute
)

type treeRow struct {
	isGroup bool
	group   string
	depth   int
	sess    store.Session
}

func (row treeRow) isRoot() bool { return row.isGroup && row.group == "" }

func rowKey(row treeRow) string {
	if row.isGroup {
		return "g:" + row.group
	}
	return "s:" + row.sess.ID
}

func loadCollapsed(st *store.Store) []string {
	raw, err := st.Setting(collapsedSetting)
	if err != nil || raw == "" {
		return nil
	}
	var paths []string
	if json.Unmarshal([]byte(raw), &paths) != nil {
		return nil
	}
	return paths
}

func (m *Model) railSnapshot() uirail.Snapshot {
	snapshot := uirail.Snapshot{
		Sessions:       make([]uirail.Session, 0, len(m.workspace.sessions)),
		Groups:         append([]string(nil), m.workspace.groups...),
		ArchivedGroups: make(map[string]bool, len(m.workspace.archivedGroups)),
	}
	for path, archived := range m.workspace.archivedGroups {
		snapshot.ArchivedGroups[path] = archived
	}
	for _, session := range m.workspace.sessions {
		prompt := m.workspace.panePrompts[session.ID]
		if prompt == "" {
			prompt = session.LastPrompt
		}
		if prompt == "" {
			prompt = launch.DeliveredPrompt(session.LaunchPrompt)
		}
		snapshot.Sessions = append(snapshot.Sessions, uirail.Session{
			ID:             session.ID,
			Name:           session.Name,
			DisplayName:    m.displayName(session),
			Tool:           session.Tool,
			Cwd:            session.Cwd,
			Group:          session.Group,
			Status:         session.Status,
			Archived:       session.Archived,
			Acked:          session.Acked,
			CreatedAt:      session.CreatedAt,
			LastStatusAt:   session.LastStatusAt,
			LaunchAt:       session.LaunchTime(),
			AgentSessionID: session.AgentSessionID,
			ParentID:       session.ParentID,
			Prompt:         prompt,
			PaneLine:       m.workspace.paneLines[session.ID],
			PanePrompt:     m.workspace.panePrompts[session.ID],
			Queued:         m.workspace.queuedMessages[session.ID],
			IsShell:        m.isShell(session.Tool),
			Elsewhere:      m.elsewhereNote(session) != "",
			AfterTurn:      session.AfterTurn,
			AfterTurnGlyph: afterTurnGlyphs[session.AfterTurn],
		})
	}
	return snapshot
}

func (m *Model) rebuildRows() uirail.SelectionChange {
	return m.rail.Reconcile(m.railSnapshot())
}

func (m *Model) selected() (store.Session, bool) {
	id, ok := m.rail.SelectedSession()
	if !ok {
		return store.Session{}, false
	}
	return m.sessionByID(id)
}

func (m *Model) selectedRow() (treeRow, bool) {
	selection, ok := m.rail.Selected()
	if !ok {
		return treeRow{}, false
	}
	if selection.Kind == uirail.GroupRow {
		return treeRow{isGroup: true, group: selection.Group}, true
	}
	session, ok := m.sessionByID(selection.SessionID)
	if !ok {
		return treeRow{}, false
	}
	return treeRow{group: session.Group, sess: session}, true
}

func (m *Model) selectedGroup() (string, bool) { return m.rail.SelectedGroup() }

func (m *Model) focusSession(id string) bool { return m.rail.FocusSession(id) }

func (m *Model) sessionByID(id string) (store.Session, bool) {
	for _, session := range m.workspace.sessions {
		if session.ID == id {
			return session, true
		}
	}
	return store.Session{}, false
}

func (m *Model) listedSessions() []store.Session {
	ids := m.rail.ListedSessionIDs()
	listed := make([]store.Session, 0, len(ids))
	for _, id := range ids {
		if session, ok := m.sessionByID(id); ok {
			listed = append(listed, session)
		}
	}
	return listed
}

func (m *Model) listedAgents() []store.Session {
	ids := m.rail.ListedAgentIDs()
	agents := make([]store.Session, 0, len(ids))
	for _, id := range ids {
		if session, ok := m.sessionByID(id); ok {
			agents = append(agents, session)
		}
	}
	return agents
}

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

func inGroupSubtree(sessionGroup, group string) bool {
	return sessionGroup == group || strings.HasPrefix(sessionGroup, group+"/")
}

func parentGroup(group string) string {
	if index := strings.LastIndex(group, "/"); index >= 0 {
		return group[:index]
	}
	return ""
}

func baseName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func (m *Model) awaitingRename(session store.Session) bool {
	awaited, ok := m.ledger.awaitedRenames[session.ID]
	if !ok {
		return false
	}
	if session.Name == awaited.generated && session.Status != status.Dead && time.Since(session.CreatedAt) < renameGrace {
		return true
	}
	delete(m.ledger.awaitedRenames, session.ID)
	return false
}

func (m *Model) displayName(session store.Session) string {
	if !m.awaitingRename(session) {
		return session.Name
	}
	if preview := promptPreview(m.ledger.awaitedRenames[session.ID].prompt); preview != "" {
		return preview
	}
	return namePlaceholder
}

func promptPreview(prompt string) string {
	words := make([]string, 0, len(strings.Fields(prompt)))
	for _, word := range strings.Fields(prompt) {
		if !clipboard.IsPastePath(word) {
			words = append(words, word)
		}
	}
	return ansi.Truncate(strings.Join(words, " "), placeholderPromptWidth, "…")
}

func (m *Model) sessionGlyph(session store.Session) string {
	if session.Status == status.Starting {
		return lipgloss.NewStyle().Foreground(statusColor(status.Starting)).
			Render(startupFrames[m.startup.startupPhase%len(startupFrames)])
	}
	if m.isShell(session.Tool) && session.Status != status.Dead && session.Status != status.Errored {
		return subtleStyle.Render(shellGlyph)
	}
	return lipgloss.NewStyle().Foreground(statusColor(session.Status)).Render(statusGlyph(session.Status))
}

func (m *Model) prepareRailFrame(width, height int) {
	footText := m.railFootLines(width)
	foot := make([]uirail.Line, len(footText))
	for i, line := range footText {
		foot[i].Text = line
	}
	rename := uirail.Rename{}
	if m.mode == modeRename {
		if m.rename.isGroup {
			rename.Target = uirail.Selection{Kind: uirail.GroupRow, Group: m.rename.path}
		} else {
			rename.Target = uirail.Selection{Kind: uirail.SessionRow, SessionID: m.rename.sessID}
			if session, ok := m.sessionByID(m.rename.sessID); ok {
				rename.Target.Group = session.Group
			}
		}
		fieldWidth := width - 4
		for _, row := range m.rail.Rows() {
			if row.Selection == rename.Target {
				fieldWidth -= 3 * row.Depth
				break
			}
		}
		if fieldWidth >= 5 {
			m.rename.input.Width = fieldWidth
		}
		rename.View = textInputView(m.rename.input)
	}
	ctx := uirail.RenderContext{
		Width:           width,
		Height:          max(height, 0),
		TerminalWidth:   m.layout.width,
		TerminalHeight:  m.layout.height,
		BodyOriginY:     m.listChromeRows(),
		Theme:           m.railTheme(),
		ListKeys:        m.services.listKeys,
		ComfortableRows: m.prefs.comfortableRows,
		MouseDisabled:   m.prefs.mouseDisabled,
		Focused:         m.mode == modeFocus,
		EnterFocuses:    m.enterFocuses(),
		StartupPhase:    m.startup.startupPhase,
		CursorMarker:    cursorAnchorMarker,
		Rename:          rename,
		Foot:            foot,
	}
	m.layout.displayedRail = m.rail.Render(ctx, m.layout.displayedRail)
}

func (m *Model) railTheme() uirail.Theme {
	return uirail.Theme{
		Bg: current.Bg, Surface: current.Surface, Overlay: current.Overlay, Border: current.Border,
		Bright: current.Bright, Text: current.Text, Dim: current.Dim, Subtle: current.Subtle,
		Accent: current.Accent, Accent2: current.Accent2,
		Working: current.Working, Waiting: current.Waiting, Finished: current.Finished,
		Errored: current.Errored, Idle: current.Idle,
	}
}

func railContentLines(lines []uirail.Line) []contentLine {
	out := make([]contentLine, len(lines))
	for i, line := range lines {
		out[i] = contentLine{text: line.Text, tone: line.Tone, rule: line.Rule, raw: line.Raw}
	}
	return out
}

func (m *Model) overlayRowMenu(frame string) string { return m.layout.displayedRail.Overlay(frame) }

func (m *Model) railLines(width, height int) []contentLine {
	m.prepareRailFrame(width, height)
	return railContentLines(m.layout.displayedRail.Lines)
}

// rowLegend is the tier for the entry under the cursor: what this session or
// this group can be told to do.
func (m *Model) rowLegend() legendSection {
	k := m.listGlyph
	enterHint, attachHint := "focus / fold", "attach"
	if !m.enterFocuses() {
		enterHint, attachHint = "attach / fold", "focus"
	}
	row, ok := m.selectedRow()
	if !ok {
		return legendSection{}
	}
	if row.isGroup {
		foldAction := "fold"
		if m.rail.IsCollapsed(row.group) {
			foldAction = "unfold"
		}
		pairs := [][2]string{{k(keybind.Open), foldAction}}
		if !m.prefs.mouseDisabled {
			pairs = append(pairs, [2]string{"double click", foldAction})
		}
		if m.prefs.arrowStep {
			pairs = append(pairs, m.legendPair(keybind.StepOut, "close", keybind.StepIn, "open"))
		}
		pairs = append(pairs, [][2]string{
			{k(keybind.Editor), "editor"}, {k(keybind.Rename), "rename"}, {k(keybind.Move), "move"},
			m.legendPair(keybind.Kill, "kill", keybind.KillAll, "all"), m.legendPair(keybind.Revive, "revive", keybind.ReviveAll, "all"),
			m.archiveRestoreLegend(), {k(keybind.Delete), "delete"},
		}...)
		return legendSection{title: "Group", leads: [][2]string{m.quickModeLead()}, pairs: legendPairsBound(pairs)}
	}
	title := "Session"
	leads := [][2]string{m.quickModeLead(), {k(keybind.Review), "review mode"}}
	hint := ""
	if k(keybind.Review) != "" {
		hint = reviewModeHint
	}
	conversation := [][2]string{{k(keybind.CopyReply), "copy"}, {k(keybind.Fork), "fork"}}
	if m.isShell(row.sess.Tool) {
		// A shell has no conversation, so the keys that would prompt,
		// review or fork one are left off rather than offered and refused.
		title, leads, hint, conversation = "Shell", nil, "", nil
	}
	pairs := [][2]string{{k(keybind.Open), enterHint}, {k(keybind.Attach), attachHint}}
	if !m.prefs.mouseDisabled {
		gesture := "double click"
		if !m.prefs.fullLayout {
			gesture = "click"
		}
		pairs = append(pairs, [2]string{gesture, "focus"})
	}
	if m.prefs.arrowStep {
		pairs = append(pairs, [2]string{k(keybind.StepIn), "focus"})
	}
	if row.sess.Status == status.Finished && !row.sess.Archived {
		pairs = append(pairs, [2]string{k(keybind.MarkIdle), "mark idle"})
	}
	if row.sess.AfterTurn != "" {
		pairs = append(pairs, [2]string{k(keybind.CancelEnd), "cancel " + row.sess.AfterTurn})
	}
	pairs = append(pairs, conversation...)
	// o sits outside the conversation keys: a shell's directory is worth
	// opening as much as an agent's.
	pairs = append(pairs, [][2]string{
		{k(keybind.Editor), "editor"}, {k(keybind.Rename), "rename"}, {k(keybind.Move), "move"},
		m.legendPair(keybind.Kill, "kill", keybind.KillAll, "all"), m.legendPair(keybind.Revive, "revive", keybind.ReviveAll, "all"), {k(keybind.Restart), "restart"},
		m.archiveRestoreLegend(), {k(keybind.Delete), "delete"},
	}...)
	return legendSection{title: title, leads: leads, hint: hint, pairs: legendPairsBound(pairs)}
}

const reviewModeHint = `tell your agent "set review mode"`

func (m *Model) quickModeLead() [2]string {
	return [2]string{m.listGlyph(keybind.Prompt), "quick prompt mode"}
}

// archiveRestoreLegend leaves out the key of the pair that no-ops in this view.
func (m *Model) archiveRestoreLegend() [2]string {
	if m.rail.ShowArchived() {
		return [2]string{m.listGlyph(keybind.Restore), "restore"}
	}
	return [2]string{m.listGlyph(keybind.Archive), "archive"}
}

func (m *Model) legendPair(first, firstLabel, second, secondLabel string) [2]string {
	switch {
	case m.listGlyph(first) == "":
		return [2]string{m.listGlyph(second), secondLabel}
	case m.listGlyph(second) == "":
		return [2]string{m.listGlyph(first), firstLabel}
	}
	return [2]string{m.listGlyph(first, second), firstLabel + " / " + secondLabel}
}

func legendPairsBound(pairs [][2]string) [][2]string {
	kept := pairs[:0]
	for _, pair := range pairs {
		if pair[0] != "" {
			kept = append(kept, pair)
		}
	}
	return kept
}

// viewLegend is the tier that never changes with the cursor: moving around
// the list, filtering it, and leaving.
func (m *Model) viewLegend() legendSection {
	emptyGroupsAction := "hide empty"
	if m.rail.HideEmptyGroups() {
		emptyGroupsAction = "show empty"
	}
	statusFilterAction := "attention"
	if m.rail.FilteringAttention() {
		statusFilterAction = "show all"
	}
	archivedAction := "archived"
	if m.rail.ShowArchived() {
		archivedAction = "back to active"
	}
	foldAllAction := "fold all"
	if m.rail.AllGroupsCollapsed() {
		foldAllAction = "unfold all"
	}
	// Ordered by what a narrow terminal must keep: unread messages, which
	// the footer is the only mouse path to, moving around, making
	// something, the filters, then the keys a user already knows to look for.
	k := m.listGlyph
	emptyGroupsKey := k(keybind.EmptyGroups)
	if m.rail.ShowArchived() {
		emptyGroupsKey = ""
	}
	var alerts [][2]string
	if glyph, label := m.messagesAlert(); glyph != "" {
		alerts = append(alerts, [2]string{glyph, label})
	}
	pairs := [][2]string{
		{strings.TrimSpace(k(keybind.Up) + " " + k(keybind.Down)), "navigate"},
		{k(keybind.NewSession), "new"}, {k(keybind.Terminal), "terminal"}, {k(keybind.NewGroup), "group"}, {k(keybind.Search), "search"},
		{k(keybind.Archived), archivedAction}, {k(keybind.Filter), statusFilterAction}, {emptyGroupsKey, emptyGroupsAction},
		{k(keybind.Help), "keys"}, {k(keybind.Quit), "quit"},
		{k(keybind.ReorderUp, keybind.ReorderDown), "reorder"}, {k(keybind.FoldAll), foldAllAction}, {k(keybind.Resize), "resize"}, {k(keybind.Settings), "settings"},
	}
	return legendSection{title: "View", quiet: true, alerts: alerts, pairs: legendPairsBound(pairs)}
}

func (m *Model) reorderFooter() string {
	info := m.rail.ReorderInfo()
	if info.DropLabel != "" {
		return m.transientFooter(legendSection{title: "Move", pairs: [][2]string{
			{"release", "move " + info.DropLabel}, {"esc", "put back"},
		}})
	}
	return m.transientFooter(legendSection{title: "Reorder", pairs: [][2]string{
		{"drag / ↑↓ / wheel", "move"}, {"↵ / release", "drop"}, {"esc", "put back"},
	}})
}

func (m *Model) applyRailDecision(decision uirail.Decision) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if decision.Error != "" {
		m.reportErr(decision.Error)
	}
	if decision.SelectionEffect != uirail.SelectionEffectNone {
		m.workspace.preview = ""
		m.workspace.proc = sysstat.ProcStat{}
		m.workspace.procFor = ""
		_, selectedSession := m.selected()
		if decision.SelectionEffect == uirail.SelectionEffectFilter || selectedSession {
			m.focus.pane.MovePreview()
		}
		if decision.SelectionEffect == uirail.SelectionEffectFilter && m.poller != nil {
			m.syncPollInput()
		}
		if selectedSession {
			commands = append(commands, m.schedulePreview())
		}
	}
	dependent := false
	for _, mutation := range decision.Mutations {
		if mutation.Kind != uirail.SaveCollapsed {
			dependent = true
		}
	}
	if dependent && m.railMutationPending() {
		decision.Mutations = nil
		m.reportErr("previous Rail change is still saving")
	}
	m.queueRail(decision.Mutations, 0, false)
	commands = append(commands, m.nextEffectCmd())
	if decision.Refresh {
		m.requestRefresh()
	}
	if decision.AutoScroll != nil {
		request := *decision.AutoScroll
		commands = append(commands, tea.Tick(request.After, func(time.Time) tea.Msg {
			return uirail.AutoScrollTick{Lift: request.Lift}
		}))
	}
	if decision.Intent.Kind != uirail.NoAction {
		model, command := m.runRailIntent(decision.Intent)
		commands = append(commands, command)
		return model, tea.Batch(commands...)
	}
	return m, tea.Batch(commands...)
}

func (m *Model) moveCursor(delta int) tea.Cmd {
	_, command := m.applyRailDecision(m.rail.Move(delta, true))
	return command
}

func (m *Model) applyRailStateDecision(decision uirail.Decision) {
	m.queueRail(decision.Mutations, 0, true)
}

func (m *Model) swapSessionInventory(id, targetID string) {
	current, target := -1, -1
	for index, session := range m.workspace.sessions {
		switch session.ID {
		case id:
			current = index
		case targetID:
			target = index
		}
	}
	if current >= 0 && target >= 0 {
		m.workspace.sessions[current], m.workspace.sessions[target] = m.workspace.sessions[target], m.workspace.sessions[current]
	}
}

func (m *Model) swapGroupInventory(path, targetPath string) {
	current, target := -1, -1
	for index, group := range m.workspace.groups {
		switch group {
		case path:
			current = index
		case targetPath:
			target = index
		}
	}
	if current >= 0 && target >= 0 {
		m.workspace.groups[current], m.workspace.groups[target] = m.workspace.groups[target], m.workspace.groups[current]
	}
}

func (m *Model) runRailIntent(intent uirail.Intent) (tea.Model, tea.Cmd) {
	switch intent.Kind {
	case uirail.Quit:
		return m.requestQuit()
	case uirail.Focus:
		return m.focusSelected()
	case uirail.Attach:
		return m.attachSelected()
	case uirail.NewSession:
		return m, m.openForm()
	case uirail.NewGroup:
		m.openGroupForm()
	case uirail.Fork:
		m.fork.open(m)
	case uirail.Revive:
		return m.reviveSelected()
	case uirail.MarkIdle:
		return m.acknowledgeSelected()
	case uirail.ReviveAll:
		return m.reviveAllDead()
	case uirail.Restart:
		return m.restartSelected()
	case uirail.Kill:
		return m.killSelected()
	case uirail.KillAll:
		return m.killAllLive()
	case uirail.Archive:
		return m.archiveSelected()
	case uirail.Restore:
		return m.restoreSelected()
	case uirail.Delete:
		m.prepareDelete()
	case uirail.Prompt:
		return m, m.openQuickMode()
	case uirail.CopyReply:
		return m.copyReplySelected()
	case uirail.CancelEnd:
		return m.cancelEndSelected()
	case uirail.OpenSettings:
		return m, m.openSettings()
	case uirail.Resize:
		return m.enterResizeMode()
	case uirail.NewTerminal:
		return m.terminalKey()
	case uirail.OpenEditor:
		return m.openEditor()
	case uirail.RenameAction:
		m.openRename()
	case uirail.MoveToGroup:
		m.openMove()
	case uirail.OpenMessages:
		m.notices.open(m, "")
	case uirail.OpenHelp:
		m.openHelp()
	case uirail.OpenReview:
		return m, m.openDiff()
	}
	return m, nil
}
