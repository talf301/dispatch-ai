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
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/mux"
)

type mode int

const (
	modeBoard   mode = iota
	modeCapture      // g — inline dt go
	modeKill         // x — reason entry
	modeCommand      // : — fuzzy instruction entry
	modeConfirm      // reviewing a proposed dt batch
	modeDedup        // confirming similar closed work before capture
	modeBrief        // rendered what-changed digest
	modePromote      // u — acceptance entry
	modeUsage        // i — provider usage drill-down
	modeHelp         // ? — scrollable key reference
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
	usage          *usageView

	// Commit-time cache for staleness: workdir → HEAD commit time.
	// Refreshed every commitCacheTTL, not every 2s tick.
	commits   map[string]time.Time
	commitsAt time.Time
}

const commitCacheTTL = time.Minute

func New(store *db.DB, m mux.Mux, dtBin string) Model {
	ti := textarea.New()
	ti.CharLimit = 400
	ti.Prompt = "› "
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6adc8"))
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#89b4fa"))
	ti.FocusedStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#cdd6f4"))
	return Model{store: store, mux: m, dtBin: dtBin, input: ti}
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
type usageMsg struct {
	view *usageView
	err  error
}

type usageView struct {
	task  *db.UsageReport
	today db.UsageTotals
	week  db.UsageTotals
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
	case t.Status == "unattended":
		return laneUnattended
	case t.Status == "parked":
		return laneParked
	default:
		return laneClosed
	}
}

// runDT shells a mutation out to the dt binary itself: the TUI never writes
// SQLite directly.
func (m Model) runDT(verb string, args ...string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(m.dtBin, append([]string{verb}, args...)...).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%s: %s", verb, strings.TrimSpace(string(out)))
		}
		return dtDoneMsg{verb: verb, err: err}
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

	case usageMsg:
		if msg.err != nil {
			m.status = "Could not read usage: " + msg.err.Error()
			return m, nil
		}
		m.usage, m.mode = msg.view, modeUsage
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
		c.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
		out, err := c.CombinedOutput()
		if err != nil {
			err = fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
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
	case modeCapture, modeKill, modeCommand, modePromote:
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
			case modeCommand:
				m.busy = true
				return m, func() tea.Msg {
					cmds, err := propose(m.store, text)
					return proposalMsg{cmds: cmds, err: err}
				}
			default:
				m.pendingThought = text
				return m, m.runDT("go", text)
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
			return m, m.runDT("go", "--no-dedup", thought)
		case "n", "esc", "q":
			m.mode, m.pendingThought, m.status = modeBoard, "", "Not started."
		}
		return m, nil

	case modeBrief:
		m.mode = modeBoard
		return m, nil
	case modeUsage:
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
	case "i":
		if t, ok := m.current(); ok {
			return m, func() tea.Msg {
				report, err := m.store.Usage(t.ID)
				if err != nil {
					return usageMsg{err: err}
				}
				now := time.Now().UTC()
				// Today/week intentionally use UTC calendar boundaries for deterministic aggregates.
				today, err := m.store.UsageSince(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
				if err != nil {
					return usageMsg{err: err}
				}
				week, err := m.store.UsageSince(now.AddDate(0, 0, -6).Truncate(24 * time.Hour))
				if err != nil {
					return usageMsg{err: err}
				}
				return usageMsg{view: &usageView{task: report, today: totals(today), week: totals(week)}}
			}
		}
		return m, nil
	case "x":
		if t, ok := m.current(); ok {
			m.mode = modeKill
			m.targetID = t.ID
			m.input.Placeholder = "why kill " + t.ID + "?"
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
	if t.Status == "live" && t.Workdir != nil && t.Repo != nil {
		return m, m.runDT("resume", t.ID)
	}
	m.status = t.ID + " has no herdr tab."
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	laneStyle   = lipgloss.NewStyle().Bold(true).MarginTop(1)
	selStyle    = lipgloss.NewStyle().Reverse(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	alertStyle  = lipgloss.NewStyle().Bold(true)
	statusStyle = lipgloss.NewStyle().MarginTop(1).Faint(true)
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
	case modeUsage:
		return m.viewUsage()
	case modeHelp:
		return m.helpView()
	}

	var b strings.Builder
	idx := 0

	for _, l := range []lane{laneStale, laneNeedsYou, laneLive, laneUnattended} {
		rows := m.rows[l]
		if len(rows) == 0 {
			continue
		}
		b.WriteString(laneStyle.Render(fmt.Sprintf("%s (%d)", laneLabels[l], len(rows))) + "\n")
		for _, r := range rows {
			m.writeRow(&b, r, idx == m.cursor)
			idx++
		}
	}

	for _, l := range []lane{laneParked, laneClosed} {
		rows := m.rows[l]
		if len(rows) == 0 {
			continue
		}
		if !m.showAll {
			b.WriteString(laneStyle.Render(fmt.Sprintf("%s (%d)", laneLabels[l], len(rows))) +
				dimStyle.Render("  z to expand") + "\n")
			continue
		}
		b.WriteString(laneStyle.Render(fmt.Sprintf("%s (%d)", laneLabels[l], len(rows))) + "\n")
		for _, r := range rows {
			m.writeRow(&b, r, idx == m.cursor)
			idx++
		}
	}

	if idx == 0 && len(m.rows[laneParked]) == 0 && len(m.rows[laneClosed]) == 0 {
		b.WriteString(dimStyle.Render("Nothing in flight. Press g and type the thought.\n"))
	}

	if m.mode == modeCapture || m.mode == modeKill || m.mode == modeCommand || m.mode == modePromote {
		b.WriteString("\n" + m.input.View() + "\n")
	}
	if m.busy {
		b.WriteString(dimStyle.Render("\nThinking…\n"))
	}
	if m.status != "" {
		b.WriteString(statusStyle.Render(m.status) + "\n")
	}
	b.WriteString(dimStyle.Render("\n? help · a approve · d done · j/k move · ⏎ focus · i usage · g capture · : command · b brief · u promote · x kill · p park · r resume · z all · q quit"))
	return boundLines(b.String(), m.width)
}

func totals(r *db.UsageReport) db.UsageTotals {
	if r == nil {
		return db.UsageTotals{}
	}
	return r.Totals
}

func (m Model) viewUsage() string {
	if m.usage == nil || m.usage.task == nil || len(m.usage.task.Attempts) == 0 {
		return dimStyle.Render("No usage recorded.\n\nAny key to return.")
	}
	r := m.usage.task
	var b strings.Builder
	b.WriteString(laneStyle.Render("Usage · "+r.TaskID) + "\n")
	b.WriteString(dimStyle.Render("Provider-emitted values; duration is derived from timestamps. Missing values are not estimated.") + "\n\n")
	b.WriteString(fmt.Sprintf("Attempts %d  ·  raw input %s  ·  cached input %s  ·  output %s  ·  turns %s\n",
		r.Totals.Attempts, number(r.Totals.InputTokens), number(r.Totals.CachedInputTokens), number(r.Totals.OutputTokens), strconv.Itoa(r.Totals.Turns)))
	b.WriteString(fmt.Sprintf("Today (all tasks)  %s in / %s out / %d turns    Week (all tasks)  %s in / %s out / %d turns\n\n",
		number(m.usage.today.InputTokens), number(m.usage.today.OutputTokens), m.usage.today.Turns,
		number(m.usage.week.InputTokens), number(m.usage.week.OutputTokens), m.usage.week.Turns))
	for _, a := range r.Attempts {
		model := "unknown"
		if a.Model != nil && *a.Model != "" {
			model = *a.Model
		}
		waits := "not recorded"
		if a.WaitOnlyCount != nil {
			waits = strconv.Itoa(*a.WaitOnlyCount) + " (detected)"
		}
		b.WriteString(fmt.Sprintf("%s/%s/%s  %s  %s in · %s cached · %s out · %s turns · waits %s\n",
			a.Provider, model, a.Role, duration(a), numberPtr(a.InputTokens), numberPtr(a.CachedInputTokens),
			numberPtr(a.OutputTokens), numberPtr(a.TurnCount), waits))
	}
	b.WriteString(dimStyle.Render("Any key to return."))
	return boundLines(b.String(), m.width)
}

func number(n int64) string { return strconv.FormatInt(n, 10) }

func numberPtr[T int64 | int](p *T) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

func duration(a db.Attempt) string {
	if a.EndedAt == nil {
		return "running"
	}
	start, e1 := time.Parse(time.RFC3339Nano, a.StartedAt)
	end, e2 := time.Parse(time.RFC3339Nano, *a.EndedAt)
	if e1 != nil || e2 != nil || end.Before(start) {
		return "duration ?"
	}
	return "duration " + end.Sub(start).Round(time.Millisecond).String()
}

func boundLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) > width {
			line = ansi.Truncate(line, width, "…")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
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

// writeRow renders one task line; the focused row reveals the verbatim
// thought — the label is a display cache, never authoritative.
func (m Model) writeRow(b *strings.Builder, r row, focused bool) {
	t := r.task
	label := t.Title
	if t.Label != nil && *t.Label != "" {
		label = *t.Label
	}
	badge := r.agent
	switch {
	case r.badge != "":
		badge = alertStyle.Render(r.badge)
	case t.Status == "proposed":
		badge = alertStyle.Render("proposed") + dimStyle.Render(" · a approve")
	case t.Status == "killed":
		badge = "killed"
	case t.Status == "done":
		badge = "done"
	case r.agent == "done":
		badge = "done ✓ awaiting your call"
	case r.agent == "blocked":
		badge = alertStyle.Render("blocked")
	case t.Status == "blocked" && t.BlockReason != nil:
		badge = alertStyle.Render("blocked")
	}
	line := fmt.Sprintf("  %s  %-36s %s", t.ID, truncate(label, 36), badge)
	if focused {
		line = selStyle.Render(line)
	}
	b.WriteString(line + "\n")

	if focused {
		if t.Thought != "" {
			b.WriteString(dimStyle.Render(fmt.Sprintf("        %q", t.Thought)) + "\n")
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
			b.WriteString(dimStyle.Render("        "+strings.Join(meta, " · ")) + "\n")
		}
	}
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
func Run(store *db.DB, m mux.Mux, dtBin string) error {
	_, err := tea.NewProgram(New(store, m, dtBin), tea.WithAltScreen()).Run()
	return err
}
