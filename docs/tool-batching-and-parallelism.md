# 工具批量与并行：现状、实测与调研

> 一句话：**执行能力早就有了，缺的是引导和度量。** `bash -c` 本来就能跑 `a && b`，八个非 bash 工具本来就声明了 `Parallel`——但实测下来模型平均每轮只发 **1.1** 个工具调用，也就是并行通道基本没被用上。所以第一步是引导 + 度量，不是改执行器。

调研日期：2026-08-13。代码基线：`797f551`（`checkpoint-retention`）。

---

## 1. 结论

| # | 结论 | 依据 |
|---|---|---|
| 0 | **最重要的一条转向：批量分两种。** 「模型侧批量」（一轮多个 `tool_use`）框架只能引导；「工具侧批量」（一次调用多个目标）框架说得上话。实测 1.10 调用/轮说明前者基本没发生，所以后者才是主路 | §5.C2 |
| 1 | **不缺执行能力。** `ExecuteStreaming` 把命令原串交给 `bash -c`，`&&` / `;` / 管道全都能跑，不需要新增任何东西 | `tools/bash.go` |
| 2 | **不缺并行声明。** 九个工具里八个是 `Parallel`，只有 `bash` 是 `Sequential` | `tools/*.go` 的 `ExecutionMode()` |
| 3 | **缺引导 → 已补。** 循环级的（一次调用一次往返、只合并独立的活）进 system prompt；工具级的（`cd` 不持久、`paths` 怎么用）进各自的 Description。合计 +96 token/轮，有天花板测试守着 | `agent/prompt.go`、`Bash.Description()`、§5.A |
| 4 | **缺度量 → 已补。** `analyze` 现在报 `AverageCalls`、`Combos`（工具组合）、`Stalled`（被 sequential 兄弟拖成串行的可并行调用数）。实测平均 **1.10** 调用/轮 | `analyze/analyze.go`、§3 |
| 5 | **批次规则偏保守 → 已改。** 原来一批里有一个 `Sequential` 就整批串行；现在按连续同模式**分段**，sequential 独占一段、parallel 照旧并发 | `agent/loop.go` 的 `segments`、§5.B |
| 6 | **`&&` 不免费。** 尾部截断、exit 归因、审批粒度、短路语义四条代价，其中截断那条和最典型的 `build && test` 场景直接冲突 | §6 |
| 7 | wire 层形状**已经是对的**，不用改：一轮的全部 `tool_result` 走同一条 neutral message，转成 N 条 `role:"tool"` 紧跟 assistant 消息。这正是 Anthropic 点名「做错就会教会模型不再并行」的那件事 | `llm/convert.go`、§4.1 |
| 8 | **`workdir` → 已做**，形状抄 Codex。附带两条当初没算到的账：它必须过 subagent 守卫（否则是一个越界出口），必须进「总是允许」的键（否则悄悄放宽了授权范围） | §5.C、`Guard.CheckDir`、`web.grantKeyOf` |
| 9 | **C2 的展示降级 → 已补。** 多文件读有自己的组件了。内容按**工具记录的字节区间**切，不解析拼接文本——后者会把一个文件的片段挂到另一个文件名下 | §5.C2、`ReadFileDetails.BodyOffset` |
| 10 | **`grep` / `find` 的 `paths[]` → 否决。** 两家第一方都不做这件事：Claude Code 的 Grep / Glob 都是单 `path`，作用域靠 `glob` / `type` 收窄（pi-go 的 `include` 已经是这个），Read 也是单文件。没有先例又没有实测需求，不加 | §4.3 |

---

## 2. 现状（代码事实）

### 2.1 bash

| 事实 | 位置 | 对批量的含义 |
|---|---|---|
| `bashArgs{Command string, Timeout float64}` | `tools/bash.go` | 一次一条命令串。无数组、无 `workdir` |
| 每次都是新的 `exec.CommandContext(ctx,"bash","-c",cmd)`，`cmd.Dir = t.Cwd` | 同上 | **无持久 session**：`cd`、env、venv 全不跨调用。目录相关操作只能靠 `cd x && ...` |
| `ExecutionMode() = Sequential` | 同上 | 一批里有它 → 整批串行 |
| `TruncateTail` + `MaxLines=2000` / `MaxBytes=50KB`，超限 spill 到临时文件 | `tools/truncate.go` | 链式命令**共享一份尾部预算**，先跑的输出先被切 |
| `BashDetails{Command, ExitCode, DurationMS, ...}` 全是单值 | `tools/result.go` | `a && b` 失败时拿不到「第几条挂的」 |
| `Guard.shellWords` 已按 `; \| & ( ) \`` 切词 | `tools/guard.go` | 链式**不会**绕过 subagent 的 git 守卫。这条是好的 |

### 2.2 其余八个工具：全部已经是 Parallel

| 工具 | 模式 | 安全性靠什么 | 我的复核 |
|---|---|---|---|
| `read` / `ls` / `find` / `grep` | Parallel | 纯只读 I/O | 成立 |
| `write` / `edit` | Parallel | `withFileLock(canonical(path))` 逐路径串行 | 成立**但只覆盖写方**，见 §7.1 |
| `todo` | Parallel | 不碰任何东西，权威列表就是最新的 tool_result | 成立 |
| `subagent` | Parallel | worktree 隔离，子进程各写各的 | 成立 |

所以「read 等能不能并行」的答案是：**已经能了，而且从代码上看是对的。** 真问题在下一节——它没被用起来。

### 2.3 批次规则与并发上限

```go
// agent/loop.go
func (a *Agent) parallelBatch(calls []llm.Block) bool {
    if len(calls) < 2 || a.toolExecution == tools.Sequential { return false }
    for _, c := range calls {
        if t, ok := a.registry.Get(c.Name); !ok || t.ExecutionMode() == tools.Sequential {
            return false   // ← 一个串行，全批串行
        }
    }
    return true
}
```

`DefaultToolConcurrency = 8`；并行批次先**串行 review 完整批**，再并行 execute（`runBatchParallel`）。这个两阶段设计本身是对的，理由见 `harness-design.md` §工具执行模式。

### 2.4 wire 层

- `llm/convert.go`：一条 neutral user message 里的 N 个 `BlockToolResult` → N 条 `role:"tool"`（带 `tool_call_id`），紧跟在请求它们的 assistant 消息之后。**顺序和聚合都正确。**
- `llm/wire.go` 的 `wireRequest` **没有 `parallel_tool_calls` 字段**。OpenAI 侧该参数默认为 true，所以不发通常没问题；但 pi-go 的目标端点里有 GLM / Kimi 这类兼容实现（见 `wireChunk` 里的 `reasoning_content` 注释），它们的默认值**未经验证**。列为待查项，不是待改项。

---

## 3. 实测

**样本：`session-check.jsonl`（2026-08-12，45KB，一个 malquery 容器内的排查任务）。这是本机能找到的唯一一份 transcript。**

方法说明：`-analyze-session` 已经能算这些数（`analyze.BatchDistribution`），但**当前分支在 windows/amd64 上编译不过**（`worktree/worktree.go` 的 `alive()` 直接调 `syscall.Kill`，没有平台构建标签），所以这次是照同样口径手算的：逐行取 `type=message` 且 `role=assistant` 的记录，数其中 `tool_use` 块。

```
turns with tool calls   : 10
total tool calls        : 11
average calls per turn  : 1.1

batch size distribution : size 1 × 9 turns,  size 2 × 1 turn
batch composition       : bash × 8,  read+bash × 1,  read × 1
calls by tool           : bash 9,  read 2

multi-call batches      : 1     （唯一那一个是 read+bash）
  contains bash         : 1     （即 100%）
  被白等的并行调用      : 1     （那个 read）

bash commands           : 9
  含 && / ; / ||        : 2
  以 cd 开头            : 1
```

读出来的五件事：

1. **模型几乎不批量。** 10 轮里 9 轮是单调用。Anthropic 给的自查指标是「平均每条消息的工具数 > 1.0 说明并行生效」——1.1 是刚过线，实质等于没生效。
2. **唯一的多调用批次正好撞上 bash**，那个 `read` 被 §2.3 的规则拖成串行。样本小，但比例是 1/1。
3. **模型在意图明显时会自己链式**：`ls -la /data/malquery.db 2>/dev/null; echo "---"; ls -la /data/ | head -30` 是它自己拼的。2/9。这说明引导的边际成本很低——它有这个习惯，只是没被鼓励。
4. **`cd` 那条正是 `workdir` 要解决的**：`cd /data/samples && for f in *; do ...` ——只因为每次 `bash -c` 都是新进程、`cd` 不持久，才必须把 `cd` 拼进命令，而 `Guard.absoluteCD` 还得专门去解析它。
5. **一次真实的重试**：`mal-decompile ...` 失败后改成绝对路径 `/opt/skills/.../mal-decompile ...` 重跑。这类「先试短的、失败再试全的」序列**不该**被鼓励合并成 `&&` 链——它本质上依赖前一条的结果，正好是 Anthropic 那句「只批量彼此独立的调用」要排除的情况。

> **样本量声明：11 个调用、一份会话。** 「模型不批量」这个结论在 n=11 上已经足够——9/10 单调用不是噪声。但**任何关于改动收益的百分比都还没有依据**，需要至少 5~10 份真实会话才能说话。§8 是这个意思。

---

## 4. 官方资料

### 4.1 Anthropic

- [Bash tool 文档](https://docs.claude.com/en/docs/agents-and-tools/tool-use/bash-tool)
  - 官方 bash tool 被定义为**持久 session**（`cd /tmp` 之后下一次调用还在 `/tmp`），并给 `restart` 参数。**pi-go 与此相反**，而这个差别模型不可能自己发现。
  - Common patterns 一节给的示例本身就是链式的：`npm install && npm run build`、`pytest && coverage report`、`git status && git add . && git commit`。
  - 命令校验只是「绊线」，不是执行边界；真正的控制是容器 / VM 隔离。文档自己承认示例校验器抓不到贴住词的 `data.txt|grep x`。
  - 输出限制由 harness 负责——API 不截断，超大请求直接被拒。
- [Parallel tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use)
  - 一个 assistant turn 可以带多个 `tool_use`，**执行策略由 harness 决定**：独立只读操作适合并行，有副作用 / 共享状态 / 有顺序要求的适合串行。这正是 `ExecutionMode` 的形状。
  - 两条硬性格式要求：全部 `tool_result` 放在**同一条 user message**，且**不能有 text 在前**，否则会「教会」模型不再并行。→ pi-go 合规（§2.4）。
  - 没跑的调用也要回 `tool_result` 并置 `is_error: true`，附一句人话原因。→ pi-go 的 `settledCall` 就是这个。
  - 自查指标：平均每条消息的工具数 > 1.0。
  - 可直接抄的一句 system prompt：只批量彼此独立的调用。
- [Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents)
  - 工具应当**合并**那些常被连续调用的多步操作到一次调用里。
  - eval 里要统计 tool call 数、token、时长、错误率——用它们发现值得合并的工作流。
  - 截断时要用文字引导模型走更省 token 的策略；错误信息要可操作，而不是给个错误码。
  - **工具描述的 prompt engineering 是最有效的手段之一**，小改动也可能有大提升。

### 4.2 OpenAI

- [Shell tool 文档](https://developers.openai.com/api/docs/guides/tools-shell)
  - `shell_call.action` 是 `{commands: [...], timeout_ms, max_output_length}`——**commands 是数组**；`shell_call_output.output` 也是数组，**每条命令有自己的 stdout / stderr / outcome**（`exit` 带 exit_code，或 `timeout`）。这是「批量执行」的第一方设计，比 `&&` 多给了逐条归因。
- Codex CLI 的 shell schema（[Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/)）
  - `command` 是 argv 数组，另有 **`workdir`** 和 `timeout_ms`。
  - 成本论证：每轮都要重发全部会话历史，请求量对轮数是**二次**的，只有 prefix caching 命中才拉回线性；每多一次工具往返 = 一次完整推理。**这是「减少往返」的量化理由。**
  - 会破坏 prefix caching 的操作里有一条值得记：**会话中途改 `tools` 列表**。所以任何「动态增删工具」的批量方案都要先算这笔账。
- [GPT-5.2 prompting guide](https://cookbook.openai.com/examples/gpt-5/gpt-5-2_prompting_guide)
  - 明确建议在 prompt 里写：**并行化彼此独立的读操作以降低延迟**；扫描代码库 / 多实体操作时显式要求并行。
  - 写操作之后要求复述「改了什么、在哪」。
- [Codex system prompt](https://github.com/openai/codex/blob/main/codex-rs/core/gpt_5_codex_prompt.md)
  - 搜索一律用 `rg`；单文件编辑用 `apply_patch`，但**跨文件批量替换这类「脚本更高效」的场景明确让模型走 shell**。

### 4.3 两家的共识与分歧

| 议题 | Anthropic | OpenAI | pi-go |
|---|---|---|---|
| 一轮多调用 | 默认开，鼓励，给自查指标 | 明确写进 prompt 模板 | 支持，**但不引导** |
| 谁决定并行/串行 | harness，按工具性质 | harness | 按工具性质（`ExecutionMode`） |
| shell 批量形状 | 一条命令串 + 文档里用 `&&` | **commands 数组 + 逐条 outcome** | 一条命令串 |
| shell 是否持久 | **是**（session + restart） | 否（每次新进程，给 workdir） | 否，**且不给 workdir** |
| 命令校验 | 绊线，不是边界；真控制靠隔离 | 同（沙箱） | 同（`Guard` 注释里已经这么写了） |

> 唯一的实质分歧是 shell 形状。Anthropic 靠持久 session 让 `cd` 自然生效，OpenAI 靠 `workdir` 参数。**pi-go 两个都没有**，这就是实测里那条 `cd ... && ...` 的来源。

---

## 5. 可选项（按性价比排序）

### D 的否决：`bash` 的 `commands[]` **不做**，理由是审批

`read` 的 `paths[]` 之所以便宜，有一个当时没说透的前提：**`read` 根本不过闸门**（`policy.go` 的 `reviewsLocked` 对 read/ls/find/grep 返回 false）。`bash` 过。而闸门是**按工具调用**问的——`agent.review()` 在 `runToolCall` 之前，工具本身拿不到闸门句柄。

所以 `commands[]` 有一条不可约的冲突：**一次调用只能问一次，N 条命令就是一次全批准**。而「看过第一条命令的输出再批准第二条」是设计文档里写明的决定，也是 §5.B 分段刻意保住的那条。想在一次调用里逐条审批，得让工具回调闸门——那是一条新的管线，还要重做流式与 UI 的「一次调用一个结果」模型。

另外两笔账：

- `web/policy.go` 的 `AllowCommand` 是**精确字符串**匹配。数组化之后「总是允许 `go build ./...`」永不命中 `["go build ./...", "go test ./..."]`。可以改成「数组里每条都被单独授权过才算命中」，但这是在给一个还没证明自己的功能加语义。
- `BashDetails` 是单值（一个 `command`、一个 `exit_code`），而 `BashResult.vue` 是**最常被渲染的**结果组件。read 那次能靠 `CodeBlock` 兜底降级，bash 这次降级的是主路。

**它换来的是 §6.1 和 §6.2（exit 归因、输出预算）**，而这两条只在长链上疼——实测模型写的链是 2/9 且都很短。**代价确定、收益要等**，所以否决。等 `Stalled` 或链长在真实会话里涨起来再谈。

> OpenAI 的 shell tool 是 `commands[]` + 逐条 `outcome`，形状确实更好——但它**没有审批闸门**这个约束（沙箱代替了审批）。抄形状不能只抄形状。

### A. 补引导：system prompt + 工具描述 —— ✅ 已做

前面所有基础工作做完之后，这是把它们**接通**的那个开关：§7.1 让并行 read 不会读到撕裂，§5.B 让更大的批次真的重叠，§5.C2 让多文件读变成一次调用——但没有任何一句话告诉过模型这些存在。

**分工按「这句话归谁」定，不按方便定：**

| 说什么 | 放哪 | 为什么 |
|---|---|---|
| 一次调用是一次往返；独立的活合并成一次（多文件用 `read` 的 `paths`，独立调用放同一条消息） | **system prompt** | 这是关于**循环**的，没有任何单个工具拥有它。一个 session 可以九个工具齐全，却没有任何东西告诉模型一轮可以带多个调用 |
| **依赖别人输出的调用不能批量** | system prompt，同一段 | 「全都批量」是比不说更糟的建议：参数来自上一条输出的调用一批量，就是花一轮生产错参数 |
| `cd` 和环境变量**不跨调用持久**，所以同目录的活链进一条命令 | **`bash` 的 Description** | 这是 pi-go 特有的事实，而且模型的先验大概率**相反**——Anthropic 官方 bash tool 是持久 session，`cd /tmp` 之后下一次调用还在 `/tmp`。按错的先验行动的结果是在错目录里跑出一个看起来合理的答案，不是报错 |
| 多文件读怎么用 | `read` 的 Description | 同理，归工具 |

**这条分工不是风格问题**：system prompt 无论工具在不在都会发送，而只读 subagent 没有 bash——告诉它一个它碰不到的 shell 既是假话，又是每轮都付的税。这也是 `todo` 工具的描述一直遵守的规则（`SystemPrompt` 的注释里写明了）。

**报价**：system prompt 从 ~930 → **1156 字节**（+226），`bash` 的 schema 从 562 → **722 字节**（+160）。合计约 **+96 token/轮**。三句话是预算上限，靠 `TestSystemPromptStaysSmall` 的 1400 字节天花板守着——**天花板刻意贴近现值**，留了余量的天花板等于没人会注意它被推高。

另外两个测试守的是这次最容易退化的两件事：`TestSystemPromptSaysWhatCannotBeBatched`（两半话必须都在，只说「合并」就变成「全都批量」）和 `TestSystemPromptLeavesToolSpecificsToTools`（prompt 里出现 `cd ` / `&&` / `timeout` 就失败——防止下一次有人图方便把工具的话写进全局）。

**为什么它排在最后而不是最前。** 我第一版报告把 A 列为「性价比最高」，那是错的：它的收益是**求来的**——只能请模型多发调用，然后祈祈祷。基础没铺好的时候求也没用（批次会被整批串行吃掉、并行 read 有撕裂风险、多文件读根本不存在）。现在基础铺完了，这句话才有东西可指。

**验收看 §8 的指标**，特别是 `Average per tool-calling turn` 能否从 1.10 明显抬起来。这是唯一能单独归因的改动——它不动执行器。

### B. 混批不再整批串行 —— ✅ 已做（`agent.segments`）

批次按**连续同模式**切段：sequential 调用独占一段，parallel 调用照旧成段并发。详细论证记在 `harness-design.md` 的执行模式一节，这里只留三条当时算错的账：

1. **我写过「实测中 1/1 的多调用批次会直接受益」，这是错的。** 安全的形式只能是分段，而分段对 `read+bash` 这种两调用批次是「read 一步 + bash 一步」，和改动前完全一样。**收益只在「一批里有 ≥2 个可并行调用 + 至少 1 个 sequential」时出现，而这种批次在实测样本里一次都没有。** 所以 B 落地时的**即时收益是零**，它买的是「等模型开始批量之后，收益不会被这条规则吃掉」。
2. **我写过这是「真取舍」，也不对。** 我以为要放弃「read 不与重写该文件的东西同批」这条保护，实际上分段之后 sequential 调用仍然独占一段，承诺一个字没变——那句承诺的内容从来是「这个调用独自运行」，不是「它的兄弟一个一个来」。
3. **我以为闸门要改，不用。** 两种派发形状原样保留（并行段整段 review 后并发执行；串行段 review-then-run 交替），所以「看过第一条命令输出再批准第二条」对 bash 依然成立。**曾经考虑过的「直接整批审批」没有采纳**：bash 的第二条命令该不该放行，经常取决于第一条的输出，那是拿一次点击换一个错答案。

### C. `workdir` 参数 —— ✅ 已做

Codex 的 shell 有 `workdir`（甚至是 `required`，那是 strict schema 的后果不是判断）。Anthropic 走的是另一条路：**让 cwd 持久**，跑出项目目录就重置并在结果里追加一行说明。pi-go 的 bash 每次都是新进程，所以跟 Codex。

**上一版这一节写错了两件事，记在这里。**

**一、「可以少一条 `Guard.absoluteCD` 的字符串解析路径」是错的。** 模型仍然可以在命令串里写 `cd`，那条解析删不掉。真实情况反过来：`workdir` **多**了一道检查。守卫拦 `cd` 靠的是从命令文本里解析绝对路径目标，而 `workdir` 到达同一个地方却从不出现在那段文本里——不补检查，加这个参数就是给子代理开了一个「长得像普通参数」的越界出口。所以有了 `Guard.CheckDir`，测试直接跑工具（`TestBashWorkdirCannotLeaveTheWorktree`）而不是调守卫。

**二、「走 `resolve()` 校验」是错的，而且方向危险。** `resolve()` 是路径守卫，而 bash 从来没有路径限制（`tools/tool.go` 的注释写明了）。给 `workdir` 加一道、命令串里的 `cd /etc` 又照样能跑，那是一道读起来像边界但不是边界的限制；顺带还会拒掉这个参数本来要服务的场景——在隔壁 checkout 里跑构建。所以只校验「存在且是目录」，理由是错误消息的质量：`chdir: no such file or directory` + exit -1 描述的是 harness，而点名解析后路径与解析基准的消息描述的是那个错误。

**第三条当初也没算到：它必须进「总是允许这条命令」的键。** 在 `workdir` 之前，「总是允许 `go build ./...`」只可能意味着「在会话目录里」。继续只按命令串做键，就把当初那一次点击悄悄提升成「在 agent 说出的任何目录里」——用户没被展示、也不可能有意做出的放宽。`web.grantKeyOf` 现在是 `命令 + \x00 + workdir`，两种形式互不覆盖。这和 `AllowCommand` 一直自称的标准是同一条：精确文本，不做泛化。

**报价**（实测，同一棵树上把 `workdir` 摘掉再量一遍）：`bash` 的描述 + schema 从 **633 → 905 字节**（描述 447 → 539，schema 186 → 366），七个工具合计 **4383 → 4655 字节（+272，约 +68 token/轮）**。这笔钱每一轮都付，买的是「模型可能用也可能不用」的一个选项——和 C2 一样的性质，所以一样要报价。

### C2. `read` 的 `paths[]` —— ✅ 已做，工具侧批量的第一个落地

**思路的转向记在这里，因为它比实现重要。** 前面的 A/B 都在做「模型侧批量」——请模型一轮多发几个调用，框架只能引导。工具侧批量是另一件事：**一次调用做更多事**，框架说了算。五个文件一次 `read`，是一次往返、一份 `tool_result`；填不满数组就退化成一个元素，没有下限风险。

落地形状：

| 决定 | 为什么 |
|---|---|
| `paths[]` 与 `path` 并存，二选一 | `path` 是绝大多数调用，而且模型对它的先验极强 |
| **`required` 变空**，「二选一」在 `ValidateArgs` 里查 | 唯一能在 schema 里表达的是 `oneOf`，而这个项目已经被某个 provider 的校验器咬过一次（见 `object` 的注释）。宁可清楚地拒绝，不要聪明地描述 |
| 预算**均分**而不是每个文件一份 | 五个文件各拿满额度就是五倍上限，而这个上限的存在意义就是防止一个 tool_result 吃掉上下文窗口 |
| 一个路径失败**不让整次调用失败** | 一个文件不存在就浪费掉这次调用本来要省的那次往返，正好是它要避免的成本。全部失败才返回 error |
| 分段格式 `==> path <==` | `head` / `tail` 的多文件约定，模型见过，不是这里新发明的 |
| 重复路径去重 | 第二份不提供新信息，还要从其他文件共享的预算里再扣一次 |
| Details **不是** `ReadDetails` 形状 | Web UI 用 `total_lines` 判别 read 结果，`ReadResult.vue` 只会画一个文件。多文件结果带自己的 `ReadManyDetails`，所以两个 guard 不会同时命中。**当时的展示降级已经补上**，见下 |

**降级补完了（`ReadManyResult.vue`）。** 记两件当时没想到的事。

**一、按行数或按分段标记去切文本都是错的。** `readMany` 写的是 `==> path <==` 一行加内容，看起来是个可以解析的固定格式。但一个文件的**内容里**如果出现 `==> other.go <==` 这样一行（文档、测试期望、这份文档本身），它的分段就提前结束，剩下的被算到下一个文件名下——**把一个文件的片段挂在另一个文件的名字下面**，这是一个查看器唯一不能有的失败。构造出来了：三个文件里有一个 markdown 文档引用了另一个文件名，切完之后 `b.go` 的内容是文档的后半截。

所以改成 `ReadFileDetails` 记 `BodyOffset` / `BodyLength`，由**写文本的同一段代码**算出来，不可能和格式脱节。正文只在 wire 上出现一次（`Text` 无论如何都要带它，那是模型读的那半），Details 只多两个整数。

**二、字节偏移和 JS 字符串下标只在 ASCII 下重合。** Go 数字节，JS 字符串按 UTF-16 code unit 索引。第一版在前端先按下标切、再用 UTF-8 字节长度校验，看着稳——**实际撞上了一次碰撞**：三个文件里一个是中文，下一个文件被切成 `"=> plain.go <"`（13 个 ASCII 字节），而它真正的内容 `"done ✅ 🎉"` 也是 13 字节，长度校验通过，读者看到的是错的东西。改成把整段文本 `TextEncoder` 编码一次，直接按字节区间 `TextDecoder` 解出来——做转换，而不是校验一个猜测。测试里那条多字节用例就是这个碰撞（`readBodies` 的 "handles multi-byte content"）。

**代价是量出来的，不是估的**（`cmd/measure-schema`）：`read` 的 schema 从 **584 → 895 字节**，七个工具合计 **4474 → 4785（+7%，约 +78 token）**。这笔钱**每一轮都付**，买的是一个模型可能用也可能不用的选项。第一版描述写长了（读到 1029 字节，+111 token），删到只留模型推不出来的两件事——一次调用一次往返、预算会被均分——省回 134 字节。**`agent/prompt.go` 的注释说 schema 是 system prompt 的 5.8 倍、该先优化的是它，所以这类改动必须报价。**

一条诚实的边界：**这仍然依赖模型愿意填数组**，只是不再依赖它一轮发多个 `tool_use`（那件事需要 provider 和模型双方支持，实测 1.10 调用/轮说明它基本没发生）。填数组只需要标准的 JSON 生成能力，门槛低得多，但不是零。

顺带修好的一个小缺陷：`GenerateSchema` 对数组只写 `type: array`、不写 `items`，模型只能猜里面装什么。现在补上了（`itemSchema`），反射 schema 和手写 schema 因此仍然逐字段等价（`TestGenerateSchemaEquivalentHandwritten` 把 `items` 的有无当作差异）。

> 踩到的坑，值得记：`ValidateArgs` 第一版用「反序列化后的 `readArgs`」判断字段在不在，`{"Path":"a.go"}` 就漏过去了——**`encoding/json` 匹配字段名是大小写不敏感的**，`"Path"` 会填进 `readArgs.Path`。而这一层的全部意义就是告诉模型「这个工具拼作 path」，所以它必须和它执行的那份 schema 一样大小写敏感。改成读原始 JSON 的 key。抓住它的是 `TestAMisspelledFieldIsPointedOut` 里的 `Path` 用例。

### D. `commands[]` 数组 + 逐条 exit / 输出 —— 形状最好，成本最高

对齐 OpenAI shell 的第一方形状：逐条 exit code、逐条输出预算、审批卡逐条显示。代价是 schema 变复杂（**每轮都付**），且模型对非标准形状的遵从度必须实测。

**判断：先别做。** 等 A / B / C 落地并有了 5~10 份会话的数据，再决定 `&&` 的四条代价（§6）是否真的疼到值得换形状。

**C2 落地之后这条的优先级反而升高了**：实测 bash 占 9/11 的调用，read 只占 2/11——工具侧批量的收益面在 bash，不在 read。C2 先做是因为它便宜（Go-only、UI 有兜底），不是因为它收益大。`commands[]` 现在有了一个可以照抄的形状（逐条分段 + 逐条错误 + 均分预算），剩下的问题只有 exit code 归因和审批卡怎么逐条显示——而这两个 OpenAI 的 shell tool 已经给了第一方答案（`output` 是数组，每条带 `outcome`）。

### E. 批量下的输出预算 —— §6 那条截断代价的解法

要么按命令分摊限额，要么对链式保留 head + tail。**依赖 D 才能做干净**（不知道边界在哪就没法分摊），所以跟着 D 一起排期。

---

## 6. `&&` 的四条代价

写进结论，别只写好处。

1. **exit 归因丢失。** `BashDetails.ExitCode` 只有一个值，UI 和模型都看不出是第几条挂的。
2. **尾部截断偏向后面的命令。** bash 用 `TruncateTail`，链越长、后面输出越多，**先失败那条越容易被切进临时文件**——而 `go build && go test` 里最该看的恰恰是先失败的那条。这是反对无脑鼓励链式的最强技术理由。
3. **审批粒度变粗。** 一次批准 = 批准整条链。而 `web/policy.go` 的 `AllowCommand` 是**精确字符串**匹配（注释里已论证前缀匹配不是安全边界），链式让「总是允许」几乎永不命中，审批疲劳变重而不是变轻。
4. **短路语义要模型自己拿准。** `&&` 遇错停、`;` 继续跑。想让 `lint; test; build` 全跑完拿全部错误，和想让 `build && test` 早停，是两个不同意图；不引导的话模型会随机选。

不受影响的一条：链式**不会**绕过 subagent 的 git 守卫，`Guard.shellWords` 已按 `; | & ( ) \`` 切词。这比业界常见的前缀白名单要稳（[Claude Code #4956](https://github.com/anthropics/claude-code/issues/4956) 报告过前缀匹配被链式绕过），而 `web/policy.go` 早就把这个道理写进注释了。

---

## 7. 顺带查出来的两个既有缺口

### 7.1 `read` 与 `edit` / `write` 同批命中同一文件，没有任何保护（✅ 已修）

`withFileLock` 只有 `write.go` 和 `edit.go` 调用，`read` **不取锁**。而两者写文件用的是 `os.WriteFile`（`O_TRUNC` 后重写，**非原子**）。所以同一批次里 `read a.go` + `edit a.go` 并发时，read 可能读到截断中的中间态。

- 这是**既有缺口，不是 §5.B 新引入的**——`read` 和 `edit` 今天都是 `Parallel`。
- `bash` 的 `Sequential` 注释声称「顺便防住 read 与重写该文件的命令同批竞态」，那句话对 bash 成立，**对 write / edit 这一半从来不成立**。
- 概率低（模型通常上一轮读、这一轮改），但不是零。

**上一版这里写着「这是读代码得出的结论，我没有构造出实际的撕裂读」。现在构造出来了，而且比预期难看：** 把锁从 `Read.Execute` 去掉，`TestReadNeverObservesAHalfWrittenFile` 在第 4~12 次读就从一个 32,000 字节的文件读回 **0 字节，不报错**。这不是「短一截」，是整个文件在那一瞬间不存在。失败是静默的——空文件是个合法的短文件，模型会从一份从不存在的内容里得出一个看起来合理的结论。

修法就是 `read` 也走 `withFileLock`（`tools/read.go`）。**仍然不覆盖 bash**：它的写是任意 shell 命令，从不经过那把锁，所以 read-vs-bash 全靠 bash 独占一段。

> 方法论上值得单独记一条：**这个测试的「牙」是单独验证过的**（`_check/teeth.sh` 把修复从容器里的副本上摘掉再跑一遍，要求它失败）。一个改动前后都绿的测试等于没写，而这条如果只在有锁的情况下跑过，它证明的只是「加了锁不崩」。

### 7.2 当前分支在 Windows 上编译不过（已修）

初查以为是一处，**实际是四处**——只有跨平台编译一遍才数得清，靠读代码数不出来，因为 `go build` 一个包报一个错就停了。

| 位置 | 症状 | 处理 |
|---|---|---|
| `worktree/worktree.go` 的 `alive()` | `syscall.Kill(pid, 0)` | 拆 `alive_unix.go` / `alive_other.go`。非 unix 版**保守返回 alive**：`Prune` 对活进程持有的 worktree 是「留着」，所以答错方向只是不回收，答错另一方向是两个 agent 进同一个 worktree 而无从检测 |
| `tui/termui.go` 的 `NewDock()` | `syscall.SIGWINCH` | 拆 `winch_unix.go` / `winch_other.go`，导出 `notifyResize`。**不能**用 `signal.Notify(sigc)` 空列表代替——那是订阅全部信号，Ctrl-C 会变成重绘 |
| `web/terminal.go` 的 `Kill()` | 自己内联了一份 `syscall.Kill(-pid, SIGKILL)` | 改调 `tools.KillGroup`（把 `tools` 里本就分好平台的 `killGroup` 导出）。**顺手消掉了一份重复实现**：进程组怎么杀，全项目现在只有一个答案 |
| `tools/subagent_test.go` 的 `TestSubagentTimeoutKillsTheChild` | 测试里的 `syscall.Kill` | 移进 `subagent_kill_unix_test.go`（`//go:build unix`）。`go vet` 会类型检查测试文件，所以一个 `_test.go` 里的裸 syscall 能让整个包在 windows 下 vet 失败，即使库本身编译得过。而且这个用例断言的是**进程组**保证，而 `setProcessGroup` 在非 unix 上是有意的 no-op，在 Windows 跑它等于断言项目没做的承诺 |

**验证**（2026-08-13，26 秒）：`gofmt` clean；`go build` + `go vet` 四目标全过（linux/amd64、windows/amd64、windows/arm64、darwin/arm64）；`go test -count=1 ./...` 全绿；`go test -race` 在 `tools` / `tui` / `web` / `worktree` 四个受影响包全绿。

> **踩到的坑，值得记：把 Windows 目录 bind mount 进容器跑 Go 测试，会慢一个数量级。** 同一套 gate 从 bind mount 上跑 17 分钟没跑完，改成**先 `tar` 拷进容器**再跑是 26 秒。Go 的编译和测试全是海量小文件 I/O，每次读写都要穿过 Docker Desktop 的文件系统转换层；一次批量顺序拷贝那层扛得住，随机小文件访问扛不住。顺带解决的第二件事：这个 checkout 是 `core.autocrlf=true`，工作树 CRLF、index LF，`gofmt -l` 在 Linux 里会把全部 ~140 个文件报成未格式化——所以要在拷贝时 `sed 's/\r$//'` 归一化后再 `gofmt`，否则那条检查等于噪声。

---

## 8. 下一步与验收口径

**先量后改。** 这是 Anthropic 的方法论，也是 pi-go 已经有手的地方（`analyze.BatchDistribution` + `ToolTiming.ByTool` + `agent.Timing()`）。

1. **攒样本。** 跑 5~10 个真实任务（混合：只读排查、多文件改动、build/test 循环），`-analyze-session sessions` 出基线。§7.2 已修，Windows 上可以直接 `go build` 出二进制自己跑；容器里的会话在 `PIGO_SESSION_DIR=/var/lib/agent/sessions`（见 `malagent/Dockerfile`），所以要么把它挂出来，要么在容器内跑分析。
2. **基线三个数**，前两个 `-analyze-session` 现在直接给：
   - `Average per tool-calling turn`（Anthropic 的指标，目标明显 > 1）；
   - `By Tool Set` + `Serialized by a sequential sibling`（分段规则还剩多少没吃到的收益；实测这份 transcript 是 1）；
   - bash 调用中含 `&&` / `;` 的比例，和以 `cd` 开头的比例（= C 的收益面）。**这条还没进 analyze**，因为它要读 tool_use 的参数而不只是名字。

   **两个已知的仪器缺口**：这份 transcript 的 `Tool Timing` 是 `No tool calls recorded`——没有 `exec_time` 记录，所以**收益暂时只能用调用个数说，不能用秒**，哪条路径不写这些记录待查。另外真实会话里 89,867 输入 token 有 **81,856 是 cache read（91%）**，所以「少一次往返省 token」这条论证在这套配置下很弱——多一次往返的边际成本主要是**输出 token 加延迟**，不是重新计费整个 prompt。批量的理由要按延迟和轮次预算写，不要按省钱写。
3. **做 A**（引导），同样口径再量一次。A 是唯一能单独归因的改动——它不动执行器。
4. **拿 A 的数据决定 B / C 的排期**；D / E 留到 `&&` 的代价被实测证明疼为止。
5. **B 落地必须带并发测试并跑 `-race`。** `harness-design.md` 那句「并行代码不跑 `-race` 等于没验证」在这里照抄。

> 本文引用的官方资料均为合规改写与摘要，非原文引用。链接见 §4。
