# KubeEdge v1.23.1-nxin.7

## Release basis and scope

- Base: the immutable NXIN `v1.23.1-nxin.6` tag at commit
  `004104eeb8ceb194acca609fe1bec7402c566251`.
- This release backports upstream KubeEdge commit
  `7a58297e79807a56573b92a27c18d08d38f78fb0`, adds the bounded
  PolicyController repair described below, preserves JSON patch operation value
  types, and fixes EdgeCore offline startup and reconnect behavior. It must not
  be built from `master` or another moving branch.
- `v1.23.1-nxin.7-rc.4` supersedes the immutable `rc.3` tag and restores one
  unified `.7` source line for CloudCore and EdgeCore. It starts from the
  immutable `rc.3` commit, includes the selected JSON patch commit from the
  former release branch, and then applies the two EdgeCore fixes. The local
  `.8-rc.1` and `.8-rc.2` tags were development candidates based directly on
  `.6`; they are not part of this release and must not be published.
- Deployment remains staged by component. The EdgeCore rc.4 canary does not
  require replacing the already validated CloudCore rc.3 deployment because
  the new runtime fixes are edge-side only. Before the final `.7` release, all
  shipped components and offline installation artifacts must be rebuilt from
  the same immutable final tag and pass their component-specific gates.

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

When EdgeCore started without a CloudCore connection, MetaServer ignored its
already-provisioned CA and serving certificate and waited only for the live
connection. The wait had a four-minute startup timeout, and MetaServer handled
that timeout with `panic`. Systemd then restarted the entire EdgeCore process
every four minutes even though its cached credentials were still valid.

The optional EdgeStream tunnel also retried CloudCore every two seconds for the
entire offline period and logged each failed dial twice. It did not cause the
four-minute restart, but it created an unnecessary connection and log storm.

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

### JSON patch value preservation

- Preserve the original JSON value type for add, replace, and test operations
  instead of converting every value to a string.
- Cover strings, numbers, booleans, objects, arrays, and null in unit tests.

### EdgeCore offline startup

- Validate the cached Kubernetes CA and MetaServer serving certificate before
  waiting for CloudCore. Validation includes the trust chain, validity period,
  exact node identity, `system:nodes` organization, server-auth usage, and all
  configured MetaServer IP SANs.
- Start MetaServer immediately from valid cached credentials while offline.
- If no trustworthy cached credentials exist, keep only MetaServer waiting on
  the EdgeCore lifecycle context. A temporary CloudCore outage no longer
  returns the old four-minute timeout or terminates EdgeCore.
- After connectivity returns, fetch and atomically persist a valid CA. An
  invalid or incompatible replacement cannot overwrite the CA that verifies
  the current serving certificate.
- Preserve explicit failure for permanent local setup or configuration errors;
  the change is limited to cloud-connectivity and certificate-readiness waits.

### EdgeStream reconnect backoff

- Reconnect with a cancelable exponential delay of 2, 4, 8, 16, and 30
  seconds, capped at 30 seconds with 20 percent jitter.
- Reset the next delay to two seconds only after a tunnel session remains
  established for at least one minute. Short-lived sessions retain the
  accumulated delay and cannot create a tight loop.
- Emit one warning per unavailable endpoint attempt instead of two errors, and
  cancel a pending delay immediately during EdgeCore shutdown.

## Verification gates for v1.23.1-nxin.7-rc.4

1. Verify that the candidate is a clean, linear descendant of the immutable
   rc.3 tag and contains only the selected JSON patch change, the two EdgeCore
   fixes, their tests, and this release-note consolidation.
2. Pass PolicyController unit and race tests, including exact current-UID
   ObjectSync lookup, old-UID rejection, in-flight handling, event predicate
   filtering, exact legacy delete UID classification, retryable Node mapping,
   Pod status filtering, terminating-ServiceAccount loop prevention, legacy
   empty-spec backfill, rule-read failure preservation, bounded retry, and exact
   message targets.
3. Pass SyncController, CloudHub, and JSON patch regression tests; compile
   CloudCore for Linux/amd64 from the integrated source without deploying it.
4. Pass focused MetaServer certificate/server and EdgeStream unit tests, race
   tests, `go vet`, and an ordinary Linux/arm64 build using the repository's
   pinned Go 1.23 build image.
5. Build EdgeCore from a clean checkout of the immutable rc.4 tag. On the
   authorized canary, record the old binary hash/version, boot ID, EdgeCore PID,
   and systemd restart counter; preserve a verified `.6` rollback binary.
6. Block only the resolved CloudCore TCP endpoints while preserving SSH and
   all other networking. Restart EdgeCore and require MetaServer to report that
   it used valid cached credentials. Observe longer than the old four-minute
   failure window: EdgeCore must retain one PID and restart count, with no
   `wait for CA ready` panic. EdgeStream retries must grow to the 30-second cap.
7. Restore CloudCore connectivity and require EdgeHub and EdgeStream to recover
   without another EdgeCore restart. Do not use business-container health as a
   pass condition for this narrowly scoped EdgeCore gate.
8. In the final field test, cold-start the device while physically offline,
   observe the same stability gate, reconnect the cable, and verify recovery.

## Rollback

For the EdgeCore rc.4 canary, atomically restore the preserved
`v1.23.1-nxin.6` EdgeCore binary and restart only the EdgeCore service. Remove
temporary CloudCore-only firewall rules before judging reconnect behavior.
CloudCore remains on the previously accepted rc.3 image, so this canary does
not require a CloudCore rollback. Its existing digest-pinned rc.2 Helm rollback
path remains unchanged.
