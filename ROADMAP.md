# Roadmap

## Next: a Go port, starting with the node

The shell version stays the reference until the port matches it. The protocol nodes speak over SSH,
the hooks contract and the bundle contract stay as they are, so projects need no changes and hubs
and nodes can move over separately.

- **One static binary** for the node (`sat`), later the hub too: nothing to install but the file.
- **A live view:** `sat` with no arguments shows each lane, its job, elapsed time, the job's own
  progress and recent log lines, with drain and stop on a key. Jobs report progress by writing
  lines to a file (`$SAT_PROGRESS`); this is the one addition to the bundle contract.
- **One SSH connection** held open per hub instead of one per call.
- **Layout:** everything under `~/.sat`, projects kept apart from the tool:

  ```
  ~/.sat/bin/sat
  ~/.sat/projects/PROJECT/{config,env,bundle/,state/,work/}
  ```

  `sat status` shows each project's disk use, since `work/` can be large. `sat migrate` moves an
  existing `~/sat` node.
- **Updates** come from this repository's GitHub releases, not from hubs (a node may serve several
  hubs). Each release carries a protocol version; a hub states which it accepts, and a node that
  doesn't match stops claiming from that hub and says why. By default a node only reports that an
  update exists. `sat self-update` shows the release notes, asks, drains, verifies the download
  against the release checksums and its build attestation, swaps the binary and restarts.
  `AUTO_UPDATE=on` opts a node into doing that unattended; a version can be pinned. Bundles keep
  coming from the hub, as now.
- **`sat uninstall [PROJECT]`** lists what it will remove and asks: the service, the project's
  directory and, with the last project, the tool, unit, SSH alias and key. It asks the hub to drop
  the node's key. It never removes packages; ones installed with `--install` are listed for the user.

## Later

- Per-project node keys, so a key reaches only its own project's repositories and directories.
- Hub-side limits on how many jobs one node may hold.
