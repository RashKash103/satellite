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
```

Leave a setting empty to allow nothing of that kind. Pick `LEASE` well above the renewal interval
(120 s), so a slow network does not cost a job.

If nodes push, install the guard in each repository they push to:

```sh
install -m 755 ~/satellite/hub/pre-receive /path/to/repo/.git/hooks/pre-receive
```

The guard checks only pushes that come through the gate; your own pushes are unaffected. If the
repository already has a `pre-receive` hook, call this one from it.

## Watching it

```sh
sat-hub PROJECT status            # leases: node, lane, seconds since the last renewal
tail ~/.config/sat/gate.log       # commands the gate refused
journalctl --user -u sat-reaper   # the reaper
```

What happened to each job is the project's business: the hooks record it wherever the project
keeps its own records.

## Removing a node

Delete its line from `~/.ssh/authorized_keys` (it ends in `sat@NODE`, and its command is
`sat-ssh-gate NODE`). Any job it
held goes back when its lease runs out.
