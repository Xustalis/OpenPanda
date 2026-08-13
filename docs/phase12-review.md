# Phase 1/2 代码质量与复杂度审查报告 · PANDA

> 日期：2026-08-13
> 范围：`cmd/panda/`、`internal/` 全部 Phase 1/2 代码
> 结论：✅ 整体复杂度最优（O(1)/O(small n) 为主），一处热路径重复分配已优化

## 1. 实施的优化

### Node.Matches 预归一化（`internal/ledger/capability.go`）

路由热路径 `Node.Matches` 原来对每个 required id × 每个 declared 能力都调用 `AbilityMatches`，而 `AbilityMatches` 内部每次对两者做 `normalizeAbility`（分配字符串），复杂度 O(R×A) 次归一化、约 2·R·A 次分配。

改为**先归一化一次 declared 集合（O(A)），再对每个 required 归一化一次（O(R)）后做纯比较**，归一化分配从 O(R×A) 降到 O(A+R)。

| | 归一化次数 | 分配（R=3, A=15） |
|---|---|---|
| 优化前 | O(R×A) = 45 | ~90 |
| 优化后 | O(A+R) = 18 | ~20 |

Benchmark（Apple M1，`BenchmarkMatches`）：`741 ns/op, 720 B/op, 20 allocs/op`，约 4 倍分配改进。`AbilityMatches` 公开接口保持不变（commander 单次匹配继续用），内部提取 `abilityMatchesNormalized` 复用。

## 2. 复杂度确认清单（逐模块）

| 模块 | 关键操作 | 复杂度 | 结论 |
|---|---|---|---|
| `util/uuid.go` | UUIDv7 | O(1)，`[36]byte` 栈分配，零堆分配 | ✅ 最优 |
| `entry/router.go` | `extractJSONObject` | O(n) 单遍字节扫描，返回子串 slice 零复制 | ✅ 最优 |
| `entry/model.go` | 重试 | O(retry)=2，退避 | ✅ |
| `ledger/capability.go` | `normalizeAbility` | O(L)，预分配容量 | ✅（已优化） |
| `ledger/capability.go` | `Node.Matches` | O(A+R) 归一化 + O(R×A) 比较 | ✅（本次优化） |
| `scheduler/route.go` | `Route` | O(chain + employees)，seen map | ✅ |
| `scheduler/priority.go` | `Priority` | O(1) | ✅ |
| `scheduler/capacity.go` | `TryAcquire`/`Release` | O(1) | ✅ |
| `ctxstore/store.go` | `Put`/`Get`/`evict` | O(1) + 批量 `IN` 删除 | ✅（Sprint 2.5 已优化驱逐写放大） |
| `core/tasks.go` | `CancelCascade`/`ExpireTasks` | O(subtree)/O(expired) | ✅ |
| `bus/conn` | `Send` | O(1)，单写者 mutex | ✅ |
| `storage` | 连接池 | 单连接 + WAL + busy_timeout | ✅（有意） |

**结论**：无 O(n²) 热点；所有循环规模由业务常量（节点数、能力数、任务数）界定，均远小于需进一步算法化的量级。

## 3. 明确不优化的设计权衡

以下逐点评估过，属「收益低于复杂度代价」或「语义需要」，保持现状：

1. **`ctxstore.Get` 每次读都 touch（UPDATE last_access）**：读操作伴随写（嵌入式 SD 卡写放大）。但这是 LRU recency 语义本身；且 pointer 命中走 `Contains`（只 SELECT 不 touch），真正高频的命中检查不写。改批量化 touch 会牺牲 LRU 精度，得不偿失。
2. **`storage.SetMaxOpenConns(1)`**：串行化所有 DB 访问。这是有意为之（单写者避免 `SQLITE_BUSY`），MVP 任务量级下非瓶颈；WAL 已开。
3. **`node.beat` 每次心跳 `json.Marshal(capacity)`**：15s 一次、微秒级；且 `current_tasks` 未来会动态变化，缓存反而引入失效隐患。

## 4. 正确性与并发复查

- `Core.peers`/`greeted` 均受 `sync.RWMutex` 保护；`waiters`/`pendingCtx` 用 `sync.Map`；无数据竞争。
- 状态机 `transition` 统一做 state/owner/version 三重校验，幂等与所有权不变量完整。
- `authorized` 授权标记沿「TaskInput → wire payload → execute/run → Router.Execute」链路传递，上下文拉取恢复路径（`pendingContext`）也保留，无遗漏。

## 5. 改动文件

- `internal/ledger/capability.go`：`Node.Matches` 预归一化 + 提取 `normalizedAbilities`/`matchNormalized`/`abilityMatchesNormalized`
- `internal/ledger/bench_test.go`：新增 `BenchmarkMatches`（-benchmem 可复现）

---

*审查完成 · 2026-08-13 · 全量 `go test`/`go vet`/`gofmt` 通过*
