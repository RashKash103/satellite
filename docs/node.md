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
   (the short hostname), using your own key login; a rerun replaces the line instead of adding one,
   and an empty or malformed key is refused;
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

`sat` on its own is the **live view**: each project's service, the hub (reachable, its protocol,
last contact), disk room, and each lane's job, how long it has run, its last renewal and its own
progress, with the selected lane's log below. Keys: `↑`/`↓` pick a lane, `s` start, `d` drain,
`x` stop (asks first), `l` hide or show the log, `q` quit. It reads what the agents write and opens
no connection of its own.

`sat [PROJECT] COMMAND`. PROJECT can be left out when the node has only one (`sat status` then shows
them all).

| command | |
|---|---|
| `start` | start taking jobs |
| `drain` | take no new jobs; stop once the running ones finish (use before shutting down) |
| `stop` | stop now; running jobs go straight back to the hub |
| `status` | running or not, autostart, the hub and its protocol, jobs in progress, disk room, the last log lines |
| `logs` | follow the service log |
| `check` | what this node is missing, and the commands that would install it |
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
| `sat version` | |

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
| `TRANSPORT` | `native` (default): `sat` holds one SSH connection to the hub open; `exec`: it runs the system `ssh` with the alias for each call, as the bash node did (setup picks this when only the system ssh gets through, say through a jump host) |

Projects may add their own keys (through `sat/defaults`) for their jobs to read. Keys may also go
in `~/.sat/config`, for every project on the node, along with:

| key | |
|---|---|
| `AUTO_UPDATE` | `on`: install new releases unattended, between jobs (default: only say one exists) |
| `PIN` | `vX.Y.Z`: stay on, or move to, exactly this release |

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

## Updates

New versions of `sat` come from this repository's GitHub releases, never from a hub. By default a
node only notes that one exists (it asks at most once a day), in `sat status` and the live view.

`sat self-update` shows the release notes and asks. It then downloads the binary for this machine,
checks it against the release's `checksums.txt`, and checks its build attestation with
`gh attestation verify` when `gh` is installed and logged in (it says so when it couldn't). Only
then does it drain the running services, swap the binary in, and restart them.

With `AUTO_UPDATE=on` in `~/.sat/config`, the agents do the same unattended: one downloads and
checks the release, and each restarts once its running jobs finish. `PIN=vX.Y.Z` holds the node on
one version. The hub states the protocol it speaks; a node that doesn't speak it claims nothing
from that hub, and says why, until one of them is updated.

## Removing a node

```sh
sat uninstall PROJECT     # one project
sat uninstall             # everything
```

It lists exactly what it will remove and asks: it stops and disables the project's service, hands
its running jobs back and removes `~/.sat/projects/PROJECT`. When no project is left on a hub, it
asks that hub to forget the node's key (`sat-hub --forget`, which removes only this node's line)
and drops the SSH alias; with the last project it also removes `~/.sat`, the unit, the
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
