// Package dashboard is an optional terminal view over the same store and engine
// used by the CLI. Opening it never dispatches work or changes schedules.
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lmuehleisen/maintainer/internal/maintainer"
)

type item struct {
	Task  maintainer.Task
	State maintainer.TaskState
	Runs  []maintainer.Run
}
type snapshot struct {
	Items      []item
	Err        error
	Generation int
}
type refreshMsg struct{}
type actionDone struct {
	Message string
	Err     error
}
type confirmation struct {
	Kind, Task, Run, Label string
	Paused                 bool
}

type model struct {
	store                      *maintainer.Store
	engine                     *maintainer.Engine
	ctx                        context.Context
	cancel                     context.CancelFunc
	operations                 sync.WaitGroup
	items                      []item
	selected, runIndex, scroll int
	page                       string
	width, height              int
	generation                 int
	loaded, busy, quitting     bool
	pending                    *confirmation
	message                    string
	err                        error
}

func Run(ctx context.Context, store *maintainer.Store, engine *maintainer.Engine) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(runCtx, cancel, store, engine)
	// Bubble Tea handles Ctrl+C as a key; external cancellation still terminates
	// the agent through the engine's context and subprocess-group cancellation.
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	cancel()
	m.operations.Wait()
	return err
}

func newModel(ctx context.Context, cancel context.CancelFunc, s *maintainer.Store, e *maintainer.Engine) *model {
	return &model{store: s, engine: e, ctx: ctx, cancel: cancel, page: "tasks", width: 100, height: 28}
}
func tick() tea.Cmd            { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return refreshMsg{} }) }
func (m *model) Init() tea.Cmd { return tea.Batch(m.load(), tick()) }
func (m *model) load() tea.Cmd {
	m.generation++
	generation := m.generation
	return func() tea.Msg {
		result := snapshot{Generation: generation}
		tasks, err := m.store.Tasks()
		if err != nil {
			result.Err = err
			return result
		}
		runs, err := m.store.Runs("")
		if err != nil {
			result.Err = err
			return result
		}
		for _, task := range tasks {
			state, err := m.store.State(task)
			if err != nil {
				result.Err = err
				return result
			}
			it := item{Task: task, State: state}
			for i := len(runs) - 1; i >= 0; i-- {
				if runs[i].Task == task.Name {
					it.Runs = append(it.Runs, runs[i])
				}
			}
			result.Items = append(result.Items, it)
		}
		return result
	}
}
func (m *model) current() *item {
	if m.selected < 0 || m.selected >= len(m.items) {
		return nil
	}
	return &m.items[m.selected]
}
func (m *model) currentRun() *maintainer.Run {
	it := m.current()
	if it == nil || m.runIndex < 0 || m.runIndex >= len(it.Runs) {
		return nil
	}
	return &it.Runs[m.runIndex]
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case refreshMsg:
		return m, tea.Batch(m.load(), tick())
	case snapshot:
		if msg.Generation != m.generation {
			return m, nil
		}
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		taskName, runID := "", ""
		if it := m.current(); it != nil {
			taskName = it.Task.Name
		}
		if r := m.currentRun(); r != nil {
			runID = r.ID
		}
		m.items = msg.Items
		m.loaded = true
		m.err = nil
		m.selected = 0
		for i, it := range m.items {
			if it.Task.Name == taskName {
				m.selected = i
				break
			}
		}
		m.runIndex = 0
		if it := m.current(); it != nil {
			for i, r := range it.Runs {
				if r.ID == runID {
					m.runIndex = i
					break
				}
			}
		}
	case actionDone:
		m.busy = false
		if m.pending != nil && m.pending.Kind == "quit" {
			m.pending = nil
		}
		m.message = msg.Message
		if msg.Err != nil {
			m.message = msg.Err.Error()
		}
		if m.quitting {
			return m, tea.Quit
		}
		return m, m.load()
	case tea.KeyPressMsg:
		key := msg.String()
		if m.pending != nil {
			switch key {
			case "y", "Y":
				p := *m.pending
				m.pending = nil
				return m, m.perform(p)
			case "n", "N", "esc", "q", "ctrl+c":
				m.pending = nil
			}
			return m, nil
		}
		switch key {
		case "q", "ctrl+c":
			if m.busy {
				m.pending = &confirmation{Kind: "quit", Label: "Cancel this dashboard's active operation and quit?"}
				return m, nil
			}
			return m, tea.Quit
		case "esc":
			if m.page == "run" {
				m.page = "history"
			} else {
				m.page = "tasks"
			}
			m.scroll = 0
		case "ctrl+r":
			return m, m.load()
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgdown":
			if m.page == "run" {
				m.scroll += max(1, m.height-10)
			}
		case "pgup":
			if m.page == "run" {
				m.scroll = max(0, m.scroll-max(1, m.height-10))
			}
		case "home", "g":
			if m.page == "run" {
				m.scroll = 0
			} else if m.page == "tasks" {
				m.selected = 0
			} else {
				m.runIndex = 0
			}
		case "enter":
			if m.current() == nil {
				return m, nil
			}
			if m.page == "tasks" {
				m.page = "history"
				m.runIndex = 0
			} else if m.page == "history" && m.currentRun() != nil {
				m.page = "run"
				m.scroll = 0
			}
		case "r", "p", "a", "x":
			if m.busy || m.err != nil {
				return m, nil
			}
			it := m.current()
			if it == nil {
				return m, nil
			}
			switch key {
			case "r":
				m.pending = &confirmation{Kind: "run", Task: it.Task.Name, Label: fmt.Sprintf("Run %s now (%s, %s)?", it.Task.Name, it.Task.Driver, it.Task.Authority)}
			case "p":
				verb := "Pause"
				if it.Task.Paused {
					verb = "Resume"
				}
				m.pending = &confirmation{Kind: "pause", Task: it.Task.Name, Paused: !it.Task.Paused, Label: verb + " " + it.Task.Name + "?"}
			case "a", "x":
				r := m.currentRun()
				if m.page != "run" || r == nil || !awaiting(*r) {
					m.message = "Open a pending run with Enter to review its plan first."
					return m, nil
				}
				if key == "a" {
					if err := maintainer.UnsafePlan(*r); err != nil {
						m.message = err.Error()
						return m, nil
					}
				}
				kind, label := "approve", "Execute this reviewed plan with write authority?"
				if key == "x" {
					kind = "reject"
					label = "Reject this proposal and unblock its task?"
				}
				m.pending = &confirmation{Kind: kind, Task: it.Task.Name, Run: r.ID, Label: label}
			}
		}
	}
	return m, nil
}
func (m *model) move(delta int) {
	switch m.page {
	case "tasks":
		m.selected = max(0, min(len(m.items)-1, m.selected+delta))
		m.runIndex = 0
	case "history":
		if it := m.current(); it != nil {
			m.runIndex = max(0, min(len(it.Runs)-1, m.runIndex+delta))
		}
	case "run":
		m.scroll = max(0, m.scroll+delta)
	}
}
func (m *model) perform(p confirmation) tea.Cmd {
	if p.Kind == "quit" {
		m.quitting = true
		m.cancel()
		m.message = "Cancelling; waiting for the run to be recorded…"
		return nil
	}
	m.busy = true
	m.message = p.Label
	done := make(chan tea.Msg, 1)
	m.operations.Add(1)
	go func() {
		defer m.operations.Done()
		var err error
		message := ""
		switch p.Kind {
		case "run", "approve":
			var run maintainer.Run
			if p.Kind == "run" {
				run, err = m.engine.Run(m.ctx, p.Task)
			} else {
				run, err = m.engine.Approve(m.ctx, p.Run)
			}
			if run.ID != "" {
				message = run.Task + ": " + run.Result.Outcome + " · " + run.ID
			}
		case "pause":
			err = m.engine.SetPaused(p.Task, p.Paused)
			message = p.Task + " updated"
		case "reject":
			err = m.engine.Reject(p.Run)
			message = "Proposal rejected"
		}
		done <- actionDone{Message: message, Err: err}
	}()
	return func() tea.Msg { return <-done }
}
func awaiting(r maintainer.Run) bool {
	return r.Status == "completed" && r.Result.Outcome == "approval_required" && r.ConsumedBy == "" && !r.Rejected
}
func runStatus(r maintainer.Run) string {
	if r.Rejected {
		return "rejected"
	}
	if r.ConsumedBy != "" {
		return "approved"
	}
	if r.Status != "completed" {
		return r.Status
	}
	return r.Result.Outcome
}
func taskStatus(it item) string {
	if it.Task.Paused {
		return "paused"
	}
	for _, r := range it.Runs {
		if awaiting(r) {
			return "approval_required"
		}
	}
	if len(it.Runs) == 0 {
		return "never run"
	}
	return runStatus(it.Runs[0])
}

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#B4A1FF"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B929F"))
var selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#413560"))

func badge(s string) string {
	color := "#8B929F"
	switch s {
	case "healthy", "maintained":
		color = "#7DD3AC"
	case "approval_required", "action_required":
		color = "#F4CE7B"
	case "failed", "interrupted":
		color = "#FF8B94"
	case "running":
		color = "#8DC7FF"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.ReplaceAll(s, "_", " "))
}

func short(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(maintainer.TerminalSafe(s), "\n", " "), max(1, width), "…")
}
func date(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("Jan 02, 15:04 MST")
}
func box(content string, width, height int) string {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#454052")).Width(max(1, width-2)).Height(max(1, height-2)).MaxHeight(height).MaxWidth(width).Render(content)
}

func (m *model) View() tea.View {
	if m.width < 50 || m.height < 16 {
		v := tea.NewView("Maintainer\nResize to at least 50 columns × 16 rows.\nq quit")
		v.AltScreen = true
		return v
	}
	bodyHeight := m.height - 7
	header := titleStyle.Render("MAINTAINER") + muted.Render("  /  responsibilities, kept up")
	healthy, attention, running := 0, 0, 0
	for _, it := range m.items {
		switch taskStatus(it) {
		case "healthy", "maintained":
			healthy++
		case "action_required", "approval_required", "failed", "interrupted":
			attention++
		case "running":
			running++
		}
	}
	sub := fmt.Sprintf("%d tasks   %d healthy   %d need attention   %d running", len(m.items), healthy, attention, running)
	body := ""
	switch {
	case !m.loaded:
		body = box("Loading responsibilities…", m.width, bodyHeight)
	case len(m.items) == 0:
		body = box("No responsibilities yet.\n\nCreate one with: maintain add\nThis dashboard refreshes automatically.", m.width, bodyHeight)
	case m.page == "tasks":
		body = m.tasksView(bodyHeight)
	case m.page == "history":
		body = m.historyView(bodyHeight)
	case m.page == "run":
		body = m.runView(bodyHeight)
	}
	help := "↑↓ select · enter history · r run · p pause/resume · ctrl+r refresh · q quit"
	if m.page == "history" {
		help = "↑↓ select run · enter inspect · esc tasks · r run · q quit"
	}
	if m.page == "run" {
		help = "↑↓/pgup/pgdn scroll · a approve · x reject · esc history · q quit"
	}
	status := m.message
	if m.busy {
		status = "● " + status
	}
	if m.err != nil {
		status = "Cannot refresh: " + m.err.Error()
	}
	if m.pending != nil {
		status = m.pending.Label + "  [y/N]"
	}
	v := tea.NewView(header + "\n" + muted.Render(short(sub, m.width)) + "\n" + body + "\n" + short(status, m.width) + "\n" + muted.Render(short(help, m.width)))
	v.AltScreen = true
	return v
}
func (m *model) tasksView(height int) string {
	listWidth := m.width
	wide := m.width >= 95
	if wide {
		listWidth = 36
	}
	rows := []string{titleStyle.Render(" Responsibilities"), ""}
	visible := max(1, height-5)
	start := max(0, m.selected-visible+1)
	for i := start; i < len(m.items) && i < start+visible; i++ {
		it := m.items[i]
		label := "  " + it.Task.Name
		if i == m.selected {
			label = "› " + it.Task.Name
			label = selectedStyle.Render(short(label, listWidth-4))
		} else {
			label = short(label, listWidth-4)
		}
		if !wide {
			label += "  " + badge(taskStatus(it))
		}
		rows = append(rows, label)
	}
	list := box(strings.Join(rows, "\n"), listWidth, height)
	if !wide {
		return list
	}
	it := m.current()
	if it == nil {
		return list
	}
	next := "awaiting first tick"
	if it.State.Digest == it.Task.Digest() && !it.State.Next.IsZero() {
		next = date(it.State.Next)
		if time.Now().After(it.State.Next) {
			next += " · overdue"
		}
	}
	if it.Task.Paused {
		next = "paused"
	}
	for _, r := range it.Runs {
		if awaiting(r) {
			next = "waiting for approval"
			break
		}
	}
	width := m.width - listWidth - 1
	detail := titleStyle.Render(maintainer.TerminalSafe(it.Task.Name)) + "\n" + badge(taskStatus(*it)) + "\n\n" +
		fmt.Sprintf("Agent      %s\nAuthority  %s\nNext       %s\nLast good  %s\nSchedule   %s\nTarget     %s\n", it.Task.Driver, it.Task.Authority, next, date(it.State.LastSuccess), maintainer.TerminalSafe(it.Task.Schedule), maintainer.TerminalSafe(it.Task.Target))
	if len(it.Runs) > 0 {
		detail += "\n" + maintainer.TerminalSafe(it.Runs[0].Result.Summary)
		if it.Runs[0].NotificationError != "" {
			detail += "\nNotification: " + maintainer.TerminalSafe(it.Runs[0].NotificationError)
		}
	} else {
		detail += "\n" + maintainer.TerminalSafe(it.Task.Objective)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, " ", box(ansi.Wrap(detail, width-4, ""), width, height))
}
func (m *model) historyView(height int) string {
	it := m.current()
	if it == nil {
		return ""
	}
	rows := []string{titleStyle.Render(maintainer.TerminalSafe(it.Task.Name)) + muted.Render(" / run history"), ""}
	visible := max(1, height-5)
	start := max(0, m.runIndex-visible+1)
	for i := start; i < len(it.Runs) && i < start+visible; i++ {
		r := it.Runs[i]
		label := fmt.Sprintf("%s  %-18s  %s", r.ID, strings.ReplaceAll(runStatus(r), "_", " "), date(r.Started))
		if i == m.runIndex {
			label = selectedStyle.Render("› " + short(label, m.width-6))
		} else {
			label = "  " + short(label, m.width-6)
		}
		rows = append(rows, label)
	}
	if len(it.Runs) == 0 {
		rows = append(rows, "No runs yet. Press r to run this task.")
	}
	return box(strings.Join(rows, "\n"), m.width, height)
}
func (m *model) runView(height int) string {
	r := m.currentRun()
	if r == nil {
		return box("This run is no longer available.", m.width, height)
	}
	text := fmt.Sprintf("%s\n%s\nStarted: %s\nFinished: %s\n\n%s", r.ID, runStatus(*r), date(r.Started), date(r.Finished), r.Result.Summary)
	if r.Error != "" {
		text += "\n\nError: " + r.Error
	}
	if r.Result.Plan != "" {
		text += "\n\nPLAN\n" + r.Result.Plan
	}
	if r.NotificationError != "" {
		text += "\n\nNotification: " + r.NotificationError
	}
	text += "\n\nArtifacts: " + m.store.RunDir(r.ID)
	lines := strings.Split(ansi.Wrap(maintainer.TerminalSafe(text), m.width-4, ""), "\n")
	visible := max(1, height-2)
	start := min(m.scroll, max(0, len(lines)-visible))
	end := min(len(lines), start+visible)
	return box(strings.Join(lines[start:end], "\n"), m.width, height)
}
