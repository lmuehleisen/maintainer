---
name: server-maintenance-review
schedule: "0 10 * * 0"
target: /absolute/path/to/server-reports
driver: codex
authority: propose
timeout: 10m
---

Review the locally exported package-update report, service health, disk space,
and recent backup evidence. Check timestamps and report stale or missing evidence
as a failure. Describe any risks and prepare a proposed maintenance plan with
verification and rollback steps. This task has no authority to contact a server,
run apt upgrade, restart services, or reboot. Actual remote execution requires
separate, scoped access and operator approval outside this initial version.
