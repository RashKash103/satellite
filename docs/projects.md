# Writing a project

A project has two parts: **hooks** that run on the hub, and a **bundle** of files the hub hands to
nodes. [`examples/hello`](../examples/hello) is a complete one in about 40 lines; read it alongside
this page.

## Hooks

One executable, named by `HOOKS` in the project file, called by `sat-hub` as
`HOOKS COMMAND ARGS...`. `sat-hub` has already checked that the node owns the lease (for `ok` and
`fail`) and that IDs and node names are plain names (letters, digits, `.`, `_`, `-`). Every hook gets
`SAT_PROJECT` in its environment.

| command | when | must |
|---|---|---|
| `claim HOST SLOT` | a node lane asks for work (under a per-project lock) | print `ID WORDS...` for the next job and mark it taken, or print nothing |
| `ok HOST ID WORDS...` | the job passed | record it; `WORDS` are what the job wrote to `SAT_RESULT`, and `SAT_CLAIM` holds the claim line |
| `fail HOST ID REASON` | the job failed | record it; the summary is on stdin |
| `requeue ID WHY` | the node handed the job back, or its lease expired | make the job available again |
| `get FILE` | a job runs `sat-call get FILE` | print the file, after checking FILE is one nodes may read |
| `put FILE` | a job runs `sat-call put FILE` | store stdin, after checking FILE; exit 2 if the project has none |
| `bundle` | a node starts, or every 5 minutes | write a tar.gz of the bundle to stdout |

A non-zero exit from `ok` or `fail` is passed back to the node and logged there. Exit 2 for "not
allowed".

Things to get right:

- **`claim` must be safe to run often.** Each idle lane calls it about every 45 seconds.
- **`ok` should verify what it is handed.** The words come from the node. If the job pushed a git
  ref, check that the ref is there and is what the words say.
- **`requeue` must accept jobs that are no longer running** (it can race with the hub's own
  workers) and do nothing harmful.
- **Validate every file name in `get` and `put`.** These are the only hooks that touch paths a node
  names. Allow a fixed pattern, never a path.
- **A job can come back stale.** If the work changed on the hub while a node had it (a new commit,
  an edited input), `ok` should requeue it rather than record the old result. `SAT_CLAIM` holds the
  claim line, so `claim` can print whatever `ok` will need to tell, such as a commit hash.

## The bundle

A directory tree with at least `sat/job`. Nodes keep it in `~/sat/PROJECT/bundle/` (`$SAT_BUNDLE`)
and replace it whole when the hub's copy changes; anything a job wants to keep belongs in
`$SAT_BASE/state/`.

| file | |
|---|---|
| `sat/job ID WORDS...` | **required.** Runs one job; see below. |
| `sat/defaults` | lines added to the node's config at setup, e.g. `SLOTS=2` |
| `sat/requirements` | prints what this node lacks for the jobs, as plan lines (below); nothing when it has it all |
| `sat/setup` | prepares the project's files under `$SAT_BASE` (a clone, a cache); run by `sat-setup` and `sat update` once nothing is missing. Make it safe to rerun, and never install software from it. |
| `sat/sync` | keeps node-side data current; run at agent start and hourly |
| `sat/selftest` | `sat PROJECT selftest`: check that this node can do the work |
| `sat/clean` | the node is low on disk (see [node.md](node.md#disk-space)): remove what the project can rebuild, such as build output, without touching lanes at work (`$SAT_BASE/state/slotN.claim` exists while lane N has a job) |
| `sat/ready` | run before each claim (with `SAT_SLOT`); exit non-zero to claim nothing for now, e.g. while this node's own quota or a local condition says to wait. Keep it fast. |

The bundle can also carry the project's own scripts for jobs to call.

## Requirements: report, never install

A node is often someone's own workstation, so Satellite never installs software on its own.
`sat/requirements` only **looks** at the node and prints one line per thing missing:

| line | |
|---|---|
| `apt PKG...` | Debian/Ubuntu packages |
| `run COMMAND` | any other install step, run as the user (write `sudo` in it if it needs root) |
| `note TEXT` | something that has to be done by hand |

Satellite adds its own needs (an SSH client, `tar`, systemd user services, ...) and then:

- `sat-setup`, `sat PROJECT check` and `sat PROJECT start` print the commands that would install
  what is missing, and stop. The user installs things themselves.
- `sat-setup --install` and `sat PROJECT install` list every step first, simulate each apt step
  (`apt-get -s`) to show exactly which packages it would install or upgrade, ask once, and only then
  run them. apt always runs with `--no-remove`, so it stops instead of removing a package.
- The agent will not start while anything is missing.

Keep `sat/requirements` fast and side-effect free: check versions where they matter (`run` a pinned
installer when the version is wrong), and print nothing when all is well.

## The job

`sat/job` is called with the claim line's words. Its exit code is the result:

| exit | meaning | `$SAT_RESULT` |
|---|---|---|
| `0` | passed | the words for `ok` (one line) |
| `1` | failed | a reason line, then the summary |
| other | couldn't run here: hand it back for another worker | ignored |

It runs in its own process group, which is killed if the lease is lost, and its output goes to
`~/sat/PROJECT/state/slotN.log`. Its environment:

| variable | |
|---|---|
| `SAT_PROJECT` | the project name |
| `SAT_BASE` | `~/sat/PROJECT`: the config is `$SAT_BASE/config`, state goes in `$SAT_BASE/state/` |
| `SAT_BUNDLE` | the bundle directory |
| `SAT_SLOT` | the lane number, from 1; use it to keep lanes' work directories apart |
| `SAT_NAME` | this node's name |
| `SAT_RESULT` | the file to write the result to |
| `PATH` | includes `sat-call` |

Talk to the hub with `sat-call COMMAND...` (`get`, `put`, `status`). For git, use the hub alias:
`git fetch "$HUB:/path/to/repo" BRANCH`, `git push "$HUB:/path/to/repo" HEAD:refs/sat/$ID`. For
directories, `rsync -a "$HUB:/path/to/data/" local/`. Source `$SAT_BASE/config` to get `HUB`.

Exit with "other" rather than 1 when the failure is the node's fault (a full disk, a network
outage), so the job is retried elsewhere instead of being recorded as failed.

## Walkthrough: `examples/hello`

The hub keeps jobs as files in a spool directory: `queue/ID` holds a number.

- **`hub-hooks`**: `claim` moves the oldest file from `queue/` to `running/` and prints its name;
  `ok` and `fail` move it to `done/` or `failed/` with the result; `requeue` moves it back to the
  front of the queue; `get` lets a job read its own input from `running/`; `bundle` tars
  `bundle/`.
- **`bundle/sat/job`**: reads its input with `sat-call get ID`, fails with a reason if it is not a
  number, and otherwise writes a checksum as its result. The input `crash` makes it exit 3 once, to
  show a job being handed back and retried.
- **`bundle/sat/defaults`**: two lanes per node.
- **`bundle/sat/requirements`**: asks for `coreutils` if `seq` or `sha256sum` is missing.
- **`project`**: the hub's project file. It allows no git, pushes or rsync.

To try it on one machine, see [testing.md](testing.md); on two, follow the quick start in the
[README](../README.md#quick-start).
