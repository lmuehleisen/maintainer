package maintainer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMixedDriverDispatch(t *testing.T) {
	e, codex, task, now := fixture(t)
	claude := &fakeDriver{Result: Result{Outcome: "healthy", Summary: "Claude checked."}}
	e.Driver = Drivers{Codex: codex, Claude: claude}
	other := task
	other.Name = "claude-task"
	other.Driver = "claude"
	if err := e.Store.SaveTask(other, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if _, err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if codex.Calls != 1 || claude.Calls != 1 {
		t.Fatalf("routing: codex=%d claude=%d", codex.Calls, claude.Calls)
	}
	other.Driver = "unknown"
	if _, err := (Drivers{}).Execute(context.Background(), Request{Task: other}); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestClaudeAuthorityPolicies(t *testing.T) {
	_, _, task, _ := fixture(t)
	task.Driver = "claude"
	for _, mode := range []string{"observe", "propose", "ask", "auto"} {
		for _, approved := range []bool{false, true} {
			if approved && mode != "ask" {
				continue
			}
			task.Authority = mode
			req := Request{Task: task, StateRoot: "/private/state"}
			if approved {
				req.ApprovedPlan = "Apply the reviewed patch."
			}
			var settings struct {
				Sandbox struct {
					Enabled, FailIfUnavailable, AllowUnsandboxedCommands bool
					Filesystem                                           struct{ DenyWrite []string }
					Network                                              struct{ DeniedDomains []string }
				}
			}
			if err := json.Unmarshal([]byte(ClaudeSettings(req)), &settings); err != nil {
				t.Fatal(err)
			}
			if !settings.Sandbox.Enabled || !settings.Sandbox.FailIfUnavailable || settings.Sandbox.AllowUnsandboxedCommands {
				t.Fatal("sandbox may fail open")
			}
			args := strings.Join(ClaudeArgs(req), " ")
			write := mode == "auto" || approved
			if !write && (len(settings.Sandbox.Filesystem.DenyWrite) != 1 || settings.Sandbox.Filesystem.DenyWrite[0] != "/") {
				t.Fatal("read-only mode allows filesystem writes")
			}
			if write && !strings.Contains(ClaudeSettings(req), "/private/state") {
				t.Fatal("ledger not protected")
			}
			if strings.Contains(args, "Read,Glob,Grep,Bash,Edit,Write") != write {
				t.Fatal("native write tools do not match authority")
			}
			if strings.Contains(args, "bypassPermissions") || !strings.Contains(args, "--restricted") || !strings.Contains(args, "--safe-mode") || !strings.Contains(args, "--permission-prompts none") {
				t.Fatal("unsafe invocation")
			}
			if len(settings.Sandbox.Network.DeniedDomains) != 1 {
				t.Fatal("network not denied")
			}
		}
	}
}

func TestClaudeResultEnvelope(t *testing.T) {
	_, _, task, _ := fixture(t)
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"type":"system","subtype":"init"}
{"type":"result","subtype":"success","is_error":false,"structured_output":{"outcome":"healthy","summary":"Verified","plan":""}}`, true},
		{"authentication", `{"type":"result","subtype":"success","is_error":true,"structured_output":{"outcome":"healthy","summary":"Not really","plan":""}}`, false},
		{"turn-limit", `{"type":"result","subtype":"error_max_turns"}`, false},
		{"missing-output", `{"type":"result","subtype":"success","result":"Looks fine"}`, false},
		{"missing-field", `{"type":"result","subtype":"success","structured_output":{"outcome":"healthy","summary":"Verified"}}`, false},
		{"extra-field", `{"type":"result","subtype":"success","structured_output":{"outcome":"healthy","summary":"Verified","plan":"","extra":1}}`, false},
		{"unauthorized-change", `{"type":"result","subtype":"success","structured_output":{"outcome":"maintained","summary":"Changed files","plan":""}}`, false},
		{"not-json", "not json", false},
		{"no-result", `{"type":"system"}`, false},
		{"null-plan", `{"type":"result","subtype":"success","structured_output":{"outcome":"healthy","summary":"Verified","plan":null}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseClaudeResult(strings.NewReader(tc.body), Request{Task: task})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestClaudeVersionFailsClosed(t *testing.T) {
	for _, version := range []string{"2.1.303 (Claude Code)", "1.9.999", "unexpected", "2.1.304-preview"} {
		if checkClaudeVersion(version) == nil {
			t.Fatalf("accepted %s", version)
		}
	}
	for _, version := range []string{"2.1.304 (Claude Code)", "2.2.0", "3.0.0"} {
		if err := checkClaudeVersion(version); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeProcessArtifactsFailureAndTimeout(t *testing.T) {
	_, _, task, _ := fixture(t)
	task.Driver = "claude"
	for _, scenario := range []string{"success", "exit", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "claude")
			script := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo '2.1.304 (Claude Code)'; exit 0; fi\ncat >/dev/null\n"
			switch scenario {
			case "success":
				script += "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"structured_output\":{\"outcome\":\"healthy\",\"summary\":\"Verified\",\"plan\":\"\"}}'\n"
			case "exit":
				script += "exit 9\n"
			case "timeout":
				script += "sleep 60 &\nwait\n"
			}
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			before := time.Now()
			_, err := (Claude{Binary: binary}).Execute(ctx, Request{Task: task, Dir: dir})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("%s: %v", scenario, err)
			}
			if time.Since(before) > 3*time.Second {
				t.Fatal("process group did not time out")
			}
			if scenario == "success" {
				for _, name := range []string{"result.json", "events.jsonl", "stderr.log", "settings.json", "driver-version.txt", "prompt.txt"} {
					if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
