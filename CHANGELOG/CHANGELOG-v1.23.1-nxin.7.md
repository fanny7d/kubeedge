# KubeEdge v1.23.1-nxin.7

## Release basis and scope

- Base: the immutable NXIN `v1.23.1-nxin.6` tag at commit
  `004104eeb8ceb194acca609fe1bec7402c566251`.
- This release backports upstream KubeEdge commit
  `7a58297e79807a56573b92a27c18d08d38f78fb0` and adds the bounded
  PolicyController repair described below. It must not be built from `master`
  or another moving branch.
- `v1.23.1-nxin.7-rc.1` is a CloudCore-only release candidate. EdgeCore,
  controller-manager, keadm, offline archives, and installation images remain
  pinned to `v1.23.1-nxin.6` and must not be rebuilt or rolled with this RC.

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

## Fix

- Persist every ServiceAccountAccess node-list change, including delete-only
  changes.
- Watch edge Node creation and edge-role label transitions, while explicitly
  ignoring Node heartbeat updates, deletion, and generic events.
- When a Node-create request finds the controller-owned ServiceAccountAccess
  absent, recreate it only if the matching live ServiceAccount and at least one
  matching Pod on a live edge Node still exist. API read and create failures
  remain retryable; stale requests without a live workload do not recreate an
  orphan object.
- Watch only deletion of ServiceAccountAccess ObjectSync records. ObjectSync
  create, status update, and generic events do not enqueue reconciliation.
- When the ServiceAccountAccess spec is unchanged, verify the exact
  namespace/name pair derived from the live ServiceAccountAccess UID and each
  retained node. Only an API `NotFound` (or an ObjectSync already terminating)
  is treated as missing; an existing record with an empty object resource
  version is treated as in flight.
- Send an idempotent insert only to missing or newly added nodes. Requeue after
  five seconds until asynchronous CloudHub delivery has created the record.

This repair is cluster-wide. A named edge node can be used as the first
acceptance probe, but it is not an allowlist and does not bound the repair to
that node.

## Verification gates for v1.23.1-nxin.7-rc.1

1. Pass PolicyController unit and race tests, including exact current-UID
   ObjectSync lookup, old-UID rejection, in-flight handling, event predicate
   filtering, deduplicated Node mapping, and exact message targets.
2. Pass SyncController and CloudHub regression tests and compile CloudCore for
   Linux/amd64.
3. Build and push only the CloudCore Linux/amd64 image from a clean checkout of
   the immutable `v1.23.1-nxin.7-rc.1` tag. Record and deploy the OCI index
   digest rather than a mutable tag or only a platform-manifest digest.
4. Roll the three CloudCore replicas with at least two continuously Ready,
   preserve the Service/NLB identity and Secret checksum, and allow at most one
   PolicyController Lease transition.
5. Use `asset-605047636244` and its exact Node UID as the first probe. Within
   60 seconds of the new leader becoming active, require all five expected
   ServiceAccountAccess ObjectSync records to exist; within 120 seconds require
   every non-empty object resource version to equal its live source resource
   version.
6. Preserve the already-correct ObjectSync UID, the target Node UID and Ready
   condition, all five Pod readiness states, and all container restart counts.
   Observe CloudCore and the PolicyController Lease for at least 15 minutes and
   reject new related error, warning, or fatal logs.

## Rollback

Roll CloudCore back to the digest-pinned Helm revision that preceded the RC,
without replacing the Service, NLB, or Secret. Newly repaired ObjectSync
records are compatible with the previous CloudCore and should not be deleted
during rollback. EdgeCore and controller-manager remain on `.6` throughout,
so they require no rollback action.
