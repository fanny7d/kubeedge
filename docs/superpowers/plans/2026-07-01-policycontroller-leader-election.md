# Policycontroller Leader Election Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable leader election for CloudCore policycontroller so multi-replica CloudCore deployments run only one active `ServiceAccountAccess` reconciler.

**Architecture:** Add a small manager-options builder in `cloud/pkg/policycontroller` that centralizes leader election settings and namespace resolution. Use those options when constructing the controller-runtime manager. Normalize CloudCore RBAC lease verbs across Helm and build manifests.

**Tech Stack:** Go, controller-runtime v0.19.7, Kubernetes Lease leader election, shell manifest tests, KubeEdge CloudCore manifests.

---

## File Structure

- Modify `cloud/pkg/policycontroller/policycontroller.go`: add constants, namespace resolver, manager options builder, and use the options in `NewAccessRoleControllerManager`.
- Modify `cloud/pkg/policycontroller/policycontroller_test.go`: add unit tests for leader-election options and namespace resolution.
- Modify `manifests/charts/cloudcore/templates/rbac_cloudcore.yaml`: add `patch` and `delete` to Lease verbs.
- Modify `build/cloud/03-clusterrole.yaml`: add `patch` and `delete` to Lease verbs.
- Modify `build/cloud/ha/01-ha-prepare.yaml`: add `patch` and `delete` to Lease verbs.
- Create `tests/scripts/cloudcore_leader_election_rbac_test.sh`: assert all deploy surfaces expose the normalized Lease verbs.

---

### Task 1: Add Failing Policycontroller Leader Election Tests

**Files:**
- Modify: `cloud/pkg/policycontroller/policycontroller_test.go`

- [ ] **Step 1: Add tests for leader election options**

Append these tests near `TestNewAccessRoleControllerManager`:

```go
func TestPolicyControllerManagerOptionsEnableLeaderElection(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "edge-system")

	opts := newAccessRoleControllerManagerOptions()

	if !opts.LeaderElection {
		t.Fatal("expected policycontroller manager to enable leader election")
	}
	if opts.LeaderElectionID != policyControllerLeaderElectionID {
		t.Fatalf("LeaderElectionID = %q, want %q", opts.LeaderElectionID, policyControllerLeaderElectionID)
	}
	if opts.LeaderElectionNamespace != "edge-system" {
		t.Fatalf("LeaderElectionNamespace = %q, want %q", opts.LeaderElectionNamespace, "edge-system")
	}
}

func TestPolicyControllerLeaderElectionNamespaceFallsBackToSystemNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")

	if got := policyControllerLeaderElectionNamespace(); got != "kubeedge" {
		t.Fatalf("policyControllerLeaderElectionNamespace() = %q, want %q", got, "kubeedge")
	}
}

func TestPolicyControllerLeaderElectionNamespaceUsesPodNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "custom-kubeedge")

	if got := policyControllerLeaderElectionNamespace(); got != "custom-kubeedge" {
		t.Fatalf("policyControllerLeaderElectionNamespace() = %q, want %q", got, "custom-kubeedge")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./cloud/pkg/policycontroller -run 'TestPolicyController(ManagerOptionsEnableLeaderElection|LeaderElectionNamespace)' -count=1
```

Expected: FAIL because `newAccessRoleControllerManagerOptions`, `policyControllerLeaderElectionID`, and `policyControllerLeaderElectionNamespace` are not defined.

- [ ] **Step 3: Commit failing tests if working in strict TDD branch checkpoints**

Do not commit to the shared branch unless the workflow explicitly wants red commits. Keep the red state as local evidence before Task 2.

---

### Task 2: Implement Policycontroller Leader Election Options

**Files:**
- Modify: `cloud/pkg/policycontroller/policycontroller.go`

- [ ] **Step 1: Add imports and constants**

Update imports to include `os`, `controllerruntimemetrics` stays as-is, and add the KubeEdge constants package:

```go
import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	controllerruntimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	policyv1alpha1 "github.com/kubeedge/api/apis/policy/v1alpha1"
	"github.com/kubeedge/beehive/pkg/core"
	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/messagelayer"
	"github.com/kubeedge/kubeedge/cloud/pkg/common/modules"
	pm "github.com/kubeedge/kubeedge/cloud/pkg/policycontroller/manager"
	"github.com/kubeedge/kubeedge/common/constants"
	kefeatures "github.com/kubeedge/kubeedge/pkg/features"
)

const (
	podNamespaceEnv                 = "POD_NAMESPACE"
	policyControllerLeaderElectionID = "kubeedge-policycontroller"
)
```

Keep alignment after `gofmt`; the exact spacing does not matter.

- [ ] **Step 2: Add namespace resolver and options builder**

Place these functions after `init()`:

```go
func policyControllerLeaderElectionNamespace() string {
	if namespace := os.Getenv(podNamespaceEnv); namespace != "" {
		return namespace
	}
	return constants.SystemNamespace
}

func newAccessRoleControllerManagerOptions() controllerruntime.Options {
	return controllerruntime.Options{
		Scheme: accessScheme,
		Metrics: controllerruntimemetrics.Options{
			SecureServing: false,
			BindAddress:   "0",
		}, // disable metrics
		LeaderElection:          true,
		LeaderElectionID:        policyControllerLeaderElectionID,
		LeaderElectionNamespace: policyControllerLeaderElectionNamespace(),
		// TODO: /healthz
	}
}
```

- [ ] **Step 3: Use the options builder**

Replace the inline options in `NewAccessRoleControllerManager`:

```go
controllerManager, err := controllerruntime.NewManager(kubeCfg, newAccessRoleControllerManagerOptions())
```

- [ ] **Step 4: Format and run focused tests**

Run:

```bash
gofmt -w cloud/pkg/policycontroller/policycontroller.go cloud/pkg/policycontroller/policycontroller_test.go
go test ./cloud/pkg/policycontroller -run 'TestPolicyController(ManagerOptionsEnableLeaderElection|LeaderElectionNamespace)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run full policycontroller package tests**

Run:

```bash
go test ./cloud/pkg/policycontroller -count=1
```

Expected: PASS.

---

### Task 3: Normalize Lease RBAC Verbs

**Files:**
- Modify: `manifests/charts/cloudcore/templates/rbac_cloudcore.yaml`
- Modify: `build/cloud/03-clusterrole.yaml`
- Modify: `build/cloud/ha/01-ha-prepare.yaml`

- [ ] **Step 1: Update Helm RBAC Lease verbs**

Change the Lease rule in `manifests/charts/cloudcore/templates/rbac_cloudcore.yaml` to:

```yaml
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

- [ ] **Step 2: Update build manifest Lease verbs**

Change the Lease rule in `build/cloud/03-clusterrole.yaml` to:

```yaml
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

- [ ] **Step 3: Update HA prepare manifest Lease verbs**

Change the Lease rule in `build/cloud/ha/01-ha-prepare.yaml` to:

```yaml
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

- [ ] **Step 4: Inspect diff**

Run:

```bash
git diff -- manifests/charts/cloudcore/templates/rbac_cloudcore.yaml build/cloud/03-clusterrole.yaml build/cloud/ha/01-ha-prepare.yaml
```

Expected: only the Lease verb arrays include `patch` and `delete`.

---

### Task 4: Add RBAC Manifest Regression Test

**Files:**
- Create: `tests/scripts/cloudcore_leader_election_rbac_test.sh`

- [ ] **Step 1: Write the failing shell test**

Create `tests/scripts/cloudcore_leader_election_rbac_test.sh`:

```bash
#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

assert_lease_verbs() {
  local file="$1"
  local line
  line="$(awk '
    /apiGroups: \["coordination.k8s.io"\]/ { in_coordination=1; next }
    in_coordination && /resources: \["leases"\]/ { in_leases=1; next }
    in_coordination && in_leases && /verbs:/ { print; exit }
  ' "${ROOT_DIR}/${file}")"

  if [[ -z "${line}" ]]; then
    echo "missing leases verbs in ${file}" >&2
    exit 1
  fi

  for verb in get list watch create update patch delete; do
    if [[ "${line}" != *"\"${verb}\""* ]]; then
      echo "missing lease verb ${verb} in ${file}: ${line}" >&2
      exit 1
    fi
  done
}

assert_lease_verbs "manifests/charts/cloudcore/templates/rbac_cloudcore.yaml"
assert_lease_verbs "build/cloud/03-clusterrole.yaml"
assert_lease_verbs "build/cloud/ha/01-ha-prepare.yaml"
```

- [ ] **Step 2: Make it executable**

Run:

```bash
chmod +x tests/scripts/cloudcore_leader_election_rbac_test.sh
```

- [ ] **Step 3: Run test**

Run:

```bash
bash tests/scripts/cloudcore_leader_election_rbac_test.sh
```

Expected after Task 3: PASS. If run before Task 3, FAIL with missing `patch` or `delete`.

---

### Task 5: Full Local Verification

**Files:**
- No new files.

- [ ] **Step 1: Run focused Go tests**

Run:

```bash
go test ./cloud/pkg/policycontroller -count=1
```

Expected: PASS.

- [ ] **Step 2: Run RBAC test**

Run:

```bash
bash tests/scripts/cloudcore_leader_election_rbac_test.sh
```

Expected: PASS.

- [ ] **Step 3: Build CloudCore**

Run:

```bash
make all WHAT=cloudcore BUILD_WITH_CONTAINER=false
```

Expected: build succeeds and produces the local `cloudcore` binary under `_output`.

- [ ] **Step 4: Optional broader test**

Run when time permits:

```bash
make test WHAT=cloud
```

Expected: cloud package tests pass. If unrelated legacy tests fail, capture the package and error text before deciding whether to fix or defer.

- [ ] **Step 5: Review git diff**

Run:

```bash
git diff -- cloud/pkg/policycontroller/policycontroller.go cloud/pkg/policycontroller/policycontroller_test.go manifests/charts/cloudcore/templates/rbac_cloudcore.yaml build/cloud/03-clusterrole.yaml build/cloud/ha/01-ha-prepare.yaml tests/scripts/cloudcore_leader_election_rbac_test.sh
```

Expected: diff only contains leader election options, tests, and Lease RBAC normalization.

---

### Task 6: Commit Implementation

**Files:**
- Stage only files modified by this plan.

- [ ] **Step 1: Stage implementation files**

Run:

```bash
git add cloud/pkg/policycontroller/policycontroller.go \
  cloud/pkg/policycontroller/policycontroller_test.go \
  manifests/charts/cloudcore/templates/rbac_cloudcore.yaml \
  build/cloud/03-clusterrole.yaml \
  build/cloud/ha/01-ha-prepare.yaml \
  tests/scripts/cloudcore_leader_election_rbac_test.sh
```

- [ ] **Step 2: Confirm staged scope**

Run:

```bash
git diff --cached --stat
git status --short
```

Expected: staged files are only the six implementation/test/RBAC files above. Existing untracked local tool files remain unstaged.

- [ ] **Step 3: Commit**

Run:

```bash
git commit -m "fix: add policycontroller leader election"
```

Expected: commit succeeds.

---

## Self-Review

- Spec coverage: implements leader election, namespace resolution, RBAC normalization, tests, verification, and rollout assumptions from the design.
- Placeholder scan: no `TBD`, `TODO`, or vague implementation steps are left except the intentional source comment `// TODO: /healthz`.
- Type consistency: tests refer to helpers and constants introduced in Task 2 with exact names.
