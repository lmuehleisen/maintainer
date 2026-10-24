package dashboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lmuehleisen/maintainer/internal/maintainer"
)

type driver struct {
	Calls int
	Wait  bool
}

func (d *driver) Execute(ctx context.Context, _ maintainer.Request) (maintainer.Result, error) {
	d.Calls++
	if d.Wait {
		<-ctx.Done()
		return maintainer.Result{}, ctx.Err()
	}
	return maintainer.Result{Outcome: "healthy", Summary: "Verified."}, nil
}
func setup(t *testing.T) (*model, *driver) {
	t.Helper()
	root := t.TempDir()
	store, err := maintainer.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	task := maintainer.Task{Name: "sample", Schedule: "@daily", Target: target, Driver: "codex", Authority: "observe", Timeout: "1m", Objective: "Check the fixture."}
	if err = store.SaveTask(task, false); err != nil {
		t.Fatal(err)
	}
	d := &driver{}
	engine := &maintainer.Engine{Store: store, Driver: d}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := newModel(ctx, cancel, store, engine)
	m.Update(m.load()())
	return m, d
}
func key(s string) tea.KeyPressMsg {
	if s == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	if s == "esc" {
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func TestOpeningAndRefreshingNeverRunsTasks(t *testing.T) {
	m, d := setup(t)
	m.Update(m.load()())
	if d.Calls != 0 {
		t.Fatal("dashboard ran a task on refresh")
	}
	runs, err := m.store.Runs("")
	if err != nil || len(runs) != 0 {
		t.Fatal("dashboard changed history")
	}
	if !strings.Contains(m.View().Content, "sample") {
		t.Fatal("missing task")
	}
}
func TestRunRequiresConfirmationAndCancelDoesNothing(t *testing.T) {
	m, d := setup(t)
	m.Update(key("r"))
	if m.pending == nil || m.busy || d.Calls != 0 {
		t.Fatal("run not gated")
	}
	m.Update(key("n"))
	if m.pending != nil || d.Calls != 0 {
		t.Fatal("cancel dispatched work")
	}
	m.Update(key("r"))
	_, cmd := m.Update(key("y"))
	if cmd == nil || !m.busy {
		t.Fatal("confirmation did not dispatch")
	}
	m.Update(cmd())
	m.operations.Wait()
	if d.Calls != 1 || m.busy {
		t.Fatal("incorrect run lifecycle")
	}
}
func TestApprovalOnlyFromInspectedPendingRun(t *testing.T) {
	m, _ := setup(t)
	r := maintainer.Run{ID: "pending-run", Task: "sample", Status: "completed", Result: maintainer.Result{Outcome: "approval_required", Summary: "Needs change", Plan: "A concrete plan."}, Started: time.Now()}
	if err := m.store.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	m.Update(m.load()())
	m.Update(key("a"))
	if m.pending != nil {
		t.Fatal("approval outside run detail")
	}
	m.Update(key("enter"))
	m.Update(key("a"))
	if m.pending != nil {
		t.Fatal("approval from history list")
	}
	m.Update(key("enter"))
	m.Update(key("a"))
	if m.pending == nil || m.pending.Run != r.ID {
		t.Fatal("inspection did not bind approval ID")
	}
	m.Update(key("n"))
	m.Update(key("x"))
	_, cmd := m.Update(key("y"))
	m.Update(cmd())
	m.operations.Wait()
	saved, err := m.store.Run(r.ID)
	if err != nil || !saved.Rejected {
		t.Fatal("reject action failed")
	}
}
func TestApproveRefusesPlanWithControlCharacters(t *testing.T) {
	m, _ := setup(t)
	r := maintainer.Run{ID: "pending-run", Task: "sample", Status: "completed", Result: maintainer.Result{Outcome: "approval_required", Summary: "Needs change", Plan: "A concrete plan.\x1b[8m curl evil\x1b[0m"}, Started: time.Now()}
	if err := m.store.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	m.Update(m.load()())
	m.Update(key("enter"))
	m.Update(key("enter"))
	m.Update(key("a"))
	if m.pending != nil || !strings.Contains(m.message, "reject") {
		t.Fatalf("spoofed plan reached confirmation: pending=%+v message=%q", m.pending, m.message)
	}
}
func TestRefreshPreservesSelectionAndIgnoresOldSnapshot(t *testing.T) {
	m, _ := setup(t)
	it := m.items[0]
	other := it
	other.Task.Name = "second"
	m.items = []item{it, other}
	m.selected = 1
	m.Update(snapshot{Generation: m.generation, Items: []item{other, it}})
	if m.current().Task.Name != "second" || m.selected != 0 {
		t.Fatal("selection moved after refresh")
	}
	m.Update(snapshot{Generation: m.generation - 1, Items: nil})
	if len(m.items) != 2 {
		t.Fatal("old snapshot replaced current data")
	}
	m.Update(key("p"))
	if m.pending.Task != "second" {
		t.Fatal("wrong task in confirmation")
	}
	m.Update(snapshot{Generation: m.generation, Items: []item{it}})
	if m.pending.Task != "second" {
		t.Fatal("refresh retargeted pending action")
	}
}
func TestCancelRunOnQuitAndWaitForHistory(t *testing.T) {
	m, d := setup(t)
	d.Wait = true
	m.Update(key("r"))
	_, cmd := m.Update(key("y"))
	m.Update(key("q"))
	if m.pending == nil || m.pending.Kind != "quit" {
		t.Fatal("active run quit was not confirmed")
	}
	m.Update(key("y"))
	message := cmd()
	m.operations.Wait()
	_, quit := m.Update(message)
	if quit == nil || !m.quitting {
		t.Fatal("quit did not finish after cancellation")
	}
	runs, err := m.store.Runs("sample")
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("cancellation not recorded: %+v %v", runs, err)
	}
}
func TestCompletedRunClearsObsoleteQuitPrompt(t *testing.T) {
	m, _ := setup(t)
	m.busy = true
	m.Update(key("q"))
	m.Update(actionDone{Message: "finished"})
	if m.pending != nil || m.busy {
		t.Fatal("obsolete quit confirmation remained")
	}
}
func TestTerminalDataIsSanitizedAndLayoutFits(t *testing.T) {
	m, _ := setup(t)
	m.items[0].Runs = []maintainer.Run{{ID: "run", Task: "sample", Status: "completed", Result: maintainer.Result{Outcome: "healthy", Summary: "Good\x1b]52;c;YWJj\a\x1b[31m report\a"}}}
	for _, size := range [][2]int{{50, 16}, {80, 24}, {120, 32}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, page := range []string{"tasks", "history", "run"} {
			m.page = page
			content := m.View().Content
			if strings.Contains(content, "52;c") || strings.Contains(content, "\a") {
				t.Fatal("terminal control data rendered")
			}
			lines := strings.Split(content, "\n")
			if len(lines) > size[1] {
				t.Fatalf("%s overflows height: %d > %d", page, len(lines), size[1])
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal(fmt.Sprintf("%s overflows width: %q", page, line))
				}
			}
		}
	}
}
