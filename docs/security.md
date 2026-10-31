# Security

Satellite connects two machines and runs code on both. This page says what each side trusts and
what a node key is allowed to do.

## What the node trusts

**A node runs whatever the hub's bundle contains**, as the node's user, with no sandbox. Anyone who
can change a project's hooks or bundle on the hub can run code on every node of that project. Only
connect a node to a hub you control, and consider a dedicated user account on the node.

The node pins nothing beyond SSH's host key check: the first `sat-setup` connection accepts the
hub's host key as any SSH client does, so check the fingerprint if your network is not trusted.

Satellite never installs software on a node unless asked (`--install`, `sat PROJECT install`),
and then only after showing every command and the exact apt changes. apt runs with
`--no-remove`.

## What a node key can do on the hub

`sat-setup` adds the node's key to the hub's `authorized_keys` with
`command="…/sat-ssh-gate NODE",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`.
Whatever the node asks to run, sshd runs the gate instead. NODE is the node's name, bound to its key:
the key can claim, renew and report jobs only as that node, and a key line without a name is
refused outright. The gate allows only:

- `git-upload-pack` of a repository listed in some project's `GIT_REPOS`;
- `git-receive-pack` of such a repository, if its project has a `PUSH_PREFIX`; `hub/pre-receive`
  in that repository then rejects any ref outside the prefix;
- `rsync --server --sender` (copying out, never in) of a directory listed in `RSYNC_DIRS`;
- `sat-hub ARGS...`, with the arguments split into words and passed directly, never through a
  shell.

Everything else is refused and logged in `~/.config/sat/gate.log`. `sat-hub` accepts only plain
names (letters, digits, `.`, `_`, `-`) for projects, nodes and job IDs, and only acts on a job's
lease for the node that holds it. File names in `get` and `put` are checked by the project's hooks,
so **a project's hooks are part of its attack surface**: validate every name a node sends.

Limits to be aware of:

- Every node key can reach every project's repositories and directories, not only its own.
- A node can claim as many jobs as the hooks will give it, and can report its own jobs as passed:
  the hub learns only what the node tells it. Have `ok` verify what it can (a pushed ref, a
  checksum), and don't give nodes work whose result you can't afford to trust them with.
- `rsync` arguments are checked against a pattern, not parsed; only the `--server --sender` form
  with a listed directory gets through.
- There is no rate limiting.

## Recommended setup

- Key logins only on the hub (`PasswordAuthentication no`, see [hub.md](hub.md)).
- One node key per machine (`sat-setup` makes `~/.ssh/sat_ed25519`), so one can be revoked alone.
- Keep secrets out of `GIT_REPOS`, `RSYNC_DIRS` and bundles: nodes can read all of them.
- Read the gate log now and then.

## Reporting a problem

Open an issue, or for anything you'd rather not post in public, use GitHub's private vulnerability
reporting on this repository.
