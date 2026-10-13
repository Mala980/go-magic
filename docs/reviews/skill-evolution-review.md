# 「技能进化」子系统审查报告

- 范围：GEPA 自进化 + 技能进化/触发优化/有效性统计 + 模式自动建技能
- 方法：静态调用链梳理（代码 + 调用方反查）＋ 运行期数据取证（`/root/.magic/cortex/**`、`/root/.magic/skills/**`）
- 结论：**进化链路整体处于"空转"状态**——唯一还在写盘的分支（GEPA→SOUL.md）做的是重复内容的再注入，且已经逼近 64 KiB 上限，下一次写入会**不可逆地截掉人格头部**；技能侧的进化/回滚/统计能力（`skills/evolution.go`、`trigger_optimizer.go`、`effectiveness.go`）**完全没有接线**，对外 API 恒返回空。

---

## 1. 现有链路地图

存在**两套互不相通的"进化"系统**：

```
A. 认知层自进化（唯一活着的）
   agent/cortex_integration.go: recordTrajectory() ──┐
                                                    ▼
                       cortex/trajectory.go  TrajectoryStore（内存，上限 1000，磁盘仅 6 条）
                                                    │ GetTrajectories(100)
                                                    ▼
   cortex/gepa.go      evolve(): evaluator→generator→fitness→best→improvement
                                           │                    │
                                           │                    └─ gepa/generations/gen_N.json（133 个）
                                           ▼
   cortex/integration.go:377 startGEPAStrategyApplier() → 每 10min applyGEPABestStrategy()
                                           │
                                           ▼
                       cortex/soul.go SetSoul()  →  /root/.magic/cortex/SOUL.md（65,385 B / 64 KiB）

B. 技能层进化（全部死代码）
   skills/evolution.go SkillEvolutionManager.RunAutoEvolution()   ← 零调用方
   skills/trigger_optimizer.go TriggerOptimizer                  ← 从未被构造
   skills/effectiveness.go EffectivenessManager.RecordInvocation ← 零调用方
   skills/version.go VersionManager.Create()                     ← 零调用方
        └─ 仅被 server/skills.go（/versions、/evolution、/statistics）和 web 前端空调用

C. 模式自动建技能（活着但产出极低）
   skills/auto_enhanced.go EnhancedAutoCreator（minPatternFreq=5）→ auto_skills/pending/
   pending/auto-Pattern-3、auto-Pattern-4（均生成于 2026-09-26，此后无新增）
```

**关键取证（运行期数据，均为实测）**

| 观测点 | 实测值 |
|---|---|
| 世代文件 | 133 个，1.93 MB，`improvement` 40% 为负、49/82 个 `<0.01` |
| `gen_127` … `gen_133` | best_strategy 文本**逐字节完全相同**，`improvement = 0.0000`，`avg_fitness` 0.93~0.97 且不重算 |
| SOUL.md | 65,385 B，上限 65,536 B，**余量 151 B** |
| GEPA 注入占比 | 222 段注入文本（56,069 B）= SOUL.md 的 **85.9%**；97/133 个世代的最优策略文本逐字出现在 SOUL.md |
| `SOUL.md` 章节数 | 196 个 `## `，平均 331 B/节，含多处语义重复（`## Core Values`×多、`quality_standards` 2089 B） |
| 进化记录 | `/root/.magic/skills/skills/effectiveness.json` = `[]`、`stats.json` = `[]`（从未写入过） |
| 轨迹样本 | 磁盘仅 6 条，内容为 "hello / 你好 / 你能干什么"，全部 `steps: 0` |

---

## 2. P0 级问题

### P0-1　下一次进化写入会截掉人格头部（不可逆）

`internal/cortex/soul.go:85-110`：

```go
const maxSoulSize = 64 * 1024
if len(content) > maxSoulSize {
    for len(content) > maxSoulSize {
        lpMarker := "## Learned Preferences"
        idx := strings.Index(content, lpMarker)
        if idx == -1 {                       // ← 当前必然走这里
            cut := content[len(content)-maxSoulSize:]  // 从**头部**裁
            ...; content = cut; break
        }
        ...                                   // 从该标记处删除整节
    }
}
```

而 `"## Learned Preferences"` 标记**只由** `SoulManager.UpdateFromFeedback()`（`soul.go:139`）写入；GEPA 走的是 `integration.go:575 applyGEPABestStrategy()` → `ApplyBestStrategy()`（`gepa.go:339`，`PromptOptimizer.applyAdd` 追加 `## <Section>` 形式），**永远不会**产生该标记。叠加 `integration.go:894` 在再摄取时主动跳过 `## Learned Preferences` 行，说明"该标记存在"这一前提在当前系统里已经失效。

**复现（按实际文件模拟 `SetSoul` 裁剪逻辑）**

```
bytes 65385  cap 65536  headroom 151
append  200B -> len 65536  trim_steps=1  header_intact=False  first60=b'our core traits:\n- Helpful...'
append  350B -> len 65536  trim_steps=1  header_intact=False  first60=b'es and limitations\n\nYour capab...'
append  900B -> len 65536  trim_steps=1  header_intact=False  first60=b'ise, choose the path that maxi...'
append 2100B -> len 65536  trim_steps=1  header_intact=False  first60=b'4) Does it include concrete next steps...'
```

即：**任何一次 ≥151 B 的 GEPA 应用都会在第一轮就砍掉文件开头**，`You are Magic…Your capabilities/Guidelines` 整段人格头被删除；剩余内容仍是非法截断（无标题起始）。且系统内**只有一份** SOUL 文件（`find` 全盘仅 `/root/.magic/cortex/SOUL.md`），没有快照/备份，**不可回滚**。

叠加 P0-2：`Applied=true` 只对内存中的最优策略生效，进程重启后 `loadGenerations()`（`gepa.go:534`）重新加载 `gen_133`，`bestStrategy.Applied=false` → 应用循环会**再写一次**同样内容 → 触发上述截断。这是当前系统里"下一次后台写入"最可能的路径。

**修复方向**
1. 重写裁剪策略：只允许淘汰自动生成/进化注入的章节（例如统一包一层 `## Learned Preferences` 或 `## GEPA/<gen>` 前缀），**绝不触碰头部**；无匹配标记时放弃写入并告警，而不是从头部裁。
2. `SetSoul` 前置快照：按世代保留最近 N 份 `SOUL.md.gen<N>.bak`（有界环形），提供 `rollbackSoul(gen)`。
3. GEPA 侧补上标记写入，使既有淘汰逻辑成立（当前分支是死代码）。
4. 裁剪行为加 warning + 计数指标。

### P0-2　进化已退化为"重复度 100% 的空转循环"

`gepa.go:249-300`：
- `improvement = best.Fitness − prevBestFitness`，而 `evaluateStrategyFitness`（`gepa_strategy.go`）只依赖**同一批轨迹快照**的 `avgScore` + 变更文本的静态特征 + 目标类型系数；
- 轨迹快照不变 → 适应度排序不变 → `selectBestStrategy` 每轮给出**逐字节相同**的策略（`gen_127`…`gen_133` 实测确认），因此 `improvement` 恒为 `0.0000`。

后果链：
1. `improvement < 0.01 && len(generations) > 50` → `isConverged = true`（`gepa.go:317`）。此后 `evolutionLoop` 只 `timer.Reset` 空转，**不再消耗 LLM**（这是"不烧钱"来自收敛而非设计）。
2. 但**应用循环仍在跑**（`integration.go:561`，10 min ticker），它唯一还生效的动作就是把同一段文本往 SOUL.md 里再插一次 —— 因为 `applyAdd` 有 `strings.Contains` 去重，内容不变时是 no-op；一旦触发写入（重启后 `Applied` 复位，或新增微小变更导致整体不一致），就会命中 P0-1 的头部截断。
3. 197 个章节 / 85.9% 注入占比是"降价不是进化"：同义章节大量重复（`## Core Values`、`mission`/`core_drive`/`core_identity`…），系统提示被噪声稀释，而**没有任何章节被淘汰或合并**。

**修复方向**
1. 种群去重：生成后与 SOUL.md / 历史世代做相似度去重，全部重复时不产出世代（不发 LLM 二次调用）。
2. 应用循环加"新数据门槛"：记录已消费的高水位（轨迹条数/最新轨迹时间），无新轨迹则跳过 `evolve()` 与应用。
3. 只有"策略确有差异"才计入 improvement；`isConverged` 需有解锁条件。
4. 补淘汰机制：注入新章节时按"信息增益/引用频次"合并或删除旧章节，保持 SOUL 有界且无冗余。

### P0-3　`isConverged` 是永久闩锁，且无观测

- `isConverged = true` 后，`evolutionLoop` 永远跳过进化；
- 唯一重置点 `GEPAEngine.Reset()`（`gepa.go:579`）**零调用方**（`grep` 全库确认）；
- 收敛状态只在 `Stats()` 里以 `is_converged` 暴露（`gepa.go:499`），没有任何路由/前端消费 → 运维看不到"进化已停"。

**修复方向**：以"新数据 + 适应度回升"为条件自动解锁；`is_converged`、`consecutive_failures`、`last_improvement_at` 接入 dashboard/日志。

---

## 3. P1 级问题

### P1-1　技能侧进化/回滚/统计是纯死代码（对外 API 恒空）

| 能力 | 实现位置 | 调用方 | 结果 |
|---|---|---|---|
| 自动进化 | `skills/evolution.go:527 RunAutoEvolution` | **0** | 从不运行 |
| 触发词优化 | `trigger_optimizer.go` | 构造函数**0 次** | 文件恒不存在 |
| 有效性统计 | `effectiveness.go:103 RecordInvocation` | **0** | `effectiveness.json`、`stats.json` 恒为 `[]` |
| 版本历史 | `version.go VersionManager.Create` | **0** | `GetVersions()` 恒空 |
| 进化历史 | `manager.go:2143 GetEvolutionRecords` | 仅 API | 每次**新建** manager 且 `effectiveness=nil, provider=nil`（`manager.go:2149`）→ 恒空 |

连带影响：`GET /api/skills/statistics`、`/api/skills/{name}/versions`、`/api/skills/{name}/evolution`（`server/skills.go:79,91`）永远返回 `[]`；前端 `web/src/api/skills.ts:89-99` 已经在调这三个接口 → 技能详情页/统计面板实际是空壳。

**修复方向**（择一，建议先裁剪再补全）：
- 要么把 `EffectivenessManager`、`TriggerOptimizer`、`SkillEvolutionManager` 真正接到 agent 工具执行路径（技能调用即 `RecordInvocation`，回合结束即 `RecordFeedback`），让统计与进化有数据源；
- 要么承认这条线已废弃，删掉死代码与前端入口，避免"看起来有、实际没有"的能力误导。

### P1-2　技能变更不可回滚

`Manager.Update()` 不写版本快照，`VersionManager.Create` 无调用方，而 `RevertEvolution`→`revertSkillContent`→`manager.Update(name, oldContent)` 依赖"旧内容已存"—— 但没有任何代码保存过 `OldContent`。即：**技能内容一旦被改，无法回到上一版**。

### P1-3　模式自动建技能产出质量低、已停更

- `auto_enhanced.go` `minFrequency=5`，但历史模式被 `prunePatterns` 裁剪后只剩 2 条（`['todo','todo','terminal']`、`['todo','todo','read_file']`，frequency=1），阈值与数据自相矛盾；
- 生成的 pending 技能（2026-09-26 之后无新增）内容为"按顺序调这三个工具"，实质是空模板；
- 好消息：pending/rejected 技能已被 `GetSkillsList()`（`manager.go:1024`）过滤，**不会污染模型可见技能索引**——这点是对的。

---

## 4. P2 级问题

1. **LLM 策略生成失败静默**：实测 `12:21 成功 → 12:26 失败`、`01:45 成功 → 01:51 失败`（失败世代无文件）。代码路径 `gepa.go:235` 只在 `consecutiveFailures==1` 打一条日志，无指标/无告警。
2. **轨迹采集覆盖不全 / 样本退化**：写盘仅 `cortex_integration.go:532`；web 与 bot 主路径走的是 `agent.go` 流式循环，其中**未见** `RecordTrajectory` 调用（需确认）。当前样本清一色是问候语且 `steps: 0`，效率分数长期贴近满分（`avg 0.93~1.0`），评分维度几乎没有动态范围。
3. **5 分钟周期的空转成本**：`gepaBaseInterval=5m` + `improvement` 恒 0，收敛前每轮都在提交 LLM 生成策略；配合 P0-2 的去重后可直接省掉这部分开销。
4. **世代史无界增长**：`saveGeneration` 全量落盘（133 个 / 1.93 MB，且每份都包含整段注入文本），无轮转/压缩。
5. **生命周期**：`GEPAEngine.Stop()` 存在且被 `Manager.Stop()` 调用，但 server 的 `Stop()`（`server.go:1824`）不调用 cortex manager 的 Stop；`Manager.Stop` 的调用方需再确认（进程退出时后台循环与 `gepa` 目录写入可能未收口）。

---

## 5. 建议修复顺序

| 顺序 | 动作 | 影响面 |
|---|---|---|
| 1 | 修 `SetSoul` 裁剪策略（禁止头部裁剪 + 无标记时不写 + 告警） | 防人格丢失，改动小、风险低 |
| 2 | SOUL 世代快照 + 回滚接口 | 让 P0-1 可恢复 |
| 3 | GEPA 种群去重 + 应用门槛（新数据高水位） | 停止空转与重复注入 |
| 4 | `isConverged` 自动解锁 + 指标暴露 | 让"进化是否在动"可见 |
| 5 | 决策：技能侧死代码"接线"还是"删除"（含前端入口） | 消灭空壳 API |
| 6 | 版本快照接入 `Manager.Update`，打通回滚 | 技能变更可回滚 |
| 7 | 轨迹采集补齐流式路径 + 评分维度加权/校准 | 提升信号质量 |

---

## 6. 一句话结论

> 当前"技能的进化"有两层：认知层（GEPA→SOUL.md）**活着的部分只做重复注入**，且已把人格文件顶到 64 KiB 上限，**下一次写入会静默删掉人格头部**；技能层（进化/优化/统计/版本）**一行都没接线**，对外接口全是空数组。优先级最高的是把 SOUL 的裁剪与快照修掉，否则任何"继续进化"都会以破坏人格文件收尾。
