# 技能审查报告（第二轮 · 2026-10-15）

> 上一轮：`builtin-skills-review.md`（含 §7 修复记录，`35110dcd` / `f19ab646` / `99180445` 三次提交落地）。
> 本轮不再复述上轮结论，而是**重新取证复核**：先确认上轮修复是否真的生效，再找**本轮新发现**的问题。

## 0. 范围与方法

对象：仓库内全部 13 个技能（`internal/skills/builtin/` 7 个 · `./skills/` 6 个）。

本轮的关键方法是**从实现侧反查**，而不是读文档猜：

| 手段 | 目的 |
|---|---|
| 临时 Go 探针 dump `Registry.List()` | 拿到 64 个**真实注册**的工具名，逐条比对技能声明 |
| 解析全部 frontmatter | 检查字段一致性、`tools` 可解析性 |
| 描述词 Jaccard 重叠计算 | 量化技能之间的路由重叠（潜在双触发） |
| 代码围栏平衡性脚本 | 13 个文件逐个校验 |
| 逐条追 `grep` 调用方 | 验证文档里的每条能力断言 |

---

## 1. 结论摘要

**上轮修复全部有效，且经独立复核确认**（§2）。本轮**没有发现上轮的问题复发**。

新发现 4 条，集中在**文档与引擎不一致**和**跨技能/跨层一致性**上，没有 P0：

| # | 级别 | 问题 | 一句话 |
|---|---|---|---|
| **R1** | P1 | `~/.magic/builtin_skills/` 是**从未被创建的空目录**，却被文档当成"内置技能副本"介绍 | 文档承诺了一个不存在的目录，`BuiltinDir` 在 server 路径上是死配置 |
| **R2** | P2 | `debug`（内置，通用）与 `debug-go`（工作区，Go）描述重叠度最高（0.212），症状词几乎一致 | Go 崩溃时两个技能同时命中；`debug` 本身已含 Go 内容 |
| **R3** | P2 | 两组技能的 frontmatter 规范**分叉**：内置 7/7 无 `license`、工作区 6/6 无 `tools`、`author` 取值也不同 | 同一套加载器、两种写法，且工作区技能漏声明它们实际要用的工具 |
| **R4** | P2 | 触发优化器的分词用 `strings.Fields`（按空格切） | 中文没有空格 → 整句变成一个"关键词"，CJK 场景下"自优化"学不到东西 |

> **2026-10-16 更新：R1–R4 已全部修复，见 §6 修复记录。**

---

## 2. 已复核确认正确（上轮修复保持有效）

### 2.1 技能声明的 `tools` 全部真实注册

dump 真实注册表得 64 个工具（`Registry.RegisterAll` 路径）。七个内置技能声明的工具**逐条命中**：

| 技能 | 声明 | 结果 |
|---|---|---|
| `code-review` | `read_file, search_in_files` | ✅ |
| `coding` | `read_file, search_in_files, lint, analyze_error, diff_patch, batch_file_ops` | ✅ |
| `daily-report` | `todo, session_search` | ✅ |
| `debug` | `read_file, search_in_files, analyze_error, execute_command` | ✅ |
| `file-organizer` | `list_files, directory_tree, file_edit, search_in_files` | ✅ |
| `summarization` | `web_fetch, read_file` | ✅ |
| `translation` | `web_fetch, read_file` | ✅ |

**MISSING_TOTAL = 0。** 上轮挖出的"`toolNames` 是死字段、`tools` 从未被解析"（R1/R2 根因）确实已修：`validateSkillTools()`（`manager.go:479-503`）是 `ManagerConfig.ToolNames` 的消费方，`server.go:315-316` 用 `registry.List()` 注入。正文里也不存在引用未注册工具的残留。

### 2.2 `chat-interrupt` 的引擎断言**逐条为真**

这是上上轮我一度误判为"照想象写"的技能，本轮回查全部成立：

| 文档断言 | 代码取证 | 结论 |
|---|---|---|
| `/req*` 只在 TUI 可用 | `cmd/magic/tui.go:656`（命令清单）、`780-797`（分派） | ✅ |
| Web/机器人只有 `/skill`、`/mcp` | `internal/server/chat_directives.go:78-80` | ✅ |
| 需求落到 `~/.magic/todos/todos.json` | `internal/tool/todo_tool.go:133-138`（`filepath.Join(GetMagicHome(),"todos")/todos.json`） | ✅ |
| 「`/reqs` 与 `todo` 工具读同一份数据」 | `tui.go:1906-1912`：`doAddRequirement` 直接调 `tool.GetTodoTool()` 且 `action=create` —— **同一个工具、同一份存储** | ✅ |
| `/req --priority high|medium|low` | `tui.go:2629-2657`（`parseReqArgsTUI`） | ✅ |
| `/stop`、`/context`、`/interrupt` | `tui.go:748`、`800` | ✅ |

> 这条是本轮最有价值的确认：一个"看起来像编的"技能，实际是**唯一一个把 TUI 专属交互写清楚的**，还主动标注了适用范围、劝模型不要在 Web 里提示用户敲 `/req`。

### 2.3 上轮的 4 处内容硬错误已修复

| 位置 | 现状 |
|---|---|
| `code-quality` Go 示例 | `skills/code-quality/SKILL.md:67` 有 `defer resp.Body.Close()`，`httpClient` 未定义问题已消 |
| `debug-go` 拿 GC 当死锁检测 | `skills/debug-go/SKILL.md:128-143` 改为 `SIGQUIT` / `runtime.Stack` / goroutine profile / `goleak`；`:165` 显式写明 `debug.SetGCPercent(-1)` **与死锁无关** |
| `docs-writing` 嵌套围栏 | 已用四反引号（`:40/67/81/105/173/180/182/191`） |
| `code-review` 嵌套围栏 | 已用四反引号（`:118/135`） |

### 2.4 围栏平衡性

13 个文件脚本校验：**ALL_FENCES_OK**。不再有"三反引号嵌三反引号"的非法结构。

### 2.5 来源标注不再伪造

`applySkillOrigin`（`manager.go:436-462`）来源一律按目录判定，不再继承 `builtin`；覆盖内置时 `log.Warnf` + `Metadata["shadows_builtin"]=true`。两组之间也不再存在同名 `code-review`。

---

## 3. 本轮新发现

### R1（P1）`~/.magic/builtin_skills/` 从未被创建，但文档把它写成"内置技能副本"

**证据链：**

1. `docs/USAGE.zh-CN.md:184` 在 magic home 目录树里列出 `builtin_skills/  内置技能副本`。
2. `internal/server/server.go:314` 把它作为配置传入：`BuiltinDir: filepath.Join(magicHome, "builtin_skills")`。
3. **但全仓没有任何代码创建或写入它**：

   ```
   $ grep -rn "builtin_skills" --include=*.go .
   ./internal/server/server.go:314   ← 仅此一处
   ```
4. 而加载逻辑是「先读 embed，成功即 `return`」（`manager.go:337-354`），`BuiltinDir` 只在 embed 失败时才走到的死分支（`manager.go:356-380`）。
5. 结论：**该目录永远不会存在，`ManagerConfig.BuiltinDir` 在 server 路径上是死配置**，文档里"内置技能副本"的描述与实况不符。

**影响**：用户按文档去 `~/.magic/builtin_skills/` 想查看/覆盖内置技能，会扑空（真正的覆盖位置是 `~/.magic/skills/` 同名技能）。

**建议（二选一，不要维持现状）**：
- **A（推荐，改动小）**：删掉 `docs/USAGE` 里的该行，并把 `BuiltinDir` 的注释改准（"仅 embed 失效时的兜底目录，正常不会被创建"）。
- **B（功能向）**：在 `NewManagerWithConfig` 里把 embed 内容同步落盘到 `magicHome/builtin_skills`（仅在目录不存在时写一次），让"副本"名副其实、也便于用户对照排查。

### R2（P2）`debug` 与 `debug-go` 路由重叠，`debug` 已覆盖 Go

描述词 Jaccard 相似度排名第一：

```
0.212  debug <-> debug-go   共享: cpu, crashes, hangs, memory, panics, program, races
```

也就是说，一个 Go 程序 panic / 崩溃 / 卡死时，**两个技能的描述都会命中**，而内置 `debug` 的正文里本来就已有 Go 专属内容：

- `internal/skills/builtin/debug/SKILL.md:150,154,161` — `go tool pprof`（CPU/内存/在线 endpoint）
- `:195` — 「### Go panic」小节

**影响**：技能集里同时存在"通用 debug"和"Go debug"，且通用版已含 Go 内容 → 重复注入、职责不清。

**建议（择一）**：
- 把 `debug-go` 定位收窄为**工作区示例技能**，描述里去掉与 `debug` 重复的症状词（panics/crashes/hangs/races），只保留"Go 特有工具链"（Delve 调试器、`goleak`、`GODEBUG`、逃逸分析）；或
- 直接把 `debug` 的 Go 专属段落下沉到 `debug-go`，让 `debug` 保持语言无关。

### R3（P2）两组技能的 frontmatter 规范分叉

| 字段 | 内置（7） | 工作区（6） |
|---|---|---|
| `license` | **0/7 有** | 6/6 有（`MIT`） |
| `tools` | 7/7 有 | **0/6 有** |
| `author` | `go-magic` | `magic` |

同一套加载器解析同一种 `SKILL.md`，却出现两种写法。

更实质的是 **`tools` 的缺失**：工作区技能正文里大量使用命令与文件操作（`git-workflow` 12 段代码块、`debug-go` 40 段、`api-testing` 12 段、`code-quality` 24 段），它们实际依赖 `execute_command` / `read_file` / `write_file` / `file_edit`，但一条都没声明。上轮刚把 `tools` 从"死字段"变成**加载期校验 + `magic skills show` 展示**（`cmd/magic/skills.go:359-361` + `unregisteredTools` `:370-387`），此时空声明等于"我什么工具都不需要"，信号丢失。

**建议**：定一份 canonical frontmatter（建议对齐内置：`name/description/version/author/tags/tools`，`license` 统一补上或统一去掉），并给工作区 5 个技能补 `tools`。

### R4（P2）触发优化器按空格分词 → 中文场景"自优化"学不到东西

`TriggerOptimizer.tokenize` 用 `strings.Fields`（`trigger_optimizer.go:473-480`）：先删标点，**再按空格切**。

```go
re := regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
text = re.ReplaceAllString(text, " ")
return strings.Fields(text)   // 中文没有空格 → 整句 = 一个 token
```

配合 `extractKeywordsFromMissedTriggers`（`:405-442`，词频统计 + 长度/停用词过滤），得到：`"帮我修复登录超时"` 会被当成**一个关键词**。而回灌侧 `applyOptimizedTriggers`（`manager.go:1086-1094`）的判据是 `strings.Contains(input, keyword)` —— 要求这整句话原样再现才命中。

**影响**：本项目文档与用户均为中文，跨会话几乎不可能复现整句，于是"从漏触发学到的关键词"实际只有技能名本身（`RecordSkillDirectiveHit` 记的 `keyword=技能名`）在起作用，其余为噪声。链路是通的，但**对中文用户收益接近 0**。

**建议**：给 `tokenize` 增加 CJK 分支（按 2-gram 切中文，或引入 `gojieba`/`sego` 之类分词），并在 `extractKeywordsFromMissedTriggers` 里对中文关键词加最小长度阈值（如 2 个汉字）与词频阈值。

---

## 4. 修复顺序建议

1. **R1-A**（改文档 1 行 + 改注释）——最低成本，直接消除"文档撒谎"。
2. **R3**（统一 frontmatter + 给工作区技能补 `tools`）——schema 一旦统一，后续审查都能自动化。
3. **R2**（拆分 `debug` / `debug-go` 职责）——纯内容调整。
4. **R4**（中文分词）——需要引依赖或自写 2-gram，改动面最大，可单独排期。

---

## 5. 与上轮结论的关系

- 上轮 §1 里我**更正过的两条错误结论**（"工具名全部真实存在""A 组字段能被正确解析"）本轮已复核为**修复后成立**，无需再改。
- 上轮 §7 的修复记录（Source 标注、`MissingTools` 校验、13 个描述改触发式、内容硬错误）本轮全部**独立复核通过**。
- 本轮 4 条新发现均为**上轮未覆盖的维度**：目录真实性、跨技能重叠、跨层 schema 一致性、分词与语言。

---

## 6. 修复记录（2026-10-16，R1–R4 全部落地）

### R1 ✅ 文档与注释改准（采纳方案 A，不实现落盘副本）

- `docs/USAGE.zh-CN.md` / `docs/USAGE.en.md`：从 magic home 目录树中**删除** `builtin_skills/` 行（它从不被创建，列在目录树里就是误导）。
- `internal/skills/manager.go` `ManagerConfig.BuiltinDir` 注释改准："仅在 embed 读取失败时兜底，正常不会被创建或读取，不要在文档中描述为内置技能副本"。
- `internal/server/server.go` 构造处加同样说明。
- **有意不采用方案 B**（embed 落盘同步）：embed 恒优先于磁盘，落盘副本会被用户误以为可编辑生效，反而制造新的误导。

### R2 ✅ debug-go 收窄为 Go 工具链技能（v1.2.0）

- description 重写：从症状词（panics/crashes/hangs/races）改为**工具链词**（Delve/pprof/race detector/goleak/GOTRACEBACK/slog），并显式声明通用排障走内置 `debug`。
- 正文新增 **Scope** 一节：`debug` 给方法、`debug-go` 给器械；"When to Use" 同步改为工具链动作。
- 内置 `debug` 的 Profiling 节头部加一行指引（措辞为 "when present"，因工作区技能不一定随安装存在）。
- **有意保留**内置 `debug` 的 Go 速查内容：它是多语言速查的一部分，且内置技能随二进制分发——若把 Go 内容下沉到工作区技能，已安装用户将完全失去 Go 指引。

### R3 ✅ frontmatter 统一为单一规范

13 个技能现在全部是同一组键：`name / description / version / author / license / tags / tools`。

- 内置 7 个：补 `license: MIT`（与仓库 LICENSE 一致）。
- 工作区 6 个：`author: magic` → `go-magic`；全部补 `tools`，且**逐个按正文实际能力声明**（只声明已注册工具）：

  | 技能 | tools | 依据 |
  |---|---|---|
  | api-testing | `execute_command, read_file` | curl/httpie 示例、读 spec |
  | chat-interrupt | `todo` | `/req` 直接调 `tool.GetTodoTool()` |
  | code-quality | `execute_command, read_file, lint, search_in_files` | golangci-lint/gofmt/ruff/eslint 命令 |
  | debug-go | `execute_command, read_file` | dlv/pprof/kill -QUIT |
  | docs-writing | `read_file, write_file, file_edit, search_in_files` | 读代码、写/改 md |
  | git-workflow | `execute_command, read_file` | git 全套命令 |

### R4 ✅ 触发优化器支持 CJK 分词

- `TriggerOptimizer.tokenize`（`trigger_optimizer.go`）：CJK 连续段按 **2-gram** 切分（汉字/假名/谚文），拉丁词保持按空格分词；单字不成 gram。
- `extractKeywordsFromMissedTriggers`：长度过滤从 `len(word) < 2`（字节）改为 `utf8.RuneCountInString(word) < 2`（rune），避免单字 CJK 噪声。
- 回灌匹配 `applyOptimizedTriggers` 的 `strings.Contains(input, kw)` 语义不变——2-gram 恰好是子串，中文样本现在能学到 `修复` / `登录` / `超时` 这类可复用的关键词。
- 新增回归测试 `internal/skills/trigger_optimizer_cjk_test.go`：中文 2-gram、中英混排、日文、标点剥离、单字丢弃、以及"漏触发样本不得产出整句关键词"。

### 验证

`go build ./...` ✔ ｜ `go vet`（skills/server/cmd）✔ ｜ `go test ./internal/skills/... ./internal/server/... ./cmd/...` 全绿 ✔

### 遗留（本轮有意不做）

- `debug` ↔ `debug-go` 的触发空间仍有残余重叠（Go panic 类输入两者都可能命中）——这是"通用+语言专属"成对技能的固有属性，靠 Scope 说明与描述词区分已足够；进一步合并属功能决策。
- 跨词 2-gram（如"录超"）会成为候选关键词，依赖词频（count>=2）与误触发率过滤（>30% 剔除）兜底；若后续噪声明显，可引入分词库或加最小词频阈值。

---

## 7. 追加修复：内置技能"从不被调用"的根因（2026-10-16，用户实测反馈）

### 现象

用户反馈内置技能好像从未被调用。

### 取证

| 环节 | 状态 |
|---|---|
| 内置技能加载（embed） | ✅ 正常，`loadBuiltinSkillsInto` 可工作 |
| `skill` 工具注册 | ✅ server / TUI / chat / gateway 各入口都注册了（`server.go:323`、`agent.go:122`、`chat.go:60`、`gateway.go:215`；仅 `cron.go` 未注册，属刻意最小化） |
| 技能清单注入系统提示 | ✅ 5 个注入点都调 `GetSkillsList` |
| **提示语指向** | ❌ **指向模型走不通的路径** |
| **自动路由** | ❌ **聊天链路零调用匹配器** |

两处断点：

1. **提示语死路**：`GetSkillsList` 写的是 `use /skill <name> for details`，但 `/skill` 指令只在**用户消息**里被 `expandChatDirectives` 解析（`sessions.go:882/1702` 只扫用户输入）。模型发不出用户消息——照提示把 `/skill xxx` 写进回复正文，什么都不会发生。
2. **更讽刺的**：`GetSkillsContextForPrompt`（`manager.go:1315`）是提示**正确**（`use skill tool with action=info`）的孪生实现，却**全仓零调用方**；5 个注入点全在用提示错误的 `GetSkillsList`。两份实现各自漂移，错的那份被接线。
3. 触发只能靠：用户手打 `/skill <name>`，或模型自发调 `skill` 工具——前者要人肉、后者被错误提示带偏，结果就是"内置技能从不被调用"。

### 修复

1. `GetSkillsList` 提示语改为模型可执行路径：请求匹配技能描述时，**先**用 `skill` 工具加载（`action=info` 读全文 / `action=invoke` 带用户输入执行）再回答。
2. `GetSkillsContextForPrompt` 改为委托 `GetSkillsList()`，消灭双份实现漂移（顺带修掉它条目格式里误导性的 `/` 前缀）。
3. `skill` 工具 description 补充"可用技能清单在系统提示里，匹配时先加载"——双向指引闭合。
4. 新增回归测试 `internal/skills/skills_list_hint_test.go`：断言提示不再含 `/skill <name>`、包含 `skill tool` 与 `action=info`、且两个函数同源。

### 有意不做

- **聊天链路自动注入技能**：`MatchSkillsByInput` 是"输入必须是技能元数据的子串"的反向匹配，句子级输入几乎永不命中；拿它做自动注入会随机性很强。路由决策交给模型（按 description 判断 + `skill` 工具加载）是当前 schema 下的标准做法。若未来要做自动注入，先要把匹配器改成"元数据词出现在输入中"的方向。
- `cron.go` 的 agent 未注册 `skill` 工具：定时任务是刻意最小化的环境，保持现状。

### 验证

`go build ./...` ✔ ｜ `go test ./internal/skills/... ./internal/server/... ./internal/agent/... ./internal/tool/... ./internal/bot/... ./cmd/...` 全绿 ✔

---

## 8. 追加修复：命令白名单的定位（2026-10-16，用户反馈「npx 不在命令白名单里」）

### 现象

技能执行过程中收到提示：`npx` 不在命令白名单里。追下去发现不是偶然——`skills/code-quality/SKILL.md:184-185` **正是我们自己的技能让模型跑 `npx eslint` / `npx prettier`**。

### 取证

| 事实 | 证据 |
|---|---|
| 校验只取**首个非 env 赋值 token** 查表 | `checkWhitelist`（`execute_tool.go:445-474`，表内 115 条） |
| 但执行的是**整条 shell 串** | Windows 侧 `shell -Command <原文>`（`:294`）、其它平台 `bash -c`（`:296`） |
| 表内已含完整逃逸口 | `cmd` 与 Windows 壳（`:81`）、`python`/`node`/`git`/`docker`/`make`/`awk`/`sed`/`find`/`tar` |
| 项目自己的策略层更严 | 审批层默认只放行**只读 git 子命令** `^git\s+(status\|log\|diff\|show\|branch\|remote\|stash)(\s\|$)`（`approval.go:330-334`），而工具层是裸 `"git": true` |
| 顺序决定修复位置 | 审批钩子跑在工具**之前**（`BeforeTool`），dangerous/injection 恒拦、injection 与 pipe-chain 仅非 coding 模式（`:236-273`） |

机械化比对技能 bash 代码块首命令 vs 清单，缺失 9 个：`npx`、`gofmt`、`golangci-lint`、`ruff`、`black`、`dlv`、`http`、`clinic`、`kernprof`。

**结论**：清单没"配错"，但它被放在了承担不起的位置——它是「常用命令清单」却被当成安全边界；`python`/`node`/`git` 属于清单本身自证其非边界（它们天然能执行任意代码），而"允许 `npm install`（会跑任意 postinstall 脚本）却拦住 `npx`"才是真正不自洽的一处。

### 决策（用户选定：降级为软策略）

1. **工具层软策略**：`allowedCommands` 更名语义为「标准工具链清单」，不再承担安全语义；未列出的命令**不再硬拦**，`log.Debugf` 记录后交给审批层。保留严格模式 `SetStrictWhitelist(true)`（返回可识别的 `*UnlistedCommandError`）供「只允许已知工具链」的部署使用。
2. **审批层升级**（关键，避免"软"等于"静默放行"）：`BeforeTool` 中，`execute_command` 若「仅因低风险被自动放行」（`reason == "Low risk command"`）且 base command 不在清单内，则**升级为询问**。不升级的情形：审批白名单 / 只读命令 / 受信 pattern / 学习结果（用户已明确认可），以及 `strategy=auto`（用户显式表达"都别问"）。
   - 踩过的坑：`smartApprove` 对低风险放行**也置 `Trusted: true`**，所以最初用 `Trusted` 判断"显式信任"会让升级永不触发；改为按 reason 区分自动放行与显式批准。
3. **补齐清单**：把技能自荐且缺失的命令（`npx`/`gofmt`/`golangci-lint`/`staticcheck`/`dlv`/`ruff`/`black`/`mypy`/`uv`/`poetry`/`pipenv`/`bun`/`bunx`/`deno`/`http`/`httpie`/`clinic`/`kernprof`）加入，并抽出 `BaseCommand` / `IsStandardCommand` / `StandardCommands` 供审批层复用，消灭两处各自实现取 base command 的漂移。
4. 文档：`docs/USAGE.zh-CN.md` / `en.md` 12.3 节补一段，明确"清单不承担安全职责"与软/严格两种模式。

### 有意不做

- **严格模式未接配置项**：`SetStrictWhitelist` 已就绪且被测试覆盖，但 `pkg/config` 里没有对应开关——不想为未被请求的部署形态新增配置面；需要时接一行即可。
- **不强行统一审批层只读 git 与工具层裸 `git` 的口径**：工具层清单已不含安全语义，该差异现在只影响"是否自动放行"（由审批层决定），冲突的实际危害已消除。

### 验证

`go build ./...` ✔ ｜ `go vet ./internal/tool/... ./internal/agent/...` ✔ ｜ `go test ./internal/... ./cmd/...` 全绿 ✔

新增用例：`internal/tool/execute_tool_guard_test.go`（软/严格两模式、类型化错误、`BaseCommand` 跳 env、清单覆盖技能自荐工具）、`internal/agent/approval_toolchain_test.go`（4 例：未列出→询问、标准工具链→不打扰、auto 策略→不升级、危险命令→仍硬拒）。
