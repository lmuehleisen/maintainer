package maintainer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Engine struct {
	Store  *Store
	Driver Driver
	Now    func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) locked(fn func(*os.File) error) error {
	lock, err := e.Store.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = e.Store.Recover(); err != nil {
		return err
	}
	return fn(lock)
}

func (e *Engine) Run(ctx context.Context, name string) (Run, error) {
	var run Run
	err := e.locked(func(lock *os.File) error {
		task, err := e.Store.Task(name)
		if err != nil {
			return err
		}
		if task.Paused {
			return fmt.Errorf("task is paused; resume it before running")
		}
		pending, err := e.Store.Pending(name)
		if err != nil {
			return err
		}
		if pending != nil {
			return fmt.Errorf("approve or reject pending run %s first", pending.ID)
		}
		run, err = e.execute(ctx, task, nil, lock)
		return err
	})
	return run, err
}

func (e *Engine) Approve(ctx context.Context, id string) (Run, error) {
	var run Run
	err := e.locked(func(lock *os.File) error {
		previous, err := e.Store.Run(id)
		if err != nil {
			return err
		}
		if previous.Status != "completed" || previous.Result.Outcome != "approval_required" || previous.ConsumedBy != "" || previous.Rejected {
			return fmt.Errorf("run is not awaiting approval")
		}
		task, err := e.Store.Task(previous.Task)
		if err != nil {
			return err
		}
		if err := CheckDriverActive(task.Driver); err != nil {
			return err
		}
		if task.Paused || task.Authority != "ask" || task.Digest() != previous.TaskDigest {
			return fmt.Errorf("task changed or paused; reject this proposal and run again")
		}
		if e.now().Sub(previous.Finished) > 24*time.Hour {
			return fmt.Errorf("approval expired after 24h; reject this proposal and run again")
		}
		revision, err := CleanRevision(ctx, task.Target)
		if err != nil {
			return err
		}
		if previous.Revision == "" || revision != previous.Revision {
			return fmt.Errorf("target revision changed; reject this proposal and run again")
		}
		run, err = e.execute(ctx, task, &previous, lock)
		return err
	})
	return run, err
}

func (e *Engine) Reject(id string) error {
	return e.locked(func(_ *os.File) error {
		r, err := e.Store.Run(id)
		if err != nil {
			return err
		}
		if r.Status != "completed" || r.Result.Outcome != "approval_required" || r.ConsumedBy != "" || r.Rejected {
			return fmt.Errorf("run is not awaiting approval")
		}
		r.Rejected = true
		return e.Store.SaveRun(r)
	})
}

func (e *Engine) execute(ctx context.Context, t Task, approved *Run, lock *os.File) (Run, error) {
	var r Run
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return r, err
	}
	r = Run{ID: e.now().Format("20060102t150405") + "-" + hex.EncodeToString(nonce), Task: t.Name, TaskDigest: t.Digest(), Started: e.now(), Status: "running"}
	if t.Authority == "ask" {
		r.Revision, _ = CleanRevision(ctx, t.Target)
	}
	plan := ""
	if approved != nil {
		r.ApprovalOf = approved.ID
		plan = approved.Result.Plan
	}
	if err := e.Store.SaveRun(r); err != nil {
		return r, err
	}
	if approved != nil {
		// Consume before dispatch. A crash never silently grants a second execution.
		approved.ConsumedBy = r.ID
		if err := e.Store.SaveRun(*approved); err != nil {
			return r, err
		}
	}
	if b, err := t.Markdown(); err != nil {
		return r, err
	} else if err = AtomicWrite(filepath.Join(e.Store.RunDir(r.ID), "task.md"), b); err != nil {
		return r, err
	}
	state, err := e.Store.State(t)
	if err != nil {
		return r, err
	}
	schedule, err := ParseSchedule(t.Schedule)
	if err != nil {
		return r, err
	}
	state.Digest = t.Digest()
	state.Next = schedule.Next(e.now())
	if err = e.Store.SaveState(t, state); err != nil {
		return r, err
	}
	timeout, _ := time.ParseDuration(t.Timeout)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := Request{Task: t, Dir: e.Store.RunDir(r.ID), StateRoot: e.Store.Root, ApprovedPlan: plan, Lock: lock}
	driver := e.Driver
	if driver == nil {
		driver = Drivers{}
	}
	// Preflight failures are recorded, scheduled, and notified like any failed run.
	var result Result
	runErr := t.CheckTarget(e.Store.Root)
	if runErr == nil {
		result, runErr = driver.Execute(runCtx, req)
	}
	if runErr == nil {
		runErr = ValidateResult(result, req)
	}
	r.Finished = e.now()
	r.Result = result
	r.Status = "completed"
	if runErr != nil {
		r.Status = "failed"
		r.Error = runErr.Error()
		r.Result = Result{Outcome: "failed", Summary: runErr.Error()}
	}
	if result.Outcome == "failed" && runErr == nil {
		r.Status = "failed"
		r.Error = result.Summary
		runErr = fmt.Errorf("agent reported failure: %s", result.Summary)
	}
	if r.Result.Outcome == "approval_required" && r.Revision == "" {
		r.Result.Outcome = "action_required"
		r.Result.Summary += " (CLI execution approval requires a clean, committed Git target; review the plan manually.)"
	}
	if err := e.Store.SaveRun(r); err != nil {
		return r, err
	}
	if r.Status == "completed" && (r.Result.Outcome == "healthy" || r.Result.Outcome == "maintained") {
		state.LastSuccess = r.Finished
	}
	// Intervals are measured from completion; long jobs do not immediately rerun.
	state.Next = schedule.Next(r.Finished)
	if err := e.Store.SaveState(t, state); err != nil {
		return r, err
	}
	if err := Notify(ctx, t, r); err != nil {
		r.NotificationError = err.Error()
		if saveErr := e.Store.SaveRun(r); saveErr != nil {
			return r, saveErr
		}
	}
	return r, runErr
}

func (e *Engine) SetPaused(name string, paused bool) error {
	return e.locked(func(_ *os.File) error {
		t, err := e.Store.Task(name)
		if err != nil {
			return err
		}
		t.Paused = paused
		return e.Store.SaveTask(t, true)
	})
}

// Tick records initial schedules without running newly discovered tasks. Once
// due, any number of missed occurrences collapse into a single execution.
func (e *Engine) Tick(ctx context.Context) ([]Run, error) {
	runs := []Run{}
	err := e.locked(func(lock *os.File) error {
		tasks, err := e.Store.Tasks()
		if err != nil {
			return err
		}
		var failures []error
		for _, t := range tasks {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if t.Paused {
				continue
			}
			pending, err := e.Store.Pending(t.Name)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if pending != nil {
				continue
			}
			state, err := e.Store.State(t)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if state.Digest != t.Digest() || state.Next.IsZero() {
				schedule, err := ParseSchedule(t.Schedule)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				state.Digest = t.Digest()
				state.Next = schedule.Next(e.now())
				if err = e.Store.SaveState(t, state); err != nil {
					failures = append(failures, err)
				}
				continue
			}
			if state.Next.After(e.now()) {
				continue
			}
			r, err := e.execute(ctx, t, nil, lock)
			if r.ID != "" {
				runs = append(runs, r)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", t.Name, err))
			}
		}
		return errors.Join(failures...)
	})
	if errors.Is(err, ErrBusy) {
		return runs, nil
	}
	return runs, err
}

func Notify(ctx context.Context, t Task, r Run) error {
	if t.WebhookEnv == "" || r.Result.Outcome == "healthy" {
		return nil
	}
	endpoint := os.Getenv(t.WebhookEnv)
	if endpoint == "" {
		return fmt.Errorf("notification environment variable %s is unset", t.WebhookEnv)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("notification endpoint must be HTTPS without userinfo")
	}
	payload, _ := json.Marshal(struct {
		ID     string `json:"id"`
		Task   string `json:"task"`
		Result Result `json:"result"`
	}{r.ID, r.Task, r.Result})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cannot create notification request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", r.ID)
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notification delivery failed (endpoint withheld)")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) RetryNotification(ctx context.Context, id string) error {
	return e.locked(func(_ *os.File) error {
		r, err := e.Store.Run(id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(r.NotificationError) == "" {
			return fmt.Errorf("run has no failed notification")
		}
		t, err := e.Store.Task(r.Task)
		if err != nil {
			return err
		}
		if t.WebhookEnv == "" {
			return fmt.Errorf("task has no notification channel")
		}
		if err = Notify(ctx, t, r); err != nil {
			return err
		}
		r.NotificationError = ""
		return e.Store.SaveRun(r)
	})
}
