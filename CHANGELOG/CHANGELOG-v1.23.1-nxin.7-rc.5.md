# KubeEdge v1.23.1-nxin.7-rc.5

Base: immutable `v1.23.1-nxin.7-rc.4`, commit
`46ff99c575720e15f1524ffb314470a729bfaef7`.

## EdgeCore DeviceTwin fix

During initial full membership synchronization, `addDevice` does not acquire a
per-device mutex. If its bounded database retries fail, the former error branch
unconditionally unlocked that mutex, terminating EdgeCore with
`fatal error: sync: unlock of unlocked mutex`.

Unlock on this error path only for a delta update, matching the existing lock
condition. Retain bounded retries, removal of the failed in-memory device and
processing of subsequent devices. Do not suppress database failures or disable
DeviceTwin.

Regression coverage injects a database-lock failure in both initial and delta
updates, checks the retry count and failed-device removal, and verifies that the
next device is processed and both mutexes remain usable. The initial-sync test
reproduces the fatal process exit on rc.4.

This candidate publishes Linux ARM64 EdgeCore and keadm from the same immutable
source, plus an ARM64 installation-package image. Deployment is staged on the
edge side. CloudCore and controller-manager deployments are outside this fix.
Artifact hashes, toolchain identity and scoped ACK observations are recorded in
the published release manifest; compilation and isolated tests do not establish
business acceptance.
