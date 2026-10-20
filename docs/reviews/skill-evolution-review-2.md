# 「技能进化」子系统复审（第二轮）

- 前序报告：`docs/reviews/skill-evolution-review.md`（P0-1 人格头截断 / P0-2 空转 / P0-3 收敛闩锁 / P1-1 死代码 / P1-2 不可回滚 / P1-3 阈值矛盾）
- 本轮对象：加固提交 `cfbffb23 feat(cortex,skills): harden the self-evolution loop and expose ops surface for SOUL/GEPA`（35 文件 / +4347 行）**之后**的实际状态
- 方法：静态调用链反查 ＋ **运行期探针实测**（临时测试文件编译执行后已删除，工作区干净）
- 结论：上一轮的 P0-1（人格头截断）**确实修好了**，SOUL 侧的保护是扎实的；但新引入的「新数据门槛」把 GEPA **永久冻结在第 100 条轨迹**（P0-1'），而技能侧的「进化」本体（改写技能内容的那条链）**依旧是零调用**——这一轮接线只把统计/版本/触发三条支线接上了，主干没接。

---

## 0. 本轮实测证据（3 个探针，全部可复现）

| 探针 | 断言 | 实测输出 |
|---|---|---|
| A. 轨迹池封顶 | `len(GetTrajectories(100))` 是否随池增长 | `pool=130 GetTrajectories(100)=100`；`hasNewSignal after 30 new trajectories = false` |
| B. 触发优化输入 | 只用生产唯一上报路径（`RecordSkillInvocation`）能否产出关键词 | `cond.Keywords=[] (len=0)`；`trigger hits=map[invocation::5] missed=0` |
| C. 版本回滚 | 回滚到初始版本后 SKILL.md 是否仍是合法技能文件 | Create 后 `frontmatter=true`；快照内容 `frontmatter=false`；回滚后 `firstLine="# body only"` |
| D. 删除后回滚 | 被删技能能否从版本历史恢复 | `err=skill directory not found for gone-skill`（Delete 的注释承诺"可从版本历史恢复"） |

---

## 1. P0-1'　新数据门槛把进化**永久冻结**在第 100 条轨迹（本轮最严重）

### 事实

`internal/cortex/gepa.go:364`

```go
trajectories := g.trajectoryStore.GetTrajectories(100)   // ← 返回值**恒 ≤ 100**
...
if len(trajectories) <= lastCount && lastCount > 0 {
    return errNoNewTrajectories      // P0-2 引入的"新数据门槛"
}
...
g.lastEvolvedTrajectoryCount = len(trajectories)   // ← 高水位也是这个封顶值
```

`internal/cortex/trajectory.go:360` 的语义是"截断到最近 limit 条"：

```go
if limit <= 0 || limit > len(ts.trajectories) { limit = len(ts.trajectories) }
return ts.trajectories[len(ts.trajectories)-limit:]   // 池再大也只给 100 条
```

而轨迹池的上限是 **1000**（`evictOldTrajectories`）。于是：

- 池 < 100 时：`len` 随池增长，门槛能正确放行；
- 池 **≥ 100** 的那一刻起：`len(trajectories) == 100 <= lastCount(100)` **恒真** → 永远返回 `errNoNewTrajectories`。

### 后果链

1. **GEPA 从此不再产生任何世代**（`evolve()` 在 LLM 调用之前就返回），不再注入 SOUL——"进化"在跑满 100 条轨迹后静默停摆，且**日志里只有 `evolution still failing` 之外一片安静**（该分支被归类为"正常态"，`consecutiveFailures=0`，不打任何日志）。
2. `hasNewSignal()` 用同一套比较 → **收敛闩锁再也无法自动解锁**（P0-3 的修复被本回归抵消）。
3. 附录在第 10 分钟 ticker 上的 `MaybeCompact`（`applyGEPABestStrategy` 内）也随之不再触发——SOUL 逼近上限时的自动回收一起停摆。
4. 重启进程救不回来：`loadGenerations()` 把高水位对齐为 `len(GetTrajectories(100))`（同样封顶 100），**首个 tick 即直接跳过**。

对一个"已经聊过 100 个回合以上"的实例，这就是"装了个看起来在跑、实际已死"的进化引擎——比上一轮的空转更糟，因为空转至少还能被 `improvement` 曲线看出来。

### 修法（择一，推荐 1）

1. 给 `TrajectoryStore` 一个**单调计数**（`RecordTrajectory` 时 `totalRecorded++`，只增不减），高水位改比 `totalRecorded`；或 `latestTrajectoryID/EndTime` 比较。**不要用任何经过 limit 截断的返回值**。
2. 兜底语义修正：`lastEvolvedTrajectoryCount` 改名体现"已消费位点"，并在循环里改成 `>= poolTotal` 判等而非 `len <= last`。

### 回归缺口（为什么 400 行新测试没抓到）

`gepa_gating_test.go::TestHasNewSignal` 只造了 **3~4 条**轨迹（远低于 100 的封顶线），`TestLoadGenerationsAlignsHighWaterMark` 用了 `i<...` 的小样本。**新测试必须覆盖 `pool > 100` 且高水位已到位的场景**，否则这条回归会再次穿网。

---

## 2. P1　技能侧「进化本体」依旧是零调用（上一轮 P1-1 只接了一半）

`internal/skills/evolution.go` 的 `SkillEvolutionManager` **全仓仍无调用方**（`grep RunAutoEvolution|EvolveSkill|ValidateEvolution|RevertEvolution` 只有定义与注释）：

| 能力 | 位置 | 调用方 | 现状 |
|---|---|---|---|
| 自动进化 | `evolution.go:527 RunAutoEvolution` | **0** | 从不运行 |
| 单技能进化 | `evolution.go:221 EvolveSkill` | **0** | 从不运行 |
| 进化验证/回滚 | `ValidateEvolution` / `RevertEvolution` | **0** | 从不运行 |
| 进化历史读侧 | `manager.go:2304 GetEvolutionRecords` | 仅 API | 读 `evolution_records.json`，而**该文件没有任何写入方** → 恒空 |

连带：`GET /api/skills/{name}/evolution` **仍恒返回 `[]`**（`server/skills.go:125`），前端 `getSkillEvolutionHistory`（`web/src/api/skills.ts:98`）也**没有任何组件调用**——读写两侧都是空的。

对比之下，这一轮确实接上了三条**支线**：
- 有效性统计：`skill_invoke_tool.go:179 → Manager.RecordSkillInvocation` ✔（前端 Skills 页的成功率/质量有数据了）
- 版本快照 + 回滚：`Create/Update/Delete → snapshotSkillVersion` + `RollbackSkillVersion` ✔（但有 §3 的缺陷）
- 触发优化器单例 + 6h 自动循环 ✔（但吃不到数据，见 §2'）

**判断**：`evolution.go` 这个文件（571 行）目前是"已具备能力、从未启用"的状态。要么把它接进 agent 回合收口（或在 `manager.Close()` 前起一个每日循环），要么**明确删掉它和 `/evolution` 路由＋前端函数**。继续留着"看起来有、实际恒空"的 API 是这一轮最值得清掉的债。

### 2'　触发优化：数据进去了，但**没人用**（实测 cond.Keywords 为空）

`manager.go:2369` 生产唯一的触发上报是：

```go
to.RecordTriggerHit(skillName, "invocation", "", success)   // triggerKey = "invocation:"
```

而 `trigger_optimizer.go::OptimizeTriggers` 只处理 `triggerType == "keyword"`：

```go
triggerType, keyword := parts[0], parts[1]
if triggerType != "keyword" { continue }        // ← "invocation" 全被丢掉
```

⇒ `keywordStats` 恒空 ⇒ `optimalKeywords` 只可能来自 `MissedTriggers`，而 **`RecordMissedSkillTrigger` 在生产里同样没有任何调用方**（只有 `evolution_wiring_test.go`）。

结果：`GetOptimizedTriggers` 因 `len(OptimalKeywords)==0` 返回 nil → `manager.go:931 applyOptimizedTriggers`（这一轮特意接上"最后一环"的代码）**在生产中恒为 no-op**。触发统计现在只是一份写进 `trigger_stats.json` 的、没人读的日志。同理 `FalsePositives` 无任何写入方，"误触发率 >30% 剔除关键词"的分支也是死的。

要闭环，必须补两个真实信号源：
1. 用户显式 `/skill <name>` 命中（`chat_directives.go::renderSkillDirective`）时上报 `RecordTriggerHit(name, "keyword", <命中的词>, true)`；
2. "用户先随手说了一句、随后才 `/skill` 点某个技能"→ `RecordMissedSkillTrigger`（这是唯一能产生新关键词的来源）。

---

## 3. P1　新接上的「回滚」自身有两个可用性缺陷（已实测）

### 3.1 回滚到初始版本会**抹掉 SKILL.md 的 YAML frontmatter**

`Create` 把模板头写进文件、却把**裸 body** 存进 `Skill.Content`：

```go
os.WriteFile(skillMdPath, []byte(skillContent), 0644)   // skillContent 带 --- name/description/version/tags ---
skill := &Skill{... Content: content ...}                // content = 用户给的正文
m.snapshotSkillVersion(skill, "created")                 // 快照存的是 Content（裸 body）
```

于是"版本内容 ≠ 磁盘文件"，而 `Rollback → restoreContentToDisk` 是**整文件覆写**。实测回滚后 `SKILL.md` 首行变成 `# body only`，frontmatter 全丢；而 loader（`loadSkillFromContent`）正是靠 frontmatter 取 `name/description/tags`——技能名会退化成目录名，描述/标签丢失，`GetSkillInfo`/技能索引随之失真。

修法：快照统一以**磁盘文件内容**为准（或让 `Create` 把 `skillContent` 写进 `Content`，二者只留一个口径）。

### 3.2 「删除后可恢复」是空承诺

`Delete` 先落快照（`manager.go:1530`，注释写"误删后可从版本历史恢复"），但 `restoreContentToDisk` 要求技能目录存在：

```go
if _, err := os.Stat(dir); err != nil { return fmt.Errorf("skill directory not found for %s: %w", ...) }
```

实测 `rollback of deleted skill err=skill directory not found`。要么在目录缺失时 `MkdirAll` 重建再写，要么把注释和 CLI 文案改成"删除不可回滚"。

### 3.3 版本历史无上界 + 重复版本

- `Update` 一次落 **2 份**（改前 + 改后），而"改后那份"与"下次改前的"逐字节相同 ⇒ 重复版本堆积；
- `UpdateMetadata`（改 name/description/tags 并写盘）**不落快照** ⇒ 元数据变更不可回滚，口径不一致；
- 每份版本带**全文**且无轮转 ⇒ 与 GEPA 世代文件同样的问题（那一侧这轮已加 `maxPersistedGenerations=200`，这一侧没有）。

---

## 4. P2　其余不合理点

1. **`EvolveSkill` 会把"改前快照"污染成新内容**（潜伏炸弹）：`Manager.Get` 返回**活指针**，而 `EvolveSkill` 先 `skill.Content = result.NewContent` 再调 `manager.Update`；`Update` 内部第一步就是 `snapshotSkillVersion(skill, ...)` ⇒ 快照存的是**已经改过的新内容**，"回滚到进化前"直接失效。今天无害（无人调用），一旦按 §2 接上 `RunAutoEvolution`，安全网立即变成摆设。修法：快照改成从磁盘文件读旧内容，或 `EvolveSkill` 不要预先改写 `Content`。
2. **策略的 `Target` 被忽略**：生成器让模型产出 `"target": "soul|system|skills"`（`gepa_strategy.go:185`），而 `ApplyBestStrategyManaged` 不看 `Target`，把**所有** change 一律写进 SOUL.md 的 `## Learned Preferences` 区。于是"target=skills 的技能优化建议"落进了人格文件；同时没有任何代码真的去改技能。语义白定义了。
3. **`best == nil` 早退不推进高水位**：策略被去重到空时 `evolve()` 直接 `return nil`（`gepa.go:397`），**不更新** `lastEvolvedTrajectoryCount` ⇒ 下一轮（5 分钟）用同一批轨迹再调一次 LLM，且返回值 nil 被当作"成功"，退避不生效。这是一条无上限的推理成本泄漏路径，而且**连日志都没有**（只 `evolveSkipped++`）。
4. **`magic cortex gepa` 这个"运维探针"读不到真实状态**：CLI (`cmd/magic/cortex.go:127`) 用 `NewGEPAEngine(dir, nil, store)` + `LoadHistory()` 构造 → `provider == nil` ⇒ `GetStats()` 的 `health` 恒为 `"disabled"`；且 `isConverged/convergenceLatched/identicalRounds/lastImprovement` **都不落盘**，`loadGenerations` 只恢复 `currentGen/bestStrategy` ⇒ CLI 恒报 `is_converged=false`。真正"进化是不是停了"的信号（本轮 P0-3 的修复目标）恰好读不到。修法：把收敛与计数状态一起持久化，或让 CLI 走 HTTP `/api/cortex/gepa`。
5. **前端只接了统计**：`getSkillStatistics` 有调用方（SkillsView），`getSkillVersions`/`getSkillEvolutionHistory` **无任何调用方**；新加的 `POST /api/skills/{name}/rollback` 在 `api/skills.ts` 里**连函数都没定义** ⇒ 回滚只能靠 CLI/curl。要么补 UI，要么承认它是运维接口并在文档里写明。
6. **`/skill` 主路径不产生统计**：产品里用技能的主路径是 chip/斜杠 → `chat_directives.go::renderSkillDirective`，它**不调用** `RecordSkillInvocation`；只有模型主动调 `skill` 工具才记。叠加 `estimateInvocationQuality = len(content)/20`（封顶 80）这个纯长度代理分，"有效性统计"目前只覆盖模型自发调用、且分数近似"内容有多长"。这不影响正确性，但会让"技能在退化"这类判断长期失真——值得在 UI 上标注口径（此前 `avgQualityHint` 已做过一次同类说明）。
7. **SOUL 注入会周期性打断前缀缓存**：`withContextBlocks` 刻意把 `cortexStatic(SOUL)` 放在稳定前缀里（顺序不变式见项目记忆），而 GEPA 每 10 分钟就可能往 SOUL 追加/更新一节 ⇒ 每会话的 system 前缀缓存被整体失效一次。至少应把"注入内容"与"稳定人格头"分开成两个 block（注入区放最后）。
8. **`skillCount` 存在数据竞争**：`EnhancedAutoCreator.GenerateSkillFromPattern` 里 `e.skillCount++` 未持 `e.mu`（调用链 `checkAndGenerateSkillsFromSnapshot` 已出锁），而 `GetStats()` 在 `e.mu.Unlock()` **之后**才读它（`auto_enhanced.go:482/490`）。本机无 gcc 跑不了 `-race`，但这是真实的竞态。
9. **`CompactSoul` 的自杀式安全闸**：压缩后若文本里不含 `"You are Magic"` 就直接放弃（`soul.go:382`）。用户自定义过 SOUL 头的实例（很常见）会表现为"压缩命令执行了但什么都没删"。至少应在返回里区分"无可回收"与"被安全闸拦下"。

---

## 5. 建议修复顺序

| 顺序 | 动作 | 影响面 | 风险 |
|---|---|---|---|
| 1 | 修轨迹高水位（单调计数 / 最新轨迹时间，禁用封顶长度）+ 补 `pool>100` 回归 | GEPA 复活 | 低 |
| 2 | 决策 `SkillEvolutionManager`：接线（补每日 `RunAutoEvolution` 循环）或删除（含 `/evolution` 路由 + 前端函数） | 消灭恒空 API | 低 |
| 3 | 修 `Create` 的 Content/文件口径不一致（回滚丢 frontmatter）；删除后回滚重建目录 or 改承诺 | 回滚真正可用 | 低 |
| 4 | 触发优化补真实信号源（`/skill` 命中上报 + 漏触发上报），否则删掉 `applyOptimizedTriggers` | 让"自优化"成立 | 中 |
| 5 | 持久化 GEPA 收敛/计数状态，让 CLI 与 HTTP 探针说真话 | 可观测性 | 低 |
| 6 | `EvolveSkill` 快照污染、`Target` 语义、`best==nil` 成本泄漏、版本轮转、`skillCount` 竞态 | 加固 | 低 |

---

## 6. 一句话结论

> 上一轮的 P0-1（SOUL 头部截断）修得干净利落，但这一轮**新加的新数据门槛用了一个被封顶的 `len()` 当高水位**，把 GEPA 从"空转"变成了"跑满 100 条轨迹后彻底停摆"，且收敛解锁、SOUL 自动压缩一起失效——**这是本轮唯一的 P0，也是优先级最高的一处**；而"技能进化"的主干（`SkillEvolutionManager` 改写技能内容）与触发优化的数据源**依然一行没接**，新加的 `/evolution` API、版本回滚 UI 与 `applyOptimizedTriggers` 都还停留在"结构在、数据不在"的状态。

---

## 7. 修复记录（第三轮：全部落地）

> 本节记录对 §1–§4 的逐条修复。验证：`go build ./...` ✔、`go vet` ✔、`go test ./internal/... ./cmd/...` **29 包全绿** ✔、`vue-tsc --noEmit` ✔。

### P0-1' 轨迹高水位（§1）— 已修

- `TrajectoryStore` 新增 `LatestTrajectoryKey()`：返回**最新一条轨迹的位点**（`ID@EndTime.UnixNano`），随每条新轨迹单调变化、不受 `limit` 截断影响、重启后可从磁盘复原。
- `GEPAEngine.lastEvolvedTrajectoryKey` 取代 `lastEvolvedTrajectoryCount` 成为**唯一门槛依据**；`hasNewSignal`、`evolve` 的门槛与高水位推进、`loadGenerations` 的重启对齐全部改用它。原计数保留但**仅供观测**（`GetStats.trajectories_consumed`）。
- 新增回归：`TestNewSignalSurvivesTrajectoryPoolCap`（130 条池 + 再加 30 条）、`TestEvolveGatePassesBeyondPoolCap`（验证 `evolve` **不再**返回 `errNoNewTrajectories`）。旧测试 `TestHasNewSignal` / `TestLoadGenerationsAlignsHighWaterMark` 已改为断言位点语义。

### P1 技能进化主干（§2）— 已接线（不是删除）

- `Manager` 新增 `startSkillEvolutionLoop`：按 `EvolutionInterval`（默认 24h）跑 `RunAutoEvolution`，与触发优化循环**共用 `m.stopChan`**，`Close()` 一起退出；`requireProvider` 门控（无 LLM provider 或 effectiveness 缺一即跳过）。
- `GetEvolutionRecords` 改用 `evolutionManager(false)`（只读历史允许无 provider），`/api/skills/{name}/evolution` 因此第一次有真实数据。
- 补上**事后验证**：`RunAutoEvolution` 先 `validatePending()`，用当前统计调用 `ValidateEvolution` —— 质量下降的记录会**自动回滚**。`hasRecentEvolution` 现在把 `pending_review` 也算"待验证"，避免次日立刻再改写一遍。
- `CheckEvolutionNeeded` / `collectFailureCases` / `EvolveSkill` 全部加 nil 守卫（`effectiveness` 为 nil 时不再 panic）。

### P1 触发优化数据源（§2'）— 已闭环

- 新增 `Manager.RecordSkillDirectiveHit`：把 `/skill <name>` 的**技能名**作为 `keyword` 命中上报（`OptimizeTriggers` 只认 `keyword`，这正是此前被丢弃的那一类）。
- `chat_directives.go`：`renderSkillDirective` 现在回传解析到的技能名；`recordDirectiveTriggerSignals` 上报"显式命中"，并把 `/skill <name>` 后面的**随手那句话**（或同消息里的非指令行，≤200 字）作为**漏触发样本**上报 —— 这是优化器学到新词的唯一来源。`SkillTriggeredByInput` 防止把"本来就能路由"的情况误记为漏触发。
- 回归：`TestDirectiveHitProducesOptimizableKeywords`、`TestOptimizedTriggersFeedBackIntoMatching`（显式命中 → 学到关键词 → 真正影响 `MatchSkillsByInput` 结果）、`TestExpandChatDirectives_RecordsTriggerSignals`、`TestExpandChatDirectives_NoResidualNoMissedTrigger`。

### P1 回滚可用性（§3）— 已修

- **口径统一**：`snapshotSkillVersion` 改为读取**磁盘上的 SKILL.md 全文**（回滚是整文件覆写）。顺带消灭 §4-1 的"改前快照被污染"：即便 `skill.Content` 已被改写，快照拿到的仍是磁盘上的旧内容。
- `restoreContentToDisk` 在目录缺失时 `MkdirAll` 重建 ⇒ **回滚已删除的技能**不再是空承诺。
- `SaveVersion` 增加**内容级去重**（与当前版本逐字节相同则不产生新版本）与**版本轮转**（`maxVersionsPerSkill=50`，保留初始版本 + 最近若干版）；`UpdateMetadata` 也落快照。
- 回归：`TestRollbackRecreatesDeletedSkill`、`TestVersionHistoryIsBounded`、`TestVersionSnapshotDeduplicatesIdenticalContent`；`TestVersionSnapshotAndRollback` 已重写为"整文件口径"（断言回滚后 frontmatter 与 name/description 均完好）。
- **新增（接线后暴露的潜伏 bug）**：`EvolveSkill` 交给 `Update` 的是模型产出的**正文**，会把 frontmatter 一起抹掉。新增 `mergeFrontmatter` 保留原 frontmatter，仅替换正文；回归 `TestEvolveSkillPreservesFrontmatter`（含 reload 后元数据校验）、`TestMergeFrontmatterEdgeCases`。

### P2 收尾（§4）— 逐条

| # | 项 | 处理 |
|---|---|---|
| 1 | `EvolveSkill` 快照污染 | 快照改读磁盘 + 去掉 `skill.Content` 预改写（双保险） |
| 2 | 策略 `Target` 被忽略 | `ApplyBestStrategyManaged` 按 Target 分流：`soul`/空 → 写 SOUL；`system`/`skills` → **跳过并留日志**（不误写进人格文件），同时标记为已消费避免日志刷屏；生成器提示词同步声明"仅 soul 会被自动应用" |
| 3 | `best==nil` 早退不推高水位 | 推进位点 + 打日志（消除无上限的 LLM 成本泄漏） |
| 4 | CLI 探针读不到真实状态 | 新增 `gepa/state.json` 持久化（收敛闩锁、失败计数、位点、指标），`loadGenerations` 开头加载；`GetStats` 增加 `provider_available` / `state_from_disk`，`health` 不再因探针无 provider 而恒报 `disabled`；回归 `TestGEPAStatePersistsAcrossProcesses` |
| 5 | 前端只接了统计 | `api/skills.ts` 补 `rollbackSkillVersion`；详情弹窗新增**版本历史（含回滚按钮）**与**进化记录**两张卡片（此前 `getSkillVersions`/`getSkillEvolutionHistory` 无调用方）；补 `skills.rollback` 等本地化键 |
| 6 | `/skill` 主路径不产生统计 | 触发侧已闭环（见上）；UI 的统计数据卡加 ⓘ 说明**口径**（仅覆盖 skill 工具调用与 `/skill` 显式命中） |
| 7 | SOUL 注入打断前缀缓存 | `SoulManager.SplitSoulForPrompt()` 把稳定头与 GEPA 机器区拆开；`Agent.cortexDynamic` 作为**最后**一个 system 块注入，稳定头留在可缓存前缀；回归 `TestSplitSoulForPromptSeparatesManagedRegion`、`TestGetSoulForPromptStaysComplete` |
| 8 | `skillCount` 竞态 | 改为 `atomic.Int64`（`Add`/`Load`），输出仍为 `int` 口径 |
| 9 | `CompactSoul` 自杀式安全闸 | 闸门改为结构性判定（结果为空、或原含标准人格头而结果丢失才算丢头）；自定义 SOUL 头不再被一刀切拦下；被拦时返回哨兵 `ErrSoulCompactionHeaderGuard`，CLI 给出**可区分**的提示；回归 `TestCompactSoulPreservesCustomHeader`、`TestCompactionKeepsHeaderGuard`（改为断言哨兵） |

### 仍未处理（有意）

- `/skill` 指令路径**不写有效性统计**（只写触发统计）：`estimateInvocationQuality` 是"内容长度"代理分，把它当成一次真实调用会拉低 `AvgQuality` 并可能触发无谓进化。已改为在 UI 上标注口径，而不是引入第二份粗糙数据。
  > **后续（`8936ae63`）已改口径**：显式 `/skill` 命中现在同时计入调用统计（`RecordSkillDirectiveHit` → `RecordSkillInvocation`），统计页说明也同步改为"覆盖 skill 工具调用与 `/skill` 显式命中"。
- GEPA 策略的 `system` / `skills` 目标只是**记录不应用** —— 目前没有对应的写入端口，宁可跳过也不猜测。

---

## 8. 第四轮补记（自动技能落盘 / SOUL 冗余回收）

### 8.1 自动生成的 SKILL.md 没有 frontmatter — 已修

`auto_enhanced.go::generateSkillMarkdown` 此前只写正文，落在 `auto_skills/<state>/<id>/SKILL.md` 的文件**不是一份合法技能文件**：任何按 SKILL.md 加载的路径（Reload、批准后目录被扫描、`magic skills show`）只能拿目录名当技能名、默认描述当描述，`tags`/`tools` 全空。也就是说"技能声明自己需要哪些工具"这条加载期校验，对自动技能完全失效。

- 现在生成完整的 `name / description / version / author / tags / tools` frontmatter；`tools` 取 `ToolSequence` 去重后的有序集合。
- **状态不写进 frontmatter**：pending/approved 由所在目录与 `meta.json` 表达，避免同一事实两处真相；`RegisterSkill` 的内存 `Tags` 也同步去掉 `pending`，与磁盘一致。
- `skillFrontmatterValue` 负责把描述压成单行双引号标量（内部的 `"` 归一为 `'`）：解析器不处理转义，这一步不做，含冒号/引号的描述会破坏 frontmatter。
- 顺带把正文里那几条"调用第一个工具→用上一步结果…"的空模板改成可执行表述（首步点明具体工具、明确"任务完成即停"），无示例时显式写 `(no examples captured yet)` 而不是留空。
- 回归：`internal/skills/auto_enhanced_frontmatter_test.go`（生成 → 解析 → 再经 `loadSkillFromContent` 加载，断言元数据不退化）、`TestSkillFrontmatterValueIsSingleLine`。

### 8.2 SOUL 只增不减 — 补上"完全重复章节"回收

上一轮只把 `CompactSoul` 收窄到"丢弃自动生成载荷"，语义重复的章节（同一段内容换个标题反复注入、`## Core Values` 出现多次）依然没有任何淘汰路径。

- `CompactSoul` 现在同时丢弃**正文指纹完全相同**的重复章节（`dedupBodyKey`：去掉标题行、抹平空白差异后比较）。
- 正文短于 40 个 rune 的区块**不参与**去重：占位标题规范化后接近空串，压掉它们是丢结构而不是回收冗余。
- `payloads` 为空时不再早退（去重不依赖载荷集合），但触发条件仍是"逼近 64 KiB"（`MaybeCompact`），不会在正常体积下改动人格文件。
- 日志区分两种回收来源：`removed N section(s) (x auto-generated payload, y duplicate)`。
- 回归：`internal/cortex/soul_dedup_test.go`（逐字节重复 / 仅排版不同 → 各删 1；短区块保留；`dedupBodyKey` 指纹口径）。

