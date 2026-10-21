# Maintainer

**Small responsibilities. Kept up.**

Maintainer is a local Go CLI for recurring maintenance delegated to an existing
coding agent. Write the desired condition in plain text, choose a schedule and
authority, and inspect a durable history of what happened.

The command is `maintain`. This is an early working version, with Codex as its
first driver and Charm forms for interactive setup.

## Quick start

Requires macOS or Linux, Go 1.26+, Git, and an authenticated Codex CLI supporting
`exec --ignore-user-config --ignore-rules` (tested with 0.163.0).

```sh
go install github.com/lmuehleisen/maintainer/cmd/maintain@latest
maintain add
maintain list
maintain run --dry-run weekly-dependencies
maintain run weekly-dependencies
maintain logs weekly-dependencies
maintain status
```

Go installs to `$(go env GOPATH)/bin` unless `GOBIN` is set; add that directory to
your PATH if needed. To build a checkout: `go build -o bin/maintain ./cmd/maintain`.

No timer is installed by these commands. See [scheduling](docs/scheduling.md)
to arrange automatic execution.

## A task is a Markdown file

```markdown
---
name: weekly-dependencies
schedule: "CRON_TZ=America/New_York 0 9 * * 1"
target: /absolute/path/to/project
driver: codex
authority: ask
timeout: 15m
---

Keep this project's dependencies supported and free of known critical issues.
Inspect the manifests and local audit evidence. Propose straightforward updates
with verification commands. Escalate major versions, migrations, or ambiguous
failures. Do not deploy, publish, or push changes.
```

Import it with `maintain add --file task.md`, or use the interactive form.
Noninteractive creation also supports `--name`, `--target`, `--objective`,
`--schedule`, `--authority`, `--timeout`, `--model`, `--network`, and `--webhook-env`.

Schedules accept five-field cron, `@daily`, `@weekly`, or intervals such as
`@every 24h` / `@every 168h`. Intervals must be at least one minute. Calendar
schedules use UTC unless an explicit `CRON_TZ` is included. Time-zone data is
bundled into the binary.

Definitions live under `$MAINTAINER_HOME/tasks`, defaulting to
`~/.local/state/maintainer/tasks` (or `$XDG_STATE_HOME/maintainer/tasks`). Edit them
directly; unknown metadata keys fail validation. `pause` and `resume` change the
file. To remove a responsibility, pause it and delete its task file; history stays.

## Authority

| Mode | First run | Next step |
| --- | --- | --- |
| `observe` | Read-only inspection | Report healthy, action required, or failure |
| `propose` | Read-only inspection and written recommendation | User applies the recommendation |
| `ask` | Read-only inspection and concrete saved plan | `maintain approve RUN` starts a separate execution |
| `auto` | Workspace-write execution | Changes within the target are allowed |

```sh
maintain inspect RUN_ID
maintain approve RUN_ID
# Explicit, scriptable approval after reviewing the plan:
maintain approve --yes RUN_ID
maintain reject RUN_ID
```

Approvals are single use and expire after 24 hours. Execution requires the same
task definition and a clean Git tree at the recorded commit. Pending proposals
block new runs until approved or rejected. A failed approved run consumes its
approval: rerun the inspection to get a fresh proposal. Non-Git or dirty targets
can receive written recommendations, but cannot use CLI execution approval yet.

The Codex sandbox enforces the local write boundary. The approved plan and
objective remain instructions to the agent, **not a command-level security
policy**. Read [permission boundaries](docs/architecture.md#permission-boundaries)
before using write authority or network access.

## Results and notifications

Each run saves its task snapshot, prompt, JSON result, Codex event stream, stderr,
and execution metadata. `inspect RUN_ID` shows the artifact directory.

Outcomes: `healthy`, `maintained`, `action_required`, `approval_required`, `failed`.
Interrupted processes are recovered as `interrupted` on the next operation that
acquires the run lock. Nonzero process exits, malformed responses, and invalid
authority/result combinations fail visibly. Agent statements are reports, not
independent proof that the objective was met.

Optional `webhook_env: MAINTAINER_WEBHOOK` names an environment variable containing
an HTTPS endpoint. Successful healthy checks are silent. Other outcomes send JSON
containing `id`, `task`, and `result` (including the plan). Configure your webhook
receiver to translate this into email, a chat message, or an external automation.
Redirects are rejected. URLs and secrets are not stored in task files or forwarded
to the agent. Plans and summaries may contain sensitive project information.

Failed delivery is recorded, visible in `status`, and can be retried with
`maintain notify RUN_ID`. There is no automatic delivery retry/outbox yet.

Use `maintain --json COMMAND` for scripts; global flags go before the command and
command flags before task/run IDs. `tick` prints nothing for healthy checks and
exits nonzero for execution failures. `run` also exits nonzero for failures;
action/approval-required results are successful checks with an outstanding decision.

## Current scope

- macOS and Linux; one serial worker per state directory, one Codex adapter.
- Local targets; no daemon, database service, remote API, or hosted account.
- Structured results, crash recovery, scheduling, approval, and optional webhooks.
- Huh v2 forms and Lip Gloss v2 output; no separate full-screen dashboard yet.

The server-maintenance example is intentionally a **local report review** in this
version. Live SSH checks/updates, arbitrary command allowlists, isolated worktrees,
PR creation, additional agents, automatic webhook retries, and log retention are
future work. No real servers or other projects are modified during development.

See [architecture](docs/architecture.md), [scheduling](docs/scheduling.md),
[examples](examples), and the original [directional scope](docs/directional-scope.md).

## Development

```sh
go test -race ./...
go vet ./...
go build -o bin/maintain ./cmd/maintain
```

Tests use temporary directories and fake agents. They do not spend model tokens,
install timers, send real notifications, or touch maintenance targets.

No license has been selected yet; public visibility does not itself grant a
license to reuse the code.
