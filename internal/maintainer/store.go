package maintainer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

var ErrBusy = errors.New("another Maintainer operation is running")

type Result struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
	Plan    string `json:"plan"`
}

type Run struct {
	ID                string    `json:"id"`
	Task              string    `json:"task"`
	TaskDigest        string    `json:"task_digest"`
	Started           time.Time `json:"started"`
	Finished          time.Time `json:"finished,omitempty"`
	Status            string    `json:"status"`
	Result            Result    `json:"result"`
	Error             string    `json:"error,omitempty"`
	ApprovalOf        string    `json:"approval_of,omitempty"`
	ConsumedBy        string    `json:"consumed_by,omitempty"`
	Rejected          bool      `json:"rejected,omitempty"`
	Revision          string    `json:"revision,omitempty"`
	NotificationError string    `json:"notification_error,omitempty"`
}

type TaskState struct {
	Digest      string    `json:"digest"`
	Next        time.Time `json:"next"`
	LastSuccess time.Time `json:"last_success,omitempty"`
}

type Store struct {
	Root string
	Warn io.Writer // receives skipped-entry warnings; nil means stderr
}

func (s *Store) warn(format string, args ...any) {
	w := s.Warn
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "maintain: warning: "+format+"\n", args...)
}

func Open(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{root, filepath.Join(root, "tasks"), filepath.Join(root, "runs"), filepath.Join(root, "schedule")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	return &Store{Root: root}, nil
}

// Lock is held throughout a run, serializing all work in one state directory.
// Passing this descriptor to the agent also prevents overlap after parent death;
// it is read-only so the agent gains no write access to the state directory.
func (s *Store) Lock() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(s.Root, "lock"), os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

func AtomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(b, '\n'))
}

func (s *Store) SaveTask(t Task, replace bool) error {
	b, err := t.Markdown()
	if err != nil {
		return err
	}
	path := filepath.Join(s.Root, "tasks", t.Name+".md")
	if !replace {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("task %s already exists", t.Name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return AtomicWrite(path, b)
}

func (s *Store) Task(name string) (Task, error) {
	if !validID.MatchString(name) {
		return Task{}, fmt.Errorf("invalid task name")
	}
	b, err := os.ReadFile(filepath.Join(s.Root, "tasks", name+".md"))
	if err != nil {
		return Task{}, err
	}
	t, err := ParseTask(b)
	if err == nil && t.Name != name {
		return t, fmt.Errorf("task name must match filename %s.md", name)
	}
	return t, err
}

func (s *Store) Tasks() ([]Task, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "tasks"))
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		t, err := s.Task(e.Name()[:len(e.Name())-3])
		if err != nil {
			s.warn("skipping tasks/%s: %v", e.Name(), err)
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Store) RunDir(id string) string { return filepath.Join(s.Root, "runs", id) }
func (s *Store) SaveRun(r Run) error {
	if !validID.MatchString(r.ID) {
		return fmt.Errorf("invalid run ID")
	}
	if err := os.MkdirAll(s.RunDir(r.ID), 0700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.RunDir(r.ID), "run.json"), r)
}
func (s *Store) Run(id string) (Run, error) {
	var r Run
	if !validID.MatchString(id) {
		return r, fmt.Errorf("invalid run ID")
	}
	b, err := os.ReadFile(filepath.Join(s.RunDir(id), "run.json"))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	if err == nil && r.ID != id {
		err = fmt.Errorf("run ID does not match directory")
	}
	return r, err
}
func (s *Store) Runs(task string) ([]Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "runs"))
	if err != nil {
		return nil, err
	}
	runs := []Run{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r, err := s.Run(e.Name())
		if err != nil {
			s.warn("skipping runs/%s: %v", e.Name(), err)
			continue
		}
		if task == "" || task == r.Task {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.Before(runs[j].Started) })
	return runs, nil
}
func (s *Store) State(t Task) (TaskState, error) {
	var state TaskState
	b, err := os.ReadFile(filepath.Join(s.Root, "schedule", t.Name+".json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(b, &state)
	return state, err
}
func (s *Store) SaveState(t Task, state TaskState) error {
	return writeJSON(filepath.Join(s.Root, "schedule", t.Name+".json"), state)
}

func (s *Store) Pending(name string) (*Run, error) {
	runs, err := s.Runs(name)
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.Status == "completed" && r.Result.Outcome == "approval_required" && r.ConsumedBy == "" && !r.Rejected {
			return &r, nil
		}
	}
	return nil, nil
}

// Recover is called only with the process lock held; no live run can own it.
func (s *Store) Recover() error {
	runs, err := s.Runs("")
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status == "running" {
			r.Status = "interrupted"
			r.Error = "runner exited before recording a result; inspect logs before retrying"
			r.Finished = time.Now().UTC()
			if err := s.SaveRun(r); err != nil {
				return err
			}
		}
	}
	return nil
}
