# Writing a project

Before you start, read [lessons.md](lessons.md): what went wrong for the first real project.

A project has two parts: **hooks** that run on the hub, and a **bundle** of files the hub hands to
nodes. [`examples/hello`](../examples/hello) is a complete one in a few short files; read it alongside
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

A directory tree with at least `sat/job`. Nodes keep it in `~/.sat/projects/PROJECT/bundle/` (`$SAT_BUNDLE`)
and replace it whole when the hub's copy changes; anything a job wants to keep belongs in
`$SAT_BASE/state/`.

| file | |
|---|---|
| `sat/job ID WORDS...` | **required.** Runs one job; see below. |
| `sat/defaults` | lines added to the node's config at setup, e.g. `SLOTS=2` |
| `sat/requirements` | prints what this node lacks for the jobs, as plan lines (below); nothing when it has it all |
| `sat/setup` | prepares the project's files under `$SAT_BASE` (a clone, a cache); run by `sat setup` and `sat update` once nothing is missing. Make it safe to rerun, and never install software from it. |
| `sat/sync` | keeps node-side data current; run at agent start and hourly |
| `sat/selftest` | `sat PROJECT selftest`: check that this node can do the work |
| `sat/clean` | the node is low on disk (see [node.md](node.md#disk-space)): remove what the project can rebuild, such as build output, without touching lanes at work (`$SAT_BASE/state/slotN.claim` exists while lane N has a job) |
| `sat/ready` | run before each claim (with `SAT_SLOT`); exit non-zero to claim nothing for now, e.g. while this node's own quota or a local condition says to wait. Keep it fast. |
| `sat/views/NAME` | a view: shows something about the project or a lane, in `sat PROJECT view NAME` and as a tab in the live view ([below](#views)) |
| `sat/moved OLD NEW` | `sat migrate` moved the project's directory from OLD (`~/sat/PROJECT`) to NEW (`~/.sat/projects/PROJECT`): repair whatever records the old path, such as git worktrees (`git -C "$NEW/repo" worktree repair "$NEW"/work/*`), absolute paths in the config or caches. Runs once, before the service starts again; a non-zero exit leaves the service stopped. |

Every script runs as a job does (below): through bash, with `$SAT_BASE/config` and `$SAT_BASE/env`
sourced and the `SAT_` variables set.
The bundle can also carry the project's own scripts for jobs to call.

## Requirements: report, never install

A node is often someone's own workstation, so Satellite never installs software on its own.
`sat/requirements` only **looks** at the node and prints one line per thing missing:

| line | |
|---|---|
| `apt PKG...` | Debian/Ubuntu packages |
| `run COMMAND` | any other install step, run as the user (write `sudo` in it if it needs root) |
| `note TEXT` | something that has to be done by hand |

Satellite adds its own needs (an SSH client, systemd user services; the bash node also wants `tar`
and the like) and then:

- `sat setup`, `sat PROJECT check` and `sat PROJECT start` print the commands that would install
  what is missing, and stop. The user installs things themselves.
- `sat setup --install` and `sat PROJECT install` list every step first, simulate each apt step
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

It runs in its own process group, which is killed if the lease is lost (or the disk is nearly
full), and its output goes to `$SAT_BASE/state/slotN.log`. It starts through bash with the node's
config and the project's `env` file sourced (`set -a`), so every setting in them is in its
environment, with shell semantics. Besides those:

| variable | |
|---|---|
| `SAT_PROJECT` | the project name |
| `SAT_BASE` | `~/.sat/projects/PROJECT` (`~/sat/PROJECT` on the bash node): the config is `$SAT_BASE/config`, state goes in `$SAT_BASE/state/` |
| `SAT_BUNDLE` | the bundle directory |
| `SAT_SLOT` | the lane number, from 1; use it to keep lanes' work directories apart |
| `SAT_NAME` | this node's name |
| `SAT_RESULT` | the file to write the result to |
| `SAT_PROGRESS` | a file the job may append progress lines to; the last one shows in the live view and `sat status`. A line may start with a percentage: `40% linking`. Optional, and new with the Go node: on the bash node it is unset, so write `[ -z "$SAT_PROGRESS" ] \|\| echo ... >> "$SAT_PROGRESS"` (which, unlike `[ -n ... ] && echo`, doesn't fail as a script's last line). |
| `PATH` | starts with `~/.sat/bin`, which holds `sat-call` |

Talk to the hub with `sat-call COMMAND...` (`get`, `put`, `status`). For git, use the hub alias:
`git fetch "$HUB:/path/to/repo" BRANCH`, `git push "$HUB:/path/to/repo" HEAD:refs/sat/$ID`. For
directories, `rsync -a "$HUB:/path/to/data/" local/`. `HUB` comes from the config (on the bash
node, source `$SAT_BASE/config` to get it).

Exit with "other" rather than 1 when the failure is the node's fault (a full disk, a network
outage), so the job is retried elsewhere instead of being recorded as failed.

## Views

A view shows something about the project while it works: the hub's queue, a lane's progress
through its steps, a test run's failures so far. It is an executable in the bundle at
`sat/views/NAME` (NAME: letters, digits, `.`, `_`, `-`), shown by `sat PROJECT view NAME`, listed by
`sat PROJECT views`, and offered as a tab in the live view's right pane.

### The header

Among its first 10 lines, a view may say how it runs:

```sh
# sat-view: title=Queue; scope=project; mode=snapshot; every=10; timeout=10; format=text
```

| key | values | default | |
|---|---|---|---|
| `title` | text | NAME | what the view is called in lists and tabs |
| `scope` | `project`, `lane` | `project` | a lane view is about one lane's job: it is offered only for a lane that has one, and gets its variables (below) |
| `mode` | `snapshot`, `stream` | `snapshot` | a snapshot prints the current state and exits; a stream keeps printing as things happen |
| `every` | seconds | 5 | how often a snapshot runs again while shown live |
| `timeout` | seconds | 10 | a snapshot run (and a stream's once run) is killed past it, and the display says so |
| `format` | `text`, `json` | `text` | what it prints: lines, or blocks for sat to draw ([below](#json-views)) |

Keys are separated by `;`, and seconds may have a fraction (`every=0.5`). A key sat doesn't know is
ignored, so a bundle can be written for a newer sat; a known key with a bad value makes the view
unusable, and `views`, `view` and its tab say why.

### Running

| | snapshot | stream |
|---|---|---|
| once (`sat PROJECT view NAME`) | runs, prints what it printed | runs with `SAT_VIEW_ONCE=1`: print the current state and exit |
| live (`-w`, or its tab) | runs every `every` seconds, each run replacing the last (redrawn in place on a terminal) | runs once and is followed: its output is shown as it comes, and it is stopped when the view closes |

A view runs as a job does: through bash, with the config and the project's `env` sourced
(`set -a`), `~/.sat/bin` (with `sat-call`) first on `PATH`, and `SAT_PROJECT`, `SAT_BASE`,
`SAT_BUNDLE` and `SAT_NAME` set. It runs in its own process group, all of which is stopped (TERM, then
KILL a second later) when it times out or its view closes. It must not ask for input: stdin is
empty. Its stderr is shown with its output (on the command line, on stderr). Besides:

| variable | |
|---|---|
| `SAT_VIEW` | the view's name |
| `SAT_VIEW_MODE` | `once` or `live` |
| `SAT_VIEW_ONCE` | `1` for a stream view's once run; unset otherwise |
| `SAT_VIEW_WIDTH` | the columns it has (80 when sat can't tell, as in a pipe) |
| `SAT_VIEW_COLOR` | `1` when colour is on; unset otherwise |

and for a lane view:

| variable | |
|---|---|
| `SAT_SLOT` | the lane number |
| `SAT_JOB` | the job's ID |
| `SAT_CLAIM` | the claim line (`ID WORDS...`) |
| `SAT_LOG` | the job's output, `$SAT_BASE/state/slotN.log` |
| `SAT_PROGRESS` | the job's progress file (read it; the job writes it) |
| `SAT_STARTED` | when the job started, in seconds since the epoch |

A text view may print ANSI colour codes; sat keeps those when colour is on (and drops any other
escape codes, which could move the cursor or clear the screen), and removes them all when it is off
(`NO_COLOR`, a pipe, `TERM=dumb`: as for sat's own output). Keep a snapshot fast: it runs every few
seconds while it is shown. A view that talks to the hub (`sat-call get`) opens a connection each run,
so give it a longer `every`.

On the command line, `sat PROJECT view NAME` picks a lane view's lane itself when only one lane has
a job; otherwise `--lane N` says which. `-w` runs until ctrl+c. In the live view, `w` moves to the
next view tab and `[` `]` go through all the tabs; a view runs only while its tab is shown, and runs
again (`r` does it at once) when the project's bundle changes. The live view remembers the tab chosen
for each project.

### JSON views

With `format=json`, a view prints one JSON document per run (a stream view: one per line, each
replacing the last), and sat draws it in its own style: charts in the live view and on a colour
terminal, plain text where there is no colour (`label  ███░░  70%` becomes `label 70%`). A document
that can't be read is shown as an error in the view's place.

```json
{"blocks": [
  {"type": "alert", "level": "warn", "text": "2 jobs are waiting for a lane"},
  {"type": "progress", "label": "finished", "value": 0.6, "note": "6 of 10 jobs"},
  {"type": "bar", "title": "Jobs", "data": [{"label": "queued", "value": 2}, {"label": "passed", "value": 6}]},
  {"type": "metric", "name": "queued", "value": 2, "unit": "jobs"},
  {"type": "kv", "rows": [["hub", "reachable"], ["lanes", 2]]},
  {"type": "table", "columns": ["job", "took"], "rows": [["j1", "3m"], ["j2", "4m"]]},
  {"type": "timeline", "items": [{"label": "fetch", "start": 1767225600, "end": 1767225660, "state": "done"},
                                 {"label": "build", "start": 1767225660, "state": "running"},
                                 {"label": "test", "state": "waiting"}]},
  {"type": "line", "title": "Build time", "series": [{"name": "s", "points": [[1, 61], [2, 58], [3, 64]]}]},
  {"type": "text", "lines": ["anything else, as lines"]}
]}
```

| block | fields | drawn as | plain |
|---|---|---|---|
| `text` | `lines` (or `text`, with newlines) | the lines (colour codes as for text views) | the lines |
| `kv` | `rows`: `[key, value]` pairs; `title` | aligned keys and values | the same |
| `table` | `columns`, `rows`; `title` | an aligned table | the same |
| `progress` | `label`, `value` (0 to 1), `note` | a bar and a percentage | `label 60% note` |
| `bar` | `data`: `[{label, value}]`; `title` | horizontal bars, with their values | `label  value` rows |
| `timeline` | `items`: `[{label, start, end, state}]`; `title` | a row per item across the span of them all, coloured by state, with its duration | `label  state  start-end  (duration)` |
| `metric` | `name`, `value`, `unit` | the value and a sparkline of its recent values | `name value unit (min, max over N)` |
| `line` | `series`: `[{name, points: [[x, y]...]}]`; `title` | a line chart, a line per series | each series' latest value, with its min and max |
| `alert` | `level` (`warn` or `error`), `text` | at the top of the view, marked; the project is highlighted in the Projects pane | `warn: text` |

`start` and `end` are seconds since the epoch; an item with no `end` is still going (up to now), and
`state` is `done`, `running`, `failed` or `waiting`. Values in `kv` and `table` rows may be strings,
numbers or booleans. sat keeps a metric's last 120 values, by name, for each view: in memory in the
live view, and in `$SAT_BASE/state/views/NAME.metrics.json` for `sat PROJECT view NAME -w` (a plain
`view` run reads that file but doesn't add to it). A block of a type sat doesn't know is shown as
such, so a bundle can use a newer sat's blocks; a known block with a bad field (a `progress` with no
`value`, an `alert` with another `level`) makes the document an error.

The Projects pane's highlight is from the last time the view ran in the live view; alerts that need
no view open are the agent's own ([node.md](node.md#alerts)).

## Walkthrough: `examples/hello`

The hub keeps jobs as files in a spool directory: `queue/ID` holds a number.

- **`hub-hooks`**: `claim` moves the oldest file from `queue/` to `running/` and prints its name;
  `ok` and `fail` move it to `done/` or `failed/` with the result; `requeue` moves it back to the
  front of the queue; `get` lets a job read its own input from `running/`, and the views read
  `.counts`, how many jobs are in each state; `bundle` tars `bundle/`.
- **`bundle/sat/job`**: reads its input with `sat-call get ID`, fails with a reason if it is not a
  number, and otherwise writes a checksum as its result, saying what it does on its output and in
  `$SAT_PROGRESS`. The input `crash` makes it exit 3 once, to show a job being handed back and
  retried.
- **`bundle/sat/views/`**: `queue`, a text snapshot of the hub's queue (`sat-call get .counts`) and
  of what this node did, coloured when `SAT_VIEW_COLOR` is set; `board`, the same counts as a JSON
  view with a progress, a bar and a metric; `output`, a lane stream view that follows the job's own
  output (`$SAT_LOG`), or prints its last lines once.
- **`bundle/sat/defaults`**: two lanes per node.
- **`bundle/sat/requirements`**: asks for `coreutils` if `seq` or `sha256sum` is missing.
- **`project`**: the hub's project file. It allows no git, pushes or rsync.

To try it on one machine, see [testing.md](testing.md); on two, follow the quick start in the
[README](../README.md#quick-start).
