# Node setup and control

A node is a machine that lends its CPU to a hub while it is on. It runs one binary, `sat`, kept in
`~/.sat/bin`. Set it up once per project.

The bash node (`node/`, `~/sat/PROJECT`) is the **legacy** version: it still works with the same
hubs, and `sat migrate` moves a node from it to the binary ([below](#moving-from-the-bash-node)).

## 1. Key login to the hub

`sat setup` uses your own SSH key login to the hub once, to install a separate, restricted key for
the node. It never uses a password. If this machine can't yet log in to the hub with a key, give
it one (`ssh-keygen -t ed25519` if it has none) and put its public key on the hub, preferably
without ever enabling password logins:

- **From GitHub:** if the key is on your GitHub account, run `ssh-import-id gh:YOUR_GITHUB_USER`
  on the hub (or `curl -s https://github.com/YOUR_GITHUB_USER.keys >> ~/.ssh/authorized_keys`).
- **By hand:** paste `~/.ssh/id_ed25519.pub` into the hub's `~/.ssh/authorized_keys` from a
  console or an existing key session on the hub.
- Only if the hub still allows password logins: `ssh-copy-id HUB_USER@HUB_IP`, then turn them off.

Check with `ssh -o PasswordAuthentication=no HUB_USER@HUB_IP true`, and keep password logins off on
the hub ([hub.md](hub.md#2-ssh-server-key-logins-only)). This first login also records the hub's
host key in `~/.ssh/known_hosts`, which `sat` checks on every connection.

## 2. Get the binary

From a [release](https://github.com/RashKash103/satellite/releases), checking it against the
release's checksums:

```sh
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSLO https://github.com/RashKash103/satellite/releases/latest/download/sat_linux_$arch
curl -fsSLO https://github.com/RashKash103/satellite/releases/latest/download/checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify sat_linux_$arch --repo RashKash103/satellite   # optional: who built it
chmod +x sat_linux_$arch
```

or build it yourself with Go 1.27 or later: `CGO_ENABLED=0 go install github.com/RashKash103/satellite/cmd/sat@latest`
(it lands in `~/go/bin/sat` and calls itself `dev`; `sat self-update` would replace it with the
latest release).

## 3. Run `sat setup`

```sh
./sat_linux_$arch setup HUB_IP PROJECT [HUB_USER]
```

It is safe to rerun, and **it installs no software**. It:

1. makes `~/.ssh/sat_ed25519` and an SSH alias `sat-hub-HUB_IP` for it (both kept when they
   exist: one key per node, whatever the number of projects);
2. adds that key to the hub's `authorized_keys` behind `sat-ssh-gate`, bound to this node's name
   (the short hostname) and to the projects it serves, using your own key login; setting up
   another project on the same hub adds that project to the key's line, a rerun replaces the line
   instead of adding one, and an empty or malformed key is refused;
3. installs itself as `~/.sat/bin/sat` (with `sat-call` beside it, for jobs), the `sat@.service`
   user unit, and a `~/.local/bin/sat` link if that name is free;
4. fetches the project's bundle and writes `~/.sat/projects/PROJECT/config` (adding the bundle's
   `sat/defaults`);
5. checks what the node is missing, for Satellite and for the project's jobs. If anything is, it
   prints the commands that would install it and stops: install them yourself and rerun;
6. runs the project's `sat/setup`, which prepares the project's own files under
   `~/.sat/projects/PROJECT`.

Nothing starts until you run `sat PROJECT start`.

To have it install what is missing, add `--install` (`sat setup --install HUB_IP PROJECT`). It lists
every command first, shows exactly which packages each apt step would install or upgrade, and asks
before running anything. apt runs with `--no-remove`, so it never removes a package; if a package
can't be installed without removing another, it stops and runs nothing. The commands it ran are
kept in `~/.sat/installed.log`.

To keep the service running while you are logged out, run `loginctl enable-linger` once (setup
leaves this to you).

## Several projects on one node

A node can take part in several projects, from one hub or several: rerun `sat setup` for each.
They share the node's key and alias, and each has its own directory, config (its own `SLOTS`),
`env`, service (`sat@PROJECT`), drain flag and logs. This is also the way to run **different kinds
of work on one machine**, as modes: say a `precheck` project for quick checks with four lanes and
a `tasks` project for long jobs with one, each started, drained and stopped on its own. The live
view shows them all.

## 4. The `sat` command

`sat` on its own is the **live view**, in panes: on the left the projects (`1`, each with a badge
when it has [alerts](#alerts)), the selected project's lanes (`2`: each lane's job, how long it has
run and its own progress line) and its details (`3`: service, autostart, the hub and its protocol,
last contact, disk room; with room to spare, this machine's CPU, memory and load as sparklines and
each project's free disk against its floor); on the right (`4`) tabs: the selected lane's log, the
service log, the output of the last action, every project's alerts, and the project's own
[views](projects.md#views) (marked ◆; a lane's views while it has a job). `[` and `]` go through
the tabs, `w` to the next view, `!` to the alerts; the tab chosen is remembered for each project.
`tab` and the numbers move between panes, `j`/`k`, the arrows, `pgup`/`pgdown`, `g`/`G` and the
mouse wheel scroll within one (the border says how much more there is), `enter` goes in, `/`
filters the pane (projects, lanes or log lines), and `?` lists every key. It follows the terminal's
size and background: as it shrinks, the charts go first, then Details. It reads what the agents
write and opens no connection of its own (a view it runs may: `sat-call`).

Everything the command line does can be done from it, and it runs the same `sat` commands to do
it: `s` start, `d` drain, `D` [schedule a drain](#scheduled-drains), `x` stop (asks first), `A`
autostart, `c` check, `t` selftest, `u` update, `e` and `E` open the project's `env` and `config` in
`$VISUAL` or `$EDITOR` (else nano, else vi), `v` sets or unsets an env key, `a` adds a project
(`sat setup`, as a form). Those stream their output into the right pane. `i` install, `U` uninstall the project, `ctrl+u` uninstall everything,
`S` self-update and `M` migrate (offered when a bash node is found) hand the terminal to `sat`
itself, which shows exactly what it will do and asks before anything runs, as on the command line;
`enter` then brings the live view back.

Questions and forms open as **dialogs** in the middle of the screen: stopping a project, the
drain schedule, adding a project, setting an env key, quitting while an action still runs, and
the key list (`?`). `tab` / `shift+tab` (or `↑` `↓`) move between a dialog's fields and buttons,
`←` `→` between the buttons or through a field's choices, `enter` or `space` choose the focused
button (on a field, `enter` moves to the next), `esc` cancels, and `y` / `n` answer a yes-or-no
question; the mouse clicks buttons and fields and scrolls a long dialog. A dialog checks what is
typed into it as you go and says what is wrong in place. Stopping, which hands running jobs back,
starts on Cancel: `enter` alone does nothing.

`sat [PROJECT] COMMAND`, or `sat COMMAND PROJECT`. PROJECT can be left out when the node has only one
(`sat status` then shows them all).

| command | |
|---|---|
| `start` | start taking jobs |
| `drain` | take no new jobs; stop once the running ones finish (use before shutting down) |
| `schedule [start\|end HH:MM [daily\|YYYY-MM-DD] \| off]` | drain at a time, or be stopped by one, once or daily ([below](#scheduled-drains)); alone, show the schedule |
| `stop` | stop now; running jobs go straight back to the hub |
| `status` | running or not, autostart, the hub and its protocol, jobs in progress, disk room, the last log lines |
| `logs` | follow the service log |
| `alerts [-w]` | what needs attention ([alerts](#alerts)); with no PROJECT, or several, for all of them; `-w` keeps the list live |
| `views` | the project's [views](projects.md#views): name, title, scope, mode, format |
| `view NAME [--lane N] [-w]` | show one: once, or live with `-w` (a snapshot redrawn in place, a stream followed) until ctrl+c. A lane view picks the only lane with a job, else `--lane N` |
| `check` | what this node is missing, and the commands that would install it. It checks in the environment the service will have (the user manager's, `systemctl --user show-environment`, plus the project's config and `env`), not your shell's: put PATH additions from shell profiles in `env` |
| `install` | the same steps, shown and simulated, then run after you confirm |
| `autostart on\|off` | start at boot, or not (off by default) |
| `selftest` | the project's own check of this node |
| `env [KEY=VALUE \| -KEY]` | list, set or unset the project's `env` (below); applies from the next job |
| `update` | refresh the bundle from the hub; check, then rerun the project's setup |

and for the node as a whole:

| command | |
|---|---|
| `sat setup [--install] HUB_IP PROJECT [HUB_USER]` | set up a project (above) |
| `sat self-update [--yes]` | install the newest release ([updates](#updates)) |
| `sat uninstall [--yes] [PROJECT]` | remove a project, or everything ([below](#removing-a-node)) |
| `sat migrate [--yes]` | move a bash node to `~/.sat` ([below](#moving-from-the-bash-node)) |
| `sat completion zsh\|bash\|install` | shell completion ([below](#shell-completion)) |
| `sat version` | |

`sat help COMMAND` (or `COMMAND --help`) explains one command.

### Shell completion

For zsh, either write the completion into a directory on your `fpath`:

```sh
sat completion zsh > "${fpath[1]}/_sat"
```

or let `sat completion install` write it to `~/.sat/completions/_sat`; it prints the one line to add
to `~/.zshrc` (it never edits it):

```sh
fpath=(~/.sat/completions $fpath); autoload -Uz compinit && compinit
```

(if your `.zshrc` already runs `compinit`, put only the `fpath` part before it). For bash,
`sat completion bash > ~/.local/share/bash-completion/completions/sat`.

It completes the commands, with what each does; project names, before or after the command; each
command's flags; `autostart on|off`; the project's views, with their titles, and `--lane` with its
lane numbers (and the job each runs); the keys of the project's `env` (`KEY=` to set one, `-KEY` to
unset it); `help` topics, `uninstall`'s projects, and `sat-call`'s project and hub commands. The
script itself is fixed: every candidate comes from `sat __complete`, which reads only this node's
files (no network), so completion follows projects, views and env files as they change, and
updates of `sat` as it is replaced.

### Output and colour

On a terminal, `sat` styles its output: colours that follow a light or dark background, marks
(✓ done, ✗ failed, ! a warning), aligned rows, a progress bar per lane and a spinner while it waits
for the hub or a download. Sent to a pipe or a file it prints plain text with no escape codes, the
same lines as always, so scripts can read them. `NO_COLOR=1` (any value) turns styling off on a
terminal too, as do `TERM=dumb` and `CLICOLOR=0`; `CLICOLOR_FORCE=1` or `FORCE_COLOR=1` turn it on
even through a pipe (`NO_COLOR` still wins). The background is taken from `COLORFGBG` when set,
else asked of the terminal. The service's own log, in journald and `state/agent.log`, is always
plain; `sat logs` colours it only when showing it on a terminal.

A normal shutdown is safe without a drain: systemd's stop hands running jobs back. Draining just
avoids throwing away work in progress.

## Node config

`~/.sat/projects/PROJECT/config` is read at each start and sourced by each job:

| key | |
|---|---|
| `HUB` | the SSH alias for the hub (written by `sat setup`); jobs use it for git and rsync |
| `HUB_IP`, `HUB_USER` | where the hub is; `HUB_PORT` if its sshd isn't on 22 |
| `SAT_NAME` | this node's name in leases and results: the short hostname. It is bound to the node's key on the hub, so change it in both places, or rerun `sat setup` after renaming the machine. |
| `SLOTS` | lanes: jobs run at the same time (default 1; the project's defaults may set it) |
| `MIN_FREE_GB`, `MIN_FREE_PCT` | the node keeps the larger of these free on the disk under the project's directory (defaults 20 GB and 10 %) |
| `MAX_GB` | caps this project's directory (default 0: no cap; a project's defaults may set one) |
| `STALL_MIN` | minutes without new output from a lane's job before it is an [alert](#alerts) (default 30; 0: never) |
| `JOB_WARN_MIN` | minutes a job may run before it is an alert (default: no limit) |
| `KEEP_AWAKE` | `off`: let the machine sleep while jobs run. By default the agent holds a logind inhibitor (`systemd-inhibit`) while any lane runs a job, so the machine does not suspend mid-job; desktops such as KDE Plasma and GNOME show it as what keeps the machine awake. Blocking sleep needs a login session: on a node nobody is logged in to, it blocks idle only, which still stops suspending for idleness. The lock goes with the last job, or with the agent |
| `JOB_EST_MIN` | minutes a job is taken to last by an end-anchored [drain schedule](#scheduled-drains) before this node has finished any (default 30) |
| `TRANSPORT` | `native` (default): `sat` holds one SSH connection to the hub open; `exec`: it runs the system `ssh` with the alias for each call, as the bash node did (setup picks this when only the system ssh gets through, say through a jump host) |

Projects may add their own keys (through `sat/defaults`) for their jobs to read. Keys may also go
in `~/.sat/config`, for every project on the node, along with:

| key | |
|---|---|
| `AUTO_UPDATE` | `on`: install new releases unattended, between jobs (default: only say one exists) |
| `PIN` | `vX.Y.Z`: stay on, or move to, exactly this release |
| `NOTIFY` | `on`: a desktop notification for each new [alert](#alerts) (default off) |

`~/.sat/projects/PROJECT/env`, if present, is sourced (with `set -a`) after the config by every job
and project script, so its lines keep their shell meaning: `export TOOL_HOME="$HOME/.tool-b"`,
`PATH=$HOME/opt/bin:$PATH`. Edit it or use `sat PROJECT env`; a change applies from the next job.

Change the rest of the config, then `sat PROJECT stop && sat PROJECT start` (the disk settings apply
at once).

### Disk space

Below the free-space floor, or over `MAX_GB`, a lane takes no new jobs: it first runs the project's
`sat/clean`, if it has one, and then waits until there is room, saying why in the log, in
`sat status` and in the live view. If free space falls below half the floor while a job runs, the
job is stopped and handed back to the hub, so a full disk never takes the machine down with it. The
project's size is measured at most every 30 minutes, at low priority.

## Scheduled drains

A project can drain by itself at a time of day, so a node lends its CPU until, say, the evening
and is idle when you want the machine back:

```sh
sat hello schedule start 18:00 daily     # stop taking jobs at 18:00 every day
sat hello schedule end 19:00 daily       # be stopped by 19:00 every day
sat hello schedule end 07:30 2026-11-03  # once, on that day (no date: the next 07:30)
sat hello schedule                       # what is scheduled, and when it drains next
sat hello schedule off
```

or `D` on a project in the live view, a dialog with the same choices (Save, Remove schedule,
Cancel) that shows what the schedule would do as you type.

- **start** drains at the time: no new jobs from then, and the service stops once the running
  ones finish, as `sat drain`.
- **end** has the service stopped by the time. It stops taking jobs once a typical job would no
  longer finish before it: at the time less the 80th percentile of how long this project's jobs
  took on this node (the last 50 that passed or failed, within 14 days). With fewer than 5 such
  jobs it uses the longest of them, and with none `JOB_EST_MIN` (30 minutes by default); `sat
  PROJECT schedule`, `sat status`, the dialog and Details say which. A job already running when
  the drain begins still runs to its end, so one that takes longer than most can run past the
  time.
- Times are this machine's local wall-clock time. A daily schedule drains at that hour every
  day, through daylight-saving changes; a time the clocks skip (02:30 on the night they go
  forward) is taken as the same moment after the jump.
- A one-time schedule removes itself once its drain has begun. A daily one stays: it is kept in
  `state/schedule.json`, apart from the drain flag, so `start` (which cancels a drain that is
  still pending), `stop` and a shutdown leave it in place, and `sat start` after a scheduled
  drain carries on until the next day's.

The service enforces the schedule, not the live view, so it holds with nothing open: the agent
reads `state/schedule.json` every couple of seconds and again before each claim, and shows its
plan in `state/agent.json` (`sat status`: "drain at 18:40 today (done by 19:00, p80 20m)"; the
live view: "drain 18:40" on the project's row, the whole line in Details). A schedule only does
something while the service runs.

**Asleep or off at the time.** A drain may begin up to 15 minutes late: a node that wakes from a
suspend (or starts) within that window drains then. Later than that the occurrence is skipped,
and the log says so: a daily schedule waits for the next day rather than stop the node hours after
the time it was meant for, and a one-time schedule that was missed is removed. Starting the
service inside an end-anchored window (when a typical job would no longer be done in time) drains
it at once: that is what the schedule asks. A schedule saved after the day's time has passed
starts with the next day's.

The agent records each job it finishes (start, end, outcome) in `state/history.jsonl`, the last
200, for these estimates.

## Alerts

Each agent watches for what someone should know about, and writes it, with its level and when it
was first seen, into its state file. An alert clears itself when the condition ends.

| alert | level | when |
|---|---|---|
| a lane stalled | warn | a job has shown nothing new for `STALL_MIN` minutes (default 30): no output, no progress line, no change to any other `state/slotN.*` file it keeps (a transcript, say) |
| a long job | warn | a job has run for `JOB_WARN_MIN` minutes (unset: never) |
| the hub unreachable | error | for more than 5 minutes |
| a protocol mismatch | error | the hub speaks another protocol: nothing is claimed |
| disk | warn | below the free-space floor, or over `MAX_GB`: no work is taken |
| requirements | error | missing, so the service won't start (`sat PROJECT check`) |
| restarts | error | the service started 3 times in 15 minutes |
| the service failed | error | systemd reports it failed (seen by `status`, `alerts` and the live view, as the agent is gone) |
| an update | info | a newer release is available |

`STALL_MIN` and `JOB_WARN_MIN` are read from the project's config (or `~/.sat/config`) as they
change. `sat status` adds a line per alert, `sat alerts` lists them (`-w` keeps it live), and the
live view badges the project, lists them under its Alerts tab (`!`), and counts them in Details.
A view's own `alert` blocks show there too ([projects.md](projects.md#json-views)). An agent that is
not running keeps only the alerts that say why (requirements, restarts).

**Desktop notifications** are off by default. With `NOTIFY=on` in `~/.sat/config`, each agent sends
one through `notify-send` for each new alert, never again while it stays (a restart of the agent
remembers what it sent), and again only if it clears and comes back. The service has no session's
environment, so the agent finds the session bus at `/run/user/UID/bus` when `DBUS_SESSION_BUS_ADDRESS`
is unset; with no `notify-send` or no bus it logs why, once per alert.

## Updates

New versions of `sat` come from this repository's GitHub releases, never from a hub. By default a
node only notes that one exists (it asks at most once a day), in `sat status` and the live view.

`sat self-update` shows the release notes and asks. It then downloads the binary for this machine,
checks it against the release's `checksums.txt`, and checks its build attestation with
`gh attestation verify` when `gh` is installed and logged in (it says so when it couldn't). Only
then does it swap the binary in, at once: it waits for no job.

**Running jobs keep running.** An agent whose binary is replaced (by `self-update`, `AUTO_UPDATE`,
or by hand) re-executes itself within seconds. The process stays the same (its PID, so the
service and its control group stay up), and its jobs, each in its own process group, keep running.
The new agent adopts them from the lanes' state files (`state/slotN.job`: the claim, the job's PID,
process group and start time, its result and progress files): it checks each process is still
that job, keeps renewing its lease, waits for it, and reports it from its exit status as always
(each job runs under a small wrapper that also writes that status to `state/slotN.exit`). A job
that ended with no status recorded is handed back. Idle lanes keep claiming throughout, and
`sat status` and the live view say "updated from A to B, adopted N running jobs" for the next hour.
An agent from before this behaviour still finishes its jobs first, that one time.

This covers re-executing, not crashes: when the agent itself dies, systemd stops the whole control
group (`KillMode=control-group`), jobs included, and the unit's stop hands them back; the restarted
agent finds nothing to adopt. `sat stop` and `sat drain` are as before: stop hands the running jobs
back now, drain lets them finish.

With `AUTO_UPDATE=on` in `~/.sat/config`, the agents do the same unattended: one downloads and
checks the release and swaps it in, and each re-executes and adopts its jobs. `PIN=vX.Y.Z` holds the node on
one version. The hub states the protocol it speaks; a node that doesn't speak it claims nothing
from that hub, and says why, until one of them is updated.

## Removing a node

```sh
sat uninstall PROJECT     # one project
sat uninstall             # everything
```

It lists exactly what it will remove and asks: it stops and disables the project's service, hands
its running jobs back and removes `~/.sat/projects/PROJECT`. While other projects stay on that hub,
it asks the hub to drop PROJECT from the node's key (`sat-hub --forget PROJECT`; a hub too old for
that is not asked, and its line stays as it is). When no project is left on a hub, it asks that hub
to forget the node's key (`sat-hub --forget`, which removes only this node's line) and drops the
SSH alias; with the last project it also removes `~/.sat`, the unit, the
`~/.local/bin/sat` link and the node key. It never removes packages: if `sat install` installed
any, it lists what it ran, and they are yours to keep or remove.

If the hub can't be reached, delete the node's line from its `authorized_keys` yourself
([hub.md](hub.md#removing-a-node)).

## Moving from the bash node

```sh
sat migrate
```

lists what it will do and asks. It stops each project's service if it runs (its jobs go back to the
hub), moves `~/sat/PROJECT` to `~/.sat/projects/PROJECT`, runs the bundle's `sat/moved OLD NEW` if
it has one (for paths a project keeps, such as git worktrees: see
[projects.md](projects.md#the-bundle)), installs the binary and the new unit, keeps each project's
autostart setting and starts again what was running. It removes the bash node's scripts
(`~/sat/bin/sat-agent`, `sat-call`, `sat-lib`) and replaces `~/.local/bin/sat` with a link, but only
files that are recognisably the bash node's own.

The legacy bash node is set up with `node/sat-setup` from the hub's checkout
(`bash ~/sat-setup HUB_IP PROJECT [HUB_USER]`) and controlled with the same `sat PROJECT ...`
commands, without the live view, `env`, updates or uninstall.
