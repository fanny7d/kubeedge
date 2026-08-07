# KubeEdge v1.23.1-nxin.3

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release is `v1.23.1` plus the reviewed NXIN fixes retained from `.1`
  and `.2`, followed by the edge-upgrade bootstrap and rollback fixes below.
- It must not be built from `master` or the moving `release-1.23` branch.
- Cloud and edge artifacts must be built from the same clean immutable `.3`
  tag and must report that tag and commit at runtime.

## Supersedes v1.23.1-nxin.1 and v1.23.1-nxin.2

The `.1` and `.2` tags, images, and archives remain immutable and are rejected
for production edge rollout. The `.2` ACK canary proved the CloudCore HA
node-task routing fix: non-session replicas no longer rejected the shared
upgrade task. Its edge Check then exposed a bootstrap problem on historical
EdgeCore versions: embedded Edged treated keadm's directly-created CRI sandbox
as an unknown runtime-only pod and terminated it before the large EdgeCore
binary finished copying. The old EdgeCore stayed active because Check failed
before the upgrade action.

## Additional fixes in v1.23.1-nxin.3

- Add a narrowly scoped, short-lived resource-copy guard shared by keadm and
  embedded Edged. Only the exact `kubeedge/edgecore` copy sandbox UID is
  protected, and stale guards expire so an interrupted copy cannot leak an
  orphan sandbox indefinitely.
- Keep the active and completion markers outside the pod log directory so CRI
  log cleanup cannot erase copy state before keadm evaluates it.
- Pull and digest-verify the installation image while the current EdgeCore is
  still serving, then stage the current EdgeCore binary for automatic recovery.
- Stop historical EdgeCore only for the bounded CRI copy and replacement
  phase. This bootstraps nodes whose old Edged does not understand the guard.
- Replace EdgeCore through a same-directory temporary file, file sync, atomic
  rename, and directory sync while preserving executable mode.
- If stopping, copying, replacing, or starting the new EdgeCore fails, restore
  the staged previous binary when needed and attempt to restart the old
  EdgeCore before returning the original error.

## Fixes retained from v1.23.1-nxin.2

- Route shared node jobs only through the CloudCore replica that owns the edge
  session, without a non-owner replica marking the task failed.
- Stage installation-image resources under unique names and commit each
  destination atomically; accept an exit 137 only when the durable completion
  marker proves all files were committed.
- Remove terminal local confirmation records while retaining only a successful
  `WaitingConfirmation` pause.
- Retain all CloudCore HA, ObjectSync, leader-election, cache-miss, Lease,
  security, cleanup, and build-metadata fixes documented in `.1` and `.2`.

## Required verification gates

1. Pass local and native Linux/ARM64 tests for CRI resource copy, the guard,
   atomic file replacement, upgrade recovery, and node-task routing.
2. Build CloudCore, controller-manager, EdgeCore, keadm, and the installation
   package from a clean independent clone of the immutable `.3` tag. Verify
   embedded version, commit, clean tree, ELF platform, archive checksums, and
   OCI digests.
3. Roll the matching `.3` cloud images through ACK while preserving the
   protected NLB identity and at least two ready CloudCore replicas.
4. Install the verified `.3` keadm on the single canary without stopping the
   old EdgeCore, then run the official digest-pinned upgrade with independent
   access and a staged rollback copy available.
5. Verify EdgeCore version and commit, Node Ready state, workloads, containerd,
   independent access, restart counters, and bounded cloud/edge error logs.
6. Exercise the official `NodeUpgradeJob` Check and confirmation workflow from
   a `.3` EdgeCore before approving wider rollout.
