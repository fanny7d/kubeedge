# KubeEdge v1.23.1-nxin.5

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release contains the reviewed NXIN fixes retained through `.4` plus
  the EdgeCore startup fix below. It must not be built from `master` or the
  moving `release-1.23` branch.
- CloudCore, controller-manager, EdgeCore, keadm, archives, and installation
  images must be built from the same clean immutable `.5` tag and must report
  that tag and commit at runtime.

## Supersedes v1.23.1-nxin.4 for edge rollout

The `.4` source tag and artifacts remain immutable. Its ACK CloudCore and
controller-manager rollout preserved three ready CloudCore replicas, the
protected NLB identity, and service endpoints, but `.4` is rejected for edge
rollout.

The digest-pinned official NodeUpgradeJob canary from `.4-rc.2` to `.4` reached
`Completed`, but the restarted EdgeCore briefly considered all existing CRI
workloads orphaned. It stopped all five pod sandboxes and 21 business
containers before consuming the initial pod SET from MetaManager. The
container-hosted hardware-watchdog feeder was stopped as a consequence; with
the 44-second `dw_wdt` timeout and no independent feeder, the host reset. The
node recovered after reboot, but workload restart counts increased, so the
canary did not pass.

## Root cause and fix

- `edged.syncPod` enqueues the initial API-server-source SET and then marks the
  initial database read ready.
- PodConfig storage consumes that SET asynchronously. On slow eMMC/SQLite
  startup, housekeeping could observe `initPodReady=true` while its
  `seenSources` set was still empty.
- The NXIN/KubeEdge `SeenAllSources` implementation checked only
  `initPodReady`, so Kubelet orphan cleanup could run against an empty
  podManager even though the 21 runtime containers were valid.
- `.5` requires both the initial database-ready flag and every configured
  PodConfig source to have been consumed before housekeeping is released.
- A regression test proves that setting initial readiness alone is
  insufficient, that the configured API-server source unlocks readiness, and
  that clearing initial readiness closes the gate again.

## Canary evidence

`v1.23.1-nxin.5-rc.1` was built from clean commit
`a4df0801eded3aa0aeb76e0d0e770d33a20ecbc2` with Go 1.23.12 and tested through
the official digest-pinned, confirmation-required NodeUpgradeJob on ARM64 node
`asset-502351839893`:

- Check, WaitingConfirmation, BackUp, and Upgrade all completed successfully.
- EdgeCore restarted while containerd remained active.
- All 21 business container IDs and attempts and all five ready sandbox IDs
  remained unchanged across the restart.
- The host boot ID remained unchanged and EdgeCore `NRestarts` remained zero.
- A transient host-level watchdog guard fed `/dev/watchdog` independently of
  EdgeCore-managed Pods during the canary.
- The installed EdgeCore and keadm hashes matched the binaries extracted from
  the verified installation image.

## Required verification gates

1. Pass local race tests and native Go 1.23 ARM64 tests with vendored modules.
2. Build from a fresh clone of the immutable `.5` tag, not a linked worktree
   whose `.git` file is unusable in the image builder. Reject any binary that
   reports `v0.0.0`, `dirty`, the wrong commit, or the wrong Go version.
3. Record the pre-upgrade boot ID, ready sandbox IDs, running container IDs,
   attempts, restart counts, EdgeCore `NRestarts`, and database integrity.
4. If a hardware watchdog is fed by an EdgeCore-managed container, establish
   and verify an independent host-level feeder before confirmation. Block the
   upgrade if this cannot be done safely.
5. Use one-node, concurrency-one, digest-pinned NodeUpgradeJobs with
   `requireConfirmation: true`; require every action and final phase to pass.
6. During EdgeCore restart, require containerd, the independent management
   path, the watchdog guard, all sandbox IDs, and all business container IDs
   to remain available and unchanged.
7. After reconnect, require Node Ready with the exact version and hashes, no
   new workload restarts, database integrity, and a bounded steady-state log
   window without new E/F/W records attributable to the target release.
