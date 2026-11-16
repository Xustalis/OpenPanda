# Windows 任务沙箱：兼容边界

[English](windows-sandbox.en.md) · [安全模型](../SECURITY.md) · [配置示例](../config.example.local.yaml)

Windows 后端使用 Windows 10+ 的原生 AppContainer 与进程创建 API，并为每次执行
建立新的 `WRITE_RESTRICTED` token、AppContainer package SID 和写限制 SID。
`standard`、`strict` 在后端不可用或无法安全建立策略时拒绝启动，不自动降为 `off`。

## 模式与临时权限

| 模式 | Windows 行为 |
| --- | --- |
| `off` | 过滤环境变量并设置工作目录，不提供 OS 隔离。 |
| `standard` | 把写入限制在任务目录与明确放行的可写路径，并应用拒写路径。目录外的公共 `ALL_APPLICATION_PACKAGES` / `ALL_RESTRICTED_APPLICATION_PACKAGES` 写 ACL 不会绕过写限制。 |
| `strict` | 在 `standard` 上，为拒读路径移除本次 package 的读授权，并在拒读根保护 DACL 继承；不能安全建立该读边界时拒绝策略。 |

新加的 ACL 授权仅面向本次执行的 SID：任务目录、明确放行的可写目录/文件，以及
实际可执行文件和已存在的绝对路径参数所需的读目录。文件参数使用所在目录，目录
参数使用该目录；这些目录需要时获得可继承的读/执行授权。任意 `PATH` 项和整个
home 不会仅因出现在环境变量中就自动开放。已有的公共 AppContainer 读权限仍受
Windows 自身的访问检查约束；`standard` 不是一份穷尽所有可读文件的白名单。

每次运行在其 AppContainer profile 内创建
独立 Temp，将子进程 `TEMP`/`TMP`/`TMPDIR` 指向它，并仅为该私有 Temp 补充本次
写限制 SID 的写授权；随 profile 清理删除，不开放宿主整个 TEMP。调用方覆盖了
`LOCALAPPDATA` 时，还会准备该 nonce 的投影 Temp；它按文件身份记入 lease，
退出或恢复时通过限定目录的删除操作清理。

启动账户必须能够建立所需的临时 ACL。管理员拥有的工具链目录如果既没有可用的
公共读权限，也无法为本次 SID 授予必要读权限，执行会被拒绝。依赖其他未授权目录、
用户配置或运行时数据的工具也可能无法运行；环境变量中有路径不等于获得文件权限。

## 运行时兼容失败

本后端不保证任意 Windows 程序都能运行。真实 Python 3.14 适配器测试中，
`off` 能完成 UTF-8 stdin/stdout 协议及临时文件写入；`standard`、`strict` 则在
执行适配器代码之前失败。加载器跟踪显示：系统 `bcrypt.dll` 初始化时请求对
`\Device\KsecDD` 的读写访问，被新写限制 SID 拒绝，进程退出码为
`0xc0000142`（`STATUS_DLL_INIT_FAILED`）。其他程序出现同一退出码时，不据此
断言其失败也来自该设备。

后端保留原退出码并输出 `sandbox backend unavailable` 兼容诊断；隔离策略继续
生效，不会自动改为 `off` 或重试为无隔离执行。允许宿主账户访问某个文件、或已
安装 Python，均不等于该运行时兼容这个沙箱。把 `Everyone` 等宽泛 SID 加进写
限制列表会削弱目录外写入边界，因此不作为兼容修复。

## 拒绝建立的策略

- `strict` 拒读根或其后代已有公共 AppContainer/package 或 capability 读授权时，
  拒绝该策略，不撤销其他应用的共享权限。这包括
  `ALL_APPLICATION_PACKAGES`、`ALL_RESTRICTED_APPLICATION_PACKAGES` 以及 capability SID。
- 拒读树内出现无法安全分析的 ACL（例如 conditional/object ACE 或无约束 DACL），
  或嵌套 reparse point 时，拒绝该策略。
- 可写根内已有多硬链接文件时，拒绝授权，避免给共享同一文件对象的根外路径增加
  写权限。这一检查也适用于明确放行的可写文件。

这些是启动前的策略拒绝，不会通过关闭隔离、放开整个 home 或修改其他应用的共享
ACL 来继续执行。

## 网络与目录操作

`allow_network: true` 提供相应的 AppContainer 网络 capability，仍受 AppContainer
的 loopback 限制；本机模型代理或其他 loopback 服务可能不可达。后端不会修改
machine-wide loopback exemptions。`allow_network: false` 不授予这些网络 capability。

运行期间会持有策略对象的 pin handle，以便 ACL 操作针对同一文件对象。相关根和
受保护路径的重命名、删除或原子替换可能受到 Windows 共享句柄规则限制。需要这类
操作的工具应把它视为兼容约束；AppContainer 不能替代共享工作树的写入锁或 `--isolate`。

## 退出与回收

ACL 回收安排在进程树退出之后：移除本次执行 SID 的临时 ACE、按需恢复 DACL
继承状态，并删除本次 AppContainer profile；不会用旧 ACL 快照覆盖其他执行或
管理员并发新增的权限。无法确认进程树已退出时保留回收记录，不提前撤掉执行边界。

异常退出可能留下临时授权、profile 或继承保护状态。下一次沙箱启动会依据持久化
lease 尝试回收；回收核对文件对象身份，路径已被替换时不会对新对象盲目恢复 ACL。
回收失败会保留记录并报告失败，不把它当成已清理完成。

实现入口：[`sandbox_windows.go`](../internal/security/sandbox_windows.go)、
[`sandbox_windows_acl.go`](../internal/security/sandbox_windows_acl.go)、
[`sandbox_windows_read.go`](../internal/security/sandbox_windows_read.go)、
[`sandbox_windows_journal.go`](../internal/security/sandbox_windows_journal.go)。
