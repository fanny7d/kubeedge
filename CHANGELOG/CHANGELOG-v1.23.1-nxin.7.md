# KubeEdge v1.23.1-nxin.7

## Release basis and scope

- Base: the immutable NXIN `v1.23.1-nxin.6` tag at commit
  `004104eeb8ceb194acca609fe1bec7402c566251`.
- This release backports upstream KubeEdge commit
  `7a58297e79807a56573b92a27c18d08d38f78fb0` and adds the bounded
  PolicyController repair described below. It must not be built from `master`
  or another moving branch.
- `v1.23.1-nxin.7-rc.3` supersedes the immutable `rc.2` tag as a
  CloudCore-only release candidate. EdgeCore, controller-manager, keadm,
  offline archives, and installation images remain pinned to
  `v1.23.1-nxin.6` and must not be rebuilt or rolled with this RC.
  The differing NXIN suffixes identify component-specific patches; both sides
  retain the same upstream KubeEdge `v1.23.1` API and cloud-edge protocol
  baseline. Any future shared API, CloudHub/EdgeHub protocol, or edge-side
  change must trigger a coordinated EdgeCore build and compatibility canary.
- The rc.3 source line starts from the immutable rc.2 commit and deliberately
  excludes the unrelated JSON patch and `cri.md` changes later merged into the
  previous release branch.

## Root cause

When an edge Node is deleted, SyncController deletes the ObjectSync delivery
records associated with that Node. If a Node with the same name is then
recreated, an existing ServiceAccountAccess can still retain the name in
`status.nodeList`. Its spec is unchanged, so the former PolicyController path
neither recreated the missing ObjectSync nor sent the authorization snapshot
again. A normal annotation or status update did not repair the missing
delivery record.

The upstream status fix is also required: a delete-only node-list change must
be persisted. Without that fix, some delete/recreate event orderings retain
the stale name and bypass the normal new-node insertion path.

If that ServiceAccountAccess served only the deleted Node, the normal cleanup
removed the complete ServiceAccountAccess object. A Node-create event that only
enqueued its former name could not repair this ordering because reconciliation
stopped at `NotFound` and the unchanged Pod emitted no new event.

CloudCore versions predating ObjectSync spec preservation could leave existing
ServiceAccountAccess ObjectSync records with an empty `objectAPIVersion` or
`objectKind`. The `rc.1` missing-record repair correctly left existing records
alone, but those incomplete records could not be processed by SyncController
and their delete events were filtered out. A new ServiceAccountAccess snapshot
is required once to backfill their spec and resume normal reliable delivery.

The `rc.1` restore path also treated a terminating ServiceAccount as live. If a
finalizer prolonged ServiceAccount deletion while a matching Pod remained, the
controller could alternate between deleting and recreating its
ServiceAccountAccess and repeatedly send delete snapshots.

The rc.2 legacy delete predicate accepted every ObjectSync with an empty kind
and non-empty object name. A node garbage collection in a cluster containing
unrelated legacy records could therefore enqueue unrelated requests on the
single-worker PolicyController. Node-create mapping also performed a direct Pod
list in an event mapper, where a transient failure could not be retried. Finally,
Role and ClusterRole reads used APIReader without matching chart RBAC, and a
failed rule read could be mistaken for a valid partial authorization snapshot.

## Fix

- Persist every ServiceAccountAccess node-list change, including delete-only
  changes.
- Watch edge Node creation and edge-role label transitions, while explicitly
  ignoring Node heartbeat updates, deletion, and generic events.
- When a Node-create request finds the controller-owned ServiceAccountAccess
  absent, recreate it only if the matching live ServiceAccount and at least one
  matching Pod on a live edge Node still exist. API read and create failures
  remain retryable; a ServiceAccount that is absent or terminating and stale
  requests without a live workload do not recreate an orphan object.
- Watch only deletion of ServiceAccountAccess ObjectSync records. ObjectSync
  create, status update, and generic events do not enqueue reconciliation.
  A legacy empty-kind delete is enqueued only when a cache lookup confirms that
  its object name and UID identify the live ServiceAccountAccess. A cache read
  failure is converted to an internal request whose API read can be retried;
  old-UID and unrelated legacy records are ignored.
- When the ServiceAccountAccess spec is unchanged, verify the exact
  namespace/name pair derived from the live ServiceAccountAccess UID and each
  retained node. Only an API `NotFound` (or an ObjectSync already terminating)
  is treated as missing. If the exact existing record lacks an API version,
  kind, or object name, send one current snapshot so CloudHub can backfill the
  legacy spec. A complete record with an empty or stale object resource version
  remains owned by SyncController and is not repeatedly sent by
  PolicyController.
- Send an idempotent insert only to missing, incomplete, or newly added nodes.
  Retry through a per-key exponential rate limiter starting at five seconds and
  capped at five minutes; a successful reconcile clears the retry state.
- Return a zero reconcile result with errors so controller-runtime does not add
  its warning about a non-zero result being ignored.
- Index Pods by node name and use the cache for normal Node event mapping. A
  mapping failure becomes a rate-limited Node request and is retried from
  Reconcile instead of losing the one-shot create event.
- Ignore Pod status-only and generic events. Existing ObjectSync checks use the
  manager cache, with APIReader confirmation only for cache misses, so retained
  nodes no longer produce one direct API read each on the normal hot path.
- Grant the authorization feature role every `get` permission required by
  APIReader, including Roles and ClusterRoles. Propagate rule list/get failures
  to Reconcile so an incomplete snapshot is never persisted or sent.

This repair is cluster-wide. A named edge node can be used as the first
acceptance probe, but it is not an allowlist and does not bound the repair to
that node.

## Verification gates for v1.23.1-nxin.7-rc.3

1. Pass PolicyController unit and race tests, including exact current-UID
   ObjectSync lookup, old-UID rejection, in-flight handling, event predicate
   filtering, exact legacy delete UID classification, retryable Node mapping,
   Pod status filtering, terminating-ServiceAccount loop prevention, legacy
   empty-spec backfill, rule-read failure preservation, bounded retry, and exact
   message targets.
2. Pass SyncController and CloudHub regression tests and compile CloudCore for
   Linux/amd64.
3. Build and push only the CloudCore Linux/amd64 image from a clean checkout of
   the immutable `v1.23.1-nxin.7-rc.3` tag. Record and deploy the OCI index
   digest rather than a mutable tag or only a platform-manifest digest.
4. Roll the three CloudCore replicas with at least two continuously Ready,
   preserve the Service/NLB identity and Secret checksum, and allow at most one
   PolicyController Lease transition.
5. Use `asset-605047636244` and its exact Node UID as the first probe. Within
   60 seconds of the new leader becoming active, require all five expected
   ServiceAccountAccess ObjectSync records to exist; within 120 seconds require
   every non-empty object resource version to equal its live source resource
   version. The six pre-existing incomplete records on `plus-17819320948` and
   `plus-497668035582` must gain complete specs and converge without deleting
   and recreating already-correct records.
6. Preserve the already-correct ObjectSync UID, the target Node UID and Ready
   condition, all five Pod readiness states, and all container restart counts.
   Observe CloudCore and the PolicyController Lease for at least 15 minutes and
   reject new related error, warning, or fatal logs.

## Rollback

Roll CloudCore back to the digest-pinned `rc.2` Helm revision that preceded
`rc.3`, without replacing the Service, NLB, or Secret. Newly repaired
ObjectSync records are compatible with the previous CloudCore and should not
be deleted during rollback. EdgeCore and controller-manager remain on `.6`
throughout, so they require no rollback action.
