# NXIN KubeEdge v1.23.1 云边升级方案

本文用于把现网云侧和边缘节点统一升级到 `v1.23.1-nxin.5`。所有正式
制品必须来自上游 `v1.23.1` tag（commit
`1a7ee88d3c61dc781cc9c9053066ab3d38519dd5`）派生的同一个 NXIN 不可变
tag，禁止使用 `master` 或移动中的 release 分支构建。

## 1. 发布物与不可变身份

正式发布前必须生成一份 release manifest，至少记录：

- 源码 tag、完整 commit、clean tree；
- CloudCore、controller-manager、EdgeCore、keadm 的版本和 SHA256；
- 云侧镜像与 installation-package 的 OCI index digest、各架构 manifest
  digest 和 OCI labels；
- ARM64 压缩包及 checksum 文件；
- 构建 Go 版本、`GOWORK=off`、`GOFLAGS=-mod=vendor`。

升级命令和 NodeUpgradeJob 必须固定 registry 返回的完整 OCI digest；镜像 tag
只用于可读性，不能作为唯一身份。

## 2. 节点分流

| 当前 EdgeCore | 升级入口 | 原因 |
| --- | --- | --- |
| 已是正式 `.5` 且 hash 一致 | 不重复升级，只验收 | 避免无意义重启 |
| `.4`、`.4-rc.1`、`.4-rc.2` 或已包含完整 copy labels 的版本 | 官方 NodeUpgradeJob，但必须先通过 watchdog 门禁 | 当前 EdgeCore 能安全完成 Check 的 CRI copy；`.4` 的启动竞态使其不能继续放量 |
| `.3` 及更早 NXIN/上游版本 | 先带外分发正式 `.5` keadm，再执行摘要锁定的本地 bootstrap | 老 EdgeCore 的 Check 可能清理临时 copy sandbox，不能把首次升级完全交给 NodeUpgradeJob |
| 版本、二进制 hash、数据库或管理链路不明 | 隔离并人工处理 | 不允许在未知状态下批量升级 |

带外分发只替换 keadm，不停止 EdgeCore。必须通过 SSH、edge access agent 或其他
独立管理链路传输，并在原子替换前验证 release manifest 中的 SHA256。正式 `.5`
keadm 保留了历史 EdgeCore bootstrap、失败恢复和旧二进制重启逻辑。

## 3. 升级顺序

### 3.1 云侧先行

1. 备份当前 ACK deployment、service、NLB 标识、CRD 和 controller 配置。
2. 先更新匹配 `.5` 的 CRD/controller-manager，再滚动 CloudCore。
3. CloudCore 三副本逐个更新；任何时刻至少两个 Ready，禁止重建或更换受保护的
   NLB、Service 地址和证书 Secret。
4. 每个副本验证 image digest、版本、leader/session owner、Ready、restart count
   和错误日志后再继续。
5. 云侧稳定后才创建边缘升级任务。云侧回滚与边侧回滚分别决策，不能因为单个
   边缘节点失败就无条件整体回滚云侧。

### 3.2 边缘节点预检

每个节点必须同时满足：

- Kubernetes Node 为 Ready，业务 Pod 全部 Ready，restart count 无异常增长；
- EdgeCore、containerd、独立管理 agent、NPC 均在线；
- `/` 的精确占用低于 NodeUpgradeJob 80% 门槛，并为备份和镜像展开预留空间；
- `/var/lib/kubeedge/edgecore.db` 的 `PRAGMA integrity_check` 返回 `ok`；
- 没有活动 upgrade task、遗留 upgrade report 或 resource-copy guard；
- 当前 EdgeCore、配置和数据库已有可读、hash 正确的回滚备份；
- installation-package 只清理“无容器引用且可从仓库恢复”的精确历史版本，禁止
  执行不加选择的 image prune；
- 至少一条不依赖 EdgeCore 的管理通道可用。
- 记录 boot ID、Ready sandbox ID、运行中 container ID、attempt、Pod restart
  count 和 EdgeCore `NRestarts`，升级后必须逐项比较；
- 如果 `/dev/watchdog*` 已启用，查明 timeout、驱动和实际 feeder。feeder 若位于
  EdgeCore 管理的 Pod 中，必须先启动并验证独立宿主机 feeder；无法安全建立时阻断
  升级，不能赌业务 Pod 在重启窗口内一定存活。

本次 ARM64 canary 的 `dw_wdt` 不支持 magic close，业务 Pod 中的 feeder 约每 11 秒
短开设备投喂，硬件 timeout 为 44 秒。测试窗口使用独立 transient systemd guard：

```bash
systemd-run \
  --unit=nxin-edge-upgrade-watchdog-guard \
  --description='NXIN edge upgrade watchdog guard' \
  --property=Restart=always \
  --property=RestartSec=1 \
  --property=KillMode=control-group \
  /bin/bash -c \
  'while :; do printf nxin-edge-upgrade-guard >/dev/watchdog || :; sleep 5; done'
```

必须先验证 guard 为 `active`、其 `NRestarts=0`，且原 feeder 仍持续输出成功日志。
只有新 EdgeCore、全部业务容器和原 feeder 完成稳定观察后才能停止 guard；停止前后
都要确认另一 feeder 的投喂间隔小于硬件 timeout。

### 3.3 历史节点 bootstrap

以下变量必须来自正式 release manifest：

```bash
TARGET_VERSION=v1.23.1-nxin.5
INSTALL_IMAGE=nxin-acr-registry.cn-beijing.cr.aliyuncs.com/kubeedge/installation-package
INSTALL_DIGEST=sha256:<arm64-verified-oci-index-digest>
```

先把正式 keadm 传到独立临时路径，核对 SHA256 和版本，保留当前 keadm，然后用
同目录临时文件和原子 rename 替换 `/usr/local/bin/keadm`。随后执行：

```bash
/usr/local/bin/keadm backup edge
/usr/local/bin/keadm upgrade edge \
  --force \
  --toVersion "${TARGET_VERSION}" \
  --image "${INSTALL_IMAGE}" \
  --image-digest "${INSTALL_DIGEST}"
```

升级窗口只允许 EdgeCore 有界停止；containerd、业务容器和独立管理通道不能随之
停止。命令失败时先检查旧 EdgeCore 是否已经被自动恢复并启动，不要立刻覆盖备份
或重复执行。

### 3.4 官方 NodeUpgradeJob

当前 EdgeCore 已具备安全 copy 能力后，使用单节点、并发度 1、摘要固定且要求确认
的任务：

```yaml
apiVersion: operations.kubeedge.io/v1alpha2
kind: NodeUpgradeJob
metadata:
  name: upgrade-<node>-v1231-nxin5
spec:
  version: v1.23.1-nxin.5
  nodeNames:
    - <node>
  image: nxin-acr-registry.cn-beijing.cr.aliyuncs.com/kubeedge/installation-package
  imageDigestGatter:
    arm64: sha256:<arm64-verified-oci-index-digest>
  concurrency: 1
  checkItems:
    - cpu
    - mem
    - disk
  failureTolerate: "0"
  timeoutSeconds: 900
  requireConfirmation: true
```

任务必须先达到 `Check=True` 和 `WaitingConfirmation=True`。确认前再次核对任务中的
version、image 和 digest，以及 Check 放置的 keadm hash。然后在目标节点执行：

```bash
/usr/local/bin/keadm ctl confirm
```

确认必须通过本地 verified mTLS 完成。禁止 `InsecureSkipVerify`、临时 wildcard
RBAC、复用 Pod token 或关闭 `requireAuthorization`。最终要求 `Check`、
`WaitingConfirmation`、`BackUp`、`Upgrade` 全部为 `True`，节点 phase 为
`Successful`，任务 phase 为 `Completed`。

## 4. 验收与放量

每批升级后验证：

- EdgeCore 和 keadm 的 SHA256、版本、commit、clean tree、Go 版本和 ARM64 平台；
- Node Ready、上报版本正确，所有业务 Pod/容器 Ready，restart count 不增长；
- boot ID、Ready sandbox ID、运行中 container ID 和 attempt 与预检基线一致；
- EdgeCore `NRestarts=0`，containerd、agent、NPC 全程在线；
- 数据库完整，无终态 upgrade task、upgrade report、copy guard 和临时容器残留；
- 备份目录包含升级前 EdgeCore、配置和完整数据库；
- 云侧 CloudCore/controller 副本、leader、session、Service/NLB 和错误日志稳定；
- 启动同步窗口结束后，连续至少 15 分钟统计 EdgeCore 与云侧 E/F/W 为 0。

放量顺序固定为：1 个 canary、约 5%、约 20%、剩余节点。每批之间必须完成完整
观察窗口；任何节点失败时停止下一批，不允许提高 failure tolerance 掩盖失败。

## 5. 回滚

回滚前先验证目标备份目录、EdgeCore SHA256 和数据库完整性：

```bash
/usr/local/bin/keadm rollback edge --historical-version <previous-version>
```

回滚期间仍要保持 containerd 和独立管理通道在线。回滚后重复完整验收，并保留失败
任务、journal 和 release manifest 作为证据。`.4` 边端包含已确认的启动竞态，
不得在没有独立 watchdog guard 的情况下自动回滚到 `.4`。只有确认 `.5` 云侧与
旧边侧不兼容或云侧自身不健康时，才按 ACK 的受保护滚动流程把
CloudCore/controller 回滚到上一套同 tag 制品；不得删除或重建 NLB。
