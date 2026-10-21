package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/lmuehleisen/maintainer/internal/maintainer"
)

const version = "0.1.0-dev"
const usage = `maintain — recurring responsibilities, explicit authority

Usage: maintain [--home DIR] [--json] COMMAND [OPTIONS]

  add                    Create a task with a short form
  add --file task.md     Import a Markdown task
  list                   List tasks (edit their Markdown files directly)
  status                 Show schedule, last success, and outstanding approvals
  show TASK              Print a task definition
  run TASK               Run immediately
  run --dry-run TASK     Inspect the exact prompt and sandbox arguments
  tick                   Run due tasks once (for launchd/systemd/cron)
  logs TASK              Show execution history
  inspect RUN            Show result, plan, and artifact directory
  approve [--yes] RUN    Execute a saved plan; --yes is explicit CLI approval
  reject RUN             Dismiss a proposal and unblock its task
  pause TASK             Pause manual and scheduled runs
  resume TASK            Resume a task
  notify RUN             Retry a failed webhook delivery
  version                Print version

Add flags: --name --target --objective --schedule --authority --timeout --model
           --network --webhook-env (or use --file)
State: MAINTAINER_HOME, or ~/.local/state/maintainer (XDG_STATE_HOME respected).
Schedules default to UTC; use CRON_TZ=America/New_York for local calendar time.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "maintain:", err)
		os.Exit(1)
	}
}
func defaultHome() string {
	if p := os.Getenv("MAINTAINER_HOME"); p != "" {
		return p
	}
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "maintainer")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "maintainer")
}
func output(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func one(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("expected exactly one task or run ID")
	}
	return args[0], nil
}

func cli(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("maintain", flag.ContinueOnError)
	home := global.String("home", defaultHome(), "state directory")
	asJSON := global.Bool("json", false, "JSON output")
	global.Usage = func() { fmt.Print(usage) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	command := args[0]
	args = args[1:]
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	store, err := maintainer.Open(*home)
	if err != nil {
		return err
	}
	engine := &maintainer.Engine{Store: store, Driver: maintainer.Codex{}}
	switch command {
	case "add":
		return add(store, args, *asJSON)
	case "list", "status":
		if len(args) != 0 {
			return fmt.Errorf("%s takes no arguments", command)
		}
		tasks, err := store.Tasks()
		if err != nil {
			return err
		}
		if command == "list" {
			if *asJSON {
				return output(tasks)
			}
			for _, t := range tasks {
				fmt.Printf("%-24s %-8s %-6s %s\n", t.Name, t.Authority, enabled(t), t.Schedule)
			}
			return nil
		}
		type row struct {
			Name              string    `json:"name"`
			Paused            bool      `json:"paused"`
			Next              time.Time `json:"next"`
			LastSuccess       time.Time `json:"last_success"`
			Pending           string    `json:"pending,omitempty"`
			LastOutcome       string    `json:"last_outcome,omitempty"`
			NotificationError string    `json:"notification_error,omitempty"`
		}
		rows := []row{}
		for _, t := range tasks {
			state, err := store.State(t)
			if err != nil {
				return err
			}
			item := row{Name: t.Name, Paused: t.Paused, LastSuccess: state.LastSuccess}
			if state.Digest == t.Digest() {
				item.Next = state.Next
			}
			p, err := store.Pending(t.Name)
			if err != nil {
				return err
			}
			if p != nil {
				item.Pending = p.ID
			}
			runs, err := store.Runs(t.Name)
			if err != nil {
				return err
			}
			if len(runs) > 0 {
				r := runs[len(runs)-1]
				item.LastOutcome = r.Result.Outcome
				if r.Status == "running" || r.Status == "interrupted" {
					item.LastOutcome = r.Status
				}
				item.NotificationError = r.NotificationError
			}
			rows = append(rows, item)
		}
		if *asJSON {
			return output(rows)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "TASK\tNEXT (UTC)\tLAST SUCCESS\tSTATUS")
		for _, r := range rows {
			status := r.LastOutcome
			if status == "" {
				status = "never run"
			}
			next := stamp(r.Next)
			if r.Next.IsZero() {
				next = "awaiting tick"
			}
			if !r.Next.IsZero() && time.Now().After(r.Next) {
				status += " / overdue"
			}
			if r.Pending != "" {
				status = "approval: " + r.Pending
				next = "blocked"
			}
			if r.Paused {
				status = "paused"
				next = "—"
			}
			if r.NotificationError != "" {
				status += " / notification failed"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, next, stamp(r.LastSuccess), status)
		}
		return w.Flush()
	case "show":
		name, err := one(args)
		if err != nil {
			return err
		}
		t, err := store.Task(name)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(t)
		}
		b, err := t.Markdown()
		if err == nil {
			_, err = os.Stdout.Write(b)
		}
		return err
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "show invocation without running")
		if err := fs.Parse(args); err != nil {
			return err
		}
		name, err := one(fs.Args())
		if err != nil {
			return err
		}
		if *dry {
			t, err := store.Task(name)
			if err != nil {
				return err
			}
			if err = t.CheckTarget(store.Root); err != nil {
				return err
			}
			req := maintainer.Request{Task: t, Dir: filepath.Join(store.Root, "runs", "<run-id>")}
			return output(struct {
				Args   []string `json:"args"`
				Prompt string   `json:"prompt"`
			}{maintainer.CodexArgs(req), maintainer.Prompt(req)})
		}
		r, err := engine.Run(ctx, name)
		printRun(r, *asJSON)
		return err
	case "tick":
		if len(args) != 0 {
			return fmt.Errorf("tick takes no arguments")
		}
		runs, err := engine.Tick(ctx)
		if *asJSON {
			if outErr := output(runs); outErr != nil {
				return outErr
			}
		} else {
			for _, r := range runs {
				if r.Result.Outcome != "healthy" {
					printRun(r, false)
				}
			}
		}
		return err
	case "logs":
		name, err := one(args)
		if err != nil {
			return err
		}
		if _, err = store.Task(name); err != nil {
			return err
		}
		runs, err := store.Runs(name)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(runs)
		}
		for _, r := range runs {
			printRun(r, false)
		}
		return nil
	case "inspect":
		id, err := one(args)
		if err != nil {
			return err
		}
		r, err := store.Run(id)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(r)
		}
		printRun(r, false)
		if r.Result.Plan != "" {
			fmt.Printf("\nPlan:\n%s\n", r.Result.Plan)
		}
		fmt.Println("\nArtifacts:", store.RunDir(id))
		return nil
	case "approve":
		fs := flag.NewFlagSet("approve", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "approve without interactive confirmation")
		if err := fs.Parse(args); err != nil {
			return err
		}
		id, err := one(fs.Args())
		if err != nil {
			return err
		}
		previous, err := store.Run(id)
		if err != nil {
			return err
		}
		if !*yes {
			fmt.Printf("Plan for %s:\n%s\n\n", previous.Task, previous.Result.Plan)
			if !terminal() {
				return fmt.Errorf("review with inspect, then use approve --yes RUN in noninteractive mode")
			}
			approved := false
			if err := huh.NewConfirm().Title("Execute this plan with workspace write access?").Value(&approved).Run(); err != nil {
				return err
			}
			if !approved {
				return nil
			}
		}
		r, err := engine.Approve(ctx, id)
		printRun(r, *asJSON)
		return err
	case "reject":
		id, err := one(args)
		if err != nil {
			return err
		}
		return engine.Reject(id)
	case "notify":
		id, err := one(args)
		if err != nil {
			return err
		}
		return engine.RetryNotification(ctx, id)
	case "pause", "resume":
		name, err := one(args)
		if err != nil {
			return err
		}
		lock, err := store.Lock()
		if err != nil {
			return err
		}
		defer lock.Close()
		t, err := store.Task(name)
		if err != nil {
			return err
		}
		t.Paused = command == "pause"
		return store.SaveTask(t, true)
	default:
		return fmt.Errorf("unknown command %q; run maintain help", command)
	}
}
func enabled(t maintainer.Task) string {
	if t.Paused {
		return "paused"
	}
	return "active"
}
func stamp(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
func terminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
func printRun(r maintainer.Run, asJSON bool) {
	if r.ID == "" {
		return
	}
	if asJSON {
		_ = output(r)
		return
	}
	outcome := r.Result.Outcome
	if outcome == "" {
		outcome = r.Status
	}
	fmt.Printf("%s  %s  %s\n  %s\n", r.ID, r.Task, outcome, r.Result.Summary)
	if r.NotificationError != "" {
		fmt.Println("  Notification:", r.NotificationError)
	}
}

func add(store *maintainer.Store, args []string, asJSON bool) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	file := fs.String("file", "", "import task file")
	name := fs.String("name", "", "task name")
	target := fs.String("target", "", "absolute target directory")
	objective := fs.String("objective", "", "maintenance objective")
	schedule := fs.String("schedule", "@weekly", "cron or @every interval")
	authority := fs.String("authority", "observe", "observe, propose, ask, auto")
	timeout := fs.String("timeout", "15m", "run timeout")
	model := fs.String("model", "", "Codex model override")
	network := fs.Bool("network", false, "allow network during authorized writes")
	webhook := fs.String("webhook-env", "", "environment variable containing HTTPS webhook")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	var task maintainer.Task
	if *file != "" {
		if fs.NFlag() != 1 {
			return fmt.Errorf("--file cannot be combined with task flags")
		}
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		task, err = maintainer.ParseTask(b)
		if err != nil {
			return err
		}
	} else {
		if len(args) == 0 {
			if !terminal() {
				return fmt.Errorf("use --file or task flags when stdin is not a terminal")
			}
			fmt.Println(lipgloss.NewStyle().Bold(true).Render("A responsibility, not just a prompt."))
			cwd, _ := os.Getwd()
			*target = cwd
			form := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Name").Placeholder("weekly-dependencies").Value(name),
				huh.NewInput().Title("Target directory").Value(target),
				huh.NewInput().Title("Schedule").Description("@weekly, @every 24h, or CRON_TZ=America/New_York 0 9 * * 1").Value(schedule),
				huh.NewSelect[string]().Title("Authority").Options(huh.NewOption("Observe — inspect and report", "observe"), huh.NewOption("Propose — write a recommendation", "propose"), huh.NewOption("Ask — save a plan for approval", "ask"), huh.NewOption("Auto — write within the target", "auto")).Value(authority),
				huh.NewText().Title("What condition should stay true?").Value(objective),
			)).WithAccessible(os.Getenv("ACCESSIBLE") == "1")
			if err := form.Run(); err != nil {
				return err
			}
		}
		if *target != "" {
			p, err := filepath.Abs(*target)
			if err != nil {
				return err
			}
			*target = p
		}
		task = maintainer.Task{Name: *name, Target: *target, Objective: strings.TrimSpace(*objective), Schedule: *schedule, Authority: *authority, Driver: "codex", Timeout: *timeout, Model: *model, Network: *network, WebhookEnv: *webhook}
	}
	if err := task.Validate(); err != nil {
		return err
	}
	if err := task.CheckTarget(store.Root); err != nil {
		return err
	}
	lock, err := store.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := store.SaveTask(task, false); err != nil {
		return err
	}
	if asJSON {
		return output(task)
	}
	_, err = io.WriteString(os.Stdout, fmt.Sprintf("Created %s\n%s\nRun now: maintain run %s\n", task.Name, filepath.Join(store.Root, "tasks", task.Name+".md"), task.Name))
	return err
}
