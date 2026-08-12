# KubeEdge v1.23.1-nxin.8

## Release basis and scope

- EdgeCore-only release based on the immutable `v1.23.1-nxin.6` tag at commit
  `004104eeb`. It does not include the CloudCore-only `.7` release line.
- CloudCore and controller-manager remain on their independently validated
  versions. This release does not require a coordinated cloud rollout.

## Root cause

When EdgeCore started without a CloudCore connection, MetaServer ignored its
already-provisioned CA and serving certificate and waited only for the live
connection. The wait had a four-minute startup timeout, and MetaServer handled
that timeout with `panic`. Because MetaServer runs inside EdgeCore, systemd then
restarted the whole EdgeCore process every four minutes even though its cached
credentials were still valid.

## Fix

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
- Preserve explicit failure for permanent local setup/configuration errors;
  the change is limited to cloud-connectivity and certificate-readiness waits.

## Required acceptance gates

1. Pass the focused MetaServer certificate and server unit tests, race tests,
   `go vet`, and an ordinary Linux ARM64 build/test using the repository's
   pinned Go 1.23 build image.
2. On an authorized edge canary, record the old binary hash/version, boot ID,
   EdgeCore PID and systemd restart counter before replacing the binary. Keep a
   verified `.6` rollback binary on the node.
3. Block only the resolved CloudCore TCP endpoints while preserving SSH and
   all other networking. Restart EdgeCore and require MetaServer to report that
   it used valid cached credentials.
4. Observe longer than the old four-minute failure window. EdgeCore must remain
   active with one stable PID and no increase in the systemd restart counter;
   no `wait for CA ready` panic may occur.
5. Restore CloudCore connectivity and require EdgeHub sessions to reconnect
   without another EdgeCore restart.
6. In the final field test, cold-start the device while physically offline,
   observe the same stability gate, reconnect the cable, and verify recovery.

## Rollback

Restore the preserved `v1.23.1-nxin.6` EdgeCore binary atomically and restart
only the EdgeCore service. Remove any temporary CloudCore-only firewall rules
before judging reconnect behavior. No CloudCore rollback is required.
