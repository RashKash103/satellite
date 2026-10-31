# Roadmap

## Done: the Go port of the node

The protocol nodes speak over SSH, the hooks contract and the bundle contract are as they were, so
projects need no changes and hubs and nodes move over separately. The bash node (`node/`) stays as
the legacy reference.

- **One static binary** for the node (`sat`): nothing to install but the file.
- **A live view:** `sat` with no arguments shows each project, its service, the hub, disk room and
  each lane: its job, elapsed time, last renewal, the job's own progress (`$SAT_PROGRESS`, the one
  addition to the bundle contract) and its log, with start, drain and stop on a key.
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

## Next: the hub

- A Go `sat-hub` and gate in the same binary, with the bash hub as the reference until it matches.

## Later

- Per-project node keys, so a key reaches only its own project's repositories and directories.
- Hub-side limits on how many jobs one node may hold.
