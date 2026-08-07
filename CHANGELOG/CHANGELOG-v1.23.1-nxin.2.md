# KubeEdge v1.23.1-nxin.2

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release is `v1.23.1` plus reviewed NXIN backports and field fixes. It
  must not be built from `master` or the moving `release-1.23` branch.
- Cloud and edge artifacts must be built from the same clean immutable tag.

## Supersedes v1.23.1-nxin.1

The `.1` artifacts remain immutable and are rejected for production rollout.
An ACK three-replica CloudCore canary exposed two upgrade-path defects before
the confirmation gate, so the `.1` tag and image digests are not overwritten.

## Additional fixes in v1.23.1-nxin.2

- In a multi-replica CloudCore deployment, a replica that does not own an edge
  session no longer marks that node's shared task status as failed. It leaves
  the task pending for the replica that owns the session. This applies to the
  common v1alpha2 node-task executor used by `NodeUpgradeJob`,
  `ImagePrePullJob`, and `ConfigUpdateJob`.
- Resource copying from an installation image now stages each file under a
  unique temporary name and atomically replaces the destination only after all
  copies complete.
- The copy command writes a completion marker last. If Edged reaps the
  directly-created CRI sandbox immediately afterward and CRI reports exit 137,
  the completed copy is accepted; a missing marker still fails the operation.
- Staged files, temporary CRI containers, sandboxes, and log directories are
  cleaned with the existing independent bounded cleanup context.
- A failed or otherwise terminal node-upgrade action now removes its saved
  local confirmation record. Only a successful `WaitingConfirmation` pause
  retains the record, preventing a later `keadm ctl confirm` from resuming a
  rejected task.

## Fixes retained from v1.23.1-nxin.1

- CloudCore HA fixes for ObjectSync delivery and persistence, policy-controller
  leader election, DeviceStatus creation races, and CRD ObjectSync specs.
- Edge cache fixes for ServiceAccount tokens and Lease error responses.
- Safe container-log path validation and tolerant CRI resource-copy cleanup.
- Clean build metadata for installation-package binaries.

## Required verification gates

1. Pass targeted and Linux/ARM64 no-inline tests for the common node-task
   executor and CRI resource copy.
2. Build all cloud and edge artifacts from the clean `.2` tag and verify
   embedded version, commit, platform, and image digests.
3. Roll CloudCore through ACK while preserving the protected NLB identity and
   at least two ready replicas.
4. Run a single-node `NodeUpgradeJob` with concurrency one, exact arm64 image
   digest, and `requireConfirmation: true`.
5. Confirm only after Check succeeds, then verify rollback data, EdgeCore
   version, node readiness, independent edge access, workloads, and bounded
   error-free logs.
