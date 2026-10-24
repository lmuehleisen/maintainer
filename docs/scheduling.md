# Scheduling

Build/install `maintain`, create a task, and run `maintain tick` once to register
its next occurrence. The worker uses the same user account and Codex authentication
as an interactive run. Use **one** scheduler and a single state directory.

Use absolute executable paths. Scheduler environments often omit your interactive
PATH; Codex and tools such as Git must be available there. Confirm `maintain run`
works in that environment before relying on the schedule.

Scheduler environments also omit variables set in your shell profile:

- **State directory.** Without `--home`, the state directory comes from
  `MAINTAINER_HOME`, then `XDG_STATE_HOME`, then `~/.local/state/maintainer`. If
  either variable is set only in your profile, the scheduled `tick` reads a
  different, empty directory and silently does nothing. The examples pass
  `--home` explicitly; use the directory your interactive commands use.
- **Webhook.** A task's `webhook_env` variable is read when the run finishes. Set
  it in the scheduler too, or every non-healthy run records an unset-variable
  notification error. The URL is a secret, so keep the file holding it mode 0600.
- **Codex.** The standalone Codex installer links `codex` into `~/.local/bin`;
  the examples include it in PATH.

## macOS: launchd

Copy this to `~/Library/LaunchAgents/com.maintainer.tick.plist`, replacing `/Users/YOU`
and PATH values with your actual paths. `go env GOPATH` locates the usual Go bin
directory; `command -v codex` locates Codex. XML-escape special characters in paths.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.maintainer.tick</string>
  <key>ProgramArguments</key><array>
    <string>/Users/YOU/go/bin/maintain</string>
    <string>--home</string><string>/Users/YOU/.local/state/maintainer</string>
    <string>tick</string>
  </array>
  <key>StartInterval</key><integer>60</integer>
  <key>RunAtLoad</key><true/>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>/Users/YOU/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>MAINTAINER_WEBHOOK</key><string>https://hooks.example.com/YOUR-SECRET</string>
  </dict>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Logs/maintainer.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/maintainer-error.log</string>
</dict></plist>
```

Omit `MAINTAINER_WEBHOOK` if no task sets `webhook_env`; otherwise the plist holds
a secret, so restrict it before loading:

```sh
chmod 600 ~/Library/LaunchAgents/com.maintainer.tick.plist
plutil -lint ~/Library/LaunchAgents/com.maintainer.tick.plist
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.maintainer.tick.plist
launchctl print "gui/$(id -u)/com.maintainer.tick"
# Disable later:
launchctl bootout "gui/$(id -u)" ~/Library/LaunchAgents/com.maintainer.tick.plist
```

The laptop need not wake for every interval. The next tick after wake coalesces
missed maintenance into one run. launchd itself will not overlap instances of the
same job; Maintainer's lock also covers manual invocations.

## Linux: systemd user timer

Save `~/.config/systemd/user/maintainer.service`:

```ini
[Unit]
Description=Run due maintenance responsibilities

[Service]
Type=oneshot
ExecStart=%h/go/bin/maintain --home %h/.local/state/maintainer tick
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
EnvironmentFile=-%h/.config/maintainer/env
TimeoutStartSec=infinity
```

If a task sets `webhook_env`, put the variable in `~/.config/maintainer/env`
(for example `MAINTAINER_WEBHOOK=https://hooks.example.com/YOUR-SECRET`) and
`chmod 600` it; the leading `-` lets the service start when the file is absent.

And `~/.config/systemd/user/maintainer.timer`:

```ini
[Unit]
Description=Check maintenance schedules every minute

[Timer]
OnCalendar=*-*-* *:*:00
Persistent=true

[Install]
WantedBy=timers.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now maintainer.timer
systemctl --user list-timers maintainer.timer
journalctl --user -u maintainer.service
# Disable later:
systemctl --user disable --now maintainer.timer
```

User timers normally depend on your user manager being active. Running after
logout may require an administrator-approved lingering setup. A VM can instead
run the service under a dedicated ordinary user.

## Cron

Run once a minute, using your actual absolute paths and the PATH containing Codex.
If a task sets `webhook_env`, add the variable too; `crontab` keeps the table
readable only by you.

```cron
PATH=/home/YOU/.local/bin:/usr/local/bin:/usr/bin:/bin
MAINTAINER_WEBHOOK=https://hooks.example.com/YOUR-SECRET
* * * * * /home/YOU/go/bin/maintain --home /home/YOU/.local/state/maintainer tick >> /home/YOU/maintainer.log 2>&1
```

Inspect `maintain status` regularly during the initial trial. A stopped scheduler
cannot report its own absence; externally monitoring overdue checks is future work.
Manage log rotation and old run directories yourself for now. Never delete a run
that is active or awaiting approval.
