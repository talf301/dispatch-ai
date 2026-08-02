// Package tui is the dispatch board: one visual surface for everything in
// flight. M0 rules: it renders deterministically from SQLite plus live herdr
// agent state — no model calls anywhere.
//
//   - SQLite is polled on a 2s tick (free, local).
//   - herdr state is an attention signal, never a correctness gate (I3).
//   - Mutations shell out to `dt` — same enforcement path as a human.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/mux"
)

type mode int

const (
	modeBoard      mode = iota
	modeCapture         // g - inline dt go
	modeKill            // x - reason entry
	modeCommand         // : - fuzzy instruction entry
	modeConfirm         // reviewing a proposed dt batch
	modeDedup           // confirming similar closed work before capture
	modeBrief           // rendered what-changed digest
	modePromote         // u - acceptance entry
	modeRename          // e - rename selected task
	modeHelp            // ? - scrollable key reference
	modeRepoSelect      // choose repo before g capture
)

type lane int

const (
	laneStale lane = iota
	laneNeedsYou
	laneLive
	laneUnattended
	laneParked
	laneClosed
)

var laneLabels = map[lane]string{
	laneStale:      "Stale · resume or kill",
	laneNeedsYou:   "Needs you",
	laneLive:       "Live now",
	laneUnattended: "Unattended",
	laneParked:     "Parked",
	laneClosed:     "Closed this week",
}

type row struct {
	task  db.Task
	agent string // herdr agent state for the task's pane, "" if none
	badge string // lane-specific badge override (e.g. "idle 6d")
}

type Model struct {
	store *db.DB
	mux   mux.Mux

	mode           mode
	repos          []string
	repoCursor     int
	captureRepo    string
	rows           map[lane][]row
	flat           []row // selectable rows in lane order
	cursor         int
	showAll        bool // expand parked/closed lanes
	input          textarea.Model
	targetID       string
	status         string
	dtBin          string // path to the dt binary (os.Executable)
	width          int
	height         int
	busy           bool     // a model call is in flight
	proposal       []string // dt batch lines awaiting confirmation
	pendingThought string
	brief          string // rendered digest
	helpAt         int    // first visible line in the help view

	// Commit-time cache for staleness: workdir → HEAD commit time.
	// Refreshed every commitCacheTTL, not every 2s tick.
	commits   map[string]time.Time
	commitsAt time.Time
}

const commitCacheTTL = time.Minute

func New(store *db.DB, m mux.Mux, dtBin string, repos []string, currentRepo string) Model {
	ti := textarea.New()
	ti.CharLimit = 400
	ti.Prompt = "› "
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8"))
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	ti.FocusedStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
	cursor := 0
	for i, repo := range repos {
		if repo == currentRepo {
			cursor = i
			break
		}
	}
	return Model{store: store, mux: m, dtBin: dtBin, input: ti, repos: repos, repoCursor: cursor}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tickMsg time.Time
type boardMsg struct {
	rows      map[lane][]row
	commits   map[string]time.Time
	commitsAt time.Time
	err       error
}
type dtDoneMsg struct {
	verb string
	err  error
}
type proposalMsg struct {
	cmds []string
	err  error
}
type briefMsg struct {
	text string
	err  error
}
type batchDoneMsg struct {
	n   int
	err error
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) refresh() tea.Cmd {
	commits, commitsAt := m.commits, m.commitsAt
	return func() tea.Msg {
		tasks, err := m.store.BoardTasks()
		if err != nil {
			return boardMsg{err: err}
		}
		// ponytail: herdr state is fetched by shelling `herdr agent list`
		// each tick; switch to a socket subscription if 1 proc/2s ever hurts.
		states, err := m.mux.AgentStates()
		if err != nil {
			states = nil // herdr down: board still renders from SQLite
		}

		now := time.Now()
		if now.Sub(commitsAt) > commitCacheTTL {
			commits = make(map[string]time.Time)
			for _, t := range tasks {
				if t.Status == "live" && t.Workdir != nil {
					commits[*t.Workdir] = lastCommitTime(*t.Workdir)
				}
			}
			commitsAt = now
		}

		threshold := staleAfter()
		rows := make(map[lane][]row)
		for _, t := range tasks {
			agent := ""
			if t.HerdrPane != nil {
				agent = states[*t.HerdrPane]
			}
			var commit time.Time
			if t.Workdir != nil {
				commit = commits[*t.Workdir]
			}
			r := row{task: t, agent: agent}
			l := classify(t, agent)
			if l == laneLive && isStale(t, agent, commit, now, threshold) {
				l = laneStale
				r.badge = staleDays(t, commit, now)
			}
			rows[l] = append(rows[l], r)
		}
		return boardMsg{rows: rows, commits: commits, commitsAt: commitsAt}
	}
}

// classify picks the lane. Lane order encodes urgency, not lifecycle.
func classify(t db.Task, agent string) lane {
	switch {
	// Proposed work needs a human call (approve or kill) just like blocked.
	case t.Status == "blocked" || t.Status == "proposed" || agent == "blocked":
		return laneNeedsYou
	case t.Status == "live":
		return laneLive
	case t.Status == "open" || t.Status == "active" || t.Status == "unattended":
		return laneUnattended
	case t.Status == "parked":
		return laneParked
	default:
		return laneClosed
	}
}

// runDT shells a mutation out to the dt binary itself: the TUI never writes
// SQLite directly.
func (m Model) runDT(command ...string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(m.dtBin, command...).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%s: %s", command[0], strings.TrimSpace(string(out)))
		}
		auditCommand(command, "", err)
		return dtDoneMsg{verb: command[0], err: err}
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(1, msg.Width-2))
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())

	case boardMsg:
		if msg.err != nil {
			m.status = "Could not read the ledger: " + msg.err.Error()
			return m, nil
		}
		m.rows = msg.rows
		m.commits, m.commitsAt = msg.commits, msg.commitsAt
		m.flat = m.selectable()
		if m.cursor >= len(m.flat) {
			m.cursor = max(0, len(m.flat)-1)
		}
		return m, nil

	case dtDoneMsg:
		if msg.err != nil {
			if msg.verb == "go" && strings.Contains(strings.ToLower(msg.err.Error()), "similar closed work") {
				m.mode = modeDedup
				m.status = msg.err.Error() + "\nStart anyway? y/N"
				return m, nil
			}
			m.status = msg.err.Error()
		} else {
			m.status = msg.verb + " done."
			if msg.verb == "go" {
				m.pendingThought = ""
			}
		}
		return m, m.refresh()

	case proposalMsg:
		m.busy = false
		if msg.err != nil {
			m.mode, m.status = modeBoard, msg.err.Error()
			return m, nil
		}
		m.proposal, m.mode = msg.cmds, modeConfirm
		return m, nil

	case briefMsg:
		m.busy = false
		if msg.err != nil {
			m.mode, m.status = modeBoard, msg.err.Error()
			return m, nil
		}
		m.brief, m.mode = msg.text, modeBrief
		m.store.MarkSeen(time.Now())
		return m, nil

	case batchDoneMsg:
		m.busy, m.mode, m.proposal = false, modeBoard, nil
		if msg.err != nil {
			m.status = "Batch failed, nothing applied: " + msg.err.Error()
		} else {
			m.status = fmt.Sprintf("Applied %d commands.", msg.n)
		}
		return m, m.refresh()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// runBatch pipes approved commands into `dt batch` on stdin: the whole set
// applies atomically or not at all, through the same enforcement path as a
// human at the shell (I4).
func (m Model) runBatch(cmds []string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command(m.dtBin, "batch")
		input := strings.Join(cmds, "\n") + "\n"
		c.Stdin = strings.NewReader(input)
		out, err := c.CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		auditCommand([]string{"batch"}, input, err)
		return batchDoneMsg{n: len(cmds), err: err}
	}
}

// selectable returns rows in display order. Parked and closed rows are only
// selectable when expanded.
func (m Model) selectable() []row {
	lanes := []lane{laneStale, laneNeedsYou, laneLive, laneUnattended}
	if m.showAll {
		lanes = append(lanes, laneParked, laneClosed)
	}
	var out []row
	for _, l := range lanes {
		out = append(out, m.rows[l]...)
	}
	return out
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeRepoSelect:
		switch msg.String() {
		case "esc":
			m.mode = modeBoard
		case "j", "down":
			m.repoCursor = min(m.repoCursor+1, len(m.repos)-1)
		case "k", "up":
			m.repoCursor = max(m.repoCursor-1, 0)
		case "enter":
			if len(m.repos) > 0 {
				m.captureRepo = m.repos[m.repoCursor]
				m.mode = modeCapture
				m.input.Placeholder = "what's the thought?"
				m.input.Focus()
				return m, m.input.Focus()
			}
		}
		return m, nil
	case modeCapture, modeKill, modeCommand, modePromote, modeRename:
		switch msg.String() {
		case "esc":
			m.mode = modeBoard
			m.input.SetValue("")
			m.input.Blur()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			m.input.Blur()
			was := m.mode
			m.mode = modeBoard
			if text == "" {
				return m, nil
			}
			switch was {
			case modeKill:
				return m, m.runDT("kill", m.targetID, text)
			case modePromote:
				kind, accept, ok := strings.Cut(text, " ")
				if !ok || (kind != "report" && kind != "ratchet") {
					m.status = "Promote needs: report <condition>  or  ratchet <command>"
					return m, nil
				}
				return m, m.runDT("promote", m.targetID, "-k", kind, "-a", accept)
			case modeRename:
				return m, m.runDT("relabel", m.targetID, text)
			case modeCommand:
				m.busy = true
				return m, func() tea.Msg {
					cmds, err := propose(m.store, text)
					return proposalMsg{cmds: cmds, err: err}
				}
			default:
				m.pendingThought = text
				return m, m.runDT(m.goVerbArgs("go", text)...)
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case modeConfirm:
		switch msg.String() {
		case "y", "enter":
			m.busy = true
			return m, m.runBatch(m.proposal)
		case "n", "esc", "q":
			m.mode, m.proposal, m.status = modeBoard, nil, "Discarded."
		}
		return m, nil

	case modeDedup:
		switch msg.String() {
		case "y", "enter":
			thought := m.pendingThought
			m.mode = modeBoard
			m.status = "Starting despite similar closed work."
			return m, m.runDT(m.goVerbArgs("go", "--no-dedup", thought)...)
		case "n", "esc", "q":
			m.mode, m.pendingThought, m.status = modeBoard, "", "Not started."
		}
		return m, nil

	case modeBrief:
		m.mode = modeBoard
		return m, nil
	case modeHelp:
		switch msg.String() {
		case "esc", "q", "?":
			m.mode = modeBoard
		case "j", "down":
			m.helpAt++
		case "k", "up":
			m.helpAt = max(0, m.helpAt-1)
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.mode, m.helpAt = modeHelp, 0
		return m, nil
	case "j", "down":
		if len(m.flat) > 0 {
			m.cursor = min(m.cursor+1, len(m.flat)-1)
		}
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "z":
		m.showAll = !m.showAll
		m.flat = m.selectable()
		if m.cursor >= len(m.flat) {
			m.cursor = max(0, len(m.flat)-1)
		}
	case "g":
		if len(m.repos) > 1 {
			m.mode = modeRepoSelect
			return m, nil
		}
		m.captureRepo = ""
		if len(m.repos) == 1 {
			m.captureRepo = m.repos[0]
		}
		m.mode = modeCapture
		m.input.Placeholder = "what's the thought?"
		m.input.Focus()
		return m, m.input.Focus()
	case ":":
		m.mode = modeCommand
		m.input.Placeholder = "e.g. park everything in sc-api until monday"
		m.input.Focus()
		return m, m.input.Focus()
	case "b":
		m.busy = true
		return m, func() tea.Msg {
			text, err := brief(m.store)
			return briefMsg{text: text, err: err}
		}
	case "x":
		if t, ok := m.current(); ok {
			m.mode = modeKill
			m.targetID = t.ID
			m.input.Placeholder = "why kill " + t.ID + "?"
			m.input.Focus()
			return m, m.input.Focus()
		}
	case "e":
		if t, ok := m.current(); ok {
			m.mode = modeRename
			m.targetID = t.ID
			label := t.Title
			if t.Label != nil && *t.Label != "" {
				label = *t.Label
			}
			m.input.Placeholder = "new task name"
			m.input.SetValue(label)
			m.input.Focus()
			return m, m.input.Focus()
		}
	case "p":
		if t, ok := m.current(); ok {
			return m, m.runDT("park", t.ID)
		}
	case "a":
		if t, ok := m.current(); ok {
			if t.Status != "proposed" {
				m.status = t.ID + " is " + t.Status + "; only proposed tasks approve."
				return m, nil
			}
			return m, m.runDT("reopen", t.ID)
		}
	case "d":
		if t, ok := m.current(); ok {
			return m, m.runDT("done", t.ID)
		}
	case "u":
		if t, ok := m.current(); ok {
			if t.Status != "live" {
				m.status = t.ID + " is " + t.Status + "; only live tasks promote."
				return m, nil
			}
			m.mode = modePromote
			m.targetID = t.ID
			m.input.Placeholder = "report <when is it done?>  or  ratchet <command that must exit 0>"
			m.input.Focus()
			return m, m.input.Focus()
		}
	case "r":
		if t, ok := m.current(); ok {
			return m, m.runDT("resume", t.ID)
		}
	case "enter":
		return m.focusCurrent()
	}
	return m, nil
}

func (m Model) goVerbArgs(verb string, args ...string) []string {
	if m.captureRepo == "" {
		return append([]string{verb}, args...)
	}
	return append([]string{verb, "--repo", m.captureRepo}, args...)
}

func (m Model) current() (db.Task, bool) {
	if m.cursor < len(m.flat) {
		return m.flat[m.cursor].task, true
	}
	return db.Task{}, false
}

// focusCurrent jumps to the task's herdr tab. Attach is focus, not handoff:
// the board stays alive in its own pane. A live task whose tab has vanished
// (herdr restarted, tab closed by hand) gets a fresh one resuming the
// conversation — this is how a stale task comes back.
func (m Model) focusCurrent() (tea.Model, tea.Cmd) {
	t, ok := m.current()
	if !ok {
		return m, nil
	}
	if t.HerdrTab != nil && *t.HerdrTab != "" {
		if err := m.mux.FocusTab(*t.HerdrTab); err == nil {
			m.status = "Focused " + t.ID + "."
			return m, nil
		}
	}
	if (t.Status == "live" || t.Status == "active") && t.Workdir != nil && t.Repo != nil {
		return m, m.runDT("resume", t.ID)
	}
	m.status = t.ID + " has no herdr tab."
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

// Catppuccin Frappé keeps the board legible on both dark terminal themes and
// long-running sessions without adding a theme framework.
var (
	base        = lipgloss.Color("#303446")
	surface     = lipgloss.Color("#414559")
	overlay     = lipgloss.Color("#737994")
	text        = lipgloss.Color("#c6d0f5")
	subtext     = lipgloss.Color("#a5adce")
	blue        = lipgloss.Color("#8caaee")
	green       = lipgloss.Color("#a6d189")
	yellow      = lipgloss.Color("#e5c890")
	red         = lipgloss.Color("#e78284")
	mauve       = lipgloss.Color("#ca9ee6")
	laneStyle   = lipgloss.NewStyle().Bold(true).Foreground(text)
	selStyle    = lipgloss.NewStyle().Foreground(base).Background(blue).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(subtext)
	alertStyle  = lipgloss.NewStyle().Bold(true).Foreground(red)
	statusStyle = lipgloss.NewStyle().MarginTop(1).Foreground(subtext)
	cardStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(surface).Padding(0, 1)
)

func (m Model) View() string {
	switch m.mode {
	case modeConfirm:
		var b strings.Builder
		b.WriteString(laneStyle.Render("Apply these?") + "\n\n")
		for _, c := range m.proposal {
			b.WriteString("  dt " + c + "\n")
		}
		b.WriteString(dimStyle.Render("\nRuns as one atomic dt batch.\ny apply · n discard"))
		return b.String()
	case modeBrief:
		return m.brief + dimStyle.Render("\n\nAny key to return.")
	case modeHelp:
		return m.helpView()
	case modeRepoSelect:
		return m.repoSelectView()
	}

	width := m.width
	if width < 48 {
		width = 96
	}
	idx := 0
	leftWidth := width
	rightWidth := width
	if width >= 96 {
		rightWidth = 40
		leftWidth = width - rightWidth - 1
	}
	left := m.renderBoard([]lane{laneStale, laneNeedsYou, laneLive}, &idx, leftWidth, "Your work")
	auto := m.renderBoard([]lane{laneUnattended}, &idx, rightWidth, "Automation")
	board := left
	if width >= 96 {
		board = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", auto)
	} else {
		board = left + "\n" + auto
	}

	var b strings.Builder
	header := lipgloss.NewStyle().Bold(true).Foreground(blue).Render("dispatch") +
		dimStyle.Render("  task ledger")
	b.WriteString(header + "\n" + board + "\n")

	for _, l := range []lane{laneParked, laneClosed} {
		rows := m.rows[l]
		if len(rows) == 0 {
			continue
		}
		if !m.showAll {
			b.WriteString(dimStyle.Render(fmt.Sprintf("%s (%d) · z to expand\n", laneLabels[l], len(rows))))
			continue
		}
		b.WriteString(m.renderBoard([]lane{l}, &idx, width, laneLabels[l]) + "\n")
	}

	if idx == 0 && len(m.rows[laneParked]) == 0 && len(m.rows[laneClosed]) == 0 {
		b.WriteString(dimStyle.Render("Nothing in flight. Press g and type the thought.\n"))
	}

	if m.mode == modeCapture || m.mode == modeKill || m.mode == modeCommand || m.mode == modePromote || m.mode == modeRename {
		b.WriteString("\n" + m.input.View() + "\n")
	}
	if m.busy {
		b.WriteString(dimStyle.Render("\nThinking…\n"))
	}
	if m.status != "" {
		b.WriteString(statusStyle.Render(m.status) + "\n")
	}
	b.WriteString(dimStyle.Render("\n? help · a approve · d done · e rename · j/k move · ⏎ open activity · g capture · : command · q quit"))
	return b.String()
}

func (m Model) renderBoard(lanes []lane, idx *int, width int, title string) string {
	if width < 40 {
		width = 40
	}
	var b strings.Builder
	for _, l := range lanes {
		rows := m.rows[l]
		if len(rows) == 0 {
			continue
		}
		b.WriteString(laneStyle.Render(fmt.Sprintf("%s  %d", laneLabels[l], len(rows))) + "\n")
		for _, r := range rows {
			m.writeRow(&b, r, *idx == m.cursor, width-4)
			*idx++
		}
	}
	if b.Len() == 0 {
		b.WriteString(dimStyle.Render("No tasks here."))
	}
	return cardStyle.Copy().Width(width).Render(lipgloss.NewStyle().Bold(true).Foreground(mauve).Render(title) + "\n" + b.String())
}

func (m Model) helpView() string {
	lines := []string{
		"Dispatch board shortcuts",
		"",
		"j / down     move selection down",
		"k / up       move selection up",
		"enter        focus or resume selected task",
		"a            approve selected proposed task",
		"d            mark selected task done",
		"e            rename selected task",
		"g            capture a new thought",
		"u            promote a live task",
		"x            kill selected task",
		"p            park selected task",
		"r            resume selected task",
		":            natural-language batch command",
		"b            show the change brief",
		"z            expand or collapse parked/closed",
		"q            quit",
		"",
		"j/k scroll · esc or ? return to board",
	}
	visible := m.height - 2
	if visible < 1 {
		visible = 1
	}
	if m.helpAt > len(lines)-visible {
		m.helpAt = max(0, len(lines)-visible)
	}
	end := min(len(lines), m.helpAt+visible)
	return strings.Join(lines[m.helpAt:end], "\n") + "\n" + dimStyle.Render("? help · esc back")
}

func (m Model) repoSelectView() string {
	var b strings.Builder
	b.WriteString(laneStyle.Render("Choose repository") + "\n\n")
	for i, repo := range m.repos {
		line := "  " + shortPath(repo)
		if i == m.repoCursor {
			line = selStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(dimStyle.Render("\nj/k move · enter select · esc cancel"))
	return cardStyle.Copy().Width(max(48, m.width-4)).Render(b.String())
}

// writeRow renders one task line; the focused row reveals the verbatim
// thought — the label is a display cache, never authoritative.
func (m Model) writeRow(b *strings.Builder, r row, focused bool, width int) {
	t := r.task
	label := t.Title
	if t.Label != nil && *t.Label != "" {
		label = *t.Label
	}
	badge := m.taskBadge(r)
	badgeWidth := lipgloss.Width(badge)
	labelWidth := max(10, width-len(t.ID)-badgeWidth-6)
	label = truncate(label, labelWidth)
	line := t.ID + "  " + label
	padding := max(1, width-lipgloss.Width(line)-badgeWidth)
	line += strings.Repeat(" ", padding) + badge
	if focused {
		line = selStyle.Render(line)
	}
	b.WriteString(line + "\n")

	if focused {
		if t.Thought != "" {
			b.WriteString(dimStyle.Render("  "+truncate(fmt.Sprintf("%q", t.Thought), max(12, width-2))) + "\n")
		}
		var meta []string
		if t.Repo != nil {
			meta = append(meta, shortPath(*t.Repo))
		}
		if t.Mode != nil {
			meta = append(meta, *t.Mode)
		}
		if t.Status == "blocked" && t.BlockReason != nil {
			meta = append(meta, *t.BlockReason)
		}
		if t.KillReason != nil {
			meta = append(meta, "killed: "+*t.KillReason)
		}
		if len(meta) > 0 {
			b.WriteString(dimStyle.Render("  "+truncate(strings.Join(meta, " · "), max(12, width-2))) + "\n")
		}
	}
}

func (m Model) taskBadge(r row) string {
	t := r.task
	badge := r.agent
	switch {
	case r.badge != "":
		return alertStyle.Render(r.badge)
	case t.Status == "proposed":
		return lipgloss.NewStyle().Foreground(yellow).Render("proposed · a approve")
	case t.Status == "killed":
		return dimStyle.Render("killed")
	case t.Status == "done":
		return lipgloss.NewStyle().Foreground(green).Render("done")
	case r.agent == "done":
		return lipgloss.NewStyle().Foreground(yellow).Render("waiting")
	case r.agent == "blocked":
		return alertStyle.Render("blocked")
	case t.Status == "blocked" && t.BlockReason != nil:
		return alertStyle.Render("blocked")
	case t.Status == "unattended" && t.Reviewing:
		return lipgloss.NewStyle().Foreground(mauve).Render(fmt.Sprintf("under review · %d", t.RejectCount+1))
	case t.Status == "unattended" && r.agent == "working":
		return lipgloss.NewStyle().Foreground(blue).Render("working")
	case t.Status == "unattended":
		if t.RejectCount > 0 {
			return lipgloss.NewStyle().Foreground(yellow).Render(fmt.Sprintf("waiting · %d retries", t.RejectCount))
		}
		return lipgloss.NewStyle().Foreground(yellow).Render("waiting")
	case r.agent == "working":
		return lipgloss.NewStyle().Foreground(blue).Render("working")
	}
	if badge == "" {
		return dimStyle.Render("waiting")
	}
	return dimStyle.Render(badge)
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Run starts the board.
func Run(store *db.DB, m mux.Mux, dtBin string, repos []string, currentRepo string) error {
	_, err := tea.NewProgram(New(store, m, dtBin, repos, currentRepo), tea.WithAltScreen()).Run()
	return err
}
