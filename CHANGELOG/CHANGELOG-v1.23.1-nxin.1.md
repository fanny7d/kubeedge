# KubeEdge v1.23.1-nxin.1

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release is built from the immutable NXIN tag `v1.23.1-nxin.1`. It must
  not be rebuilt from `master` or the moving `release-1.23` branch.
- Cloud and edge artifacts must be built from the same clean source commit.

## Upstream fixes inherited from v1.23.1

- Avoid shell command execution in `NodeUpgradeJob`.
- Avoid shell command execution in `ConfigUpdateJob`.
- Reject unsafe paths while extracting archives.
- Limit Viaduct message payload size.

The upstream release includes the fix for
[GHSA-5jpj-293f-rhvj](https://github.com/kubeedge/kubeedge/security/advisories/GHSA-5jpj-293f-rhvj).

## NXIN cloud fixes

- Harden `ObjectSync` creation and delivery when CloudCore runs with multiple
  replicas.
- Add leader election for the policy controller.
- Tolerate concurrent `DeviceStatus` creation.
- Preserve CRD `ObjectSync` specifications during synchronization.
- Persist cluster synchronization state even when the receiving CloudCore
  replica does not own the local edge session.

## NXIN edge and upgrade fixes

- Treat ServiceAccount cache misses as normal cache misses and normalize token
  audiences before lookup.
- Return Lease request errors instead of decoding an error response as a
  successful object.
- Give the temporary CRI sandbox and container used by `keadm` explicit log
  paths.
- Reject empty, relative, root-level, or out-of-tree container log paths before
  log cleanup. This prevents an empty CRI log path from expanding to a glob at
  the host root.
- Use an independent bounded cleanup context for temporary upgrade containers,
  and do not fail an otherwise successful resource copy solely because cleanup
  reports an error.
- Keep installation-package build metadata clean so the embedded version and
  commit identify the tagged source exactly.

## Required artifacts

- `cloudcore`, linux/amd64 OCI image.
- `controller-manager`, linux/amd64 OCI image.
- `edgecore`, linux/arm64 binary and release archive.
- `keadm`, linux/arm64 binary and release archive.
- `installation-package`, linux/arm64 OCI image containing the matching
  `edgecore` and `keadm` binaries.

Every OCI image must carry the immutable source revision and version labels.
Deploy and upgrade specifications must use the published image digest, not a
mutable tag alone.

## Verification gates

- Run cloud HA unit tests for CloudHub dispatch/session, sync controller,
  device controller, and policy controller.
- Run edge unit tests for MetaManager, ServiceAccount, Lease, CRI resource
  copy, and vendored kubelet log cleanup.
- Run NodeUpgradeJob, ConfigUpdateJob, admission, validation, and legacy task
  executor security tests with the project's official no-inline test flags.
- Verify that every binary reports `v1.23.1-nxin.1`, the release commit, and a
  clean tree.
- Verify image architecture and digest before changing the cluster.

## Rollout and rollback requirements

1. Back up the live CloudCore configuration, workload manifests, CRDs, and
   image digests. Preserve the existing protected NLB Service.
2. Upgrade the cloud components first. Roll CloudCore one replica at a time and
   keep at least two replicas ready throughout the rollout.
3. Observe CloudCore leader election, edge sessions, API availability, and
   error logs before upgrading any edge node.
4. Upgrade one explicitly named arm64 canary node with concurrency one,
   confirmation enabled, the exact installation-package digest, and a verified
   independent management path.
5. Keep the previous `edgecore`, `keadm`, configuration, and image digest on the
   canary until the observation window passes. Do not stop containerd, Docker,
   or the independent access agent as part of the upgrade.
6. Roll back CloudCore to the recorded image digest or restore the canary
   binaries and configuration if any availability, compatibility, or log gate
   fails.

