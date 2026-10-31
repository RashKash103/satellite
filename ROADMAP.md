# Roadmap

## Done: the Go port of the node

The protocol nodes speak over SSH, the hooks contract and the bundle contract are as they were, so
projects need no changes and hubs and nodes move over separately. The bash node (`node/`) stays as
the legacy reference.

- **One static binary** for the node (`sat`): nothing to install but the file.
- **A live view:** `sat` with no arguments shows each project, its service, the hub, disk room and
  each lane: its job, elapsed time, last renewal, the job's own progress (`$SAT_PROGRESS`, the one
  addition to the bundle contract) and its log, in panes; every command can be run from it.
- **One SSH connection** held open per hub instead of one per call (the system ssh remains an
  option, `TRANSPORT=exec`).
- **Layout:** everything under `~/.sat`, projects kept apart from the tool; `sat status` shows each
  project's disk use; `sat migrate` moves an existing `~/sat` node (with the bundle's `sat/moved`
  hook for paths a project keeps).
- **Updates** from this repository's GitHub releases, not from hubs. The hub states its protocol
  (`sat-hub --protocol`); a node that doesn't speak it stops claiming from that hub and says why.
  By default a node only reports that an update exists; `sat self-update` shows the notes, asks,
  verifies the checksum and build attestation, drains, swaps and restarts; `AUTO_UPDATE=on` and
  `PIN` as documented. A release workflow builds, checksums and attests tagged versions.
- **`sat uninstall [PROJECT]`** lists what it will remove and asks, has the hub forget the node's key
  (`sat-hub --forget`), and never removes packages: the commands `sat install` ran are listed.
- **Several projects as modes** on one node, each with its own lanes, `env` and service.

Still to do: the first tagged release, and running the Go node across two machines for a while
before the bash node is retired.

## Done: views and alerts

- **Project views:** a bundle's `sat/views/NAME` shows the project's or a lane's state, once or live,
  as text or as JSON blocks sat draws (tables, progress, bars, timelines, metrics with their recent
  values, line charts, alerts), in `sat PROJECT view NAME` and as tabs in the live view. It adds
  to the bundle contract and changes nothing in it.
- **Alerts:** the agent raises and clears its own (a stalled lane, a long job, the hub unreachable,
  a protocol mismatch, disk holding work back, missing requirements, repeated restarts, an update);
  `sat status`, `sat alerts` and the live view show them, and `NOTIFY=on` sends a desktop
  notification once per new one.
- **The live view** gives way as the terminal shrinks, scrolls every pane, and charts this machine's
  CPU, memory and load and each project's disk room.

## Next: the hub

- A Go `sat-hub` and gate in the same binary, with the bash hub as the reference until it matches.

## Done: keys limited to projects, and job limits

- **Per-project node keys:** a key line lists the projects it serves
  (`sat-ssh-gate NODE PROJECT[,PROJECT...]`) and reaches only their repositories, directories and
  `sat-hub`. `sat setup` adds a project to the line, `sat uninstall PROJECT` has the hub drop it
  (`sat-hub --forget PROJECT`). Lines without a list (the legacy form) still serve every project.
- **Hub-side limits:** `MAX_PER_NODE` and `MAX_TOTAL` in the project file cap the jobs one node, and
  all nodes together, hold at once.
- **`sat-hub PROJECT nodes`** (and `--nodes`): each node's last call, jobs held and key projects.
