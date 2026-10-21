---
name: repository-maintenance
schedule: "CRON_TZ=America/New_York 0 9 * * 1"
target: /absolute/path/to/clean-git-repository
driver: codex
authority: ask
timeout: 15m
---

Keep this project maintainable. Inspect manifests, lockfiles, and locally available
audit reports for unsupported dependencies or known security findings. Record
missing evidence rather than claiming an online vulnerability check occurred.
For a straightforward fix, propose the exact files, commands, verification, and
rollback. Ask for decisions about major upgrades. Do not publish, deploy, push,
or change infrastructure. No network access is granted by this definition.
