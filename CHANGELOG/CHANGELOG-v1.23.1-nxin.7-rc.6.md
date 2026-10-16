# KubeEdge v1.23.1-nxin.7-rc.6

Includes the DeviceTwin initial-membership database-failure unlock fix from rc.5.

The rc.5 installation-package used an incorrect scratch base. Official CRI
resource copy requires /bin/sh, sleep, cp and mv. Its canary copy failed and the
upgrade executor restored rc.4; all original running container IDs, attempts,
PIDs and host Boot ID were unchanged. rc.5 was never made the installer default.

This release uses the official Go 1.23.12 ARM64 build-tools image pinned by digest
and the official Ubuntu 18.04 installation-package runtime pinned by digest.
The checked prebuilt recipe executes shell, copy utilities and keadm version
during image construction. Release checks exercise the actual copy command and
verify that the copied EdgeCore hash equals the published binary.

Publish Linux ARM64 EdgeCore and keadm from one immutable source. CloudCore and
controller-manager deployments remain outside this edge-only repair. ACK
canary observations are recorded separately from isolated build and unit checks.
