# Maintainer — Directional Project Scope

## 1. Vision

A lightweight, CLI-first tool for delegating ongoing maintenance responsibilities to AI agents.

Modern AI coding tools make it easy to create applications, scripts, services, and infrastructure, but these projects accumulate maintenance needs long after their initial creation.

Maintainer lets a user describe a recurring responsibility in natural language, select when it should run, choose an AI agent, and specify what that agent may do autonomously.

**The core idea: cron for AI-powered maintenance, with sensible autonomy and oversight.**

The product should feel like a small, dependable Unix utility rather than an agent orchestration platform.

## 2. Core user experience

The primary interface is a CLI, supported by human-readable configuration files.

Illustrative commands (not prescriptive):

```bash
maintain add              # Create a maintenance task
maintain list             # List responsibilities
maintain status           # Overall health and next runs
maintain run <task>       # Execute immediately
maintain logs <task>      # Inspect previous executions
maintain approve <run>    # Approve a pending action
maintain pause <task>     # Suspend a responsibility
```

The `add` flow should be simple and interactive, with the option to supply or edit configuration directly.

A task should capture:

- **Objective:** What ongoing condition or responsibility should the agent maintain? Defined in natural language.
- **Schedule:** How frequently should it check? Cron expressions or simple intervals.
- **Target/context:** A repository, local directory, server, application, or other resource.
- **Agent/driver:** Which existing CLI agent or inference backend should execute it?
- **Authority:** What may it inspect, modify, propose, or execute without approval?
- **Notification preferences:** When and how should it involve the user?

Avoid requiring complex schemas or workflows for basic tasks.

## 3. Functional scope

### A. Maintenance definitions

- Tasks are persistent, individually configurable, and straightforward to create, edit, disable, or remove.
- Store definitions in portable, human-readable files, ideally Markdown with minimal metadata or a similarly simple format.
- Allow both code maintenance and broader operational responsibilities.
- Support natural-language instructions rather than predefined categories of maintenance.
- Allow optional additional context, constraints, and instructions.

### B. Scheduling and execution

- Run unattended on a laptop, workstation, VM, or server.
- Execute at configurable intervals using a lightweight scheduler.
- Handle restarts, missed runs, overlapping executions, timeouts, and failed jobs sensibly.
- Allow immediate manual execution for testing or troubleshooting.
- Avoid unnecessary long-running processes or infrastructure where practical.

### C. AI agent integration

Prefer delegating work to **existing coding-agent harnesses** rather than building another agent framework.

Potential initial drivers:

- Claude Code (`claude -p`)
- Codex (`codex exec`)
- OpenCode (`opencode run`)

A simple adapter interface should make additional CLI agents possible.

Direct API inference could be supported later, potentially through an existing agent framework.

Ideally, model selection, authentication, tool execution, and context management remain responsibilities of the underlying agent where feasible.

### D. Autonomy and permissions

Support a few understandable operating modes:

- **Observe:** Inspect, analyze, and report.
- **Propose:** Prepare changes or recommendations, but leave application to the user.
- **Ask:** Request approval before performing certain actions.
- **Auto:** Execute permitted actions independently.

The precise modes can evolve.

Provide a mechanism for defining what actions are authorized, potentially including specific commands, directories, repositories, or operational boundaries.

Permission enforcement should use real execution restrictions where appropriate, not merely natural-language instructions. Especially sensitive operations should require explicit authorization or narrowly scoped access.

Keep this practical: leverage existing sandboxing and permissions mechanisms rather than building an elaborate security framework.

### E. Results, history, and notifications

Each execution should produce a useful, structured outcome, such as:

- Healthy / nothing to do
- Maintenance performed
- Action or approval required
- Failed / unable to complete

Maintain local execution history, logs, relevant artifacts, and outstanding issues.

Notifications should be **exception-oriented**. Successful checks with nothing to report should normally be silent.

Support one or more simple notification channels, such as email, webhooks, or messaging integrations. Allow notifications to trigger an external agent or automation system as well.

Make it easy to identify responsibilities that haven't run successfully in too long.

## 4. Initial use cases

The tool should accommodate examples such as:

**Server maintenance**

> Every Sunday, inspect my Coolify server for system package updates. Check for obvious compatibility or operational risks. Apply safe, authorized updates, or ask me before making consequential changes.

**Agent/tool compatibility**

> Periodically compare my installed AGY version against the versions supported by XO's auto-judge. If compatibility has broken, notify FirstMate or initiate an appropriate fix.

**Repository maintenance**

> Weekly, inspect this old project for security vulnerabilities, deprecated dependencies, or likely compatibility issues. Implement straightforward fixes, run tests, and prepare a pull request. Escalate consequential decisions.

These represent different kinds of responsibility, execution environments, and autonomy. The architecture should support them without implementing specialized logic for each.

## 5. Technical direction

Favor:

- Local-first execution and storage.
- A single small installable CLI.
- Minimal dependencies and operational overhead.
- Existing system capabilities over custom infrastructure.
- Pluggable agent execution.
- Human-readable configuration.
- Simple local persistence, potentially SQLite.
- Sensible defaults, visible failures, and predictable behavior.
- Clear separation between task instructions, agent execution, and permission enforcement.

The language, scheduler implementation, state storage, and adapter design should be chosen pragmatically.

Cross-platform operation is desirable, particularly macOS and Linux, without requiring full platform parity immediately.

## 6. Explicit non-goals

At least initially, avoid:

- A full agent framework or multi-agent orchestration system.
- Building a new coding-agent harness.
- A hosted SaaS platform or mandatory cloud infrastructure.
- An elaborate graphical dashboard.
- Complex workflow builders or dependency graphs.
- Custom AI model routing or inference infrastructure.
- General-purpose chat or assistant functionality.
- Heavy observability infrastructure.
- Duplicating existing specialized tools like Renovate or unattended-upgrades.

Integrating with existing tools is preferable to replacing them.

## 7. Suggested initial milestone

A small but useful implementation should allow someone to:

1. Install the CLI and configure an existing AI agent.
2. Create a natural-language maintenance responsibility with a schedule and authority level.
3. Run it manually and inspect the result.
4. Have it execute automatically and reliably on schedule.
5. Review what happened, including failures and pending decisions.
6. Receive notifications only when attention is useful.

Validate against a read-only compatibility check, a repository maintenance task, and a server-maintenance task requiring approval.

The goal is a tool that can be left running for weeks without babysitting.

## 8. Guiding principles

**Small by default.** Keep configuration, installation, and everyday operation minimal.

**Use existing intelligence.** Rely on established coding agents for reasoning and execution.

**Delegate responsibility, not just prompts.** Jobs describe an ongoing desired condition, not necessarily a fixed sequence of commands.

**Autonomy with boundaries.** Users should be able to grant meaningful independence without granting unrestricted access.

**Quiet when healthy, clear when not.** Minimize noise while making exceptions and failures visible.

**Composable.** Work naturally with shells, Git, schedulers, existing infrastructure tools, and external agents.

**Incremental.** Prefer a narrow, reliable core that can grow organically over a comprehensive architecture designed in advance.

Ultimately, Maintainer should make maintaining a small project almost as easy as creating one.