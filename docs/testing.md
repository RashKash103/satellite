# Testing

## The end-to-end test

```sh
test/run
```

It runs a hub and a node on one machine, in a scratch directory, without sshd: an `ssh` shim on
`PATH` drops ssh's options and host and hands the rest to the real `sat-ssh-gate` as
`SSH_ORIGINAL_COMMAND`, which is what sshd does for a node key. It checks:

- the gate's refusals, that it runs no shell on what it is sent, and that a key acts only as its
  own node;
- the push guard: pushes under the prefix pass, others are refused;
- leases: only the owner can renew or report a job, and a silent lease is reaped back;
- a node agent running `examples/hello` with two lanes: a job that passes, one that fails, one
  that crashes and is retried, and a drain;
- requirement checks: what is missing is reported with its commands, and an install with no one at
  a terminal to confirm runs nothing.

It takes about 30 seconds and needs bash, git and flock. It touches nothing outside its scratch
directory. CI runs it, and ShellCheck, on every push.

## Testing your own project

Copy `test/run` and change the project file it writes (`HOOKS`, `GIT_REPOS`, ...), the items it
queues and the checks. The same shim lets your project's `sat/job` run against your real hooks on
one machine. Two environment variables point Satellite at the scratch directory:

- `SAT_CONF`: the hub's config directory (default `~/.config/sat`);
- `HOME`: where the node keeps `~/sat/PROJECT/`.

If your hooks write to fixed paths, give them an override for tests, as `examples/hello` does with
`HELLO_DIR`.

Things the test can't cover: `sat-setup`'s real SSH key exchange, systemd units, and lingering. Try
those on a real node with the hello example before pointing one at real work.
