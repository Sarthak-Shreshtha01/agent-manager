package ui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/YoanWai/agent-manager/internal/clipboard"
	"github.com/YoanWai/agent-manager/internal/keybind"
	"github.com/YoanWai/agent-manager/internal/status"
	"github.com/YoanWai/agent-manager/internal/store"
	"github.com/YoanWai/agent-manager/internal/tmux"
	uifocus "github.com/YoanWai/agent-manager/internal/ui/focus"
	uirail "github.com/YoanWai/agent-manager/internal/ui/rail"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type focusRuntimeState struct {
	watch             *focusWatch
	imeCursor         *cursorAnchor
	lastPaneSizes     map[string][2]int
	lastPanePIDs      map[string]int
	lastPublishedSize [2]int
}

func (m *Model) routeFocusMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case linkOpenErrMsg:
		m.reportErr(msg.err.Error())
		return routed(m, nil)

	case linkPageMsg:
		return routed(m, showLinkPage(msg.url))

	case focusCopiedMsg:
		// The clipboard writer runs off the update loop and can take
		// hundreds of milliseconds, long enough for a click elsewhere to
		// drop the highlight this count belongs to.
		if !m.focus.pane.ApplyCopied(msg.gen, msg.chars) {
			return routed(m, nil)
		}
		m.clearErr()
		return routed(m, nil)

	case focusScrollMsg:
		currentID := ""
		if sess, ok := m.selected(); ok {
			currentID = sess.ID
		}
		result := m.focus.pane.ApplyRegion(uifocus.RegionResult{
			SessionID: msg.sessID,
			Offset:    msg.offset,
			Rows:      msg.rows,
			Preview:   msg.preview,
			OK:        msg.ok,
		}, currentID, m.focusPaneRows())
		if result.Next != nil {
			return routed(m, m.focusRegionRequestCmd(*result.Next))
		}
		if result.Apply {
			m.workspace.preview = result.Preview
		}
		return routed(m, nil)

	case focusPreviewMsg:
		currentID := ""
		if sess, ok := m.selected(); ok {
			currentID = sess.ID
		}
		result := m.focus.pane.ApplyPane(uifocus.PaneUpdate{
			SessionID: msg.sessID,
			Mouse:     msg.paneMouse,
			Alt:       msg.paneAlt,
			Motion:    msg.paneMotion,
			SGR:       msg.paneSGR,
			History:   msg.historySize,
			Cursor: uifocus.Cursor{
				X: msg.cursorX, Y: msg.cursorY,
				Visible: msg.cursorOK, PositionKnown: msg.paneStateOK,
			},
			Writes: msg.writes,
		}, currentID)
		if result.UsePreview {
			m.workspace.preview = msg.preview
		}
		return routed(m, nil)
	}
	return nil, nil, false
}

// focusKeyCommand encodes one key press as a tmux send-keys command for
// the focused session. ok is false for keys tmux cannot represent, which
// are dropped.
func focusKeyCommand(target string, msg tea.KeyMsg) (string, bool) {
	raw, name, ok := uifocus.EncodeKey(msg)
	if !ok {
		return "", false
	}
	if name != "" {
		return tmux.SendKeyName(target, name), true
	}
	return tmux.SendKeysHex(target, raw), true
}

// focusSelected enters focus mode: keys go to the selected session's pane
// while the manager, its rail and its live preview stay on screen. The
// dead-pane probe and the finished acknowledgement run on the effect
// lane; the completion enters focus only while this selection holds.
func (m *Model) focusSelected() (tea.Model, tea.Cmd) {
	sess, ok := m.selected()
	if !ok {
		return m, nil
	}
	if sess.Archived {
		return m.attachSelected()
	}
	// Cleared on the press, as attach does, so a notice the lane posts
	// before entry, such as a dropped detach request, stays up.
	m.clearErr()
	m.enqueueEffect(focusRequest{sessionID: sess.ID, generation: m.gens.foreground}, 0, false)
	return m, m.nextEffectCmd()
}

// caretAtInputStart reports whether the agent's caret sits at the head of
// its prompt, with nothing but the prompt marker to its left. Left is a
// no-op for the agent there, which is what frees the key to mean "back to
// the list" without ever costing a keystroke inside the prompt: anywhere
// else it still moves the caret.
//
// A wrapped prompt's continuation rows carry no marker, so a caret at the
// head of one of them forwards Left as usual and reaches the end of the
// row above.
//
// A tool may park the terminal cursor below its footer and paint its own
// block cursor inside the composer instead (command-code does). For one
// declaring composer_placeholder, a caret on a blank row reads the
// composer row instead: the placeholder being on screen is the evidence
// the composer is empty and its caret at the head of the prompt; a draft
// replaces the placeholder and Left belongs to the agent.
//
// A zero-width input_prefix declares a bare composer row (pi's). Its
// painted cursor is the only thing locating that row, so tmux's position
// stays meaningful when the application hides the terminal cursor.
func (m *Model) caretAtInputStart(sessID, tool string) bool {
	pane := m.focus.pane.Pane()
	if m.services.engine == nil || pane.SessionID != sessID || m.focus.pane.ScrolledBack() {
		return false
	}
	// A frame taken before keys still on their way shows the caret where
	// typing was, not where it is.
	if watch := m.focus.runtime.watch; watch != nil && !watch.caughtUp(pane.Writes) {
		return false
	}
	_, bareInput := m.services.engine.InputPrefix(tool, "")
	caretCellKnown := pane.Cursor.Visible ||
		(pane.Cursor.PositionKnown && (m.services.engine.ParksItsCaret(tool) || bareInput))
	if !caretCellKnown {
		return false
	}
	rows := strings.Split(strings.TrimSuffix(m.workspace.preview, "\n"), "\n")
	if pane.Cursor.Y < 0 || pane.Cursor.Y >= len(rows) {
		return false
	}
	row := ansi.Strip(rows[pane.Cursor.Y])
	prefix, ok := m.services.engine.InputPrefix(tool, row)
	if !ok {
		return m.caretParksAndComposerIsEmpty(tool, rows)
	}
	if !status.HasTextBeforeCaret(m.services.engine, tool, row, pane.Cursor.X) &&
		pane.Cursor.X >= ansi.StringWidth(prefix) {
		return m.caretRowEndsAPromptHead(tool, rows, pane.Cursor.Y)
	}
	return false
}

// caretParksAndComposerIsEmpty serves tools that park the terminal cursor
// below their footer and paint the composer's caret themselves. The parked
// cell sits on a blank row, so the composer is found by searching up for
// the nearest marker row, and an empty composer there is what proves Left
// costs the agent nothing: a draft holds the key instead.
// A caret cell that is not parked on a blank row is none of this path's
// business: the marker rules decide it as usual.
func (m *Model) caretParksAndComposerIsEmpty(tool string, rows []string) bool {
	cursor := m.focus.pane.Pane().Cursor
	// The parking spot is a blank corner cell: column zero on a row with
	// nothing painted on it. A cursor at column zero over any other
	// content is not the park, whatever sits above it.
	if cursor.X != 0 || strings.TrimSpace(ansi.Strip(rows[cursor.Y])) != "" {
		return false
	}
	for y := cursor.Y - 1; y >= 0; y-- {
		row := ansi.Strip(rows[y])
		if _, ok := m.services.engine.InputPrefix(tool, row); !ok {
			continue
		}
		return m.services.engine.ComposerIsEmpty(tool, row)
	}
	return false
}

// caretRowEndsAPromptHead rejects the row when a draft continues onto it:
// a multi-line prompt's blank continuation line looks exactly like an empty
// composer, and Left there belongs to the agent. The row above tells them
// apart when it carries the same marker with text past it. A wrapped line
// is rejected the same way; the rule that bounds the input box (pi's rule)
// is not draft text and does not reject.
func (m *Model) caretRowEndsAPromptHead(tool string, rows []string, y int) bool {
	if y == 0 {
		return true
	}
	above := ansi.Strip(rows[y-1])
	abovePrefix, ok := m.services.engine.InputPrefix(tool, above)
	if !ok || m.services.engine.MatchesActivityCutoff(tool, above) {
		return true
	}
	return strings.TrimSpace(above[len(abovePrefix):]) == ""
}

// textBeforeCaret reports whether anything but blanks sits between a tool's
// prompt marker and the caret on this row, which is how a line someone has
// half written is told from an empty prompt. A row that is not an input line
// carries no such text. Claude pads its marker with a non-breaking space, so
// blank means any space rune, not the ASCII one alone; tmux trims a row's
// trailing blanks, so a row that ends before the caret is blank the rest of
// the way.

// leaveFocus returns to the list. Mouse reporting stays on: handing it back
// to the terminal here would let a wheel notch scroll the manager out of
// view, so the list swallows the wheel instead.
func (m *Model) leaveFocus() tea.Cmd {
	if report := m.focus.pane.Leave(); report != "" {
		m.sendFocusReport(report, false)
	}
	return m.leaveFocusMode()
}

func (m *Model) leaveFocusMode() tea.Cmd {
	m.mode = modeList
	// A run opened before the session was entered would pair with the very
	// click that comes back here, focusing it again instead of leaving.
	m.rail.ResetClickHistory()
	m.flushPendingNotice()
	return nil
}

// focusPagesScrollback reads the Focus policy for the footer and status
// line, which name the page keys only while they page.
func (m *Model) focusPagesScrollback(sess store.Session) bool {
	return m.focus.pane.PagesScrollback(sess.ID)
}

// handleFocusKey forwards focused input to the pane except for session
// actions and paging a normal-screen pane's history. Every plain
// character, q included, still reaches the agent.
func (m *Model) handleFocusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sess, ok := m.selected()
	if !ok {
		return m, m.leaveFocus()
	}
	result := m.focus.pane.Key(msg, uifocus.KeyContext{
		SessionID:   sess.ID,
		Rows:        m.focusPaneRows(),
		Detach:      m.services.keys.Binding(keybind.Detach).Has(msg.String()),
		Editor:      m.services.keys.Binding(keybind.Editor).Has(msg.String()),
		Review:      m.services.keys.Binding(keybind.Review).Has(msg.String()),
		ArrowStep:   m.prefs.arrowStep,
		AtInputHead: msg.Type == tea.KeyLeft && !msg.Alt && m.caretAtInputStart(sess.ID, sess.Tool),
	})
	switch result.Action {
	case uifocus.LeaveFocus:
		if result.SendReport != "" {
			m.sendFocusReport(result.SendReport, false)
		}
		return m, m.leaveFocusMode()
	case uifocus.ScrollPane:
		if result.Region == nil {
			return m, nil
		}
		return m, m.focusRegionRequestCmd(*result.Region)
	case uifocus.OpenEditor:
		return m.openEditor()
	case uifocus.OpenReview:
		cmd := m.openDiff()
		if m.mode == modeDiff {
			m.reviewNav.ret = reviewReturn{kind: reviewReturnFocus, sessionID: sess.ID}
		}
		return m, cmd
	}
	var resume tea.Cmd
	if result.Region != nil {
		resume = m.focusRegionRequestCmd(*result.Region)
	}
	if msg.Paste {
		m.dispatchInput(inputRequest{kind: inputPaste, session: sess, text: string(msg.Runes), echo: true})
		return m, tea.Batch(resume, m.nextEffectCmd())
	}
	command, ok := focusKeyCommand(tmux.PaneTarget(sess.ID), msg)
	if !ok {
		return m, resume
	}
	m.dispatchInput(inputRequest{kind: inputKeys, session: sess, command: command, submit: result.Submit, echo: true})

	return m, tea.Batch(resume, m.nextEffectCmd())
}

// pasteFocused is the seam tests swap to observe pastes into the pane.
var pasteFocused = func(driver *tmux.Driver, id, text string) error {
	return driver.Paste(id, text)
}

func (m *Model) handleFocusMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	sess, ok := m.selected()
	if !ok {
		return m, nil
	}
	result := m.focus.pane.Mouse(msg, uifocus.MouseContext{
		SessionID:    sess.ID,
		Rows:         m.focusPaneRows(),
		RuntimeReady: m.focus.runtime.watch != nil,
	})
	return m.applyFocusMouse(result)
}

func (m *Model) applyFocusMouse(result uifocus.MouseResult) (tea.Model, tea.Cmd) {
	if result.SendReport != "" {
		m.sendFocusReport(result.SendReport, result.Input)
	}
	if result.Leave {
		return m, m.leaveFocusMode()
	}
	if result.OpenURL != "" {
		return m, openLinkCmd(result.OpenURL)
	}
	if result.Copy != nil {
		return m, copyFocusSelectionCmd(*result.Copy)
	}
	if result.Region != nil {
		return m, m.focusRegionRequestCmd(*result.Region)
	}
	return m, nil
}

func copyFocusSelectionCmd(request uifocus.CopyRequest) tea.Cmd {
	return copyTextCmd(request.Text, func(chars int) tea.Msg {
		return focusCopiedMsg{chars: chars, gen: request.Generation}
	})
}

func copyTextCmd(text string, done func(chars int) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if err := clipboard.WriteText(text); err != nil {
			return errMsg{err}
		}
		return done(utf8.RuneCountInString(text))
	}
}

type focusCopiedMsg struct {
	chars int
	gen   uint64
}

// paneCaretRow resolves the root-owned status fact that a tool's terminal
// cursor is parked below its own painted composer. Focus owns crop geometry
// after this value crosses the boundary.
func (m *Model) paneCaretRow() int {
	pane := m.focus.pane.Pane()
	if m.mode != modeFocus || !pane.Cursor.Visible || m.focus.pane.ScrolledBack() {
		return -1
	}
	caret := pane.Cursor.Y
	if sess, ok := m.selected(); ok && m.services.engine != nil && m.services.engine.ParksItsCaret(sess.Tool) {
		rows := strings.Split(strings.TrimSuffix(m.workspace.preview, "\n"), "\n")
		if caret < len(rows) && caretParkedBelowContent(rows, caret, pane.Cursor.X) {
			return -1
		}
	}
	return caret
}

func caretParkedBelowContent(rows []string, caret, column int) bool {
	if column != 0 {
		return false
	}
	for y := caret; y < len(rows); y++ {
		if strings.TrimSpace(ansi.Strip(rows[y])) != "" {
			return false
		}
	}
	for y := caret - 1; y >= 0; y-- {
		if strings.TrimSpace(ansi.Strip(rows[y])) != "" {
			return true
		}
	}
	return false
}

var openURL = func(url string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	return exec.Command(opener, url).Start()
}

type linkOpenErrMsg struct{ err error }

func openLinkCmd(url string) tea.Cmd {
	return func() tea.Msg {
		if remoteTerminal() {
			return linkPageMsg{url: url}
		}
		if err := openURL(url); err != nil {
			return linkOpenErrMsg{err: fmt.Errorf("open link: %w", err)}
		}
		return nil
	}
}

type focusScrollMsg struct {
	sessID  string
	offset  int
	rows    int
	preview string
	ok      bool
}

func (m *Model) sendFocusReport(report string, echo bool) {
	sess, ok := m.selected()
	watch := m.focus.runtime.watch
	if !ok || watch == nil {
		return
	}
	command, args := tmux.GuardedMouseReport(tmux.PaneTarget(sess.ID), report)
	m.dispatchInput(inputRequest{kind: inputMouse, session: sess, command: command, args: args, echo: echo})
}

func (m *Model) focusRegionCmd(sessID string, offset int) tea.Cmd {
	return m.focusRegionRequestCmd(uifocus.RegionRequest{
		SessionID: sessID,
		Offset:    offset,
		Rows:      m.focusPaneRows(),
	})
}

func (m *Model) focusRegionRequestCmd(request uifocus.RegionRequest) tea.Cmd {
	command := fmt.Sprintf(`capture-pane -p -e -t %s -S %d -E -`,
		tmux.PaneTarget(request.SessionID), -(request.Offset + request.Rows))
	watch := m.focus.runtime.watch
	return func() tea.Msg {
		if watch == nil {
			return focusScrollMsg{sessID: request.SessionID, offset: request.Offset, rows: request.Rows}
		}
		out, ok := watch.query(command)
		if ok {
			out = matchExecShape(out)
		} else {
			// No control client: a forked capture of the same rows comes
			// back in the exec shape already.
			ctx, cancel := context.WithTimeout(context.Background(), focusControlCallTimeout)
			var err error
			out, err = watch.driver.CapturePaneHistoryContext(ctx, request.SessionID, request.Offset+request.Rows)
			cancel()
			ok = err == nil
		}
		return focusScrollMsg{
			sessID:  request.SessionID,
			offset:  request.Offset,
			rows:    request.Rows,
			preview: bottomWindow(out, request.Rows, request.Offset),
			ok:      ok,
		}
	}
}

func bottomWindow(capture string, rows, offset int) string {
	lines := strings.Split(strings.TrimSuffix(capture, "\n"), "\n")
	end := max(len(lines)-offset, 0)
	start := max(end-rows, 0)
	return strings.Join(lines[start:end], "\n") + "\n"
}

// viewFullFocusFrame is a session opened from the full screen list: the
// captured pane owns the whole terminal body and the list waits behind
// it. The focus rules cap and close the pane the way they do in the
// split, stretched to the terminal's edges.
func (m *Model) viewFullFocusFrame() string {
	footer := m.viewFooter()
	bodyHeight := m.listBodyHeight()
	// This frame paints no rail, so a click lands on no row: the list
	// frame's hits would otherwise select a row nobody pointed at.
	m.layout.displayedRail = uirail.Frame{}
	frame := []string{}
	for _, line := range m.viewHeaderRows() {
		frame = append(frame, paint(line, m.layout.width, backdropHex()))
	}
	frame = append(frame, paint(hrule(m.layout.width), m.layout.width, backdropHex()))
	frame = append(frame, paint(m.focusFactsLine(m.layout.width), m.layout.width, backdropHex()))
	frame = append(frame, paint(m.focusEdge(m.layout.width), m.layout.width, backdropHex()))
	paneRows := m.previewLines(m.layout.width, bodyHeight, strings.Repeat(" ", contentGutter))
	frame = append(frame, paintContent(paneRows, m.layout.width, bodyHeight, backdropHex())...)
	frame = append(frame, paint(m.focusEdge(m.layout.width), m.layout.width, backdropHex()))
	for _, line := range splitLines(footer) {
		frame = append(frame, paint(line, m.layout.width, backdropHex()))
	}
	return m.overlayTopRight(strings.Join(frame, "\n"), m.statusToast(), m.listChromeRows()+1)
}

// focusFactsLine says which session a full screen frame is showing, where
// nothing else on it does: the state dot and the name on the left, then
// where it runs and what it costs against the right edge, with the whole
// width to itself and a rule under it holding it off the pane. The keys
// the split's rule names are in the footer, so they stay there.
func (m *Model) focusFactsLine(width int) string {
	sess, ok := m.selected()
	if !ok {
		return ""
	}
	sep := subtleStyle.Render(" · ")
	left := " " + m.sessionGlyph(sess) + " " + valueStyle.Render(m.displayName(sess)) +
		sep + valueStyle.Render(sess.Tool) +
		sep + lipgloss.NewStyle().Foreground(statusColor(sess.Status)).Render(statusLabel(sess.Status)) +
		sep + subtleStyle.Render(relSince(lastActivity(sess)))
	// The facts give way one at a time as the terminal narrows, the least
	// telling first, so a tight line still carries what it has room for
	// rather than dropping the lot.
	facts := []focusFact{{text: valueStyle.Render(truncateTail(shortHome(m.sessionDir(sess), m.env.homeDir), focusFactsDirCap)), spare: 3}}
	if sess.WorktreeBranch != "" {
		facts = append(facts, focusFact{text: subtleStyle.Render("⑂ ") + valueStyle.Render(sess.WorktreeBranch), spare: 2})
	}
	if m.workspace.procFor == sess.ID && m.workspace.proc.OK {
		facts = append(facts,
			focusFact{text: labelStyle.Render("cpu ") + valueStyle.Render(fmt.Sprintf("%.1f%%", m.workspace.proc.CPUPercent)), spare: 1},
			focusFact{text: labelStyle.Render("ram ") + valueStyle.Render(humanBytes(m.workspace.proc.RSS)), spare: 1})
	}
	facts = append(facts, focusFact{text: labelStyle.Render("started ") + valueStyle.Render(relSince(sess.CreatedAt)), spare: 4})
	if queued := m.workspace.queuedMessages[sess.ID]; queued > 0 {
		facts = append(facts, focusFact{text: valueStyle.Render(fmt.Sprintf("%d queued", queued)), spare: 0})
	}

	right := joinFacts(facts, sep)
	for len(facts) > 0 && ansi.StringWidth(left)+focusFactsGap+ansi.StringWidth(right) > width {
		facts = dropSparest(facts)
		right = joinFacts(facts, sep)
	}
	if ansi.StringWidth(left) > width {
		return ansi.Truncate(left, max(width, 0), "…")
	}
	return rowColumns(left, right, width)
}

// focusFact is one reading on the full screen focus line, spare ranking
// how readily it gives up its room: the higher, the sooner it goes.
type focusFact struct {
	text  string
	spare int
}

func joinFacts(facts []focusFact, sep string) string {
	if len(facts) == 0 {
		return ""
	}
	parts := make([]string, len(facts))
	for i, fact := range facts {
		parts[i] = fact.text
	}
	return strings.Join(parts, sep) + " "
}

func dropSparest(facts []focusFact) []focusFact {
	sparest := 0
	for i, fact := range facts {
		if fact.spare >= facts[sparest].spare {
			sparest = i
		}
	}
	return append(facts[:sparest], facts[sparest+1:]...)
}

// focusEdge is the hairline holding the full screen pane off what sits
// above and below it, in the pane's own tone once it has a box to trace.
func (m *Model) focusEdge(width int) string {
	if m.focus.pane.FrameBox().Valid {
		return focusEdgeStyle.Render(strings.Repeat("─", max(width, 0)))
	}
	return hrule(width)
}

// focusFactsDirCap keeps a deep path from crowding the readings beside it,
// and focusFactsGap is the least space kept between the name and them.
const (
	focusFactsDirCap = 40
	focusFactsGap    = 2
)

func focusTopRule(width int, keys keybind.Table) string {
	hints := []string{"focused", keys.Binding(keybind.Detach).Label() + " back"}
	if label := keys.Binding(keybind.Review).Label(); label != "" {
		hints = append(hints, label+" review")
	}
	if label := keys.Binding(keybind.Editor).Label(); label != "" {
		hints = append(hints, label+" editor")
	}
	title := " " + strings.Join(hints, " · ") + " "
	rule := annotationStyle.Render(title)
	rest := width - lipgloss.Width(title)
	if rest > 0 {
		rule += focusEdgeStyle.Render(strings.Repeat("─", rest))
	}
	return rule
}
