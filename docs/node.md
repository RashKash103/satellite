# Node setup and control

A node is a machine that lends its CPU to a hub while it is on. Set it up once per project.

## 1. Key login to the hub

`sat-setup` uses your own SSH key login to the hub once, to install a separate, restricted key for
the node. It never uses a password. If this machine can't yet log in to the hub with a key, give
it one (`ssh-keygen -t ed25519` if it has none) and put its public key on the hub, preferably
without ever enabling password logins:

- **From GitHub:** if the key is on your GitHub account, run `ssh-import-id gh:YOUR_GITHUB_USER`
  on the hub (or `curl -s https://github.com/YOUR_GITHUB_USER.keys >> ~/.ssh/authorized_keys`).
- **By hand:** paste `~/.ssh/id_ed25519.pub` into the hub's `~/.ssh/authorized_keys` from a
  console or an existing key session on the hub.
- Only if the hub still allows password logins: `ssh-copy-id HUB_USER@HUB_IP`, then turn them off.

Check with `ssh -o PasswordAuthentication=no HUB_USER@HUB_IP true`, and keep password logins off on
the hub ([hub.md](hub.md#2-ssh-server-key-logins-only)).

## 2. Run `sat-setup`

```sh
scp HUB_USER@HUB_IP:satellite/node/sat-setup ~/     # the path of the hub's checkout
bash ~/sat-setup HUB_IP PROJECT [HUB_USER]
```

It is safe to rerun, and **it installs no software**. It:

1. makes `~/.ssh/sat_ed25519` and an SSH alias `sat-hub-HUB_IP` for it;
2. adds that key to the hub's `authorized_keys` behind `sat-ssh-gate`, bound to this node's name
   (the short hostname), using your own key login; a rerun replaces the line instead of adding one;
3. installs `sat-agent` and `sat-call` in `~/sat/bin`, the `sat` command in `~/.local/bin`, and
   the `sat@.service` user unit;
4. fetches the project's bundle and writes `~/sat/PROJECT/config` (adding the bundle's
   `sat/defaults`);
5. checks what the node is missing, for Satellite and for the project's jobs. If anything is, it
   prints the commands that would install it and stops: install them yourself and rerun;
6. runs the project's `sat/setup`, which prepares the project's own files under `~/sat/PROJECT`.

Nothing starts until you run `sat PROJECT start`.

To have it install what is missing, add `--install` (`bash ~/sat-setup --install HUB_IP PROJECT`).
It lists every command first, shows exactly which packages each apt step would install or upgrade,
and asks before running anything. apt runs with `--no-remove`, so it never removes a package; if a
package can't be installed without removing another, it stops and runs nothing.

To keep the service running while you are logged out, run `loginctl enable-linger` once (setup
leaves this to you).

## 3. The `sat` command

`sat [PROJECT] COMMAND`. PROJECT can be left out when the node has only one.

| command | |
|---|---|
| `start` | start taking jobs |
| `drain` | take no new jobs; stop once the running ones finish (use before shutting down) |
| `stop` | stop now; running jobs go straight back to the hub |
| `status` | running or not, autostart, hub reachability, jobs in progress, the last log lines |
| `logs` | follow the service log |
| `check` | what this node is missing, and the commands that would install it |
| `install` | the same steps, shown and simulated, then run after you confirm |
| `autostart on\|off` | start at boot, or not (off by default) |
| `selftest` | the project's own check of this node |
| `update` | refresh the node scripts and the bundle from the hub; check, then rerun the project's setup |

A normal shutdown is safe without a drain: systemd's stop hands running jobs back. Draining just
avoids throwing away work in progress.

## Node config

`~/sat/PROJECT/config` is read at each start and by each job:

| key | |
|---|---|
| `HUB` | the SSH alias for the hub (written by `sat-setup`) |
| `HUB_IP`, `HUB_USER` | where the hub is |
| `SAT_NAME` | this node's name in leases and results: the short hostname. It is bound to the node's key on the hub, so change it in both places, or rerun `sat-setup` after renaming the machine. |
| `SLOTS` | lanes: jobs run at the same time (default 1; the project's defaults may set it) |

| `MIN_FREE_GB`, `MIN_FREE_PCT` | the node keeps the larger of these free on the disk under `~/sat` (defaults 20 GB and 10 %) |
| `MAX_GB` | caps this project's directory (default 0: no cap; a project's defaults may set one) |

Projects may add their own keys (through `sat/defaults`) for their jobs to read.

### Disk space

Below the free-space floor, or over `MAX_GB`, a lane takes no new jobs: it first runs the project's
`sat/clean`, if it has one, and then waits until there is room, saying why in the log and in
`sat status`. If free space falls below half the floor while a job runs, the job is stopped and
handed back to the hub, so a full disk never takes the machine down with it.

`~/sat/PROJECT/env`, if present, is sourced by the agent before any job runs; use it for
node-local environment such as `PATH` additions.

Change the config, then `sat PROJECT stop && sat PROJECT start`.

## Removing a node

```sh
sat PROJECT stop; sat PROJECT autostart off
rm -rf ~/sat/PROJECT ~/.config/systemd/user/sat@.service
```

Satellite installed no packages, so none need removing.

and delete its key from the hub's `authorized_keys` ([hub.md](hub.md#removing-a-node)).
