# Hub setup

The hub is the machine that owns the work. Set it up once, then add a project file per project.

## 1. Install

```sh
git clone https://github.com/RashKash103/satellite ~/satellite    # anywhere you like
~/satellite/hub/install
```

`hub/install` links `sat-hub`, `sat-ssh-gate` and `sat-reaper` into `~/.local/bin`, installs the
`sat-reaper.service` user unit (not enabled), and creates `~/.config/sat/projects/`. Node keys run
`~/.local/bin/sat-ssh-gate`, so if you move the checkout, rerun `hub/install` and nothing else
changes.

## 2. SSH server, key logins only

Nodes reach the hub over SSH. Install a server (`sudo apt install openssh-server` on Debian and
Ubuntu) and turn password logins off, so the only way in is a key:

```sh
sudo tee /etc/ssh/sshd_config.d/10-keys-only.conf <<'CONF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
CONF
sudo systemctl reload ssh
```

Before you do this, make sure the machines you log in from have their keys in the hub's
`~/.ssh/authorized_keys` (see [node.md](node.md#1-key-login-to-the-hub)), or keep a console
session open while you check.

## 3. The reaper

The reaper hands jobs back when their node goes quiet. Run it as a user service:

```sh
systemctl --user enable --now sat-reaper
loginctl enable-linger "$USER"     # keep user services running without a login session
```

or start `sat-reaper` alongside your pipeline's other daemons. Without it, a job whose node
vanished stays claimed.

## 4. A project file

`~/.config/sat/projects/PROJECT`, sourced by `sat-hub` and `sat-ssh-gate` (the name may use letters,
digits, `.`, `_` and `-`):

```sh
HOOKS=/path/to/hub-hooks       # the project's hooks script (docs/projects.md)
LEASE=600                      # seconds without a renewal before a job goes back
GIT_REPOS="/path/to/repo"      # repositories nodes may fetch (space-separated, absolute paths)
PUSH_PREFIX=refs/sat/          # ... and push to, but only refs under this prefix; empty: no pushes
RSYNC_DIRS="/path/to/data"     # directories nodes may copy out with rsync
MAX_PER_NODE=0                 # jobs one node may hold at once; 0 (the default): no limit
MAX_TOTAL=0                    # jobs all nodes together may hold at once; 0 or unset: no limit
```

Leave a setting empty to allow nothing of that kind. Pick `LEASE` well above the renewal interval
(120 s), so a slow network does not cost a job.

`MAX_PER_NODE` and `MAX_TOTAL` count leases, so the hub enforces them whatever a node's `SLOTS`
says: at a limit, `sat-hub PROJECT claim` returns nothing, as when the queue is empty, and the
node's idle lane asks again later. A job counts until it is reported, handed back or reaped. A value
that isn't a whole number makes `claim` fail (the node logs it) rather than claim without a limit.
Your `claim` hook can still refuse anything else it likes.

If nodes push, install the guard in each repository they push to:

```sh
install -m 755 ~/satellite/hub/pre-receive /path/to/repo/.git/hooks/pre-receive
```

The guard checks only pushes that come through the gate; your own pushes are unaffected. If the
repository already has a `pre-receive` hook, call this one from it.

## Watching it

```sh
sat-hub PROJECT status            # leases: node, lane, seconds since the last renewal
sat-hub PROJECT nodes             # nodes: last call, jobs held, the projects their key serves
sat-hub --nodes                   # the same for every project
tail ~/.config/sat/gate.log       # commands the gate refused
journalctl --user -u sat-reaper   # the reaper
```

`nodes` prints one aligned row per node seen for the project:

```
PROJECT  NODE    LAST-SEEN  JOBS  KEY
hello    laptop  42s        2     hello,other
hello    tower   3h         0     all
```

A node is seen when it holds a lease, or when it has called the hub through the gate for that
project (each call touches `~/.config/sat/seen/PROJECT/NODE`; `-` means only a lease, no call
recorded yet). `KEY` is the project list on the node's line in `~/.ssh/authorized_keys`: `all` for a
line without a list (the legacy form), `none` when it has no line (forgotten, or added some other
way); several lines are separated by `;`. A node asking `nodes` or `--nodes` through the gate sees
only its own rows.

What happened to each job is the project's business: the hooks record it wherever the project
keeps its own records.

## Updating the hub

`git pull` in the checkout is all it takes: node keys run `~/.local/bin/sat-ssh-gate`, which links
into it. Old and new nodes speak the same protocol to the same `sat-hub`; the Go node also needs
`sat-hub --protocol`, so update the hub before you move nodes to the binary. Update the hub before
nodes that write project lists, too: an older gate ignores a list (the key reaches every project),
and an older `sat-hub --forget` can't drop one project, so those nodes don't ask it to.

## Node keys and their projects

`sat setup` adds the node's key with a forced command that names the node and the projects it
serves:

```
command="/home/you/.local/bin/sat-ssh-gate laptop hello,other",no-port-forwarding,... ssh-ed25519 AAAA... sat@laptop
```

The key reaches only those projects: the gate loads only their files (`GIT_REPOS`, `PUSH_PREFIX`,
`RSYNC_DIRS`), and `sat-hub` refuses any other project, logging the refusal in `gate.log`. Setting
the node up for one more project adds that project to its line (one line per key, never two).

A line with no list, `command=".../sat-ssh-gate NODE"`, is the **legacy form**, which nodes set up
before project lists got: it still serves every project, and `sat setup` leaves it that way. To
limit such a key, add the list to its line yourself (`sat-ssh-gate NODE PROJECT[,PROJECT...]`, no
spaces in the list); the next call through it uses the new list.

## Removing a node, or one of its projects

`sat uninstall` on the node asks the hub to forget its key (`sat-hub --forget` removes only that
node's line or lines). `sat uninstall PROJECT`, when the node keeps other projects on this hub, asks
it to drop just that project (`sat-hub --forget PROJECT`): the project leaves the node's list, and
the line goes when no project is left on it. A legacy line (no list) is left as it is.

Without the node, edit `~/.ssh/authorized_keys` yourself: its line ends in `sat@NODE` and its
command is `sat-ssh-gate NODE` (followed by its projects). Delete the line, or a project from its
list. Any job the node held goes back when its lease runs out.
