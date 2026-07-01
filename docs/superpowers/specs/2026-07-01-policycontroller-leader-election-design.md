# Policycontroller Leader Election Design

## Context

CloudCore can run multiple replicas behind a load balancer, but not every CloudCore module is safe to run as an active controller in every replica. `cloud/pkg/policycontroller/policycontroller.go` creates a controller-runtime manager without leader election and still carries a `TODO: leader election` comment. When `requireAuthorization` is enabled, every CloudCore replica starts the policycontroller and reconciles the same `ServiceAccountAccess` objects.

The policycontroller reconciler updates both `ServiceAccountAccess.spec` and `ServiceAccountAccess.status`, then sends the object to edge nodes. With three CloudCore replicas, concurrent reconciles can race on the same object and produce Kubernetes update conflicts such as `object has been modified`. In the observed failure chain, the downstream `ObjectSync` remained at `objectResourceVersion: "0"`, so the edge node did not receive the `ServiceAccountAccess`.

## Goal

Make `policycontroller` safe in a multi-replica CloudCore deployment by ensuring only one CloudCore replica actively runs the `ServiceAccountAccess` reconciler at a time, while standby replicas can automatically take over when the leader exits.

## Non-Goals

- Do not make every CloudCore module single-active. CloudHub and CloudStream are connection-facing modules and should remain horizontally scalable.
- Do not redesign `ServiceAccountAccess` reconciliation logic or message delivery semantics in this phase.
- Do not change the default `requireAuthorization` feature gate behavior.
- Do not solve all CloudCore HA gaps in one patch. Follow-up phases should separately audit `synccontroller`, EdgeController, DeviceController, task manager, and CloudStream failover behavior.

## Design

Enable controller-runtime leader election in `NewAccessRoleControllerManager`. The policycontroller manager will use Kubernetes Lease objects as its lock resource. Only the elected CloudCore replica starts the controller-runtime leader-election runnables, including the `ServiceAccountAccess` reconciler. Other replicas keep the manager process alive as standby and wait for the lease.

The manager options should set:

```go
LeaderElection:          true,
LeaderElectionID:        "kubeedge-policycontroller",
LeaderElectionNamespace: <resolved namespace>,
```

The namespace should not be permanently hard-coded in source. The preferred implementation is:

1. Use `POD_NAMESPACE` when present, which is straightforward for the Helm/container deployment.
2. Fall back to `constants.SystemNamespace` (`kubeedge`) when `POD_NAMESPACE` is empty, preserving current local and binary-mode behavior.

This keeps the immediate deployment compatible with the existing `kubeedge` namespace while avoiding a source-level assumption that CloudCore always runs there.

## RBAC

The existing CloudCore ClusterRole already grants `coordination.k8s.io/leases` with `get`, `list`, `watch`, `create`, and `update` in the Helm chart and build manifest. The LeaseLock path used by controller-runtime primarily needs get/create/update. Still, the generated/deployable RBAC should be normalized to include:

```yaml
verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

This matches common controller-runtime leader election RBAC and avoids future breakage if controller-runtime changes lock behavior or cleanup behavior.

Update these deploy surfaces:

- `manifests/charts/cloudcore/templates/rbac_cloudcore.yaml`
- `build/cloud/03-clusterrole.yaml`
- `build/cloud/ha/01-ha-prepare.yaml`

## Tests

Add focused unit coverage in `cloud/pkg/policycontroller`:

- Verify the policycontroller manager options enable leader election.
- Verify the lock ID is `kubeedge-policycontroller`.
- Verify the namespace resolver uses `POD_NAMESPACE` when set.
- Verify the namespace resolver falls back to `kubeedge` when `POD_NAMESPACE` is empty.

Avoid tests that require a live Kubernetes API server for the first patch. The behavior that matters for this phase is that the manager is constructed with the correct leader election options and deploy manifests include the needed Lease permissions.

Add manifest checks with existing shell-test style if practical:

- Render or inspect Helm RBAC and assert Lease verbs include `patch` and `delete`.
- Inspect `build/cloud/03-clusterrole.yaml` and `build/cloud/ha/01-ha-prepare.yaml` for the same Lease verbs.

## Verification

Local verification:

```bash
go test ./cloud/pkg/policycontroller
make all WHAT=cloudcore BUILD_WITH_CONTAINER=false
```

Repository-level verification when time allows:

```bash
make test WHAT=cloud
make verify
```

Live verification after building a patched image:

```bash
kubectl -n kubeedge rollout status deploy/cloudcore --timeout=180s
kubectl -n kubeedge get lease kubeedge-policycontroller -w
kubectl -n kubeedge logs deploy/cloudcore --all-containers --tail=200 | grep -i leader
```

Then trigger or recreate one affected `ServiceAccountAccess` and verify:

```bash
kubectl get serviceaccountaccess -A
kubectl get objectsync -A -o yaml | grep -A3 objectResourceVersion
```

The expected result is a non-zero `ObjectSync.status.objectResourceVersion` for the affected edge node, followed by the object appearing in the edge node's local metastore.

## Rollout

1. Apply RBAC first.
2. Roll out the patched CloudCore image with three replicas.
3. Confirm a single `kubeedge-policycontroller` Lease holder.
4. Reconcile or recreate affected `ServiceAccountAccess` objects.
5. Confirm ObjectSync and edge-side persistence.

## Rollback

Rollback is simple:

1. Revert the CloudCore image to the previous version.
2. If the old image is used with three replicas and `requireAuthorization` remains enabled, temporarily scale CloudCore to one replica before reconciling `ServiceAccountAccess`.
3. The extra Lease RBAC permissions are harmless and do not need immediate rollback.

## Risks

- If the namespace resolver chooses the wrong namespace, policycontroller will fail to acquire its Lease. Using `POD_NAMESPACE` first and `kubeedge` fallback keeps this risk low.
- Leader election only protects controller-runtime runnables in policycontroller. It does not make other CloudCore modules single-active.
- The existing reconcile logic still uses update semantics that can conflict with other writers. Leader election removes same-controller multi-replica conflicts, but unrelated concurrent edits can still need normal requeue handling.
