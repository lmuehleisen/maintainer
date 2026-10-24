package maintainer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

type Task struct {
	Name       string `yaml:"name" json:"name"`
	Schedule   string `yaml:"schedule" json:"schedule"`
	Target     string `yaml:"target" json:"target"`
	Driver     string `yaml:"driver" json:"driver"`
	Authority  string `yaml:"authority" json:"authority"`
	Timeout    string `yaml:"timeout" json:"timeout"`
	Paused     bool   `yaml:"paused,omitempty" json:"paused"`
	Model      string `yaml:"model,omitempty" json:"model,omitempty"`
	Network    bool   `yaml:"network,omitempty" json:"network"`
	WebhookEnv string `yaml:"webhook_env,omitempty" json:"webhook_env,omitempty"`
	Objective  string `yaml:"-" json:"objective"`
}

func ParseTask(data []byte) (Task, error) {
	var t Task
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return t, fmt.Errorf("task must start with YAML front matter (---)")
	}
	parts := bytes.SplitN(data[4:], []byte("\n---\n"), 2)
	if len(parts) != 2 {
		return t, fmt.Errorf("missing closing front matter delimiter")
	}
	d := yaml.NewDecoder(bytes.NewReader(parts[0]))
	d.KnownFields(true)
	if err := d.Decode(&t); err != nil {
		return t, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return t, fmt.Errorf("expected one YAML document")
	}
	t.Objective = strings.TrimSpace(string(parts[1]))
	if t.Driver == "" {
		t.Driver = "codex"
	}
	if t.Authority == "" {
		t.Authority = "observe"
	}
	if t.Timeout == "" {
		t.Timeout = "15m"
	}
	return t, t.Validate()
}

func (t Task) Validate() error {
	if !validID.MatchString(t.Name) {
		return fmt.Errorf("name must be 1–80 lowercase letters, numbers, or hyphens")
	}
	if strings.TrimSpace(t.Objective) == "" {
		return fmt.Errorf("objective cannot be empty")
	}
	if !filepath.IsAbs(t.Target) {
		return fmt.Errorf("target must be an absolute local directory")
	}
	if t.Driver != "codex" && t.Driver != "claude" {
		return fmt.Errorf("unsupported driver %q (choose codex; Claude is inactive)", t.Driver)
	}
	switch t.Authority {
	case "observe", "propose", "ask", "auto":
	default:
		return fmt.Errorf("unknown authority %q", t.Authority)
	}
	if t.Network && t.Authority != "auto" && t.Authority != "ask" {
		return fmt.Errorf("network is only available for auto or the approved execution of ask tasks")
	}
	if t.WebhookEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(t.WebhookEnv) {
		return fmt.Errorf("webhook_env must be an environment variable name")
	}
	timeout, err := time.ParseDuration(t.Timeout)
	if err != nil || timeout < time.Second || timeout > 24*time.Hour {
		return fmt.Errorf("timeout must be between 1s and 24h")
	}
	_, err = ParseSchedule(t.Schedule)
	return err
}

func ParseSchedule(spec string) (s cron.Schedule, err error) {
	// robfig/cron v3.0.1 panics on a timezone prefix without a following space.
	for _, p := range []string{"CRON_TZ=", "TZ="} {
		if strings.HasPrefix(spec, p) && !strings.Contains(spec, " ") {
			return nil, fmt.Errorf("schedule: %s must be followed by a cron expression", p)
		}
	}
	defer func() {
		if v := recover(); v != nil {
			s, err = nil, fmt.Errorf("schedule: invalid expression %q", spec)
		}
	}()
	if strings.HasPrefix(spec, "@every ") {
		d, err := time.ParseDuration(strings.TrimPrefix(spec, "@every "))
		if err != nil || d < time.Minute {
			return nil, fmt.Errorf("interval must be at least 1m; use @every 168h for weekly intervals")
		}
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	s, err = p.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	if s.Next(time.Now().UTC()).IsZero() {
		return nil, fmt.Errorf("schedule has no occurrence within the next five years")
	}
	return s, nil
}

func (t Task) Digest() string {
	b, _ := t.Markdown()
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (t Task) Markdown() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(t)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(b) + "---\n\n" + t.Objective + "\n"), nil
}

func (t Task) CheckTarget(stateRoot string) error {
	path, err := filepath.EvalSymlinks(t.Target)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("target is not a directory")
	}
	// Codex's ignore-user-config flag does not suppress project configuration.
	// Fail closed instead of inheriting project MCP servers or hooks. Check both
	// lexical and resolved ancestors (targets may be symlinked).
	userHome, _ := os.UserHomeDir()
	resolvedHome, _ := filepath.EvalSymlinks(userHome)
	for _, start := range []string{filepath.Clean(t.Target), path} {
		if t.Driver != "codex" {
			break
		}
		for dir := start; ; dir = filepath.Dir(dir) {
			// This location is user configuration, already suppressed by the
			// adapter, rather than a project config inherited from an ancestor.
			if dir == userHome || dir == resolvedHome {
				if filepath.Dir(dir) == dir {
					break
				}
				continue
			}
			config := filepath.Join(dir, ".codex", "config.toml")
			if _, err := os.Lstat(config); err == nil {
				return fmt.Errorf("project Codex configuration at %s is not supported by the isolated driver", config)
			} else if !os.IsNotExist(err) {
				return err
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	root, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		return err
	}
	// An agent with workspace write access must never be able to edit its own approval ledger.
	for _, pair := range [][2]string{{path, root}, {root, path}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("target and Maintainer state must not contain one another")
		}
	}
	return nil
}
