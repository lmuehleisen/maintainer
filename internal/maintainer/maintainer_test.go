package maintainer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDriver struct {
	Calls    int
	Result   Result
	Err      error
	Requests []Request
}

func (f *fakeDriver) Execute(_ context.Context, r Request) (Result, error) {
	f.Calls++
	f.Requests = append(f.Requests, r)
	return f.Result, f.Err
}
func fixture(t *testing.T) (*Engine, *fakeDriver, Task, *time.Time) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	task := Task{Name: "test", Schedule: "@every 1h", Target: target, Authority: "observe", Driver: "codex", Timeout: "1m", Objective: "Verify the local fixture."}
	if err = s.SaveTask(task, false); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	d := &fakeDriver{Result: Result{Outcome: "healthy", Summary: "Verified."}}
	e := &Engine{Store: s, Driver: d, Now: func() time.Time { return now }}
	return e, d, task, &now
}
func gitFixture(t *testing.T, target string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", target}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
}

func TestStrictTaskAndSchedule(t *testing.T) {
	_, _, task, _ := fixture(t)
	data, err := task.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTask(data)
	if err != nil || parsed.Digest() != task.Digest() {
		t.Fatalf("round trip: %v", err)
	}
	bad := strings.Replace(string(data), "driver: codex", "driver: codex\nunknown: surprise", 1)
	if _, err = ParseTask([]byte(bad)); err == nil {
		t.Fatal("unknown key accepted")
	}
	for _, spec := range []string{"@every 0s", "@every -1h", "@every 1s", "not cron", "0 0 30 2 *"} {
		if _, err := ParseSchedule(spec); err == nil {
			t.Errorf("invalid schedule accepted: %s", spec)
		}
	}
	schedule, err := ParseSchedule("CRON_TZ=America/New_York 0 9 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	next := schedule.Next(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	if next.Weekday() != time.Monday || next.Hour() != 13 {
		t.Fatalf("timezone schedule: %s", next)
	}
}
func TestMissedRunsCoalesceAndDoNotStorm(t *testing.T) {
	e, d, _, now := fixture(t)
	ctx := context.Background()
	if _, err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if d.Calls != 0 {
		t.Fatal("new task ran immediately")
	}
	*now = now.Add(10 * time.Hour)
	runs, err := e.Tick(ctx)
	if err != nil || len(runs) != 1 || d.Calls != 1 {
		t.Fatalf("catchup: %v %d", err, d.Calls)
	}
	if _, err = e.Tick(ctx); err != nil || d.Calls != 1 {
		t.Fatal("duplicate tick reran job")
	}
	*now = now.Add(time.Hour)
	d.Err = errors.New("driver unavailable")
	if _, err = e.Tick(ctx); err == nil {
		t.Fatal("failure not surfaced")
	}
	if _, err = e.Tick(ctx); err != nil || d.Calls != 2 {
		t.Fatal("failed job retried without cadence")
	}
}
func TestApprovalIsBoundAndSingleUse(t *testing.T) {
	e, d, task, _ := fixture(t)
	gitFixture(t, task.Target)
	task.Authority = "ask"
	if err := e.Store.SaveTask(task, true); err != nil {
		t.Fatal(err)
	}
	d.Result = Result{Outcome: "approval_required", Summary: "Update needed.", Plan: "Create CHANGELOG.md and verify contents."}
	proposed, err := e.Run(context.Background(), task.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Run(context.Background(), task.Name); err == nil {
		t.Fatal("pending proposal did not block new run")
	}
	d.Result = Result{Outcome: "maintained", Summary: "Done."}
	approved, err := e.Approve(context.Background(), proposed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ApprovalOf != proposed.ID || d.Requests[1].ApprovedPlan != proposed.Result.Plan {
		t.Fatal("approval linkage lost")
	}
	if _, err = e.Approve(context.Background(), proposed.ID); err == nil {
		t.Fatal("duplicate approval accepted")
	}
}
func TestApprovalRejectsChangedTaskDirtyTargetAndExpiry(t *testing.T) {
	for _, scenario := range []string{"task", "dirty", "revision", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			e, d, task, now := fixture(t)
			gitFixture(t, task.Target)
			task.Authority = "ask"
			_ = e.Store.SaveTask(task, true)
			d.Result = Result{Outcome: "approval_required", Summary: "Needed.", Plan: "Make a small change."}
			proposed, err := e.Run(context.Background(), task.Name)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "task":
				task.Objective = "Do something else"
				_ = e.Store.SaveTask(task, true)
			case "dirty":
				_ = os.WriteFile(filepath.Join(task.Target, "new.txt"), []byte("changed"), 0600)
			case "revision":
				cmd := exec.Command("git", "-C", task.Target, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "change")
				if err = cmd.Run(); err != nil {
					t.Fatal(err)
				}
			case "expired":
				*now = now.Add(25 * time.Hour)
			}
			if _, err = e.Approve(context.Background(), proposed.ID); err == nil {
				t.Fatal("stale approval accepted")
			}
			if d.Calls != 1 {
				t.Fatal("agent ran after stale approval")
			}
			if err = e.Reject(proposed.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFailedApprovedExecutionConsumesApproval(t *testing.T) {
	e, d, task, _ := fixture(t)
	gitFixture(t, task.Target)
	task.Authority = "ask"
	_ = e.Store.SaveTask(task, true)
	d.Result = Result{Outcome: "approval_required", Summary: "Needed.", Plan: "Make a small change."}
	r, err := e.Run(context.Background(), task.Name)
	if err != nil {
		t.Fatal(err)
	}
	d.Err = errors.New("crashed")
	if _, err = e.Approve(context.Background(), r.ID); err == nil {
		t.Fatal("failure hidden")
	}
	if _, err = e.Approve(context.Background(), r.ID); err == nil {
		t.Fatal("failed approval reused")
	}
}
func TestLockAndInterruptedRecovery(t *testing.T) {
	e, d, task, _ := fixture(t)
	lock, err := e.Store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Run(context.Background(), task.Name); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected lock: %v", err)
	}
	if _, err = e.Tick(context.Background()); err != nil || d.Calls != 0 {
		t.Fatal("tick did not skip occupied lock")
	}
	r := Run{ID: "interrupted-run", Task: task.Name, Status: "running", Started: e.now()}
	if err = e.Store.SaveRun(r); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if _, err = e.Run(context.Background(), task.Name); err != nil {
		t.Fatal(err)
	}
	recovered, err := e.Store.Run(r.ID)
	if err != nil || recovered.Status != "interrupted" {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
}
func TestBoundaryChecks(t *testing.T) {
	e, _, task, _ := fixture(t)
	task.Target = filepath.Dir(e.Store.Root)
	if err := task.CheckTarget(e.Store.Root); err == nil {
		t.Fatal("target containing ledger accepted")
	}
	task.Target = filepath.Join(filepath.Dir(e.Store.Root), "target")
	if err := os.Mkdir(filepath.Join(task.Target, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task.Target, ".codex", "config.toml"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := task.CheckTarget(e.Store.Root); err == nil {
		t.Fatal("project configuration accepted")
	}
	if _, err := e.Store.Task("../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
func TestSandboxModesAndResults(t *testing.T) {
	_, _, task, _ := fixture(t)
	for _, mode := range []string{"observe", "propose", "ask", "auto"} {
		task.Authority = mode
		req := Request{Task: task, Dir: "/tmp/run"}
		args := strings.Join(CodexArgs(req), " ")
		expected := "--sandbox read-only"
		if mode == "auto" {
			expected = "--sandbox workspace-write"
		}
		if !strings.Contains(args, expected) || strings.Contains(args, "danger-full-access") || !strings.Contains(args, "network_access=false") {
			t.Fatal(args)
		}
		err := ValidateResult(Result{Outcome: "maintained", Summary: "changed"}, req)
		if (mode == "auto") != (err == nil) {
			t.Fatalf("incorrect write result validation for %s", mode)
		}
	}
	task.Authority = "ask"
	req := Request{Task: task}
	if ValidateResult(Result{Outcome: "approval_required", Summary: "needs work"}, req) == nil {
		t.Fatal("empty approval accepted")
	}
	if ValidateResult(Result{Outcome: "made-up", Summary: "unknown"}, req) == nil {
		t.Fatal("unknown result accepted")
	}
}
func TestAgentEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("MAINTAINER_TEST_SECRET", "do-not-inherit")
	t.Setenv("OPENAI_API_KEY", "do-not-inherit")
	env := strings.Join(agentEnv(), "\n")
	if strings.Contains(env, "do-not-inherit") {
		t.Fatal("secret inherited")
	}
}
func TestCodexDriverParsesOutputAndTimesOut(t *testing.T) {
	e, _, task, _ := fixture(t)
	for _, scenario := range []string{"valid", "malformed", "nonzero", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "codex")
			body := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do if [ \"$1\" = '-o' ]; then shift; out=$1; fi; shift; done\ncat >/dev/null\n"
			switch scenario {
			case "valid":
				body += "printf '%s' '{\"outcome\":\"healthy\",\"summary\":\"verified\",\"plan\":\"\"}' > \"$out\"\n"
			case "malformed":
				body += "printf '%s' '{\"outcome\":\"healthy\"}' > \"$out\"\n"
			case "nonzero":
				body += "exit 7\n"
			case "timeout":
				body += "sleep 60 &\nwait\n"
			}
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			before := time.Now()
			_, err := (Codex{Binary: script}).Execute(ctx, Request{Task: task, Dir: dir})
			if (scenario == "valid") != (err == nil) {
				t.Fatalf("%s: %v", scenario, err)
			}
			if time.Since(before) > 3*time.Second {
				t.Fatal("timeout failed to terminate subprocess group")
			}
		})
	}
	_ = e
}
func TestHealthyNotificationIsSilentAndMissingChannelVisible(t *testing.T) {
	e, d, task, _ := fixture(t)
	task.WebhookEnv = "MAINTAINER_UNSET_TEST_WEBHOOK"
	t.Setenv(task.WebhookEnv, "")
	_ = e.Store.SaveTask(task, true)
	r, err := e.Run(context.Background(), task.Name)
	if err != nil || r.NotificationError != "" {
		t.Fatal("healthy check tried to notify")
	}
	d.Result = Result{Outcome: "action_required", Summary: "Needs attention."}
	r, err = e.Run(context.Background(), task.Name)
	if err != nil || r.NotificationError == "" {
		t.Fatal("notification failure not recorded")
	}
}

func TestWebhookDeliveryRejectsRedirectsAndRedactsEndpoint(t *testing.T) {
	requests := 0
	status := http.StatusNoContent
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "test-run" {
			t.Error("missing delivery identity")
		}
		var payload struct {
			Task   string `json:"task"`
			Result Result `json:"result"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Task != "test" || payload.Result.Outcome != "action_required" {
			t.Error("incorrect webhook payload")
		}
		w.Header().Set("Location", serverURLForTest(r))
		w.WriteHeader(status)
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	t.Setenv("MAINTAINER_TEST_WEBHOOK", server.URL+"/?token=sensitive-value")
	task := Task{Name: "test", WebhookEnv: "MAINTAINER_TEST_WEBHOOK"}
	run := Run{ID: "test-run", Task: "test", Result: Result{Outcome: "action_required", Summary: "Attention needed."}}
	if err := Notify(context.Background(), task, run); err != nil {
		t.Fatal(err)
	}
	status = http.StatusFound
	err := Notify(context.Background(), task, run)
	if err == nil || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("redirect or credential leak: %v", err)
	}
	if requests != 2 {
		t.Fatalf("redirect was followed: %d requests", requests)
	}
}

func serverURLForTest(r *http.Request) string { return "https://" + r.Host + "/redirected" }
