# Security

Satellite connects two machines and runs code on both. This page says what each side trusts and
what a node key is allowed to do.

## What the node trusts

**A node runs whatever the hub's bundle contains**, as the node's user, with no sandbox. Anyone who
can change a project's hooks or bundle on the hub can run code on every node of that project. Only
connect a node to a hub you control, and consider a dedicated user account on the node.

The node pins nothing beyond SSH's host key check: your first key login to the hub (which
`sat setup` uses) accepts the hub's host key as any SSH client does, so check the fingerprint if
your network is not trusted. After that, `sat` checks the hub against `~/.ssh/known_hosts` on every
connection and refuses a hub whose key changed.

## What the node trusts for its own updates

The binary updates itself only from this repository's GitHub releases, never from a hub, and by
default not at all: it only says that a release exists. `sat self-update` (or `AUTO_UPDATE=on`):

- downloads `sat_linux_<arch>` and checks its sha256 against the release's `checksums.txt`. That
  catches a damaged download, but both files come from the same release, so it is not proof of who
  made it;
- runs `gh attestation verify` on it when `gh` is installed and logged in. That checks the build
  provenance signed by the repository's release workflow: the binary was built by GitHub Actions
  from this repository, at that tag. Without `gh` it says it couldn't check, and goes on with the
  checksum alone; install `gh` to make this check, or `PIN` a version you have verified yourself.

You are trusting GitHub and whoever can push tags to this repository. `PIN=vX.Y.Z` in
`~/.sat/config` keeps a node on a version you chose.

Satellite never installs software on a node unless asked (`--install`, `sat PROJECT install`),
and then only after showing every command and the exact apt changes. apt runs with
`--no-remove`.

## What a node key can do on the hub

`sat setup` adds the node's key to the hub's `authorized_keys` with
`command="…/sat-ssh-gate NODE PROJECT[,PROJECT...]",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty`.
Whatever the node asks to run, sshd runs the gate instead. NODE is the node's name, bound to its key:
the key can claim, renew and report jobs only as that node, and a key line without a name is
refused outright. The list names the projects the key serves: the gate reads only those projects'
files, and `sat-hub` refuses (and logs) any other project. A malformed list is refused outright.

A line with no list (`command="…/sat-ssh-gate NODE",...`) is the **legacy form**, which the bash
`sat-setup` and nodes set up before project lists still have: it serves every project on the hub.
`sat setup` keeps such a line as it is; add a list to it by hand to limit it
([hub.md](hub.md#node-keys-and-their-projects)).

For the projects its key serves, the gate allows only:

- `git-upload-pack` of a repository listed in one of those projects' `GIT_REPOS`;
- `git-receive-pack` of such a repository, if its project has a `PUSH_PREFIX`; `hub/pre-receive`
  in that repository then rejects any ref outside the prefix;
- `rsync --server --sender` (copying out, never in) of a directory listed in `RSYNC_DIRS`;
- `sat-hub ARGS...`, with the arguments split into words and passed directly, never through a
  shell.

`sat-hub --forget`, sent through the gate, removes from the hub's `authorized_keys` the line(s)
whose forced command is `…sat-ssh-gate NODE` (with or without a list) for the calling node, and
nothing else: a key can revoke only itself. `sat-hub --forget PROJECT` only takes PROJECT out of
those lines' lists, removing a line whose list it empties; it never adds a project, and leaves a
legacy line alone. NODE comes from the gate (the name bound to the key), never from the request;
a name that isn't a plain name, or a file with no line for it, is refused, and the file is
rewritten beside and renamed into place. `sat uninstall` uses it.

`sat-hub PROJECT nodes` and `sat-hub --nodes` show a node only its own rows when it asks through
the gate.

Everything else is refused and logged in `~/.config/sat/gate.log`. `sat-hub` accepts only plain
names (letters, digits, `.`, `_`, `-`) for projects, nodes and job IDs, and only acts on a job's
lease for the node that holds it. File names in `get` and `put` are checked by the project's hooks,
so **a project's hooks are part of its attack surface**: validate every name a node sends.

Limits to be aware of:

- A key line in the legacy form (no project list) reaches every project's repositories and
  directories, not only its own. Listed keys reach only their projects.
- A node can report its own jobs as passed: the hub learns only what the node tells it. Have `ok`
  verify what it can (a pushed ref, a checksum), and don't give nodes work whose result you can't
  afford to trust them with. How many jobs it holds at once is up to the hooks, and to
  `MAX_PER_NODE` and `MAX_TOTAL` in the project file ([hub.md](hub.md#4-a-project-file)).
- `rsync` arguments are checked against a pattern, not parsed; only the `--server --sender` form
  with a listed directory gets through.
- There is no rate limiting of calls.

## Recommended setup

- Key logins only on the hub (`PasswordAuthentication no`, see [hub.md](hub.md)).
- One node key per machine (`sat setup` makes `~/.ssh/sat_ed25519`), so one can be revoked alone.
- Give legacy key lines a project list.
- Keep secrets out of `GIT_REPOS`, `RSYNC_DIRS` and bundles: nodes can read all of them.
- Read the gate log now and then.

## Reporting a problem

Open an issue, or for anything you'd rather not post in public, use GitHub's private vulnerability
reporting on this repository.
