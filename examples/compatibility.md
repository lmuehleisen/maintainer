---
name: tool-compatibility
schedule: "@every 24h"
target: /absolute/path/to/compatibility-fixture
driver: codex
authority: observe
timeout: 5m
---

Compare installed-version.txt with supported-versions.txt. The installed tool
should be supported by the reviewer. Report healthy only when it is listed.
Otherwise explain the discrepancy and the smallest suggested follow-up. Do not
modify either file. If the evidence is missing or ambiguous, report failed.
