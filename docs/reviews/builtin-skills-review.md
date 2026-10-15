# 内置 / 工作区技能内容审查报告

- 范围：仓库内全部技能定义文件（SKILL.md），共 **14 个 / 两组**
- 方法：逐文件精读 ＋ **代码反查取证**（加载器、解析器、命令注册表、工具注册表）
- 结论：**没有"照空想的文档"级别的严重事故**——技能里引用的斜杠命令与工具名绝大多数真实存在；但存在三类系统性问题：**(1) 两套技能体系（embed 内置 / 工作区）规范不统一且同名互相覆盖；(2) 若干处内容硬错误与渲染断裂；(3) description 缺少触发条件，导致技能难以被正确路由**。

---

## 0. 审查对象

| 组 | 位置 | 数量 | 加载方式 |
|---|---|---|---|
| **A 组：真·内置技能** | `internal/skills/builtin/` | 7 | 编译期 `go:embed`（`internal/skills/embed.go`） |
| **B 组：工作区技能** | 仓库根 `skills/` | 7 | 运行期扫描 `./skills/`（`manager.go:82-85`） |

**A 组**：`code-review`、`coding`、`daily-report`、`debug`、`file-organizer`、`summarization`、`translation`
**B 组**：`api-testing`、`chat-interrupt`、`code-quality`、`code-review`、`debug-go`、`docs-writing`、`git-workflow`

加载优先级（`docs/USAGE.zh-CN.md:785-786`，与代码一致）：`内置 < ~/.magic/skills/（全局） < ./skills/（工作区）`，**同名时后者覆盖前者**。

---

## 1. 先说结论好的部分（避免误伤）

1. **A 组声明的 `tools` 名字没有凭空编造**——`read_file` / `file_search` / `lint` / `format_code` / `analyze_error` / `suggest_fix` / `trace_execution` / `list_files` / `directory_tree` / `file_edit` / `web_fetch` / `todo` 在 `internal/tool/*.go` 里都有对应的 `Tool` 实现（如 `lint`=`lint.go:980`，`format_code`=`lint.go:1076`）。
   > ⚠️ **更正（见 §7.0 / §7.3）**：本节初版据此写了"没有编造工具名"。这个结论**只对了名字**：其中 `file_search`、`format_code`、`suggest_fix`、`trace_execution` 四个**从未被 `RegisterAll` 注册**，属于孤儿工具；技能声明它们 = 声明与实况不符。而"有实现"和"被解析"是两件事——见下条。
2. **B 组 `chat-interrupt` 声明的 `/req` 系列命令真实存在且真的会落盘**。我最初怀疑它是"照想象写的"，反查后**推翻**：`cmd/magic/tui.go:780-805` 注册了 `/req`、`/reqs`、`/req-done`、`/req-del`、`/req-priority`，实现走 `tool.GetTodoTool()`，落盘路径 `~/.magic/todos/todos.json`（`internal/tool/todo_tool.go:136-140`），优先级 high/medium/low 也与 `validPriorities` 一致。技能里的持久化说明是**准确的**。
3. A 组 frontmatter **写法**用的是扁平 `tags:` / `tools:`，这正是内置加载器 `loadSkillFromContent` 的解析口径（`manager.go:651-664`），格式上没有问题。
   > ⚠️ **更正（见 §7.0 的 R1/R2）**：本节初版据此写了"A 组字段能被正确解析"，**这是错的**。实测两条链都断了——`ParseYAMLFrontmatter` 方向反了（把正文当 frontmatter），且 `loadSkillFromContent` 用 `[]interface{}` 断言而 `parseSimpleYAML` 返回 `[]string`。结论：**7 个内置技能的 tags/tools 从未被真正解析过**，`MissingTools` 校验接线后才第一次暴露出来。

---

## 2. P1 级问题

### P1-1　B 组 `code-review` 与 A 组 `code-review` 同名 → 静默覆盖 + 来源标注失真

- A 组内置 `code-review`（85 行）与 B 组工作区 `code-review`（237 行）**同名**。
- `loadSkillsInto` 在扫描到同名技能时**直接覆盖**，但把 `Source` 保留为 `builtin`：

  ```go
  // internal/skills/manager.go:404-406
  if existingSkill, exists := target[skill.Name]; exists {
      if existingSkill.Source == "builtin" {
          skill.Source = existingSkill.Source      // ← 本地内容被贴上 builtin 标签
      }
  }
  target[skill.Name] = skill                        // ← 内容已被本地文件替换
  ```
- 后果：`magic skills list --source builtin`、技能详情页会把**本地文件**当成"内置技能"展示。运维/用户无法区分"这是打包基线"还是"有人本地改过"。覆盖本身符合文档语义（同名后者优先），**失真的是来源标注**。

### P1-2　A 组的 `tools` 字段是纯装饰，没有任何运行时效果

- `ManagerConfig.ToolNames` 注释写着"可用工具名称列表（**用于技能验证**）"（`manager.go:68`），但**全仓没有任何消费方**（grep 确认只剩该注释与无关的 agent/toolset 命中）。
- `Skill.GetTools()` 唯一调用方是 `manager.go:1185`（`magic skills show` 的展示用）。
- 即：技能声明 `tools: [lint, format_code]` **既不做权限收敛，也不做存在性校验**。声明错的工具名不会被发现，声明对的也不会带来任何约束。→ 要么接线（作为技能可用性的前置校验/门控），要么从文档和技能模板里去掉，以免读者以为它有约束力。

### P1-3　两套 frontmatter 规范并存，B 组字段大半是死的

| 字段 | A 组 | B 组 | 加载器实际读取 |
|---|---|---|---|
| `tags` | 顶层扁平 ✅ | 写在 `metadata.hermes.tags` | 顶层 `tags`（`manager.go:651`）；B 组靠 `GetTags()` 兜底才读到 |
| `tools` | 顶层扁平 ✅ | **未声明** | 顶层 `tools` |
| `category` | 无 | 全部写 `category: software-development` ❌ | **无人读取** |

- `category` 在 commit `c2f9fbc1 Remove skill category feature and simplify skill system` 里已被移除，B 组 7 个技能**仍全部携带该字段** → 死字段，误导后续维护者以为还有分类能力。
- 一个仓库里同时存在 `tags:`（扁平）与 `metadata.hermes.tags`（嵌套）两种风格，格式判定也会分叉（前者 `FormatMagic`、后者 `FormatHermes`，见 `parser/common.go:236-257`）。建议统一为一种。

---

## 3. P2 级问题

### P2-1　4 处内容硬错误（会实际误导模型 / 渲染断裂）

| # | 文件:行 | 问题 |
|---|---|---|
| 1 | `skills/code-quality/SKILL.md:64-71` | 被标为 "Good" 的 Go 示例**不可编译且逻辑错误**：`httpClient` 未定义；先 `defer response.Body.Close()` 又 `return response.Body.Close()`（重复关闭、且返回的是 Close 的错误而非处理结果）。作为"正面范例"出现在质量规范里，危害大于收益。 |
| 2 | `skills/debug-go/SKILL.md:127-137` | 「Deadlock Detection」一节给的是 `debug.SetGCPercent(-1)`——**GC 与死锁检测无关**，属错误知识。真正的工具是 `go test -race`、`goleak`（同文件别处已提到），此处应删或改。 |
| 3 | `skills/docs-writing/SKILL.md:206-224` | 「Markdown Enhancements」把 ` ```mermaid ` 围栏嵌在 ` ```bash ` 围栏内，**围栏嵌套非法**，渲染必错乱。 |
| 4 | `skills/code-review/SKILL.md:128-136` | Comment Templates 在 ` ``` ` 块内再嵌 ` ```[language] `，同类围栏错误，模板复制出去会破格式。 |

### P2-2　A 组技能之间功能重叠、粒度混乱

- `coding`（能力 1 = Code Review）与 `code-review`：Review Checklist、Comment Format 逐条重复，**同一能力两份技能**。
- `debug`（Error Analysis / 性能指标）又与 `coding` 的 Correctness/Performance 检查项重叠。
- 7 个内置技能里至少 2 个在做同一件事，直接挤占技能索引与触发匹配预算（触发匹配依赖 description，见下条）。

### P2-3　A 组内 `daily-report` / `summarization` / `translation` 是"充数技能"

- 三者都是纯模板/通识（日报模板、摘要结构、翻译流程），**没有任何与本运行时的真实对接**（会话历史、`todo` 工具、`web_fetch` 只在末尾一句带过）。模型自身即可完成这类任务，技能边际价值接近零，主要作用是稀释技能索引信噪比。建议要么补上真实的 magic 集成（例如 daily-report 明确"从 `todo` 工具读当日任务 + 从 session 读对话"），要么移出内置集。

### P2-4　技能 `description` 普遍缺少触发条件（全 14 个）

- A 组清一色 `"…best practices"` / `"…assistance"`，B 组清一色 `"…guidelines"`，**没有一个**写 "Use when the user asks to …"。
- 这与前两轮报告里"触发优化器学不到关键词（`cond.Keywords` 恒空）"的问题叠加：技能本来就靠 description 参与路由，描述越泛，越只能靠模型盲猜，技能越难被正确加载。

### P2-5　B 组 `chat-interrupt` 的作用域与其命令可用域不匹配

- `/req*` 只在 **TUI**（`cmd/magic/tui.go`）实现；而 `./skills/` 是**工作区技能，web / bot / TUI 三种会话都会加载**。
- 技能正文没有任何"仅限 TUI"的说明，web 用户按文档敲 `/req` 会落空（web 侧只有 `/skill`、`/mcp` 两类 directive，`internal/server/chat_directives.go:66-80`）。
- 另：`chat-interrupt/README.md` 与同目录 `SKILL.md` **逐字节重复**，只是多了一层没意义的副本。

### P2-6　B 组内容与项目语境脱节

- `api-testing` 大篇幅列举 Postman/Insomnia/Bruno/xh 等 GUI/CLI 工具表，与本项目（Go 后端 + 自研 agent）无对应关系。
- A 组 `debug` 的 "Common Error Patterns" 全是 JS/Python 报错模板（`Cannot read property 'X' of undefined`），对 Go 项目帮助有限。
- A 组 `file-organizer` 的 Naming Conventions 表把"Files=kebab-case / Functions=snake_case"当通则，与 `coding` 里给 Go/Python 的建议（PEP8、gofmt 惯用驼峰）**互相矛盾**。

---

## 4. 问题清单（按严重度汇总）

| 级别 | 编号 | 问题 | 位置 |
|---|---|---|---|
| P1 | P1-1 | 同名 `code-review` 覆盖内置 + `Source` 仍标 `builtin` | `manager.go:404-406` + `skills/code-review/` |
| P1 | P1-2 | `tools` 字段无任何消费方，纯装饰；`ToolNames` 是死字段 | `manager.go:68`、`types.go:57` |
| P1 | P1-3 | 两套 frontmatter 规范并存；`category` 是死字段 ×7 | B 组全部 7 个 SKILL.md |
| P2 | P2-1 | 4 处内容硬错误（Go 示例不可编译 / GC 当死锁检测 / 两处围栏断裂） | 见上表 |
| P2 | P2-2 | A 组 `coding` 与 `code-review` 功能重复 | A 组 |
| P2 | P2-3 | A 组 3 个纯模板技能边际价值低 | A 组 |
| P2 | P2-4 | 14 个技能 description 均无触发条件 | 全部 |
| P2 | P2-5 | `chat-interrupt` 全局可见但命令仅 TUI 可用；README 与 SKILL 重复 | B 组 |
| P2 | P2-6 | 部分内容与项目语境脱节 / 技能间口径矛盾 | A/B 组 |

---

## 5. 建议修复顺序

| 顺序 | 动作 | 影响面 | 风险 |
|---|---|---|---|
| 1 | 修 4 处内容硬错误（P2-1），尤其 `code-quality` 的 Go 示例与 `debug-go` 的 GC 段 | 防止把错误知识喂给模型 | 低 |
| 2 | 统一 B 组 frontmatter：删 `category`，`metadata.hermes.tags` → 顶层 `tags`；或直接统一为 A 组扁平风格 | 字段真正生效、规范一致 | 低 |
| 3 | 处置 `code-review` 重名：B 组改名（如 `code-review-strict`）或删其一；即便保留也修 `Source` 标注逻辑 | 消灭"假的 builtin" | 低 |
| 4 | 决策 `tools` 字段：接线为技能能力校验/门控，或从技能模板与文档中移除 | 消歧 | 低-中 |
| 5 | 给全部技能 description 补 "Use when the user asks to …" 触发条件 | 提升路由命中率 | 低 |
| 6 | 合并/裁剪 A 组重叠与充数技能（`coding`+`code-review`、`daily-report`/`summarization`/`translation`） | 提升技能索引信噪比 | 中 |
| 7 | `chat-interrupt` 加 TUI 适用范围说明、删重复 README；`api-testing` 等补项目语境或移出 | 去除误导 | 低 |

---

## 6. 一句话结论

> 这一次**没有发现"文档凭空捏造能力"的 P0**（`/req` 与 `tools` 经反查都真实存在），真正的问题在**体系一致性**：`code-review` 同名互相覆盖却仍标 `builtin`、`tools`/`category`/`ToolNames` 三处字段是死的、4 处内容硬错误、以及 14 个技能全部缺少触发条件。优先修内容硬错误与 frontmatter 统一，其次解决重名覆盖，最后做技能集的合并裁剪。

---

## 7. 修复记录（本轮已全部落地）

验证：`go build ./...` ✔ ｜ `go vet ./internal/skills/... ./internal/server/... ./cmd/magic/...` ✔ ｜ `go test ./internal/skills/... ./internal/server/...` **全绿** ✔

### 7.0 修复过程中挖出的三个更深层根因（报告初版未覆盖）

修 P1-2 时顺链反查，发现"`tools` 字段没有消费方"只是表症，底下还有三个更根本的缺陷：

| # | 根因 | 证据 | 影响 |
|---|---|---|---|
| R1 | **`parser.ParseYAMLFrontmatter` 方向反了** | 探针实测：`fm={}`、`rest="name: demo\ndescription: hello\ntags: [a, b]"` —— 它把**正文**当 frontmatter 返回、把 **frontmatter** 当正文返回 | `DetectFormat` 恒判 `FormatMagic`；内置技能 description 恒为默认值；`GetSkillContent`（`manager.go:1233`）把元数据当正文返回给模型 |
| R2 | **`loadSkillFromContent` 用 `[]interface{}` 断言** | `parseSimpleYAML` 对内联数组返回 `[]string`，断言恒 false | 内置技能的 `tags`/`tools` **从来没被解析出来过**（即使 R1 修好也一样） |
| R3 | **`parser.Parse` 不支持 `FormatMagic`** | `default: return nil, fmt.Errorf("unsupported format: %s", format)` | `FormatMagic` 这个检测结果形同虚设；上层退回简单解析路径，`version`/`author`/`license` 被静默丢弃 |

三处修复：

- `parser/common.go`：重写 `ParseYAMLFrontmatter`——开头 `---` 之后第一条去行尾空白的 `---` 为闭合符，返回 `(frontmatter, body)`，与全部 5 个调用方的既有契约一致；未闭合时仍按旧行为整体视为正文。
- `parser/parser.go`：新增 `case FormatMagic`，解析扁平字段（name/description/version/author/license/tags/tools），不再走 `default` 报错。
- `manager.go`：`loadSkillFromContent` 改用 `extractStringSlice`（同时处理 `[]string` 与 `[]interface{}`）。

**R1 的额外收获**：`FormatMagic` 能解析后，`HermesParser`/`OpenClawParser`（`cortex_parser.go` / `openclaw_parser.go`）也终于拿到正确的 frontmatter 映射——它们此前一直收到**正文**，等于从未真正解析过元数据。

### 7.1 逐条对照

| 编号 | 问题 | 处理 |
|---|---|---|
| P1-1 | 同名 `code-review` 覆盖内置 + `Source` 仍标 `builtin` | ① 新增 `applySkillOrigin`：来源一律由目录决定（`registry` 安装记录除外），**不再继承 builtin 标签**；覆盖内置时 `log.Warnf` + 写 `Metadata["shadows_builtin"]`。② 删除工作区重复的 `skills/code-review/`（内容已并入内置版，见下），彻底消除重名。 |
| P1-2 | `tools` 字段无消费方；`ToolNames` 是死字段 | 接线为**加载期校验**：`Manager.toolNames` + `validateSkillTools()`（`NewManagerWithConfig` 与 `Reload` 后各跑一次），未注册的工具记入新增字段 `Skill.MissingTools` 并告警；`server.go` 以 `registry.List()` 注入；`magic skills show` 新增 `Unregistered:` 行暴露偏差。 |
| P1-3 | 两套 frontmatter 规范并存；`category` 死字段 | 工作区 6 个技能全部改为扁平 `tags`，删除 `category` 与 `metadata.hermes`；统一为一种规范（= 内置技能风格）。 |
| P2-1 | 4 处内容硬错误 | ① `code-quality` 的 Go 示例改为可编译且正确释放 body 的版本；② `debug-go` 的把 `SetGCPercent(-1)` 当死锁检测 → 改为真实手段（`SIGQUIT`/`GOTRACEBACK=all`/`runtime.Stack`/goroutine profile/`goleak`）并显式注明该 API 与死锁无关；③ `docs-writing` 与 ④ `code-review` 的嵌套围栏改为四反引号外层。 |
| P2-2 | `coding` 与 `code-review` 功能重复 | 内置 `code-review` 升级为完整版（吸收工作区版内容，v2.0.0）；`coding` 剥离重复的 Review Checklist/Comment Format，改为一句"评审约定见 code-review 技能"。 |
| P2-3 | `daily-report`/`summarization`/`translation` 是纯模板 | 三者补上真实集成：`daily-report` 明确从 `todo`(action=list) + `session_search` 取数、无数据时写 "no data" 且**禁止编造**；`summarization`/`translation` 补 `read_file`、内容获取路径与校验清单。 |
| P2-4 | 14 个技能 description 均无触发条件 | 全部 13 个技能（删 1 个）重写 description 为 "Use when …" 触发式描述。 |
| P2-5 | `chat-interrupt` 全局可见但命令仅 TUI 可用；README 重复 | 新增「适用范围」章节明确 **仅 TUI**、Web/机器人会话不支持并给出替代话术；删掉不存在的 `/continue`；`/interrupt`、`Esc` 补全；删除逐字节重复的 `README.md`。 |
| P2-6 | 内容与项目语境脱节 / 口径矛盾 | `api-testing` 新增「In This Repo (Go)」小节（`httptest` + `go test -race`）与本地代理陷阱说明；`file-organizer` 命名表改为**按语言**区分（不再断言"文件一律 kebab-case"）并加防误删安全规则；内置 `debug` 的错误模式补 Go 语境。 |

### 7.2 新增回归测试

| 文件 | 用例 |
|---|---|
| `internal/skills/parser/frontmatter_test.go` | `TestParseYAMLFrontmatterOrientation`（R1 方向）、`...NoFrontmatter`、`...Unterminated`、`TestParseFormatMagicFlatFrontmatter`（R3） |
| `internal/skills/origin_tools_test.go` | `TestShadowedBuiltinSkillIsNotLabeledBuiltin`（P1-1）、`TestGlobalDirSkillIsLabeledGlobal`、`TestValidateSkillToolsFlagsUnregistered`（P1-2）、`TestValidateSkillToolsSkippedWithoutToolNames`、`TestBuiltinSkillFrontmatterIsParsed`（R2） |

### 7.3 顺带发现，本轮**有意未改**

- **4 个"孤儿工具"**：`file_search`（`file_search_tool.go`）、`format_code`（`lint.go`）、`suggest_fix` / `trace_execution`（`debug_tools.go`）**定义了但从未注册**（`RegisterAll` 里没有）。技能此前声明它们，正是"声明与实况不符"；本轮已把技能声明改成实际注册的等价工具（`file_search` → `search_in_files`，其余移除），但**没有**擅自注册它们。
  注册会改变 agent 的工具 schema 与行为，属于功能决策，建议单独定夺：`file_search` 与已注册的 `search_in_files` 功能重复，倾向删除；`format_code` 与已注册的 `lint` 成对，倾向注册。
- `parseSimpleYAML` 会把嵌套键**压平**到顶层（`metadata:` → `hermes:` → `tags:` 最终变成顶层 `tags`）。本轮不动它，以免影响 Hub 安装的第三方技能；但它意味着 `DetectFormat` 的嵌套 `metadata.hermes` 判据实际上永远不会命中。

