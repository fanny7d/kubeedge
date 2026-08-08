# KubeEdge v1.23.1-nxin.6

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release contains every reviewed NXIN fix retained through `.5` plus the
  stale edge Pod convergence fix below. It must not be built from `master` or
  a moving upstream branch.
- CloudCore, controller-manager, EdgeCore, keadm, archives, and installation
  images must be built from one clean immutable `.6` tag and report the same
  tag and commit at runtime.

## Root cause

An edge node can retain an old Pod after the Kubernetes API object has already
been deleted and replaced. The stale Pod then keeps patching status and
requesting bound service account tokens. CloudCore returned the API `NotFound`
response but did not converge the edge cache, so the requests and CloudCore
error logs repeated indefinitely.

The older Pod status path attempted to send a delete, but the delete object did
not contain the stale Pod UID. Edge MetaManager deliberately deletes Pods by
both key and UID to protect a newer same-name Pod, so the empty-UID cleanup was
ineffective.

## Fix

- When a Pod status patch returns `NotFound`, CloudCore preserves the real
  error response and sends an exact delete for the authenticated source node,
  namespace, name, and stale UID.
- The older Pod status update path now includes the reported UID in its delete.
- A service account token `NotFound` triggers the same cleanup only when the
  API status identifies the missing object as the exact Pod referenced by the
  token request.
- Expected stale-Pod convergence is logged only at verbose level. Empty UIDs,
  non-Pod `NotFound` errors, mismatched Pod names, response failures, and delete
  delivery failures remain visible as real failures.

The UID guard is mandatory: a delayed cleanup must not delete a newer Pod that
reuses the same namespace and name.

## Edge token refresh log correctness

EdgeCore proactively discards a cached projected service account token after
80 percent of its requested lifetime and immediately asks CloudCore for a new
token. The successful refresh path previously logged the deliberate cache
invalidation as `E... token expired`, even though the replacement token was
stored and the workload remained healthy.

The refresh threshold is now classified as an expected cache miss and logged
only at verbose level. Database deletion failures, malformed token responses,
CloudCore request failures, and replacement-token failures remain visible as
real errors.

## Edge Pod delete-event correctness

Kubelet's final Pod cleanup is sent through MetaManager as a `DeleteOptions`
request. It is not a Kubernetes resource watch event and therefore has no
`apiVersion` or `kind`. MetaServer previously attempted to decode it as an
object and emitted a misleading `Object 'Kind' is missing` error even though
the Pod, container, token row, and cached metadata were removed successfully.

- Edge-originated Pod deletion requests are forwarded to CloudCore but are no
  longer injected into the MetaServer watch cache.
- CloudCore stale-Pod tombstones and SyncController orphan tombstones carry an
  explicit group/version/kind so authoritative delete events remain decodable.
- `meta_v2` deletion now requires the cached UID to match the event UID. A
  delayed delete, including one with an empty UID, cannot remove a same-name
  replacement object or emit a false `watch.Deleted` event.
- Repeated deletes and already-absent rows are treated as expected idempotent
  outcomes; database corruption and actual storage failures remain errors.

## MetaServer non-watch message filtering

MetaManager receives both authoritative resource events and edge-to-cloud
requests on the same internal path. Edge-originated Node requests and
PolicyController `ServiceAccountAccess` snapshots intentionally omit
`apiVersion` and `kind`; neither is a MetaServer Kubernetes watch event.

- Edge-originated Node requests continue to be stored in the legacy meta path
  and forwarded to CloudCore, but are not injected into `meta_v2` as if the
  edge's requested state were already authoritative.
- `ServiceAccountAccess` continues to be stored in the legacy meta table used
  by edge authorization, but is not injected into the unrelated MetaServer
  watch cache.
- `ServiceAccountAccess` insert, update, and delete messages are acknowledged
  after MetaManager persistence but are not forwarded to Edged, whose resource
  loop accepts only Pod and CSI Volume events.
- Cloud-originated Node resources and all other authoritative events continue
  through the normal MetaServer decode, persistence, and watch path.

## Cloud control-plane high availability

The Helm release already ran three CloudCore replicas, but the separate
`kubeedge-controller-manager` Deployment remained a single replica. Simply
scaling the old binary would run multiple NodeUpgradeJob, NodeGroup, and
EdgeApplication reconcilers concurrently because its controller-runtime
manager did not use leader election.

- `kubeedge-controller-manager` now uses a namespace-scoped Lease named
  `kubeedge-controller-manager`; the Pod namespace comes from the downward API
  and falls back to the KubeEdge system namespace outside Kubernetes.
- The chart grants Lease access through a namespaced Role and RoleBinding,
  grants the same identity permission to publish namespace-scoped leader
  election Events,
  supports two controller-manager replicas, uses a zero-unavailable rolling
  strategy, exposes `/healthz` and `/readyz`, and spreads replicas across cloud
  hosts when capacity permits.
- EdgeApplication status-controller garbage collection waits for the manager
  cache to synchronize before its first List, avoiding a cache-not-started
  error on every controller-manager startup.
- Controller-manager and CloudCore PodDisruptionBudgets are configurable. The
  ACK HA values must use controller-manager `minAvailable=1` and CloudCore
  `minAvailable=2`.
- CloudCore `minReadySeconds` is configurable so a newly started process must
  remain available before the next old replica is removed.

## Required canary gates

1. Pass the complete edgecontroller test package, focused MetaManager token
   tests, controller-manager leader-election tests, repeated tests, and race
   tests for status-patch, Pod-status, bound-token cleanup, and proactive token
   refresh paths. Lint and render the HA Helm values and validate the rendered
   resources with Kubernetes dry-run.
2. Build immutable `.6-rc` CloudCore and controller-manager images from a clean
   checkout and verify their version, commit, clean-tree marker, Go version,
   platform, binary hashes, and OCI digests before deployment.
3. Before the ACK canary, identify every stale Pod UID and verify that each
   affected node already has a healthy desired replacement for management,
   networking, MQTT, agent, and business workloads.
4. Obtain explicit authorization for every affected edge node. Deploying the
   fixed CloudCore can immediately delete the identified stale UID from those
   nodes; ACK authorization alone does not authorize that edge mutation.
5. Before rolling either image, require three cross-host CloudCore replicas,
   CloudCore PDB `minAvailable=2`, two controller-manager replicas,
   controller-manager PDB `minAvailable=1`, `maxUnavailable=0`, working health
   probes, and the namespace-scoped leader-election Role/RoleBinding.
6. When migrating from `.5`, install the Lease/Event RBAC, stop the singleton
   `.5` controller completely, start one `.6` replica and wait for its Lease,
   then scale `.6` to two replicas. This short controller-only gap prevents the
   non-leader-elected `.5` process from reconciling concurrently and avoids a
   first-create Lease race. For later leader-elected releases, use the normal
   zero-unavailable rolling strategy. Verify both replicas are Ready on different
   cloud hosts, exactly one holder owns the `kubeedge-controller-manager`
   Lease, completed NodeUpgradeJobs remain unchanged, and leadership transfers
   without duplicate reconciliation when the leader is deliberately replaced.
7. Roll CloudCore with three replicas and `maxUnavailable=1`. Preserve the
   existing Service/NLB UID, ClusterIP, health-check port, NodePorts, external
   endpoints, and all active sessions.
8. Verify that only the expected stale UIDs are deleted, replacement Pod and
   container IDs remain unchanged, management paths remain available, and no
   business workload restarts.
9. After convergence, require a bounded steady-state window with no new E/F/W
   records in CloudCore, controller-manager, or the authorized canary edge.
10. Exercise at least one projected service account token refresh on the edge
   canary. Verify that the expiration timestamp advances, the Pod and container
   IDs remain unchanged, and no `token expired` error is emitted.
11. Delete the token-refresh canary normally and verify that no `Kind is
   missing` error is emitted, the exact UID is removed from both metadata
   tables, and a simulated stale UID cannot delete a same-name replacement.
12. Restart the edge canary and exercise a projected token request; verify that
    edge-originated Node requests and `ServiceAccountAccess` snapshots produce
    no `Kind is missing` records while their forwarding and authorization
    behavior remains functional.
13. Rebuild all formal cloud and edge artifacts from the final immutable `.6`
   tag; do not promote an RC digest or mix `.5` and `.6` source commits in the
   formal release set.

## Rollback

CloudCore can be rolled back to the digest-pinned `.5` image through the same
protected three-replica strategy without replacing the Service or NLB. A stale
Pod deletion is an intended convergence action and is not undone by rolling
CloudCore back; verify healthy desired replacement Pods before canary approval.

Do not run more than one `.5` controller-manager replica: that binary has no
leader election. A controller-manager rollback must restore `replicas=1`
before or atomically with the `.5` image, while preserving the current Lease,
ServiceAccount, and RBAC for a later retry.
