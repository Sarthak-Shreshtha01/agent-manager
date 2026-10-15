package ui

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/YoanWai/agent-manager/internal/catalog"
	"github.com/YoanWai/agent-manager/internal/config"
	"github.com/YoanWai/agent-manager/internal/launch"
	"github.com/YoanWai/agent-manager/internal/sessioncmd"
	"github.com/YoanWai/agent-manager/internal/store"
	"github.com/YoanWai/agent-manager/internal/ui/spawn"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// formDialog is the New Session form (internal/ui/spawn) with what the
// root holds for it: the prompt box, the path completer under its dir
// field, and the group picker the group form and move dialog share.
type formDialog struct {
	spawn.Form
	// prompt is a composer rather than a plain textarea: a first task is
	// often a screenshot, so the box has to hold pasted images the way the
	// quick prompt does.
	prompt     composer
	paths      pathComplete
	groups     []groupOption
	groupIndex int
	// remote is the connection a spawn goes to, when one is the target.
	remote formRemote
}

type groupOption struct {
	path   string
	depth  int
	sessID string
	name   string
}

// spawnHost is the root as the New Session form and its choice see it.
type spawnHost struct{ m *Model }

func (m *Model) spawnHost() spawnHost { return spawnHost{m: m} }

func (h spawnHost) ChoiceCaps(toolName string) spawn.Caps {
	tool := h.m.services.cfg.Tools[toolName]
	return spawn.Caps{
		Catalog:  tool.Catalog != "",
		Profiles: tool.ProfileArgs != "",
		Models:   tool.ModelArgs != "",
		Efforts:  tool.EffortArgs != "",
	}
}

// ChoiceAnswer hands the choice a copy, so only the root's catalog
// handlers change what the CLI answered.
func (h spawnHost) ChoiceAnswer(toolName string) *spawn.Answer {
	state := h.m.ledger.catalogs[toolName]
	if state == nil {
		return nil
	}
	return &spawn.Answer{Cat: state.cat, Err: state.err, Loaded: state.loaded, Loading: state.loading}
}

func (h spawnHost) ChoiceSetting(key string) string { return h.m.settings.cache.value(key) }

func (h spawnHost) SaveChoiceSetting(key, value string) { h.m.saveChoiceSetting(key, value) }

func (h spawnHost) ReportErr(text string) { h.m.reportErr(text) }

func (h spawnHost) ClearErr() { h.m.clearErr() }

func (h spawnHost) Prompt() spawn.Prompt {
	return promptBox{c: &h.m.form.prompt, target: composerForm, host: h.m}
}

func (h spawnHost) Paths() spawn.Paths {
	// A connection's directory is the host's: nothing here completes it.
	return pathField{pc: &h.m.form.paths, target: pathSuggestionForm, off: h.m.form.remote.on()}
}

// promptBox lends a feature package one of the root's composers.
type promptBox struct {
	c      *composer
	target composerID
	host   composerHost
}

func (p promptBox) Focus() { p.c.input.Focus() }

func (p promptBox) Blur() { p.c.input.Blur() }

func (p promptBox) Fit(width, rows int) {
	p.c.input.SetWidth(width)
	p.c.maxRows = rows
}

func (p promptBox) View() string { return p.c.view() }

func (p promptBox) StepRow(msg tea.KeyMsg) (tea.Cmd, bool) { return p.c.stepRow(msg) }

func (p promptBox) ChipKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	return p.c.handleChipKey(p.host, p.target, msg)
}

func (p promptBox) Type(msg tea.KeyMsg) tea.Cmd { return p.c.typeKey(msg) }

func (p promptBox) Release() { p.c.release() }

func (p promptBox) Pasting() bool { return p.c.pasting() }

func (p promptBox) Message() string { return p.c.message() }

// pathField lends the form the root's path completer.
type pathField struct {
	pc     *pathComplete
	target pathSuggestionTarget
	// off asks for no suggestions.
	off bool
}

func (p pathField) Active() bool { return p.pc.active() }

func (p pathField) Chosen() bool { return p.pc.chosen }

func (p pathField) Move(delta int) bool { return p.pc.move(delta) }

func (p pathField) Reset() { p.pc.reset() }

func (p pathField) Suggest(typed string) tea.Cmd {
	if p.off {
		return nil
	}
	return p.pc.request(p.target, typed, systemPathSuggestionReader{})
}

func (p pathField) View() string { return p.pc.view() }

func (p pathField) Hint() [][2]string { return pathSuggestHint(p.pc.chosen) }

// spawnStyles copies the active theme for one paint.
func spawnStyles() spawn.Styles {
	return spawn.Styles{
		Subtle:      subtleStyle,
		Value:       valueStyle,
		Muted:       mutedStyle,
		Warn:        warnStyle,
		Label:       labelStyle,
		SubtleColor: colorSubtle,
		Accent:      colorAccent,
		Accent2:     colorAccent2,
		Input:       textInputView,
	}
}

func textField(placeholder string, limit int) textinput.Model {
	return spawn.TextField(placeholder, limit)
}

const formPromptMaxRows = 4

func promptField() composer {
	in := textarea.New()
	in.CharLimit = 2000
	in.Placeholder = "first task (optional)"
	in.ShowLineNumbers = false
	in.SetPromptFunc(2, func(lineIndex int) string {
		if lineIndex == 0 {
			return "> "
		}
		return "  "
	})
	in.FocusedStyle.CursorLine = lipgloss.NewStyle()
	holdOpen(&in)
	return composer{input: in, maxRows: formPromptMaxRows}
}

func formField(label, value string, focused bool) string {
	return spawn.FormField(spawnStyles(), label, value, focused)
}

func groupBadge(path string) string {
	return lipgloss.NewStyle().Foreground(colorAccent2).Render(path)
}

func pathSuggestHint(chosen bool) [][2]string {
	if chosen {
		return [][2]string{{"↑↓", "pick"}, {"↵/tab", "complete"}, {"esc", "close"}}
	}
	return [][2]string{{"↑↓", "pick"}, {"tab", "complete"}, {"↵", "create"}, {"esc", "close"}}
}

// formValueWidth is the columns a field value can occupy inside the card.
func (m *Model) formValueWidth() int {
	return cardInnerWidth(m.cardWidth()) - spawn.LabelColumn
}

// syncFormFieldWidths fits the fields to the card so long values scroll
// (inputs) or wrap (prompt) instead of clipping at the card edge.
func (m *Model) syncFormFieldWidths() {
	inner := m.formValueWidth()
	m.form.SyncWidths(inner)
	m.form.prompt.input.SetWidth(inner)
}

// contextGroup is the group the cursor currently sits in: a highlighted
// group row itself, or the group holding a highlighted session.
func (m *Model) contextGroup() string {
	if entry, ok := m.selectedRow(); ok {
		if entry.isGroup {
			return entry.group
		}
		return entry.sess.Group
	}
	return ""
}

func (m *Model) openForm() tea.Cmd {
	return m.openFormWithReader(storeSettingWriter{st: m.services.store})
}

func (m *Model) openFormWithReader(reader settingsValueReader) tea.Cmd {
	tools, toolIndex := m.cachedSpawnToolSelection()
	if len(tools) == 0 {
		m.reportErr("no CLIs enabled: open settings (s), then CLIs, to turn some on")
		return nil
	}
	prompt := promptField()
	prompt.gen = m.nextComposerGen()
	m.form = formDialog{
		Form:   spawn.NewForm(m.spawnHost(), tools, toolIndex),
		prompt: prompt,
		paths:  m.form.paths.fresh(),
	}
	m.clearErr()
	m.syncFormFieldWidths()
	m.forgetWorktreeCapability()
	m.rebuildGroupOptions(m.contextGroup())
	m.form.SetDir(m.capturedGroupDefaultDir(m.selectedGroupPath()))
	m.form.SeedWorktree(m.cachedSpawnWorktreeDefault(m.selectedGroupPath()))
	if target, ok := m.remoteTarget(); ok {
		m.form.aimRemote(target)
	}
	m.mode = modeForm
	catalog := m.ensureCatalog(tools[toolIndex])
	if m.settings.pending > 0 {
		return tea.Batch(m.formWorktreeProbeCmd(false), catalog)
	}
	return tea.Batch(settingsLoadCmd(settingsLoadRequest{target: settingsLoadForm, generation: uint64(m.form.prompt.gen), extra: m.choiceSettingKeys()}, reader), catalog)
}

// applyCachedFormDefaults takes the loaded tool and worktree defaults. A
// tool the load moved to starts its choice rows over and asks its CLI.
func (m *Model) applyCachedFormDefaults() tea.Cmd {
	before := m.form.Tool()
	m.form.SetTools(m.cachedSpawnToolSelection())
	m.form.DefaultWorktree(m.cachedSpawnWorktreeDefault(m.selectedGroupPath()))
	toolName := m.form.Tool()
	if toolName == before || toolName == "" {
		return nil
	}
	m.form.RestartChoice(m.spawnHost())
	m.syncFormFieldWidths()
	return m.ensureCatalog(toolName)
}

func (m *Model) formTool() string { return m.form.Tool() }

func (m *Model) selectedGroupPath() string { return m.form.groupPath() }

// groupPath is the group the picker highlights. The group form and the
// move dialog pick from the same list.
func (d *formDialog) groupPath() string {
	if d.groupIndex >= 0 && d.groupIndex < len(d.groups) {
		return d.groups[d.groupIndex].path
	}
	return ""
}

// rebuildGroupOptions flattens the group tree into picker rows.
// Index 0 is always the root; selectPath moves the highlight when given.
func (m *Model) rebuildGroupOptions(selectPath string) {
	paths := groupClosure(m.workspace.groups, m.workspace.sessions)
	for path := range paths {
		if m.groupEffectivelyArchived(path) {
			delete(paths, path)
		}
	}
	children := childIndex(paths, m.workspace.groups)

	options := []groupOption{{path: "", depth: 0}}
	var walk func(path string, depth int)
	walk = func(path string, depth int) {
		options = append(options, groupOption{path: path, depth: depth})
		for _, child := range children[path] {
			walk(child, depth+1)
		}
	}
	for _, root := range children[""] {
		walk(root, 1)
	}

	m.form.groups = options
	m.form.groupIndex = 0
	for i, opt := range options {
		if selectPath != "" && opt.path == selectPath {
			m.form.groupIndex = i
			return
		}
	}
}

func groupClosure(groups []string, sessions []store.Session) map[string]bool {
	paths := map[string]bool{}
	add := func(path string) {
		for path != "" {
			paths[path] = true
			index := strings.LastIndex(path, "/")
			if index < 0 {
				break
			}
			path = path[:index]
		}
	}
	for _, group := range groups {
		add(group)
	}
	for _, session := range sessions {
		add(session.Group)
	}
	return paths
}

func childIndex(paths map[string]bool, ordered []string) map[string][]string {
	rank := make(map[string]int, len(ordered))
	for index, path := range ordered {
		rank[path] = index
	}
	children := map[string][]string{}
	for path := range paths {
		parent := parentGroup(path)
		children[parent] = append(children[parent], path)
	}
	for _, siblings := range children {
		sort.SliceStable(siblings, func(i, j int) bool {
			left, leftKnown := rank[siblings[i]]
			right, rightKnown := rank[siblings[j]]
			if leftKnown && rightKnown {
				return left < right
			}
			if leftKnown != rightKnown {
				return leftKnown
			}
			return siblings[i] < siblings[j]
		})
	}
	return children
}

func (m *Model) groupEffectivelyArchived(path string) bool {
	return store.EffectivelyArchived(m.workspace.archivedGroups, path)
}

func (m *Model) materializeGroupsLocal(paths []string) {
	known := make(map[string]bool, len(m.workspace.groups))
	for _, path := range m.workspace.groups {
		known[path] = true
	}
	for _, path := range paths {
		if !known[path] {
			m.workspace.groups = append(m.workspace.groups, path)
			known[path] = true
		}
	}
}

func (m *Model) handleFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, request := m.form.HandleKey(m.spawnHost(), typedText(msg))
	return m, tea.Batch(cmd, m.runFormRequest(request, systemDirectoryPreflight{git: m.services.gitDrv}))
}

// runFormRequest executes what a form key or click asked of the root.
func (m *Model) runFormRequest(request spawn.FormRequest, reader directoryPreflight) tea.Cmd {
	if cmd, taken := m.remoteFormRequest(request); taken {
		return cmd
	}
	switch {
	case request.Close:
		m.mode = modeList
	case request.ApplyPath:
		return m.applyPathSuggestion()
	case request.Group != 0:
		return m.moveGroupCursor(request.Group)
	case request.PickGroup:
		if delta := request.GroupEntry - m.form.groupIndex; delta != 0 {
			return m.moveGroupCursor(delta)
		}
	case request.Toggle:
		return m.toggleFormWorktree()
	case request.Probe:
		return m.formWorktreeProbeCmd(false)
	case request.Catalog != "":
		m.syncFormFieldWidths()
		return m.ensureCatalog(request.Catalog)
	case request.Spawn != nil:
		req := m.formSpawnRequest(*request.Spawn)
		req.dirReader = reader
		m.clearErr()
		m.dispatchSpawn(req)
		return m.nextEffectCmd()
	}
	return nil
}

// formSpawnRequest names the session, places it in the picked group and
// captures the group's launch inputs and the prompt's images.
func (m *Model) formSpawnRequest(request spawn.Request) spawnRequest {
	name, autoNamed := request.Name, request.Name == ""
	if autoNamed {
		name = request.Tool + "-" + newID()[:4]
	}
	group := m.form.groupPath()
	defaults := m.spawnDefaults(group)
	return spawnRequest{
		kind:         spawnForm,
		toolName:     request.Tool,
		name:         name,
		group:        group,
		prompt:       request.Prompt,
		autoNamed:    autoNamed,
		pickWorktree: request.Worktree,
		choice:       config.Choice(request.Pick),
		base:         defaults.base,
		pane:         defaults.pane,
		composerGen:  m.form.prompt.gen,
		images:       m.form.prompt.attachments,
		draft:        m.form.prompt.input.Value(),
		draftName:    m.form.Name(),
		draftDir:     m.form.Dir(),
		rawDir:       request.Dir,
		dirFallbacks: defaults.fallbacks,
		wantWorktree: request.Worktree,
	}
}

func (m *Model) handleFormClick(x, y int) (tea.Model, tea.Cmd) {
	// The body starts under the card's title row and the blank row after it.
	line := y - m.layout.cardTop - 2
	if x < m.layout.cardLeft || x >= m.layout.cardRight {
		return m, nil
	}
	return m, m.runFormRequest(m.form.Click(m.spawnHost(), line), nil)
}

// moveGroupCursor moves within the expanded group picker, wrapping at the
// ends; a delta of 0 re-resolves the dependent defaults in place.
func (m *Model) moveGroupCursor(delta int) tea.Cmd {
	count := len(m.form.groups)
	if count == 0 {
		return nil
	}
	m.form.groupIndex = (m.form.groupIndex + delta + count) % count
	if m.mode == modeForm && m.form.DirAuto() {
		m.form.SetDir(m.capturedGroupDefaultDir(m.selectedGroupPath()))
	}
	if m.mode == modeForm {
		m.form.DefaultWorktree(m.cachedSpawnWorktreeDefault(m.selectedGroupPath()))
	}
	if m.mode == modeGroupForm && m.groupForm.pathAuto {
		m.groupForm.path.SetValue(m.capturedAncestorGroupDir(m.selectedGroupPath()))
	}
	if m.mode == modeForm {
		return m.formWorktreeProbeCmd(false)
	}
	return nil
}

// formSpawnDir is the directory the form would launch in, resolved the
// same way submit resolves it.
func (m *Model) formSpawnDir() string {
	return m.capturedAbsolutePath(m.form.Dir(), m.env.workDir)
}

// formWorktreeOn is the worktree state the form shows and spawns with: the
// toggle, unless the chosen directory cannot host a worktree.
func (m *Model) formWorktreeOn() bool {
	capable, known := m.cachedWorktreeCapability(m.formSpawnDir())
	return m.form.Worktree() && known && capable
}

// toggleFormWorktree flips the toggle, or explains why the chosen
// directory rules a worktree out.
func (m *Model) toggleFormWorktree() tea.Cmd {
	dir := m.formSpawnDir()
	capable, known := m.cachedWorktreeCapability(dir)
	if !known {
		return m.formWorktreeProbeCmd(true)
	}
	if !capable {
		m.reportErr("worktree sessions need a git repository: " + dir + " is not one")
		return nil
	}
	m.clearErr()
	m.form.SetWorktree(!m.form.Worktree())
	return nil
}

func (m *Model) submitForm() (tea.Model, tea.Cmd) {
	return m.submitFormWithReader(systemDirectoryPreflight{git: m.services.gitDrv})
}

func (m *Model) submitFormWithReader(reader directoryPreflight) (tea.Model, tea.Cmd) {
	return m, m.runFormRequest(m.form.Submit(m.spawnHost()), reader)
}

func (m *Model) viewForm() string {
	facts := spawn.FormFacts{
		Width:  m.formValueWidth(),
		NoRepo: worktreeUnavailable,
		Group:  escapeControlsInline(displayGroup(m.form.groups[m.form.groupIndex].path)),
		Styles: spawnStyles(),
	}
	if m.form.Focused() == spawn.FieldGroup {
		facts.GroupPicker = strings.Split(m.form.viewGroupPicker(), "\n")
	}
	facts.WorktreeCapable, facts.WorktreeKnown = m.cachedWorktreeCapability(m.formSpawnDir())
	if m.form.remote.on() {
		facts.WorktreeCapable, facts.WorktreeKnown = true, true
		facts.Host = lipgloss.NewStyle().Foreground(colorRemote).Render(m.form.remote.host)
	} else if m.formWorktreeOn() {
		facts.Base = m.spawnBaseLabel(m.formSpawnDir(), m.selectedGroupPath())
	}
	body, hint := m.form.View(m.spawnHost(), facts)
	return m.card("◆ New Session", body, hint)
}

func (m *Model) rememberSpawnPick(tool string, worktree bool) {
	m.ledger.lastSpawnTool = tool
	m.ledger.lastSpawnWorktree = worktree
}

func (m *Model) buildLaunch(toolName string, tool config.Tool, baseCommand, id, cwd string, launchedAt time.Time) (string, map[string]string, error) {
	return launch.Environment(m.services.hooks, toolName, tool, baseCommand, id, cwd, launchedAt)
}

func (m *Model) routeSpawnMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case worktreeProbeMsg:
		return routed(m.handleWorktreeProbe(msg))

	case pathSuggestionsMsg:
		m.completer(msg.request.target).handle(m, msg)
		return routed(m, nil)

	case terminalDirectoryMsg:
		return routed(m.handleTerminalDirectory(msg))
	}
	return nil, nil, false
}

type catalogState struct {
	cat       catalog.Catalog
	err       error
	loaded    bool
	loading   bool
	checkedAt time.Time
}

// A running manager rereads the kept answer to catch a CLI upgrade or a
// spawn's refresh, and retries a failure sooner, since a login may be all
// it lacked.
const (
	catalogRecheck = 10 * time.Minute
	catalogRetry   = 30 * time.Second
)

type catalogMsg struct {
	tool string
	cat  catalog.Catalog
	err  error
	// stale is shown while a fresh answer is asked.
	stale bool
}

// ensureCatalog reads the kept answer or asks the CLI off the update path,
// since an answer takes up to seconds.
func (m *Model) ensureCatalog(toolName string) tea.Cmd {
	tool := m.services.cfg.Tools[toolName]
	if tool.Catalog == "" {
		return nil
	}
	if m.ledger.catalogs == nil {
		m.ledger.catalogs = map[string]*catalogState{}
	}
	state := m.ledger.catalogs[toolName]
	if state == nil {
		state = &catalogState{}
		m.ledger.catalogs[toolName] = state
	}
	recheck := catalogRecheck
	if state.err != nil {
		recheck = catalogRetry
	}
	if state.loading || (state.loaded && time.Since(state.checkedAt) < recheck) {
		return nil
	}
	state.loading = true
	configDir := m.services.configDir
	return func() tea.Msg {
		if cat, fresh, ok := catalog.Cached(configDir, toolName, tool); ok {
			return catalogMsg{tool: toolName, cat: cat, stale: !fresh}
		}
		return refreshCatalog(configDir, toolName, tool)
	}
}

func refreshCatalog(configDir, toolName string, tool config.Tool) tea.Msg {
	cat, err := catalog.Refresh(context.Background(), configDir, toolName, tool)
	return catalogMsg{tool: toolName, cat: cat, err: err}
}

func (m *Model) handleCatalog(msg catalogMsg) tea.Cmd {
	state := m.ledger.catalogs[msg.tool]
	if msg.err == nil {
		state.cat = msg.cat
	}
	state.err = msg.err
	state.loaded = true
	state.loading = msg.stale
	state.checkedAt = time.Now()
	m.fitChoices()
	if !msg.stale {
		return nil
	}
	tool, configDir := m.services.cfg.Tools[msg.tool], m.services.configDir
	return func() tea.Msg { return refreshCatalog(configDir, msg.tool, tool) }
}

func (m *Model) fitChoices() {
	if m.mode == modeForm {
		m.form.Choice().Refit(m.spawnHost(), m.form.Tool())
	}
	if m.quick.active {
		m.quick.Choice().Refit(m.spawnHost(), m.quick.Tool())
	}
}

func (m *Model) saveChoiceSetting(key, value string) {
	m.settings.cache.applyValues([]settingValue{{Key: key, Value: value}})
	m.enqueueEffect(choiceSaveRequest{key: key, value: value}, 0, false)
}

func (m *Model) choiceSettingKeys() []string {
	return choiceSettingKeys(slices.Sorted(maps.Keys(m.services.cfg.Tools)))
}

// choiceSettingKeys are the per-CLI keys the form and quick prompt read
// from the settings cache.
func choiceSettingKeys(tools []string) []string { return spawn.SettingKeys(tools) }

// refreshFormChoicePrefs and refreshQuickChoicePrefs take a newer read of
// the CLI's kept choice into an open choice.
func (m *Model) refreshFormChoicePrefs() {
	m.form.Choice().RefreshPrefs(m.spawnHost(), m.form.Tool())
}

func (m *Model) refreshQuickChoicePrefs() {
	m.quick.Choice().RefreshPrefs(m.spawnHost(), m.quick.Tool())
}

// capturedAbsolutePath normalizes a displayed path without consulting the
// filesystem. It uses the startup directory/home captured before the event
// loop began.
func (m *Model) capturedAbsolutePath(raw, fallback string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		path = fallback
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = filepath.Join(m.env.homeDir, strings.TrimPrefix(path, "~"))
	}
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(m.env.workDir, path)
	}
	return filepath.Clean(path)
}

// spawnDefaults are the root's launch inputs for a spawn into a group,
// read when the form or the quick bar builds its request.
type spawnDefaults struct {
	// base is the ref a worktree branches from, or "" to detect it.
	base string
	pane sessioncmd.PaneSize
	// groupDir is the group's own default path, and fallbacks the nearest
	// ancestor's and then the startup directory.
	groupDir  string
	fallbacks []string
	worktree  bool
}

func (m *Model) spawnDefaults(group string) spawnDefaults {
	paneW, paneH := m.paneTargetSize()
	return spawnDefaults{
		base:      m.groupBase(group),
		pane:      sessioncmd.PaneSize{Width: paneW, Height: paneH},
		groupDir:  m.workspace.groupPaths[group],
		fallbacks: m.groupDirCandidates(group),
		worktree:  m.cachedSpawnWorktreeDefault(group),
	}
}

// groupDirCandidates captures the same nearest-ancestor preference as the
// old synchronous lookup. Validation happens later inside the command.
func (m *Model) groupDirCandidates(group string) []string {
	candidates := make([]string, 0, 4)
	for current := group; current != ""; current = parentGroup(current) {
		if path := m.workspace.groupPaths[current]; path != "" {
			candidates = append(candidates, path)
		}
	}
	if m.env.workDir != "" {
		candidates = append(candidates, m.env.workDir)
	}
	return candidates
}

func (m *Model) capturedGroupDefaultDir(group string) string {
	candidates := m.groupDirCandidates(group)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func (m *Model) capturedAncestorGroupDir(group string) string {
	for current := group; current != ""; current = parentGroup(current) {
		if path := m.workspace.groupPaths[current]; path != "" {
			return path
		}
	}
	return ""
}

func (m *Model) cachedWorktreeCapability(dir string) (bool, bool) {
	answer, ok := m.ledger.worktreeRepos[dir]
	if !ok || answer.at.IsZero() || time.Since(answer.at) >= worktreeLookupTTL {
		return false, false
	}
	return answer.capable, true
}

func (m *Model) requestWorktreeProbe(target worktreeProbeTarget, generation int, dir string, toggle bool, from bool) tea.Cmd {
	m.gens.worktreeProbe++
	request := worktreeProbeRequest{
		target: target, generation: generation, sequence: m.gens.worktreeProbe,
		dir: dir, toggle: toggle, from: from,
	}
	return worktreeProbeCmd(request, systemDirectoryPreflight{git: m.services.gitDrv})
}

func (m *Model) formWorktreeProbeCmd(toggleOn bool) tea.Cmd {
	if m.form.remote.on() {
		return nil
	}
	return m.requestWorktreeProbe(worktreeProbeForm, m.form.prompt.gen, m.formSpawnDir(), toggleOn, m.form.Worktree())
}

func (m *Model) quickWorktreeProbeCmd(toggleOn bool) tea.Cmd {
	if _, remote := m.remoteTarget(); remote {
		return nil
	}
	return m.requestWorktreeProbe(worktreeProbeQuick, m.quick.gen, m.quickTargetDir(), toggleOn, m.quick.Worktree())
}

func (m *Model) handleWorktreeProbe(msg worktreeProbeMsg) (tea.Model, tea.Cmd) {
	request := msg.request
	if request.sequence != m.gens.worktreeProbe {
		return m, nil
	}
	switch request.target {
	case worktreeProbeForm:
		if m.mode != modeForm || m.form.prompt.gen != request.generation || m.formSpawnDir() != request.dir {
			return m, nil
		}
	case worktreeProbeQuick:
		if !m.quick.active || m.quick.gen != request.generation || m.quickTargetDir() != request.dir {
			return m, nil
		}
	default:
		return m, nil
	}
	if m.ledger.worktreeRepos == nil {
		m.ledger.worktreeRepos = make(map[string]repoAnswer)
	}
	m.ledger.worktreeRepos[request.dir] = repoAnswer{capable: msg.capable, at: time.Now()}
	if !request.toggle {
		return m, nil
	}
	if !msg.capable {
		m.reportErr("worktree sessions need a git repository: " + request.dir + " is not one")
		return m, nil
	}
	m.clearErr()
	if request.target == worktreeProbeForm {
		if m.form.Worktree() != request.from {
			return m, nil
		}
		m.form.SetWorktree(!request.from)
	} else {
		if m.quick.Worktree() != request.from {
			return m, nil
		}
		m.quick.SetWorktree(!request.from)
	}
	return m, nil
}
