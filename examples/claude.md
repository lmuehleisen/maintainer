---
name: claude-project-check
schedule: "@weekly"
target: /absolute/path/to/project
driver: claude
authority: observe
timeout: 10m
---

**Reference only: the Claude driver is currently inactive; this example cannot be imported.**

Check this project's local dependency manifests, lockfiles, and maintenance notes
for version inconsistencies, unsupported tooling, or obvious maintenance work.
Report actionable findings with file references. Do not claim current vulnerability
coverage from local files alone. Do not edit files or run package installation.
