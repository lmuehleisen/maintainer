package maintainer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Claude struct{ Binary string }

func checkClaudeVersion(version string) error {
	m := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:\s|$)`).FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return fmt.Errorf("cannot verify Claude Code version")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	if major < 2 || (major == 2 && (minor < 1 || (minor == 1 && patch < 304))) {
		return fmt.Errorf("Claude Code 2.1.304 or later is required for the isolated driver")
	}
	return nil
}

// Restricted mode confines native file tools; the OS sandbox confines Bash.
// Neither unattended permissions nor plan mode alone is a read-only boundary.
func ClaudeSettings(req Request) string {
	write := req.Task.Authority == "auto" || req.ApprovedPlan != ""
	denyWrite := []string{"/"}
	if write {
		denyWrite = []string{}
		if req.StateRoot != "" {
			denyWrite = append(denyWrite, req.StateRoot)
			if resolved, err := filepath.EvalSymlinks(req.StateRoot); err == nil && resolved != req.StateRoot {
				denyWrite = append(denyWrite, resolved)
			}
		}
	}
	domains := []string{}
	deniedDomains := []string{"*"}
	if write && req.Task.Network {
		domains = []string{"*"}
		deniedDomains = []string{}
	}
	settings := map[string]any{
		"disableAllHooks": true,
		"permissions":     map[string]any{"defaultMode": "dontAsk"},
		"sandbox": map[string]any{
			"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false,
			"autoAllowBashIfSandboxed": true, "excludedCommands": []string{},
			"filesystem": map[string]any{"denyWrite": denyWrite},
			"network":    map[string]any{"allowedDomains": domains, "deniedDomains": deniedDomains, "allowLocalBinding": false, "allowAllUnixSockets": false},
		},
	}
	b, _ := json.Marshal(settings)
	return string(b)
}

func ClaudeArgs(req Request) []string {
	tools := "Read,Glob,Grep,Bash"
	if req.Task.Authority == "auto" || req.ApprovedPlan != "" {
		tools += ",Edit,Write"
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--json-schema", ResultSchema,
		"--no-session-persistence", "--safe-mode", "--restricted", "--disable-slash-commands", "--no-chrome",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "",
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--tools", tools, "--allowedTools", tools, "--settings", ClaudeSettings(req),
	}
	if req.Task.Model != "" {
		args = append(args, "--model", req.Task.Model)
	}
	return args
}

func (d Claude) Execute(ctx context.Context, req Request) (Result, error) {
	var result Result
	binary := d.Binary
	if binary == "" {
		binary = "claude"
	}
	probe := exec.CommandContext(ctx, binary, "--version")
	probe.Env = agentEnv()
	probe.Dir = req.Task.Target
	version, err := probe.Output()
	if err != nil {
		return result, fmt.Errorf("cannot check Claude version: %w", err)
	}
	if err = checkClaudeVersion(string(version)); err != nil {
		return result, err
	}
	if err = AtomicWrite(filepath.Join(req.Dir, "driver-version.txt"), version); err != nil {
		return result, err
	}
	if err := AtomicWrite(filepath.Join(req.Dir, "schema.json"), []byte(ResultSchema)); err != nil {
		return result, err
	}
	if err := AtomicWrite(filepath.Join(req.Dir, "settings.json"), []byte(ClaudeSettings(req))); err != nil {
		return result, err
	}
	prompt := Prompt(req)
	if err := AtomicWrite(filepath.Join(req.Dir, "prompt.txt"), []byte(prompt)); err != nil {
		return result, err
	}
	stdout, err := os.OpenFile(filepath.Join(req.Dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return result, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(req.Dir, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return result, err
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, binary, ClaudeArgs(req)...)
	cmd.Dir = req.Task.Target
	cmd.Env = agentEnv()
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if req.Lock != nil {
		cmd.ExtraFiles = []*os.File{req.Lock}
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return result, fmt.Errorf("claude failed (see events.jsonl and stderr.log): %w", err)
	}
	file, err := os.Open(filepath.Join(req.Dir, "events.jsonl"))
	if err != nil {
		return result, err
	}
	defer file.Close()
	result, err = ParseClaudeResult(file, req)
	if err != nil {
		return result, err
	}
	if err = writeJSON(filepath.Join(req.Dir, "result.json"), result); err != nil {
		return result, err
	}
	return result, nil
}

func ParseClaudeResult(reader io.Reader, req Request) (Result, error) {
	var result Result
	found := false
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			IsError bool            `json:"is_error"`
			Output  json.RawMessage `json:"structured_output"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return result, fmt.Errorf("invalid Claude event: %w", err)
		}
		if event.Type != "result" {
			continue
		}
		if found {
			return result, fmt.Errorf("multiple Claude result events")
		}
		found = true
		if event.Subtype != "success" || event.IsError {
			return result, fmt.Errorf("Claude did not complete successfully (%s)", event.Subtype)
		}
		if len(event.Output) == 0 || string(event.Output) == "null" {
			return result, fmt.Errorf("Claude returned no structured output")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(event.Output, &fields); err != nil {
			return result, fmt.Errorf("invalid Claude structured output: %w", err)
		}
		for _, key := range []string{"outcome", "summary", "plan"} {
			if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return result, fmt.Errorf("Claude result is missing %s", key)
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(event.Output))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			return result, fmt.Errorf("invalid Claude structured output: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read Claude events: %w", err)
	}
	if !found {
		return result, fmt.Errorf("Claude returned no result event")
	}
	return result, ValidateResult(result, req)
}
