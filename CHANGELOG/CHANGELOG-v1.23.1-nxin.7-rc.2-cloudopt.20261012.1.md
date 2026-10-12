# v1.23.1-nxin.7-rc.2-cloudopt.20261012.1

Cloud-side release built directly on `08fec0b3c389c7977ccc0f2cfdb29e1ed6117f66` (`v1.23.1-nxin.7-rc.2`).
Only CloudCore and controller-manager images are published. EdgeCore, keadm, LED and business workload images are outside this release.

## Changes

- Reuse one edge Node snapshot per ServiceAccountAccess reconciliation instead of listing the complete fleet for every Pod node.
- Ignore Pod status-only updates before any API reads. Preserve Pod scheduling, ServiceAccount, deletion and identity changes; retry membership reads in Reconcile.
- Coalesce RBAC changes into a retryable sweep of existing ServiceAccountAccess resources. Preserve old/new membership transitions and retry Node create mapping failures.
- Propagate RBAC read failures without changing or distributing partial policies. A genuinely deleted Role or ClusterRole grants no permissions even if a dangling binding remains.
- Demote per-message success/trace logs and the NodeGroup reconcile entry to verbosity 4, retaining warning/error and lifecycle logs.
- Preserve ObjectSync repair, legacy backfill, offline membership and reconnection retry behavior.
- Configure the mock Close expectation before starting session goroutines in the KeepAlive tests.

## Validation

PolicyController, DeviceController, DynamicController, NodeGroup, SyncController and CloudHub tests and race checks passed offline. Permission failure injection covers Role/ClusterRole reads and binding lists, recovery, role deletion, ServiceAccount deletion, Pod event filtering, and node/ObjectSync restoration. Linux/AMD64 builds and vet passed.

macOS/ARM64 test runs used a temporary copy of gomonkey v2.12.0 with the upstream instruction-cache invalidation fix (PR 184); repository dependency versions are unchanged. Local benchmarks use synthetic typed API snapshots and are not production CPU/RSS claims.

## Operational scope

Deploy the two cloud components separately using immutable image digests. Preserve the ACK NLB identity, certificate/configuration checksums, existing resource settings, and Pod disruption budgets. Validate edge connectivity, SAA/ObjectSync convergence and business workload identity after rollout. API failures defer policy changes until a successful retry; they do not justify clearing existing authorization.
