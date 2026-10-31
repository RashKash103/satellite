# Lessons from the first project

The first real project on Satellite was a build pipeline: nodes ran its pre-merge checks, and later
its coding-agent tasks. Everything below went wrong at least once. Read it before you write your
hooks and bundle; most of it is easy to get right on the first try and slow to diagnose later.

## Sharing work with the hub's own workers

**Give nodes first pick, or they starve.** If the hub's own workers take from the same queue, they
usually win: they poll more often than a node's idle 45 s, and they take the next item the moment
they finish one. The node never got an item until the hub's workers left it room:

- in `claim`, when there is nothing for a lane, touch a marker such as `idle/HOST-SLOT` (remove it
  when the lane claims);
- in the hub's workers, while a fresh marker exists (under a minute old), leave a new item in the
  queue for the node;
- but only for a bounded time (2 minutes worked): a node may skip some items (below), and those
  must still be taken.

**Use the hub's lock.** `claim` edits the same queue as the hub's workers. Take the lock they take,
not a lock of your own.

**Keep items a node holds visible to the hub's logic.** If the hub avoids running related items
side by side (two that touch the same files, say), it must see the node's items too. It should not
count them against its own capacity, though: they don't load the hub.

**A daemon edited in place keeps its old logic, and bash may not survive the edit.** Bash reads a
script as it runs it: replace scripts by writing a new file and renaming it over the old one, never
by editing in place. The running process keeps the old version until it restarts, so either restart
it between items or let it re-exec itself when its file changes.

**A daemon that re-execs can keep its children running, if it planned for it.** `exec` keeps the
PID, so the children stay its children and can still be waited for, but only what was written to
disk survives: what they were, and where their output goes. Give children files, not pipes (a pipe
to the old process is gone, and the child dies writing to it), record each one's PID and start
time (a PID alone may be reused), and have each one write its own exit status to a file, for the
moment no one is waiting. Sat's agent does this to update without stopping jobs that run for
hours.

## Claim filters must agree with the hub

**Compute a filter exactly as the hub does, at the same point.** The hub refused branches that
deleted more than N lines, measured after rebasing onto main. The node's `claim` measured the same
thing before the rebase, so main's newer commits counted as deletions, and the node skipped every
item. Before a rebase, measure from the fork point (`git diff main...HEAD`), not against main's tip
(`git diff main HEAD`).

**Safety checks the hub runs must run in the job too, or be left to the hub.** A check the hub
makes after its own preparation step (a rebase, a fetch) can't be done in `claim`. Run it in the job
at the same point. If it trips and the item is one the hub must handle itself, mark it (`put` a
skip file the `claim` hook honours) and hand it back, or the node will claim it again in a loop.

**Results go stale.** Work can change on the hub while a node has it: a new commit on the branch,
an edited input. Put what identifies the version in the claim line (a commit hash), and have `ok`
compare it and requeue rather than record an old result.

**Keep the hub's bookkeeping consistent when a node's verdict differs from an earlier one.** An
item that had passed once and was already queued later failed on a node, because main had moved.
Take it out of every downstream queue as well, or the next stage trips over it.

## Trusting a node's results

**A node is a different machine: prove it agrees with the hub before you trust it.** A much faster
node failed two UI tests every time; the hub passed them every time. Same code, same software
renderer: the faster machine exposed a timing race in the application that the slower hub hid.
Until that was fixed, the node's red results were noise that sent healthy work to be "fixed".

- Make `sat/selftest` run a known-green sample of the real work, including the slow or
  timing-sensitive parts, not just a build.
- Give `claim` a pause file the hub's operator can touch, so node claims stop at once while you
  investigate.
- When a failure reproduces only on a node, fix it there: keep a node-only queue (one the hub's
  workers never read) and send the fix task to it.

**Don't assume GPU rendering under a virtual display.** Xvfb has no DRI3, so Vulkan can't present
and drivers fall back to llvmpipe (software) on every machine. That is good for determinism, and it
means differences between machines are about CPU speed and core count, not graphics.

## Tools that install themselves on first use

**A first install interrupted by a time limit can break for good.** A tool launched through `npx`
(a coding agent's MCP server) downloaded itself on first use. The launching program allowed 30 s,
killed the download half-way, and npx then reused the half-written cache every time. The symptom was
a module-resolution error deep in a dependency, which looked like a version problem and wasn't.

- Install such tools as a requirement step, `run …`, with no time limit, before anything launches
  them under one.
- In `sat/requirements`, start the tool only if it is already cached (a check must not install).
  If it crashes at start, print a `run` line that removes its cache entry and installs it again.
- Know what healthy looks like. An MCP server started with no input exits 0 at end of input;
  a crash exits with 1 and a stack trace. Accept 0, and 124 for a timeout, as healthy.

## Coding agents on a node

These apply to Claude Code; other agents have their own versions of the same issues.

- **A separate login** for the node's agents: set `CLAUDE_CONFIG_DIR` in the project's `env` file
  and sign in once with it. Create the env file before `sat setup`, so its requirement checks see
  that login and not the user's own.
- **Project MCP servers need approval** in headless runs, and approval is per directory. On the hub
  it may live in the repository's untracked `.claude/settings.local.json`
  (`"enabledMcpjsonServers": [...]`): have `sat/setup` write the same file into the node's clone.
- **Folders need trust, per login and per path.** A new login has never trusted the node's clone,
  and settings there (approvals included) don't apply until it does: open the agent once in that
  folder with the node's login and accept. Task worktrees inside the clone inherit the trust. Moving
  the clone (`sat migrate`) means trusting the new path again.
- **The node's usage limit is the node's own.** When the node's agent hits its quota, don't hand
  tasks back to churn through the queue: pause only that node with `sat/ready` until the reset time.
- **Shared state that travels through its own sync channel must be published first.** An issue
  tracker kept its board in a git branch that each machine syncs on demand. An issue created on the
  hub for a node's task wasn't there for the node's agent to close, because the hub hadn't synced.
  Publish such state before a node needs it, and pull before relying on what nodes changed.
- **Tell the agent where it is.** Paths in shared instructions point at the hub. Say in the prompt
  which paths differ on the node, which tools aren't installed there, and how many build jobs to use.

## Moving, disk and paths

- **Absolute paths break when a project directory moves.** Git keeps worktree paths absolute.
  Provide `sat/moved` (`git worktree repair` over the worktrees, then `git worktree prune`). Make
  sure the node has the latest bundle before `sat migrate`, since the hook comes from it.
- **Not every worktree lives inside the clone.** Tools can register their own elsewhere (an issue
  tracker kept its state branch in a worktree under its own directory in `~`). Those don't move, but
  their `.git` file still points at the old clone. Repair them too: pass `git worktree repair` every
  registered worktree outside the old clone that still exists (from `git worktree list --porcelain`).
- **Build output is large.** One lane's build directory reached 40-50 GB. Set `MAX_GB` in
  `sat/defaults`, and give `sat/clean` something to remove (build output of idle lanes, leftover
  worktrees, old logs), never anything a running lane uses (`state/slotN.claim`).
- **Scratch filesystems can be below the floor.** Tests that run an agent under `/tmp` may find
  less free space than the 20 GB floor and quietly take no work: set `MIN_FREE_GB=0` and
  `MIN_FREE_PCT=0` in test configs.

## Requirement checks

- **Check commands, not package names.** Package names change between releases (a `pkg-config`
  package became `pkgconf`). Test `command -v`, then print the package to install.
- **Read tool versions carefully.** Some print several lines with a version on each; take the first
  (`awk 'NR==1'`).
- **Pin what jobs rely on**, and check the pinned version, not just presence.
- **Services don't see shell PATH additions.** A systemd service reads no shell profile, so tools
  that nvm, cargo or `~/.local/bin` put on your shell's PATH are missing for it: a check run from
  that shell passed while the service refused to start. Put such lines in the project's `env` file
  (`. "$HOME/.nvm/nvm.sh"`, `PATH=$HOME/.cargo/bin:$PATH`), and check in the service's environment
  (`sat PROJECT check` does: the user manager's, plus the project's config and `env`).
- **Ship what the checks read.** If a check needs a file from the hub (a toolchain pin), put it in
  the bundle; the node may not have a clone yet when the check first runs.

## Shell pitfalls in hooks and jobs

- `exit` inside `$( … )` leaves only the subshell. `sha=$(push)` hid a failed push and reported an
  empty hash: write `sha=$(push) || exit 10`.
- Decide where an item goes before deleting the markers that say where it came from. A task from a
  node-only queue went back to the general queue because its "came from" marker was removed first.
- Validate every name a node sends (`get`, `put`, ids) against a fixed pattern, never a path.
- Never filter a security-relevant file on a value that might be empty: `grep -v ""` matches every
  line. Check the value first.

## The hub is live

The hub runs from its checkout: `~/.local/bin` links into it. A `git pull`, or an edit there, is
deployed the moment it lands. Keep hub changes backward compatible with the nodes already
connected. When a change needs data in place first (a key line that must carry a node name before
the gate requires one), put the data in place before deploying the code.
