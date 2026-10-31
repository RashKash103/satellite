# satellite

> [!WARNING]
> **This project was built by AI.** Satellite's code, tests and documentation were written by AI
> agents (Claude Opus 5.5) directed by RashKash103, and have not been fully reviewed by a human.
> It is young: its tests run on one machine, and it has not yet run across two. It runs commands on
> your machines and opens an SSH entry point on the hub: read [docs/security.md](docs/security.md)
> before you use it, and don't use it anywhere a mistake would be costly without checking it
> yourself.

**Lend a second machine's CPU to a build pipeline for as long as it happens to be on.**

![Shell](https://img.shields.io/badge/100%25-bash-4EAA25)
![Platform](https://img.shields.io/badge/platform-Linux%20%2B%20systemd-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Status](https://img.shields.io/badge/status-early-orange)

You have one machine that runs a queue of work: builds, test runs, checks. You have another, often
faster, machine that is only switched on part of the day. Satellite lets the second machine take
jobs from the first while it is on, and lets it disappear at any moment without anything waiting
for it.

- **Pull, not push.** Nodes ask the hub for work over SSH. The hub never connects to a node, so a
  node can sit behind NAT, sleep, or be switched off.
- **Leases.** A node renews each job's lease every 2 minutes. A job whose node goes quiet goes back
  to the hub's own queue after `LEASE` seconds (10 minutes by default).
- **Any project.** The engine knows nothing about your work. A project plugs in with one hooks
  script on the hub and a few files the hub hands to nodes.
- **Locked-down keys.** A node's key can only reach what its project file allows: fetch named git
  repositories, push under one ref prefix, copy named directories out, and talk to the lease engine.
- **Easy on and off.** `sat start`, `sat drain` (finish what's running, then stop), `sat stop`,
  `sat autostart on|off`. A node is a systemd user service.
- **Installs nothing behind your back.** Setup checks the node and lists what's missing with the
  commands to install it. `--install` runs them only after showing each one (apt changes
  simulated, never removing a package) and asking.
- **Plain bash.** No daemon to install on the hub beyond a one-minute reaper, and no database:
  leases are files.

## How it works

```mermaid
sequenceDiagram
    participant N as Node (sat-agent)
    participant G as Hub: sat-ssh-gate
    participant H as Hub: sat-hub + your hooks
    N->>G: ssh … sat-hub PROJECT claim
    G->>H: claim
    H-->>N: "ID words…" (lease taken)
    loop every 2 min while the job runs
        N->>H: renew ID
    end
    N->>H: ok ID result… / fail ID reason (summary on stdin)
    Note over H: no renewal for LEASE s → the reaper calls your requeue hook
```

More in [docs/how-it-works.md](docs/how-it-works.md).

## Quick start

On the **hub** (the machine that owns the work):

```sh
git clone https://github.com/RashKash103/satellite ~/satellite
~/satellite/hub/install                       # links the hub commands, installs the reaper unit
systemctl --user enable --now sat-reaper
cp ~/satellite/examples/hello/project ~/.config/sat/projects/hello   # edit HOOKS if the checkout is elsewhere
```

The hub also needs `sshd`, with key-only logins ([docs/hub.md](docs/hub.md)).

On the **node** (the part-time machine), once it can already log in to the hub with an SSH key:

```sh
scp HUB_USER@HUB:satellite/node/sat-setup ~/ && bash ~/sat-setup HUB_IP hello HUB_USER
sat hello check          # what's missing, if anything, and how to install it
sat hello selftest
sat hello start
```

Then give the hub some work and watch it come back:

```sh
mkdir -p ~/.local/share/sat-hello/queue && echo 2000000 > ~/.local/share/sat-hello/queue/job1   # on the hub
sat hello logs                                          # on the node
cat ~/.local/share/sat-hello/done/job1                  # on the hub
```

## Documentation

| | |
|---|---|
| [How it works](docs/how-it-works.md) | roles, the lease lifecycle, what happens when a node vanishes |
| [Hub setup](docs/hub.md) | install, project files, sshd, the reaper, watching it |
| [Node setup and control](docs/node.md) | key login, `sat-setup`, the `sat` command, node config |
| [Writing a project](docs/projects.md) | the hooks contract, the bundle, the job environment, a walkthrough of `examples/hello` |
| [Security](docs/security.md) | what a node key can and can't do, and what you are trusting |
| [Testing](docs/testing.md) | the end-to-end test, and testing your own project without a second machine |
| [Roadmap](ROADMAP.md) | the Go port: a live view, one binary, updates, uninstall |

## Requirements

- **Both:** Linux, bash 4.4+, OpenSSH, coreutils, `tar`, `gzip`.
- **Hub:** an SSH server, `flock` (util-linux), and `git` and `rsync` if your projects use them.
- **Node:** systemd with user services (and `loginctl enable-linger` so the service runs without
  a login session).

## Layout

```
hub/install          set a machine up as a hub
hub/sat-hub          lease engine: claim, renew, ok, fail, release, get/put, bundle, status, --reap
hub/sat-ssh-gate     forced command for node keys
hub/sat-reaper       reaps silent leases every minute (hub/sat-reaper.service runs it)
hub/pre-receive      git hook: pushes through the gate may write only the project's PUSH_PREFIX
node/sat-setup       one-time node setup for a project
node/sat-agent       the node's service: N lanes claiming and running jobs
node/sat             control: start, stop, drain, status, logs, autostart, selftest, update
node/sat-call        call the hub's sat-hub from a job
node/sat-lib         requirement checks and confirmed installs
node/sat@.service    systemd user unit, one instance per project
examples/hello/      a complete small project
test/run             end-to-end test on one machine
```

## Contributing

Issues and pull requests are welcome. Run `test/run` and
[ShellCheck](https://www.shellcheck.net/) before you send a change; CI runs both.

## License

[MIT](LICENSE).
