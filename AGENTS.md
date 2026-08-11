# AGENTS.md

## Cursor Cloud specific instructions

KubeEdge is a Go monorepo (Kubernetes-native edge computing platform). It builds several
binaries (`cloudcore`, `edgecore`, `keadm`, `admission`, `controllermanager`, `csidriver`,
`edgesite-*`, `iptablesmanager`, `edgemark`, `conformance`). The primary developer loop is
`make` (see `Makefile`) plus the `hack/` scripts. There is no JS/Python app here.

### Toolchain / dependencies
- Go is provided by a wrapper that auto-downloads the pinned toolchain (`go1.23.12` from
  `go.mod` / `go.work`). Third-party deps are vendored in `vendor/`, so builds/tests run
  offline without fetching modules. The startup update script only runs `go mod download`.
- `make lint` auto-installs `golangci-lint v1.64.5` into `$GOPATH/bin` (`hack/lib/install.sh`);
  `kind`/`kubectl` are only installed on demand by the e2e scripts. None of these are in the
  update script.

### Build / test / lint (work out of the box)
- Build: `make all BUILD_WITH_CONTAINER=false` (or `WHAT=cloudcore` etc). Non-obvious gotcha:
  `make all` defaults to `BUILD_WITH_CONTAINER=true`, which needs Docker; always pass
  `BUILD_WITH_CONTAINER=false` for a local dev build. `make test` and `make lint` already run
  locally and do NOT need this flag.
- Test: `make test` (unit tests, ~3 min). Note it runs `make clean` first, which deletes
  `_output/local/bin`; rebuild binaries afterward if you still need them.
- Lint: `make lint` (~3 min on first run because it installs golangci-lint).

### Running the full cloud+edge platform (manual; heavy one-off setup)
`hack/local-up-kubeedge.sh` is the reference flow but does NOT work unmodified here: this VM
has no systemd (PID 1 is `tini`), and the script uses `systemctl` to manage containerd/cri.
Run the daemons manually instead. One-off host setup needed (not in the update script):
install Docker + `kind` + `kubectl`, plus a host `containerd` + CNI plugins for `edgecore`.
Key non-obvious gotchas discovered when bringing it up:
- Start `dockerd` and `containerd` directly (no `systemctl`). Docker 29 + `fuse-overlayfs`
  needs `/etc/docker/daemon.json` = `{"storage-driver":"fuse-overlayfs","features":{"containerd-snapshotter":false}}`.
- The host `containerd` that `edgecore` uses must NOT use the `overlayfs` snapshotter — nested
  overlay mounts fail with `invalid argument`. Set the CRI snapshotter to `native`
  (`[plugins.'io.containerd.cri.v1.images'] snapshotter = 'native'`) AND add a transfer
  unpack_config, otherwise image pulls fail with `no unpack platforms defined`:
  `[[plugins.'io.containerd.transfer.v1.local'.unpack_config]]` with `platform='linux/amd64'`
  and `snapshotter='native'`.
- Run `edgecore` with `CHECK_EDGECORE_ENVIRONMENT=false` (its preflight aborts if it detects a
  running kubelet, e.g. the kind node's), and point `--config` at a root-owned writable path
  like `/etc/kubeedge/config/edgecore.yaml` — edgecore rewrites its config file on startup and
  fails on a file it cannot open for writing.
- cloudcore and edgecore run on the same host, so set cloudcore `advertiseAddress` and the
  edgecore `httpServer`/`websocket`/`quic` server addresses to `127.0.0.1`. Apply the CRDs in
  `build/crds/**` after cloudcore starts (it auto-creates the `kubeedge` ns, CA, and
  `tokensecret`); pass the decoded `tokensecret` as the edgecore `token`.
- A quick end-to-end smoke test: deploy a pod with `nodeName: edge-node`, `hostNetwork: true`,
  and a catch-all toleration; it should reach `Running` on `edge-node`.
