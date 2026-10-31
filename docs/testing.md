# Testing

```sh
go test ./...     # the Go node: unit tests, and the end-to-end test against the bash hub
test/run          # the hub, and the legacy bash node, end to end
```

Both run a hub and a node on one machine, in scratch directories, without sshd or a second
machine, and touch nothing outside them. CI runs both, `go vet` and ShellCheck on every push.

## The Go end-to-end test (`e2e/`)

It runs the real bash hub (`hub/sat-hub`, `hub/sat-ssh-gate`, `examples/hello`) behind an SSH
server inside the test, written with Go's `x/crypto/ssh`: each node key is bound to a node name,
and each command runs the gate with `SSH_ORIGINAL_COMMAND`, as sshd does for a node key. The node is
the real `sat` (the test binary plays it) with its own `HOME`, a `known_hosts` for the test hub, and
a fake `systemctl` that only records what it is asked. It checks:

- jobs that pass, fail, and crash and are retried; no leases left; a drain stops the agent;
- `sat/ready` holding claims back; a lost lease killing the job's process group; progress from
  `$SAT_PROGRESS` in `state/agent.json`;
- the disk floor (sat/clean runs first, then no claims, said once) and a job stopped and handed back
  when the disk is nearly full;
- the project's `env` reaching jobs with shell semantics, and `sat PROJECT env`;
- two projects on one node: a second `sat setup` reusing the key and alias, each with its own lanes;
- a hub with another protocol, and one too old to state it: no claims, and why;
- the requirement gate: the agent won't start, `check`, `start` and `install` say what is missing
  and install nothing;
- the exec transport (the system ssh, through a shim);
- `sat uninstall`: asks, lists what it removes and what `install` ran, has the hub forget only this
  node's key line, removes the rest;
- keys limited to projects (the test server reads the hub's `authorized_keys` for the gate's
  arguments, as sshd does): another project's `sat-hub`, repository and directory refused and
  logged; a second `sat setup` adding its project to the existing line; `sat uninstall PROJECT`
  dropping it again, then the last uninstall removing the line, other lines untouched; and against
  a hub too old for that, `sat uninstall PROJECT` not asking it;
- `MAX_PER_NODE`: a node with two lanes holding one job at a time, without errors, and
  `sat-hub PROJECT nodes` showing it;
- `sat migrate` from the bash layout, with the bundle's `sat/moved`;
- the hello example's views against the running agent: listed, run once (asking the hub through
  `sat-call`), and a lane's stream followed as the job goes and stopped by ctrl+c;
- an alert raised by the agent (the disk floor), listed by `alerts` and `status`, notified once
  through a fake `notify-send`, and cleared.

It takes about 50 seconds and needs bash and coreutils. Timings are shortened with
`SAT_TEST_FAST=1`.

## `test/run`

An `ssh` shim on `PATH` drops ssh's options and host and hands the rest to the real `sat-ssh-gate`
as `SSH_ORIGINAL_COMMAND`. It checks:

- the gate's refusals, that it runs no shell on what it is sent, and that a key acts only as its
  own node;
- `sat-hub --protocol`, and `sat-hub --forget`: only through the gate, only the caller's own lines;
- keys limited to projects: another project's `sat-hub`, repository and directory refused and
  logged, a malformed list refused, a legacy key (no list) reaching every project; `--forget
  PROJECT` dropping one project, then the line with the last, leaving legacy lines alone;
- the key line `sat setup` writes (its script, run as the user's own login): a new key listed for
  its project, a second project added to the same line, a legacy line kept legacy, an empty key
  refused;
- `MAX_PER_NODE` and `MAX_TOTAL`: an empty claim at the limit, not an error; `sat-hub PROJECT
  nodes` and `--nodes`, and a node seeing only its own rows through the gate;
- the push guard: pushes under the prefix pass, others are refused;
- leases: only the owner can renew or report a job, and a silent lease is reaped back;
- the bash node agent running `examples/hello` with two lanes: the disk floor, `sat/ready`, a job
  that passes, one that fails, one that crashes and is retried, and a drain;
- requirement checks: what is missing is reported with its commands, and an install with no one at
  a terminal to confirm runs nothing.

It takes about 30 seconds and needs bash, git and flock.

## Testing your own project

Copy `test/run` and change the project file it writes (`HOOKS`, `GIT_REPOS`, ...), the items it
queues and the checks. The same shim lets your project's `sat/job` run against your real hooks on
one machine. Two environment variables point Satellite at the scratch directory:

- `SAT_CONF`: the hub's config directory (default `~/.config/sat`);
- `HOME`: where the node keeps its projects.

If your hooks write to fixed paths, give them an override for tests, as `examples/hello` does with
`HELLO_DIR`.

Things the tests can't cover: `sat setup`'s real key exchange through your own login, systemd
units, lingering and self-update from real releases. Try those on a real node with the hello example
before pointing one at real work.
