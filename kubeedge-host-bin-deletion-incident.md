# KubeEdge Join 后宿主机 `/bin` 被删除故障分析

## 1. 结论摘要

本次故障不是 `yunhe-lite` DaemonSet、CloudCore 或安装器直接执行了
`rm /bin`，而是 KubeEdge v1.23.0 在以下条件组合下触发的边端日志回收缺陷：

1. `keadm join` 使用 `installation-package` 临时容器复制 `edgecore` 时，
   创建的 CRI sandbox 未设置 `LogDirectory`，容器未设置 `LogPath`。
2. 临时容器退出后，containerd 未能在超时时间内完成删除，留下一个已退出容器。
3. EdgeCore 启动后立即回收该容器。其日志 GC 未校验空 `LogPath`，将空字符串
   拼接成通配模式 `*`。
4. EdgeCore 的工作目录是 `/`，所以 `Glob("*")` 实际枚举宿主机根目录。
5. GC 按名称顺序先对 `bin` 调用 `os.Remove`。Debian 12 的 `/bin` 是指向
   `usr/bin` 的符号链接，因此删除成功；随后删除非空目录 `boot` 失败并停止。

最终表现为 `/usr/bin/bash`、`/usr/bin/dash` 等真实文件仍存在，但 `/bin/bash`、
`/bin/sh` 和所有使用这些解释器的脚本无法启动。

该缺陷位于边端 `keadm` 和 EdgeCore。CloudCore 不执行宿主机容器日志 GC，不是
直接故障组件。当前官方 v1.23.1 和 `master` 仍保留相同的关键代码，仅升级到
v1.23.1 不能解决该问题。

## 2. 现场环境

| 项目 | 现场值 |
|---|---|
| 故障日期 | 2026-07-16 |
| 宿主机 | Orange Pi，`linux/arm64` |
| 操作系统 | Debian 12 Bookworm / Orange Pi 1.2.2 |
| KubeEdge | v1.23.0 |
| EdgeCore | v1.23.0 |
| keadm | v1.23.0 |
| containerd | v2.2.2 |
| CloudCore | `v1.23.0-ha-20260715-2790fe235`，3 副本 |
| Kubernetes context | `ACK` |
| 节点名 | `lite-281621867119` |
| 安装器 | Yunhe Lite Installer v1.1.4 |

安装完成日志：

```text
/home/data/logs/yunhe-install/202607161019/install-lite-281621867119.log
```

## 3. 用户影响

### 3.1 已确认影响

- `/bin` 符号链接消失。
- `/bin/bash`、`/bin/sh` 不存在。
- 默认以 `/bin/bash` 启动的命令通道报：

  ```text
  No such file or directory (os error 2)
  ```

- 带 `#!/bin/sh` 或 `#!/bin/bash` shebang 的脚本无法启动。
- 依赖 `/bin` 路径的 systemd 服务、APT maintainer script、运维脚本可能失败。

### 3.2 未被删除的内容

- `/usr/bin/bash` 存在且 ELF 文件正常。
- `/usr/bin/dash` 存在。
- `/usr/bin/sh -> dash` 存在。
- `/lib -> usr/lib` 存在。
- `/sbin -> usr/sbin` 存在。

因此这是 Debian usrmerge 根级兼容符号链接被删除，不是 `/usr/bin` 中的系统命令
文件被批量删除。

### 3.3 潜在扩大风险

本次 `Glob("*")` 的结果按名称排序。`/bin` 删除后，下一项 `/boot` 是非空目录，
`os.Remove("boot")` 返回 `directory not empty`，本轮 GC 因错误停止。

如果 `/boot` 为空或后续代码忽略目录非空错误，GC 可能继续处理 `/dev`、`/etc`、
`/home` 等根目录条目，风险级别为严重。不能将本故障仅视为一个缺失软链接的小问题。

## 4. 事件时间线

以下时间均为 CST（UTC+8）。

| 时间 | 事件 |
|---|---|
| 10:19:44 | 第二次真实安装开始 |
| 10:21:53 | `keadm join` 开始，本次已通过 Node 名称校验 |
| 10:22:09.334 | containerd 为资源复制创建临时 Pod sandbox |
| 10:22:09.537 | 创建临时容器 `1f136fd5de47...` |
| 10:22:09.809 | 临时容器启动成功 |
| 10:22:10.002 | 临时容器退出，状态码 137 |
| 10:22:19.965 | `RemoveContainer` 超时失败 |
| 10:22:29.967 | `RemovePodSandbox` 超时失败 |
| 10:22:30.785 | `edgecore.service` 启动 |
| 10:23:19 | 安装器完成服务及 `agent`、`npc` 健康检查 |
| 10:23:31.381956 | EdgeCore GC 再次回收临时容器 |
| 10:23:31.382983 | 宿主机根目录 mtime 更新，`/bin` 在此时被删除 |
| 10:23:31.384925 | EdgeCore 报删除 `boot` 失败 |

关键日志：

```text
E0716 10:23:31.384925 kuberuntime_gc.go:150]
"Failed to remove container"
err="failed to remove container \"1f136fd5de47...\" log \"boot\":
remove boot: directory not empty"
```

`/` 的现场时间戳：

```text
2026-07-16 10:23:31.382983553 +0800 /
```

日志报错与根目录修改时间相差约 2 毫秒，容器 ID 也与 `keadm` 资源复制临时容器
完全一致。

## 5. 完整故障调用链

```text
yunhe-install
  -> keadm join
    -> CopyResources()
      -> 创建没有 LogDirectory 的临时 Pod sandbox
      -> 创建没有 LogPath 的临时 container
      -> 从 installation-package 复制 edgecore
      -> 删除临时 container 超时，残留已退出 container
    -> 启动 edgecore.service
      -> minimumGCAge=0s，容器 GC 回收残留 container
      -> containerLogManager.Clean()
      -> ContainerStatus.LogPath == ""
      -> pattern = "" + "*" == "*"
      -> filepath.Glob("*") 在工作目录 / 下枚举根目录
      -> os.Remove("bin") 成功删除 /bin 符号链接
      -> os.Remove("boot") 失败：directory not empty
```

## 6. 代码级根因

### 6.1 `keadm` 创建了缺少日志字段的 CRI 对象

KubeEdge v1.23.0：

```text
pkg/containers/container_runtime.go
```

`CopyResources` 构造 `PodSandboxConfig` 时没有设置 `LogDirectory`，构造
`ContainerConfig` 时没有设置 `LogPath`：

```go
psc := &runtimeapi.PodSandboxConfig{
    Metadata: ...,
    Linux: ...,
}

containerConfig := &runtimeapi.ContainerConfig{
    Metadata: ...,
    Image: ...,
    Command: []string{"/bin/sh", "-c", "sleep infinity"},
    Mounts: mounts,
    Linux: ...,
}
```

现场 `crictl inspect` 结果：

```json
{
  "status": {
    "id": "1f136fd5de47...",
    "logPath": "",
    "exitCode": 137
  }
}
```

对应 sandbox 的 `info.config` 也没有 `log_directory`。containerd 在创建容器时已记录：

```text
Logging will be disabled due to empty log paths for sandbox ("") or container ("")
```

上游代码：

- <https://github.com/kubeedge/kubeedge/blob/v1.23.0/pkg/containers/container_runtime.go#L70-L157>

### 6.2 临时容器删除失败，EdgeCore 接管残留对象

`CopyResources` 使用 defer 调用：

```go
runtime.ctrsvc.RemoveContainer(ctx, containerID)
runtime.ctrsvc.RemovePodSandbox(ctx, sandbox)
```

现场 containerd 日志显示两次清理都超时，容器和 sandbox 没有立即移除。临时容器
因此在 EdgeCore 启动后仍能被其 GC 枚举。

这不是触发任意根目录删除的唯一根因，但它是本次空日志路径进入 EdgeCore GC 的必要
条件。清理逻辑应使用独立、足够长的 cleanup context，并显式执行 Stop、Remove、
RemovePodSandbox。

### 6.3 EdgeCore 日志 GC 未校验空路径

KubeEdge v1.23.0 vendored Kubernetes 代码：

```text
vendor/k8s.io/kubernetes/pkg/kubelet/logs/container_log_manager.go
```

现有实现：

```go
pattern := fmt.Sprintf("%s*", resp.GetStatus().GetLogPath())
logs, err := c.osInterface.Glob(pattern)

for _, l := range logs {
    if err := c.osInterface.Remove(l); err != nil && !os.IsNotExist(err) {
        return fmt.Errorf("failed to remove container %q log %q: %v", containerID, l, err)
    }
}
```

当 `GetLogPath()` 返回空字符串时，`pattern` 为 `*`。实现没有验证：

- 日志路径是否为空；
- 日志路径是否为绝对路径；
- 日志路径是否位于受控日志根目录内。

上游代码：

- <https://github.com/kubeedge/kubeedge/blob/v1.23.0/vendor/k8s.io/kubernetes/pkg/kubelet/logs/container_log_manager.go#L202-L225>

### 6.4 EdgeCore 配置放大了触发概率

现场配置：

```yaml
modules:
  edged:
    minimumGCAge: 0s
    rootDirectory: /var/lib/kubelet
    tailoredKubeletConfig:
      podLogsDir: /var/log/pods
```

`minimumGCAge: 0s` 使 EdgeCore 启动后无需等待即可回收刚退出的临时容器。它不是
删除根目录的根因，但缩短了从 join 到故障的时间窗口。

### 6.5 安装器验收存在时间窗口

安装器于 10:23:19 完成验收，EdgeCore GC 于 10:23:31 删除 `/bin`。当前验收只确认：

- containerd active；
- edgecore active 且稳定；
- `agent`、`npc` 容器 Running。

它没有在 EdgeCore GC 周期后复查 `/bin`、`/sbin`、`/lib` 等宿主机基础路径，因此
会先报告成功，约 12 秒后宿主机才损坏。

## 7. 已排除项

### 7.1 `yunhe-lite/yunhe-lite` DaemonSet

通过 `kubectl --context ACK` 检查当前 generation 16 的完整 Pod template：

- 没有 `hostPath: /`；
- 仅挂载 `/home/data`、`/home/nxin`、`/etc/NetworkManager`、`/dev`、
  `/var/log` 等明确路径；
- 不具备通过容器内 `/bin` 直接修改宿主机 `/bin` 的挂载条件。

因此 `yunhe-lite` 不是本次 `/bin` 删除者。

### 7.2 其他带根目录挂载的 DaemonSet

集群中以下 DaemonSet 挂载了宿主机 `/`：

- `edge-monitoring/edge-exporter`
- `edgeaccess-gateway/edgeaccess-agent`
- `led/orangepi-agent`

现场清单中对应根目录挂载均为只读，且没有与 10:23:31 精确对应的删除日志。直接
证据仍指向 EdgeCore GC。

### 7.3 Yunhe 安装器自身

安装器直接修改的系统范围包括：

- `/etc/fstab`
- `/etc/hostname`
- `/etc/containerd/config.toml`
- `/etc/crictl.yaml`
- `/etc/cni/net.d/*`
- `/etc/systemd/system/containerd.service`
- `/usr/local/bin/*`
- `/usr/local/sbin/runc`
- `/opt/cni/bin/*`

安装器没有删除 `/bin` 的代码路径。第一次安装在 Node 已存在处终止，EdgeCore 未启动，
当时 `/bin` 未受影响；第二次成功启动 EdgeCore 后才稳定复现。

### 7.4 Docker 命令不可用

`docker` 命令缺失是另一个独立的预期行为：安装器按设计 purge 了 Docker CE、
Docker CLI 和 `containerd.io`，再安装自带 containerd。该行为不导致 `/bin` 被删除，
但需要在运维交接中明确说明。

## 8. 当前上游状态

检查日期为 2026-07-16：

- GitHub 最新 release 为 v1.23.1，发布于 2026-07-15；
- v1.23.1 对应分支仍未给 `CopyResources` 设置日志路径；
- 当前 `master` 仍有相同实现；
- vendored `containerLogManager.Clean` 仍未校验空日志路径。

因此不能将“升级到官方 v1.23.1”作为本故障的修复方案。应先提交上游修复或维护
内部 patched build，并在安装包中固定经过验证的二进制和校验和。

## 9. 建议修复方案

建议同时修复生产者和消费者，不能只依赖单点补丁。

### 9.1 P0：EdgeCore GC 防御空路径和越界路径

最低限度：

```go
logPath := strings.TrimSpace(resp.GetStatus().GetLogPath())
if logPath == "" {
    klog.InfoS("Skip container log cleanup because runtime returned empty log path",
        "containerID", containerID)
    return nil
}

if !filepath.IsAbs(logPath) {
    return fmt.Errorf("refusing to clean non-absolute container log path %q", logPath)
}

pattern := logPath + "*"
```

更稳妥的实现还应：

- `filepath.Clean(logPath)`；
- 验证路径位于配置的 `podLogsDir` 或允许的容器日志根目录；
- 拒绝 `/`、`.`、空路径和相对路径；
- 在删除前对每个 glob 结果再次做目录边界校验；
- 对异常 CRI 返回只记录告警，不进行任何文件删除。

该补丁需要编译进 `edgecore`，CloudCore 无需为本缺陷修改。

### 9.2 P0：`keadm CopyResources` 设置合法日志字段

建议为一次资源复制创建唯一日志目录：

```go
setupID := uuid.New().String()
logDirectory := filepath.Join("/var/log/pods/kubeedge-setup", setupID)

psc := &runtimeapi.PodSandboxConfig{
    Metadata: ...,
    LogDirectory: logDirectory,
    Linux: ...,
}

containerConfig := &runtimeapi.ContainerConfig{
    Metadata: ...,
    LogPath: "copy-resource.log",
    Image: ...,
    Command: ...,
    Mounts: ...,
    Linux: ...,
}
```

还应确保日志目录生命周期明确，并符合 CRI 对 `LogDirectory`、`LogPath` 的约束。
该补丁需要编译进安装器调用的 `keadm`。

### 9.3 P0：修复临时容器清理逻辑

不要在 defer 中继续复用可能已经超时或取消的 join context。建议：

1. 创建独立 cleanup context；
2. 设置合理的清理超时；
3. 先 StopContainer；
4. 再 RemoveContainer；
5. 最后 RemovePodSandbox；
6. 清理失败时让 `keadm join` 返回明确错误，而不是继续启动 EdgeCore。

示意：

```go
cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

_ = runtime.ctrsvc.StopContainer(cleanupCtx, containerID, 5)
if err := runtime.ctrsvc.RemoveContainer(cleanupCtx, containerID); err != nil {
    return fmt.Errorf("remove setup container: %w", err)
}
if err := runtime.ctrsvc.RemovePodSandbox(cleanupCtx, sandbox); err != nil {
    return fmt.Errorf("remove setup sandbox: %w", err)
}
```

### 9.4 P1：安装器增加宿主机完整性保护

在真实安装开始前记录并校验：

```text
/bin
/bin/sh
/bin/bash
/sbin
/lib
/lib64（存在时）
```

在 `keadm join` 返回后、EdgeCore 稳定窗口后、最终容器验收后分别复查。对于 Debian 12
usrmerge 主机至少验证：

```text
/bin  -> usr/bin
/sbin -> usr/sbin
/lib  -> usr/lib
```

发现基础路径消失时：

1. 立即停止 `edgecore.service`；
2. 将安装标记为失败；
3. 输出精确诊断信息和恢复建议；
4. 不要自动猜测并重建任意系统链接，除非安装前已记录了原始类型和目标。

### 9.5 P1：延长安装验收观察窗口

当前故障在安装器报告成功 12 秒后发生。验收必须至少跨过一次 EdgeCore 容器 GC 周期，
并在观察窗口内持续检查基础系统路径，而不是只检查服务 `active`。

## 10. 临时处置建议

在 patched build 发布前，不应在生产宿主机上继续运行当前 v1.23.0/v1.23.1 的完整
`keadm join` 流程。

现场恢复顺序建议如下，由实施人员确认后执行：

```bash
# 这些命令必须显式使用 /usr/bin 中的可执行文件，因为 /bin 当前不存在。
/usr/bin/systemctl stop edgecore.service

# Debian 12 usrmerge 主机恢复原始兼容链接。
/usr/bin/ln -s usr/bin /bin

# 验证基础命令路径。
/usr/bin/readlink /bin
/bin/bash --version
/bin/sh -c 'echo shell-ok'
```

然后处理残留的资源复制容器和 sandbox。现场 ID 为：

```text
container: 1f136fd5de479f94942a83d902f7fac29b2697a8e6c7ec5d99d32a104862a3ce
sandbox:   aab0db9dec6bdfc1aa162b6419504d1345cc4b67442077118939b255786931ad
```

清理前应保留 `crictl inspect`、containerd journal 和 EdgeCore journal 作为故障证据。
不要在 EdgeCore 仍运行且 `/bin` 未恢复时反复执行 join。

## 11. 测试要求

### 11.1 EdgeCore 单元测试

使用 fake RuntimeService 返回以下 `ContainerStatus.LogPath`：

| 输入 | 预期 |
|---|---|
| `""` | 返回成功或可识别告警，不调用 Glob/Remove |
| `"."` | 拒绝，不删除文件 |
| `"boot"` | 拒绝相对路径，不删除文件 |
| `"/"` | 拒绝，不删除根目录内容 |
| `/var/log/pods/.../0.log` | 仅清理该日志及轮转文件 |

fake OS 接口必须断言异常路径下 `Remove` 调用次数为 0。

### 11.2 `keadm` 单元测试

捕获发送给 fake CRI 的请求并断言：

- `PodSandboxConfig.LogDirectory` 非空且为绝对路径；
- `ContainerConfig.LogPath` 非空且为安全相对文件名；
- 清理使用独立 context；
- RemoveContainer 失败时不会无条件继续启动 EdgeCore。

### 11.3 containerd 集成测试

1. 使用真实 containerd CRI 创建资源复制容器；
2. 注入首次 RemoveContainer 超时；
3. 启动 patched EdgeCore；
4. 等待至少两个 GC 周期；
5. 验证测试根目录中所有条目未变化；
6. 验证残留容器最终被安全清理。

集成测试必须使用隔离 mount namespace 或临时根目录，禁止对测试机真实 `/` 做故障注入。

### 11.4 安装器端到端测试

在 Debian 12 arm64 usrmerge 镜像或专用测试机上执行完整 join：

```bash
before_bin=$(/usr/bin/readlink /bin)
./yunhe-install-linux-arm64 --plain
after_bin=$(/usr/bin/readlink /bin)

test "$before_bin" = "$after_bin"
test -x /bin/bash
/bin/sh -c 'true'
```

同时验证：

- EdgeCore 日志中没有 `log "boot"`；
- 没有针对空日志路径的 glob；
- 资源复制临时容器的 `status.logPath` 非空；
- containerd 中没有残留的 `kubeedge/setup/podcopyresource` sandbox；
- CloudCore 与 EdgeCore 仍能正常建立连接；
- `agent` 和 `npc` 正常 Running。

## 12. 验收标准

修复只有同时满足以下条件才可发布：

- 空、相对、根目录日志路径均不能触发文件删除；
- `keadm` 创建的所有临时 CRI 对象日志字段有效；
- 临时容器清理失败会阻止安装成功，而不是留下对象后继续；
- Debian 12 `/bin`、`/sbin`、`/lib` 在完整安装前后类型及目标完全一致；
- EdgeCore 连续运行两个以上 GC 周期无宿主机基础路径变化；
- CloudCore v1.23.0 与 patched EdgeCore v1.23 构建连接正常；
- 安装器不会在后续 GC 仍可能破坏宿主机时提前报告成功；
- 发布包记录 patched commit、二进制 SHA256/SHA512 和回归测试结果。

## 13. 组件责任边界与升级建议

| 组件 | 是否直接涉及 | 建议 |
|---|---|---|
| CloudCore | 否 | 保持现有 v1.23.0，除非整体版本兼容策略另有要求 |
| EdgeCore | 是，执行危险 GC | 必须加入空路径及目录边界防护并重新构建 |
| keadm | 是，产生空日志路径临时容器 | 必须设置合法日志字段并改进清理流程 |
| containerd | 放大条件之一，清理超时 | 保留现场日志；验证 v2.2.2 清理行为，但不能依赖 runtime 规避上层空路径缺陷 |
| yunhe-install | 未直接删除 `/bin`，但验收未覆盖 | 增加系统路径基线与延迟验收 |
| yunhe-lite DaemonSet | 已排除 | 无需为本故障修改 |

推荐保持 CloudCore v1.23.0，交付同一 minor line 的 patched `keadm` 和 patched
`edgecore`。在上游发布明确包含本修复并通过现场回归之前，不要仅凭版本号升级。

## 14. 研发交付物

研发修复应至少包含：

1. EdgeCore GC 防御补丁及单元测试；
2. `keadm CopyResources` 日志配置和可靠清理补丁及单元测试；
3. 安装器基础路径保护和延迟验收；
4. containerd v2.2.2 故障注入集成测试记录；
5. Debian 12 arm64 完整安装回归报告；
6. patched 源码 commit 和所有交付二进制校验和；
7. 上游 KubeEdge issue/PR 链接，便于后续切回官方版本。

