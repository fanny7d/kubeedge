# KubeEdge v1.23.1-nxin.4

## Release basis

- Base: the upstream `v1.23.1` release tag at commit
  `1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`.
- This release is upstream `v1.23.1` plus the reviewed NXIN fixes retained
  from `.1`, `.2`, and `.3`, followed by the fixes below.
- It must not be built from `master` or the moving `release-1.23` branch.
- CloudCore, controller-manager, EdgeCore, keadm, archives, and installation
  images must be built from the same clean immutable `.4` tag and must report
  that tag and commit at runtime.

## Supersedes v1.23.1-nxin.3

The `.3` tag and artifacts remain immutable but are rejected for the final
edge rollout. Its resource-copy recovery path kept the old EdgeCore available,
but the official NodeUpgradeJob field test exposed three remaining issues:

- the directly-created CRI copy container did not carry the full Kubernetes
  pod labels, so embedded Kubelet could assign it an empty pod UID and reap it;
- the verified installation-image digest was not forwarded from the job to
  the keadm upgrade process; and
- a standalone successful keadm upgrade report could be retried after
  reconnect even when no active database task existed.

`v1.23.1-nxin.4-rc.1` fixed those issues and passed the digest-pinned copy and
upgrade flow, but it is also rejected for release. Its real-node confirmation
test showed that `keadm ctl confirm` trusted the configured KubeEdge CA while
MetaServer actually served a dynamically issued Kubernetes-CA certificate.
After correcting server verification, MetaServer still had no authenticated,
least-privilege path for the local node certificate to confirm its own task.

## Additional fixes in v1.23.1-nxin.4

- Apply the complete Kubernetes pod identity labels to both the direct CRI
  sandbox and copy container, keeping PLEG and runtime cleanup associated with
  the same guarded sandbox UID.
- Forward the architecture-specific, verified installation-image digest from
  NodeUpgradeJob to `keadm upgrade edge --image-digest`; the tag is never used
  as the only artifact identity.
- Remove a terminal standalone upgrade report when there is no corresponding
  database task, avoiding deterministic reconnect error loops without hiding
  an active task failure.
- Make the keadm MetaServer client trust the dynamic serving CA at
  `/etc/kubeedge/pki/metaserver/ca.crt` while continuing to present the
  root-owned node certificate and private key.
- Make MetaServer accept client chains from both its dynamic serving CA and
  the configured KubeEdge node CA.
- Authenticate only TLS-verified client certificate chains with the standard
  Kubernetes CommonName and Organization identity mapping.
- Add a narrow local authorizer that permits only an X.509 identity named
  `system:node:<local-node>`, in group `system:nodes`, to create the exact core
  `v1` resource path `taskupgrade/confirm-upgrade`. Every other request keeps
  the existing discovery and RBAC behavior.
- Do not use `InsecureSkipVerify`, bearer-token extraction, wildcard RBAC, or
  a persistent cloud-side confirmation binding.

## Fixes retained from earlier NXIN releases

- Preserve CloudCore HA task routing so only the replica owning the edge
  session handles a shared node task.
- Preserve ObjectSync delivery, controller leader election, service-account
  audience normalization, Lease response handling, and security/path cleanup
  fixes.
- Preserve the short-lived resource-copy guard, atomic EdgeCore replacement,
  staged rollback binary, and automatic restart of the previous EdgeCore when
  an upgrade step fails.
- Preserve clean version metadata and deterministic cloud/edge artifact
  manifests.

## Required verification gates

1. Pass local and native Linux/ARM64 tests with `GOWORK=off` and
   `GOFLAGS=-mod=vendor`, including no-inline tests where gomonkey requires it.
2. Build every formal artifact from a fresh clone of the immutable `.4` tag;
   verify embedded version, exact commit, clean tree, Go version, ELF platform,
   archive checksums, OCI labels, and registry digests.
3. Keep the edge root filesystem below the NodeUpgradeJob disk threshold
   before creating the job. Remove only verified-unused, recoverable historical
   installation images; never prune business images indiscriminately.
4. Roll the matching `.4` cloud images through ACK while preserving the
   protected NLB identity, service endpoints, and at least two ready CloudCore
   replicas.
5. On one canary, keep containerd and the independent management channel
   online, verify a usable rollback backup, and run the digest-pinned official
   NodeUpgradeJob with `requireConfirmation: true`.
6. Require the target keadm copied by Check to complete `keadm ctl confirm`
   through verified mTLS without temporary RBAC, then require Check, BackUp,
   Upgrade, and node status to be successful.
7. Verify exact EdgeCore and keadm hashes, Node Ready and reported version,
   workload readiness and restart counts, database integrity, empty terminal
   task/report/guard state, and bounded steady-state cloud and edge error logs.
