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

## Required canary gates

1. Pass the complete edgecontroller test package, focused MetaManager token
   tests, repeated tests, and race tests for status-patch, Pod-status,
   bound-token cleanup, and proactive token refresh paths.
2. Build an immutable `.6-rc` CloudCore from a clean checkout and verify its
   version, commit, clean-tree marker, Go version, platform, binary hash, and
   OCI digest before deployment.
3. Before the ACK canary, identify every stale Pod UID and verify that each
   affected node already has a healthy desired replacement for management,
   networking, MQTT, agent, and business workloads.
4. Obtain explicit authorization for every affected edge node. Deploying the
   fixed CloudCore can immediately delete the identified stale UID from those
   nodes; ACK authorization alone does not authorize that edge mutation.
5. Roll CloudCore with three replicas and `maxUnavailable=1`. Preserve the
   existing Service/NLB UID, ClusterIP, health-check port, NodePorts, external
   endpoints, and all active sessions.
6. Verify that only the expected stale UIDs are deleted, replacement Pod and
   container IDs remain unchanged, management paths remain available, and no
   business workload restarts.
7. After convergence, require a bounded steady-state window with no new E/F/W
   records in CloudCore, controller-manager, or the authorized canary edge.
8. Exercise at least one projected service account token refresh on the edge
   canary. Verify that the expiration timestamp advances, the Pod and container
   IDs remain unchanged, and no `token expired` error is emitted.
9. Rebuild all formal cloud and edge artifacts from the final immutable `.6`
   tag; do not promote an RC digest or mix `.5` and `.6` source commits in the
   formal release set.

## Rollback

CloudCore can be rolled back to the digest-pinned `.5` image through the same
protected three-replica strategy without replacing the Service or NLB. A stale
Pod deletion is an intended convergence action and is not undone by rolling
CloudCore back; verify healthy desired replacement Pods before canary approval.
