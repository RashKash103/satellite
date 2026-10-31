# How it works

## Roles

- The **hub** owns the work. It keeps the queue, decides what a job is, and records results. It
  must be able to do all of that on its own: Satellite only adds helpers, it never becomes a
  dependency.
- A **node** lends CPU. It runs `sat agent PROJECT` as a systemd user service, one instance per
  project, with `SLOTS` lanes. Each lane claims a job, runs it, reports it and claims the next.
  (The legacy bash node runs `sat-agent` the same way.)
- A **project** is one kind of work. On the hub it is a file in `~/.config/sat/projects/` that
  names a hooks script and lists what its nodes may reach. Several projects can share one hub and
  one node.

Nodes always start the conversation, over SSH, as the hub user, with a key that the hub's
`authorized_keys` ties to `sat-ssh-gate`. The hub never connects to a node. The Go node keeps one
connection per hub open and runs each call as a session on it; jobs reach the hub (for git and
rsync) through the system ssh and the `sat-hub-IP` alias.

On each new connection the node asks `sat-hub --protocol`. The hub prints the protocol it speaks
(`1`); a node that speaks another, or a hub too old to answer, means the node claims nothing from
it and says why in its log, `sat status` and the live view. Bash nodes don't ask: the protocol they
speak is `1`.

## A job's life

1. **Claim.** Unless the bundle's `sat/ready` says "not now" or the node is short of disk
   ([node.md](node.md#disk-space)), a lane runs `sat-hub PROJECT claim NODE SLOT`. On the hub, `sat-hub` takes a per-project
   lock and calls the project's `claim` hook, which picks a job, marks it taken in the project's own
   bookkeeping and prints `ID WORDS...`. `sat-hub` writes a lease file
   (`~/.config/sat/leases/PROJECT/ID`: node, slot, time, the claim line) and returns the line.
2. **Run.** The lane runs the bundle's `sat/job ID WORDS...` in its own process group, through
   bash with the config and the project's `env` sourced. Every 2 minutes it renews the lease
   (`renew` touches the lease file). If a renewal answers "gone" (exit 3), the lease was reaped or
   taken by another node, and the lane kills the job; if the disk is nearly full, it kills the job
   and hands it back. The job may append progress lines to `$SAT_PROGRESS`, which the live view
   shows.
3. **Report.** By the job's exit code:
   - `0`: passed. The lane sends `ok NODE ID WORDS...`, the words being what the job wrote to
     `$SAT_RESULT`.
   - `1`: failed. The first line of `$SAT_RESULT` is the reason; the rest is sent on stdin as the
     summary.
   - anything else: the job could not run here (a crash, a missing tool, a lost network). The lane
     sends `release`, and the hub's `requeue` hook puts it back.

   `sat-hub` checks that the reporting node owns the lease, calls the project's `ok`, `fail` or
   `requeue` hook, and deletes the lease.

## When a node vanishes

Shutting down, sleeping, or losing the network all look the same from the hub: renewals stop. The
reaper (`sat-hub --reap`, every minute) finds leases older than the project's `LEASE` and calls its
`requeue` hook with the reason. The hub's own workers pick the job up as if no node had ever
existed. If the node comes back and reports the job late, `sat-hub` refuses: it no longer owns the
lease.

A clean stop is quicker:

- `sat drain`: no new claims; the service stops when the running jobs finish.
- `sat stop` or a normal shutdown: systemd's `ExecStop` releases the running jobs at once, then stops
  the processes.

## Keeping nodes current

The hub serves each project's **bundle** (`sat-hub PROJECT bundle`, a tar.gz from the `bundle`
hook). Nodes fetch it at start and every 5 minutes, and swap it in whole only when it changed, so
editing a job on the hub reaches every node without touching them. The project's `sat/sync`, if
any, runs at start and hourly, for data a node keeps locally. `sat update` fetches the bundle at
once and reruns the project's `sat/setup`. The node's own binary comes from this repository's
GitHub releases instead ([node.md](node.md#updates)). Satellite installs no
software on a node unless asked ([projects.md](projects.md#requirements-report-never-install)).

## Moving data

Jobs carry little in the claim line (an ID and a few words). For more, a project can let nodes:

- **fetch git repositories** listed in `GIT_REPOS`, and **push results** under `PUSH_PREFIX` (for
  example `refs/sat/`); the hub's `ok` hook then checks and uses the pushed ref;
- **copy directories out** with `rsync`, for those in `RSYNC_DIRS`;
- **read and write small state files** with `sat-call get FILE` / `put FILE`, through the project's
  `get` and `put` hooks, which decide which names are allowed.

Anything else a job needs it fetches itself, from wherever it likes: public package registries, a
git host, and so on.

## Files on the hub

```
~/.config/sat/projects/PROJECT     project file (sourced by sat-hub and sat-ssh-gate)
~/.config/sat/leases/PROJECT/ID    one per job a node holds; the mtime is the last renewal
~/.config/sat/gate.log             commands sat-ssh-gate refused
```

## Files on a node

```
~/.sat/bin/sat                      the binary: the agent, the control command, the live view
~/.sat/bin/sat-call                 a link to it, which jobs call the hub with
~/.sat/config                       node-wide settings: AUTO_UPDATE, PIN, TRANSPORT
~/.sat/installed.log                the commands `sat install` ran
~/.sat/update.json                  the last release check
~/.sat/projects/PROJECT/config      HUB, SAT_NAME, SLOTS and the project's own settings
~/.sat/projects/PROJECT/env         optional, sourced before each job (e.g. PATH)
~/.sat/projects/PROJECT/bundle/     the project's files from the hub
~/.sat/projects/PROJECT/state/      agent.json (what the live view reads), agent.log, lane claims,
                                    logs and progress, the drain flag, the project's own state
~/.config/systemd/user/sat@.service the unit, one instance per project
~/.ssh/sat_ed25519, ~/.ssh/config   the node key and the sat-hub-IP alias
```

The legacy bash node keeps `~/sat/PROJECT/` (the same contents), `~/sat/bin/` and
`~/.local/bin/sat`.
