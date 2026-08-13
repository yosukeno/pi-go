// Command pi-go is a minimal coding agent: one loop, a handful of tools, one
// protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/analyze"
	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/llm"
	"github.com/yosukeno/pi-go/memory"
	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/skills"
	"github.com/yosukeno/pi-go/tools"
	"github.com/yosukeno/pi-go/tui"
	"github.com/yosukeno/pi-go/web"
	"github.com/yosukeno/pi-go/wire"
)

// Flag descriptions are written as "English\n中文" and split by the help
// printer. Keeping both languages at the definition site is what stops them from
// drifting apart when a flag changes.
func main() {
	// Before the flags, because -model's help text names the default and -models
	// lists the catalog: both have to describe the configuration that is actually in
	// effect. Warnings are collected rather than printed, because stdout may be a
	// JSONL stream and this runs before we know.
	configWarnings, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pi-go:", err)
		os.Exit(2)
	}

	var (
		prompt = flag.String("p", "",
			"Run a single `prompt` and exit.\n运行单个 prompt 后退出")
		model = flag.String("model", "",
			"Model `name` or alias. Run -models to see them all. Default: "+config.DefaultModel()+
				"\n模型 id 或别名，用 -models 查看全部，默认 "+config.DefaultModel())
		listModels = flag.Bool("models", false,
			"List known models and exit.\n列出已知模型后退出")
		listSessions = flag.Bool("sessions", false,
			"List saved sessions and exit.\n列出已保存的会话后退出")
		cwd = flag.String("C", "",
			"Working `directory` the agent operates in. Default: the current one"+
				"\nagent 的工作目录，默认为当前目录")
		resume = flag.String("resume", "",
			"Resume a session: \"last\" or a `path` to a .jsonl file.\n恢复会话：\"last\" 或 .jsonl 文件路径")
		quiet = flag.Bool("quiet", false,
			"Hide thinking and tool output, keep only the answer.\n隐藏思考过程与工具输出，只留最终回答")
		mode = flag.String("mode", modeText,
			"Output `mode`: text for people, json for programs (one event per line on stdout)."+
				"\n输出模式：text 给人看，json 给程序读（stdout 每行一个事件）")
		maxTurns = flag.Int("max-turns", agent.DefaultMaxTurns,
			"Give up after this many turns (`n`) in one run.\n单次运行的轮次上限")
		softTurns = flag.Int("soft-turns", agent.DefaultSoftTurns,
			"Inject a checkpoint every this many turns (`n`) rather than running silently\n"+
				"to -max-turns: the model is told where it stands, then either finishes or\n"+
				"states what is left. 0 disables; a value >= -max-turns never fires."+
				"\n每经过该轮数插入一次检查点，而不是静默跑到 -max-turns：模型被告知当前进度，"+
				"\n然后要么收尾、要么说明还差什么。0 关闭；大于等于 -max-turns 时不会触发")
		maxRuns = flag.Int("max-runs", 1,
			"Let one task span at most this many runs (`n`), -p only: when a run ends on a\n"+
				"continue-disposition limit (the turn cap or a budget), the session forks and a\n"+
				"fresh run picks up from .pi-go/handoff.md. The default 1 is a single run."+
				"\n让一个任务最多跨 n 次运行（仅 -p）：撞 continue 类上限（轮次或预算）就 fork\n"+
				"\n会话、由新 run 从 .pi-go/handoff.md 接着做。默认 1 即单次运行")
		evaluate = flag.Bool("evaluate", false,
			"Check the result with a fresh read-only evaluator before accepting it (-p only):\n"+
				"a claimed completion that fails the check chains another run with the findings\n"+
				"(within -max-runs); a chain that ran out of runs still exits 0 when the\n"+
				"evaluator confirms the work landed."+
				"\n接受结果前先过一个全新的只读 evaluator（仅 -p）：声称完工但没通过核验的，"+
				"\n带着发现接力下一棒（在 -max-runs 内）；run 数用尽但活核验属实的，仍算成功")
		retries = flag.Int("retries", llm.DefaultMaxRetries,
			"Retries (`n`) per LLM call on 429/5xx. -1 disables them."+
				"\n每次 LLM 调用在 429/5xx 时的重试次数，-1 关闭")
		stagnationThreshold = flag.Int("stagnation-threshold", 3,
			"Stop after this many identical tool results (`n`). 0 to disable."+
				"\n连续相同工具结果达到此次数后停止，0 禁用")
		tokenBudget = flag.Int64("token-budget", 0,
			"Maximum total tokens (`n`) for this run. 0 means unlimited."+
				"\n本次运行的最大总 token 数，0 表示无限制")
		costBudget = flag.Float64("cost-budget", 0.0,
			"Maximum estimated cost (`f`) for this run, in the same unit as the model's"+
				"\ndeclared price. 0 means unlimited. Needs a price in providers.json;"+
				"\nrefuses to start without one rather than running uncapped."+
				"\n本次运行的最大预估成本，单位与模型声明的价格一致，0 表示无限制。"+
				"\n需要 providers.json 里声明价格；没有价格时拒绝启动而不是不设上限")
		timeBudget = flag.Duration("time-budget", 0,
			"Maximum wall-clock time (`dur`) for this run. 0 means unlimited."+
				"\n本次运行的最大墙钟时间，0 表示无限制")
		contextEdit = flag.String("context-edit", "auto",
			"Drop old tool results from the prompt once it passes this size: `auto` "+
				"(four fifths of the model's window), off, or a token count."+
				"\n超过该大小后丢弃旧的工具输出：auto（模型窗口的五分之四）、off 关闭、或给 token 数")
		serve = flag.Bool("web", false,
			"Serve the browser UI instead of running in the terminal."+
				"\n启动浏览器界面，而不是在终端里运行")
		listen = flag.String("listen", "127.0.0.1:7777",
			"`address` for -web. Loopback by default; a token is always required."+
				"\n-web 的监听地址，默认只绑本机；无论如何都需要 token")
		webDev = flag.String("web-dev", "",
			"Proxy non-API routes to a vite dev server at this `url`."+
				"\n把非 API 路由反代到 vite 开发服务器")
		gateTimeout = flag.Duration("gate-timeout", web.DefaultGateTimeout,
			"How long (`dur`) a tool call waits for approval before being refused."+
				"\n工具调用等待人工审批的时长，超时按拒绝处理")
		listWorktrees = flag.Bool("worktrees", false,
			"List isolated worktrees for this repository and exit."+
				"\n列出本仓库的隔离 worktree 后退出")
		pruneWorktrees = flag.Bool("worktrees-prune", false,
			"Remove isolated worktrees that hold no work and no live lock, then exit."+
				"\n清理没有未保存改动、也没有活进程占用的隔离 worktree 后退出")
		pruneCheckpoints = flag.Bool("checkpoints-prune", false,
			"Discard rewind points beyond the retention policy for this workspace, then exit."+
				"\n清理本工作区超出保留策略的撤回点后退出")
		listSkills = flag.Bool("skills", false,
			"List discovered skills and exit.\n列出已发现的 skills 后退出")
		noSkills = flag.Bool("no-skills", false,
			"Skip the default skill locations. -skill paths still load."+
				"\n不扫描默认 skill 目录；-skill 指定的仍然加载")
		projectSkills = flag.Bool("project-skills", false,
			"Also load skills from ./.pi-go/skills. Off by default: a skill rewrites the system prompt."+
				"\n同时加载 ./.pi-go/skills；默认关闭，因为 skill 会改写 system prompt")
		listMemory = flag.Bool("memory", false,
			"List the agent's memory notes and exit.\n列出 agent 的记忆笔记后退出")
		noMemory = flag.Bool("no-memory", false,
			"Do not give the agent a memory directory."+
				"\n不给 agent 记忆目录")
		projectMemory = flag.Bool("project-memory", false,
			"Also use ./.pi-go/memory. Off by default: notes arrive with a checkout and "+
				"speak to the model as its own earlier conclusions."+
				"\n同时使用 ./.pi-go/memory；默认关闭，因为笔记会随 checkout 到来，"+
				"而且是以「你自己之前的结论」这个身份说话")
		analyzeSession = flag.String("analyze-session", "",
			"Analyze a session JSONL file and output statistics. A directory, or the\n"+
				"word `sessions` for your own history, reports the turn-count distribution\n"+
				"across every run in it instead — the number a -max-turns cap comes from."+
				"\n分析会话 JSONL 文件并输出统计信息。传目录或 sessions 则统计其中所有 run 的"+
				"\n轮次分布——这是选 -max-turns 上限该依据的数据")
		analyzeFormat = flag.String("analyze-format", "text",
			"Output format for session analysis: text or json. Default: text."+
				"\n会话分析的输出格式：text 或 json，默认 text")
		analyzeOutput = flag.String("analyze-output", "",
			"Write analysis output to the specified file instead of stdout."+
				"\n将分析输出写入指定文件而非标准输出")
	)
	// -skill is repeatable, so it needs a Var rather than a String.
	var skillPaths repeatedFlag
	flag.Var(&skillPaths, "skill",
		"Load a skill from this `path` (file or directory). Repeatable."+
			"\n从指定路径加载 skill（文件或目录），可重复")
	// -web-panel is repeatable too: name=url per panel.
	var panelFlags repeatedFlag
	flag.Var(&panelFlags, "web-panel",
		"Show an external web app as a dock sheet: `name=url`. Repeatable. The app is reverse-proxied at /panels/name/."+
			"\n把一个外部 web 应用挂进 dock 作为 sheet 显示：name=url，可重复；应用被反代到 /panels/name/")
	flag.Usage = usage
	flag.Parse()

	// Reported after parsing so that -C is known, and on diagOut so that a JSONL
	// stream on stdout stays clean.
	if w := config.WarnIfInWorkingDir(*cwd); w != "" {
		configWarnings = append(configWarnings, w)
	}
	for _, w := range configWarnings {
		fmt.Fprintf(diagOut, "%sconfig: %s%s\n", tui.Yellow, w, tui.Reset)
	}

	if *listModels {
		// Marks the one that would be used, which is the default unless -model says
		// otherwise. Worth doing now that a config file can move the default: a list
		// with nothing marked leaves the reader to guess which line is in effect.
		if *model != "" {
			printModels(*model)
		} else {
			printModels(config.DefaultModel())
		}
		return
	}
	if *listSessions {
		if err := printSessions(); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	skillOpts := skills.Options{
		Paths:          skillPaths,
		Disabled:       *noSkills,
		IncludeProject: *projectSkills,
	}
	if *listSkills {
		if err := printSkills(skillOpts, *cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	memOpts := memory.Options{
		Cwd:     *cwd,
		User:    !*noMemory,
		Project: *projectMemory && !*noMemory,
	}
	if *listMemory {
		if err := printMemory(memOpts); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *listWorktrees || *pruneWorktrees {
		if err := worktreeCommand(os.Stdout, *cwd, *pruneWorktrees); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *pruneCheckpoints {
		if err := checkpointCommand(os.Stdout, *cwd); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *maxRuns < 1 {
		fmt.Fprintf(os.Stderr, "pi-go: -max-runs must be at least 1 (got %d)\n", *maxRuns)
		os.Exit(1)
	}
	if *analyzeSession != "" {
		if err := analyzeSessionFile(*analyzeSession, *analyzeFormat, *analyzeOutput); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *serve {
		modelName := *model
		if modelName == "" {
			modelName = config.DefaultModel()
		}
		if err := serveWeb(webOptions{
			listen: *listen, dev: *webDev, model: modelName, cwd: *cwd,
			maxTurns: *maxTurns, retries: *retries, gateTimeout: *gateTimeout,
			stagnationThreshold: *stagnationThreshold, tokenBudget: *tokenBudget,
			costBudget: *costBudget, timeBudget: *timeBudget,
			contextEdit: *contextEdit,
			skills:      skillOpts,
			memory:      memOpts,
			panels:      panelFlags,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "pi-go: %v\n", err)
			os.Exit(1)
		}
		return
	}
	opts := options{
		prompt: *prompt, model: *model, cwd: *cwd, mode: *mode,
		maxTurns: *maxTurns, quiet: *quiet, resume: *resume, retries: *retries,
		softTurns:           *softTurns,
		maxRuns:             *maxRuns,
		evaluate:            *evaluate,
		stagnationThreshold: *stagnationThreshold, tokenBudget: *tokenBudget,
		costBudget: *costBudget, timeBudget: *timeBudget,
		contextEdit: *contextEdit,
		skills:      skillOpts,
		memory:      memOpts,
	}
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "\npi-go: %v\n", err)
		os.Exit(1)
	}
}

// repeatedFlag collects a flag given more than once.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

type options struct {
	prompt, model, cwd, resume string
	// mode is modeText or modeJSON. An enum rather than a -json boolean: text,
	// json and a future protocol front end are three points on one axis, and
	// boolean flags for them would be mutually exclusive in a way the flag package
	// cannot express.
	mode                string
	maxTurns, retries   int
	softTurns           int
	maxRuns             int
	stagnationThreshold int
	tokenBudget         int64
	costBudget          float64
	timeBudget          time.Duration
	// contextEdit is the raw -context-edit spec. It is resolved against the model's
	// window in run(), which is the first place both are known.
	contextEdit string
	quiet       bool
	evaluate    bool
	skills      skills.Options
	memory      memory.Options
}

// The output modes. text is the default and behaves exactly as before.
const (
	modeText = "text"
	modeJSON = "json"
)

// diagOut is where everything a human would read goes: load warnings, drift
// notices, retry announcements, and the resumed banner.
//
// A variable rather than os.Stderr at each call site for two reasons. It is the
// seam the JSON mode test uses to prove diagnostics left stdout, and it is the
// single place to look when answering "can this write corrupt a piped stream?".
var diagOut io.Writer = os.Stderr

func run(o options) error {
	switch o.mode {
	case modeText, modeJSON:
	default:
		return fmt.Errorf("unknown -mode %q: want %s or %s", o.mode, modeText, modeJSON)
	}
	if o.mode == modeJSON {
		// Escape codes belong to neither stream here: stdout is JSON, and colouring
		// the diagnostics on stderr only makes them harder to grep.
		tui.SetPlain()
		if o.quiet {
			// Answered rather than silently obeyed. Filtering events would hand the
			// consumer a stream with holes in it, which is worse than a stream it
			// has to filter itself.
			fmt.Fprintf(diagOut, "pi-go: -quiet has no effect with -mode json; "+
				"the event stream is always complete. Filter it downstream.\n")
		}
	}

	cwd := o.cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	// Read stdin before anything else. Piped input means there is no interactive
	// user, so it also decides which mode we end up in.
	piped, err := readPipedStdin()
	if err != nil {
		return err
	}
	prompt := buildPrompt(piped, o.prompt)

	// Refused before anything is resolved, because it is a usage error: the REPL
	// prints a banner, a prompt and command output, none of which is an event, and
	// reading prompts line by line is the shape of a protocol rather than a
	// one-shot. Failing here rather than after loading a session and a model keeps
	// the message about the flag combination instead of about a missing API key.
	if o.mode == modeJSON && prompt == "" {
		return fmt.Errorf("-mode json needs a prompt: pass -p, or pipe some input " +
			"(interactive use is text mode only)")
	}

	// A resumed session is opened before the model is chosen, because the model it
	// was running is the right default for continuing it. Resolving the client
	// first would silently switch a resumed conversation to the default model.
	var store *session.Store
	if o.resume != "" {
		if store, err = openExisting(o.resume); err != nil {
			return err
		}
		reportSessionDiagnostics(store)
		if o.model == "" {
			if m := store.Meta(); m != nil && m.Model != "" {
				o.model = m.Model
			}
		}
	}

	client, cfg, err := newClient(o.model, o.retries)
	if err != nil {
		return err
	}

	// Resolved here because "auto" means half this model's window, and the model is
	// only settled after -resume has had its say about which one to use.
	editCfg, err := agent.ParseContextEdit(o.contextEdit, cfg.ContextWindow)
	if err != nil {
		return err
	}

	// Checked in the same place and for the same reason: the answer depends on which
	// model -resume settled on.
	if err := checkCostBudget(o.costBudget, cfg); err != nil {
		return err
	}

	// Skills are resolved once, here, and never re-scanned. A system prompt that
	// changed mid-session would invalidate the cached prefix every turn and make
	// -resume unreproducible.
	o.skills.Cwd = cwd
	skillList, skillDiags := skills.Load(o.skills)
	reportSkillDiagnostics(skillDiags)

	// Memory is resolved here for the same reason skills is: it contributes to the
	// system prompt, and a system prompt that changed mid-session would invalidate the
	// cached prefix every turn. The listing is a snapshot from now, so notes the model
	// writes during this run do not appear in it until the next session — which is the
	// correct behaviour, not a limitation: the model knows what it just wrote.
	o.memory.Cwd = cwd
	mem, memDiags := memory.Load(o.memory)
	for _, d := range memDiags {
		fmt.Fprintf(diagOut, "%s%s%s\n", tui.Yellow, d, tui.Reset)
	}

	// The system prompt's sections are fixed for the whole process, chain
	// included: a chained run is a fresh agent from the same config, and the
	// section is what tells run 1 — before any limit is near — that its context
	// will not carry over.
	sections := []string{skills.PromptSection(skillList), mem.PromptSection()}
	if o.maxRuns > 1 {
		sections = append(sections, chainSection)
	}
	agentCfg := agent.Config{
		Client:       client,
		Registry:     tools.New(toolOptions(cwd, cfg.Model, skills.Roots(skillList), mem.Roots())),
		SystemPrompt: agent.SystemPrompt(cwd, sections...),
		MaxTurns:     o.maxTurns,
		SoftTurns:    o.softTurns,
		// Nil unless this process is a subagent whose parent handed it an approval
		// channel. The terminal keeps no gate, which is what keeps -p scriptable.
		Gate:                newChildGate(),
		StagnationThreshold: o.stagnationThreshold,
		TokenBudget:         o.tokenBudget,
		CostBudget:          o.costBudget,
		Price:               cfg.Price,
		TimeBudget:          o.timeBudget,
		ContextEdit:         editCfg,
	}
	a := agent.New(agentCfg)

	// recorded tracks how much of the agent's running totals is already on disk;
	// a resumed session starts from the totals recovered below.
	var recorded session.Recorded
	if store == nil {
		if store, err = session.Create(session.DefaultDir(), cwd, cfg.Model, skills.Names(skillList)...); err != nil {
			return err
		}
	} else {
		reportSessionDrift(store, cwd, skills.Names(skillList))
		prior := store.Messages("")
		a.SetMessages(prior)
		// The cost counters resume with the conversation: without this a
		// continued session reports only what it spent since the restart.
		usage, delegated := store.UsageTotals()
		a.SetUsage(usage, delegated)
		recorded = session.Recorded{Usage: usage, Delegated: delegated}
		reportResumed(store.Path(), len(prior))
	}
	// persisted tracks how much of the agent's history is already on disk, so
	// each turn appends only what is new.
	persisted := len(a.Messages())
	// recordCost writes the tokens spent since the last such record. A delta rather
	// than the running total, because the analyzer sums these records — writing the
	// total each time would make a five-turn session look like fifteen turns' worth.
	//
	// Shared by flush and compact so the two can never disagree about the baseline:
	// they both move `recorded`, and a second copy of this block would eventually
	// double-count or skip a call.
	recordCost := func(msgs []llm.Message) error {
		st, ok := session.UsageDelta(&recorded, a.Usage(), a.Delegated())
		if !ok {
			return nil
		}
		// Attached after the fact rather than passed into UsageDelta, because it is
		// not an accumulator: it is a snapshot of the history as it now stands, so
		// there is no previous value it has to stay in step with. Safe here for the
		// same reason Messages() is — no run is in flight.
		comp := session.Compose(msgs, a.OverheadTokens())
		comp.Measured = a.LastInput()
		comp.Cleared = a.ClearedFromPrompt()
		st.Composition = &comp
		return store.AppendStats(st)
	}
	flush := func() error {
		msgs := a.Messages()
		if err := store.AppendAll(msgs[persisted:]); err != nil {
			return err
		}
		persisted = len(msgs)
		return recordCost(msgs)
	}
	// compact summarises the conversation and writes the result as a new branch.
	//
	// The fork is the whole reason this is not just "replace the messages". The
	// transcript is append-only and the original conversation stays in the file;
	// Fork("") abandons it as an unreachable branch and starts a fresh chain, which
	// is exactly what the tree was built for and what rewind already does. A -resume
	// therefore replays the compacted history, while the full original is still there
	// to read.
	//
	// persisted has to move with it. Without that, the next flush would slice the
	// agent's much shorter history at the old offset — out of range, and a panic in
	// the place least able to afford one.
	compact := func(ctx context.Context) error {
		res, err := a.Compact(ctx)
		if err != nil {
			return err
		}
		if err := store.Fork(""); err != nil {
			return err
		}
		msgs := a.Messages()
		if err := store.AppendAll(msgs); err != nil {
			return err
		}
		persisted = len(msgs)
		// Recorded now rather than at the next flush: the summarising call is spend
		// that already happened, and a session compacted and then closed would
		// otherwise lose it.
		if err := recordCost(msgs); err != nil {
			return err
		}
		fmt.Printf("%scompacted: %d messages → 1, about %d → %d tokens (freed ~%d); the summary cost %d in / %d out.\n"+
			"the full conversation is still in %s as an abandoned branch.%s\n",
			tui.Dim, res.Before, res.BeforeTokens, res.AfterTokens, res.Freed(),
			res.Usage.Input, res.Usage.Output, store.Path(), tui.Reset)
		return nil
	}

	// One Ctrl-C cancels the in-flight turn; the loop unwinds through the same
	// context that reaches every HTTP request and child process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The front end is chosen once, here, and the rest of run() only knows it can
	// consume events. Everything below this line is identical in both modes, which
	// is the point of the interface.
	rend := tui.NewRenderer(o.quiet, skillList, cwd, o.mode == modeJSON)
	if rend.Dock != nil {
		defer rend.Dock.Close()
		// Retry notices write to stderr, the same terminal the dock redraws:
		// route them through the dock's lock so the two never interleave bytes.
		diagOut = rend.Dock.LockWriter(diagOut)
	}
	var out consumer = rend
	if o.mode == modeJSON {
		j := newJSONEmitter(os.Stdout)
		// The header goes out before any event so a consumer knows which transcript
		// it is reading — including a subagent's parent, which needs the path to
		// hand back.
		h := wire.Header{Session: store.Path(), Cwd: cwd, Model: cfg.Model, Skills: skills.Names(skillList)}
		if err := j.header(h); err != nil {
			return err
		}
		out = j
	}

	// consume runs one prompt end to end, persisting as it goes: the prompt
	// first (the loop does not announce it), then each turn as its events pass,
	// so a kill mid-run loses only the in-flight turn rather than the run. The
	// flush() that follows is no longer the writer but the reconciliation —
	// whatever the persister failed on still lands there, because persisted
	// counts only appends that succeeded.
	var lastEnd agent.EndReason
	consume := func(prompt string) error {
		lastEnd = ""
		p := agent.NewTurnPersister(func(m llm.Message) error {
			if err := store.Append(m); err != nil {
				return err
			}
			persisted++
			return nil
		}, func(err error) {
			fmt.Fprintf(diagOut, "%spi-go: incremental persist failed (%v); the run is still saved when it ends%s\n",
				tui.Yellow, err, tui.Reset)
		})
		p.Add(llm.UserText(prompt))
		return out.Consume(tapEvents(a.Run(ctx, prompt), func(e agent.Event) {
			p.OnEvent(e)
			if e.Kind == agent.EventAgentEnd {
				lastEnd = e.EndReason
			}
		}))
	}

	// One-shot whenever we have a prompt, whether it came from -p or a pipe.
	// Falling through to the REPL with piped input would read the piped document
	// line by line and submit each line as its own prompt.
	if prompt != "" {
		// The same reasoning as the /skill: expansion below, applied to commands: a
		// flag that silently sent the literal text "/compact" to the model would be a
		// trap exactly where people script against it, and the model would answer it.
		//
		// /compact is the only command that does anything here, and that is a
		// distinction rather than a special case: every other one either has a flag
		// (-model, -h) or describes interactive state the one-shot path does not have
		// (there is no gate in the terminal, and nothing has run yet to report usage
		// for). /compact operates on the session, which -resume has just handed us,
		// and `-resume <path> -p /compact` is a real thing to want.
		if cmd := commandWord(prompt); cmd != "" {
			if cmd != "/compact" {
				return fmt.Errorf("%s is an interactive command and does nothing in -p; "+
					"see -h for the flag that covers it", cmd)
			}
			return compact(ctx)
		}
		// -p expands /skill: too. Every other mode does, and a flag that silently
		// sent the literal text "/skill:x" to the model would be a trap in exactly
		// the place people script against.
		if name, extra, ok := skills.ParseCommand(prompt); ok {
			s, found := skills.Find(skillList, name)
			if !found {
				return fmt.Errorf("no skill named %q; run -skills to list them", name)
			}
			if prompt, err = skills.Invocation(s, extra); err != nil {
				return err
			}
		}
		if rend.Dock != nil {
			rend.Dock.SetStatus(cfg.Model, tilde(cwd), rend.LastCtxInput, cfg.ContextWindow)
		}
		// A task may span several runs (-max-runs). When a run ends because an
		// allowance ran out, the session forks and a fresh run picks up from the
		// handoff file — reset, not compaction: nothing is summarised away, the
		// old transcript stays in the file as an abandoned branch. The
		// disposition table decides what chains: a turn cap or budget is an
		// allowance, stagnation or an overflow is a failure that repeating would
		// reproduce.
		original := prompt
		var next string
		for runN := 1; ; runN++ {
			if runN > 1 {
				if err := store.Fork(""); err != nil {
					return err
				}
				// A fresh agent on the new branch: empty history, per-run budgets
				// refilled, and the same config — the chain section of the system
				// prompt is what it shares with its predecessor.
				a = agent.New(agentCfg)
				persisted = 0
				recorded = session.Recorded{}
				prompt = next
			}
			runErr := consume(prompt)
			if err := flush(); err != nil {
				return err
			}

			// The evaluator speaks where the driver otherwise has only the run's
			// own word: a claimed completion, and (below) a chain that ran out of
			// runs on an allowance.
			if o.evaluate && lastEnd == agent.EndCompleted {
				pass, findings, err := evaluateRun(ctx, agentCfg.Client, cwd, original)
				switch {
				case err != nil:
					// A failed evaluator changes nothing: the run's own word stands.
					fmt.Fprintf(diagOut, "%spi-go: evaluator failed (%v); the run's own result stands%s\n", tui.Yellow, err, tui.Reset)
					return runErr
				case pass:
					fmt.Fprintf(diagOut, "%spi-go: evaluator: PASS%s\n", tui.Dim, tui.Reset)
					return runErr
				case runN >= o.maxRuns:
					return fmt.Errorf("run %d claimed done, but the evaluator disagrees:\n%s", runN, findings)
				default:
					fmt.Fprintf(diagOut, "%spi-go: run %d claimed done; the evaluator disagrees — chaining run %d with its findings%s\n",
						tui.Yellow, runN, runN+1, tui.Reset)
					next = findingsPrompt(original, runN+1, findings)
					continue
				}
			}
			if !chainContinues(lastEnd, runN, o.maxRuns) {
				if o.evaluate && lastEnd.Disposition() == agent.DispositionContinue {
					pass, findings, err := evaluateRun(ctx, agentCfg.Client, cwd, original)
					switch {
					case err != nil:
						fmt.Fprintf(diagOut, "%spi-go: evaluator failed (%v); the run's own result stands%s\n", tui.Yellow, err, tui.Reset)
					case pass:
						fmt.Fprintf(diagOut, "%spi-go: runs ran out on %s, but the evaluator confirms the work is done%s\n",
							tui.Yellow, lastEnd, tui.Reset)
						return nil
					default:
						fmt.Fprintf(diagOut, "%spi-go: runs ran out on %s, and the evaluator agrees it is not done:\n%s%s\n",
							tui.Yellow, lastEnd, findings, tui.Reset)
					}
				}
				return runErr
			}
			fmt.Fprintf(diagOut, "%spi-go: run %d ended on %s — chaining run %d (of at most %d), handoff in .pi-go/handoff.md%s\n",
				tui.Yellow, runN, lastEnd, runN+1, o.maxRuns, tui.Reset)
			next = handoffPrompt(original, runN+1)
		}
	}

	// Unreachable under -mode json: the prompt check above already returned.
	fmt.Printf("pi-go  model=%s (%s)  cwd=%s\nsession %s\n%s/help lists commands (Tab completes them), /exit to quit%s\n",
		cfg.Model, cfg.Provider, cwd, tui.Link(tui.FileURL(store.Path()), store.Path()), tui.Dim, tui.Reset)
	ed := tui.NewEditor()
	ed.Skills = skills.Names(skillList)
	if rend.Dock != nil && rend.Dock.Viable {
		// The session pins once: the status row lives at the screen bottom from
		// here on and the editor docks above it. Consume() skips its own
		// pin/unpin while the session is on.
		rend.Dock.Pin()
		defer rend.Dock.Unpin()
		ed.Dock = rend.Dock
	}
	submitted := 0
	for {
		// Status: which model is on, where it is working, how full the context
		// window is. With a dock it is the pinned bottom row; without one it is
		// printed above the prompt as before.
		if ed.Dock != nil {
			rend.Dock.SetStatus(cfg.Model, tilde(cwd), rend.LastCtxInput, cfg.ContextWindow)
		} else {
			_, cols := tui.TermSize()
			fmt.Print(tui.StatusLine(cfg.Model, tilde(cwd), "", rend.LastCtxInput, cfg.ContextWindow, cols))
		}
		line, err := ed.ReadLine(tui.Bold + "❯" + tui.Reset + " ")
		if err == io.EOF {
			// EOF before a single prompt: stdin was /dev/null, an empty pipe, or
			// closed. Detecting that here covers every such case without needing
			// to tell a terminal from a character device.
			if submitted == 0 {
				return fmt.Errorf("no prompt given and stdin is empty: pass -p, or pipe some input")
			}
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A command prefix resolves to the first candidate the completion list
		// showed, so Enter on "/e" executes /exit rather than complaining.
		line = resolveCommand(line)
		// A skill invocation is not a command: it expands into the prompt that
		// gets submitted, which is why it is handled here rather than in
		// command() and why the expansion is visible in the transcript.
		if name, extra, ok := skills.ParseCommand(line); ok {
			s, found := skills.Find(skillList, name)
			if !found {
				fmt.Fprintf(os.Stderr, "%s! no skill named %q; /skills lists them%s\n", tui.Red, name, tui.Reset)
				continue
			}
			text, err := skills.Invocation(s, extra)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s! %v%s\n", tui.Red, err, tui.Reset)
				continue
			}
			fmt.Printf("%sloaded skill %s from %s%s\n", tui.Dim, s.Name, tui.Link(tui.FileURL(s.Path), s.Path), tui.Reset)
			line = text
		} else if handled, err := command(line, a, &cfg, o.retries, skillList, o.contextEdit, func() error { return compact(ctx) }); handled {
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s! %v%s\n", tui.Red, err, tui.Reset)
			}
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if strings.HasPrefix(line, "/") {
			// A leading slash means a command was intended; submitting a typo'd
			// command to the model as a prompt helps no one.
			fmt.Printf("%sunknown command - /help lists them all%s\n", tui.Dim, tui.Reset)
			continue
		}
		submitted++
		if ed.Dock != nil {
			rend.Dock.SetStatus(cfg.Model, tilde(cwd), rend.LastCtxInput, cfg.ContextWindow)
			rend.Dock.BeginRun()
		}
		runErr := consume(line)
		if ed.Dock != nil {
			rend.Dock.EndRun()
		}
		if err := flush(); err != nil {
			return err
		}
		if runErr != nil {
			// Recoverable at the prompt: report and keep the session alive.
			fmt.Fprintf(os.Stderr, "%s! %v%s\n", tui.Red, runErr, tui.Reset)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// command handles REPL slash commands. It returns whether the line was a
// command, separately from whether that command succeeded.
// contextEditSpec is the -context-edit flag as given, needed by /model to
// re-resolve "auto" against the window of the model being switched to.
// compact is injected rather than performed here because compaction has to move
// the session file's head and the persistence offset along with the history, and
// both of those live in run().
func command(line string, a *agent.Agent, cfg *config.Resolved, retries int,
	skillList []skills.Skill, contextEditSpec string, compact func() error) (bool, error) {
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)

	switch name {
	case "/models":
		printModels(cfg.Model)
		return true, nil

	case "/skills":
		printSkillList(skillList)
		return true, nil

	case "/model":
		if arg == "" {
			fmt.Printf("current: %s (%s)\n", cfg.Model, cfg.Provider)
			printModels(cfg.Model)
			return true, nil
		}
		client, next, err := newClient(arg, retries)
		if err != nil {
			return true, err
		}
		// Refused before anything is mutated. A cost ceiling is in force and the
		// target model has no rate to measure it against, so completing this switch
		// would silently remove a limit the user asked for — mid-session, with no
		// prompt in flight to make it visible. The startup check makes the same call;
		// this is the same rule applied to the one place the model can change later.
		if err := checkCostBudget(a.CostBudget(), next); err != nil {
			return true, fmt.Errorf("staying on %s: %w", cfg.Model, err)
		}
		// The conversation is kept. Every provider here speaks the same wire
		// format, so the transcript needs no translation to move between them.
		a.SetClient(client)
		// The rate travels with the model, like the clearing threshold below: it is a
		// property of the model, so keeping the old one would measure the new model's
		// tokens at the previous model's price.
		a.SetPrice(next.Price)
		// The clearing threshold does need translating: "auto" is a fraction of the
		// model's window and the catalogue spans 262K to 1M, so carrying the old trigger
		// over would clear at a fifth of a 1M window, or set a trigger a 262K one cannot
		// reach. The spec was validated at startup, so an error here means the new
		// model has no known window and "off" is the honest answer.
		if edit, err := agent.ParseContextEdit(contextEditSpec, next.ContextWindow); err == nil {
			a.SetContextEdit(edit)
		}
		*cfg = next
		fmt.Printf("%sswitched to %s (%s), %d messages carried over%s\n",
			tui.Dim, next.Model, next.Provider, len(a.Messages()), tui.Reset)
		return true, nil

	case "/usage":
		tui.PrintUsage(os.Stdout, a.Usage(), a.Timing())
		return true, nil

	case "/compact":
		// Handled as a command rather than a prompt, so the request itself never
		// enters the conversation it is about to replace.
		return true, compact()

	case "/auto", "/strict", "/standard":
		// Answered rather than ignored: silence would leave the user believing
		// they had changed something. The terminal passes a nil ToolGate, which
		// is also what keeps -p scriptable and usable as a subagent.
		fmt.Printf("%sthe terminal has no approval gate, so every tool call runs as-is.\n"+
			"approval modes only apply to the browser UI (pi-go -web).%s\n", tui.Dim, tui.Reset)
		return true, nil

	case "/help", "/?":
		printCommands(os.Stdout)
		return true, nil
	}
	return false, nil
}

// checkCostBudget refuses a spend ceiling that cannot be enforced.
//
// -cost-budget needs a per-million-token rate, and pi-go ships none: what a model
// costs is a claim about someone's billing arrangement, and both built-in providers
// are subscription plans where per-token cost is not a quantity that exists (see
// llm.Price). So the flag has exactly two honest behaviours, and this is the one
// that matches what was asked. The other — accept the number and ignore it — is what
// it used to do: the flag was defined, documented in two languages, and plumbed all
// the way to the agent, where nothing read it. A silent no-op is worse than a
// missing feature, because the value someone typed reads back as a safeguard.
//
// Refusing to start rather than warning, unlike a bad subagent_model reference. The
// difference is that a subagent mapping has a sane degraded mode — inherit the
// parent's model, session continues — and a spend ceiling does not: "carry on with
// no limit" is the precise opposite of the request, and the run this guards is the
// kind nobody is watching.
func checkCostBudget(budget float64, cfg config.Resolved) error {
	if budget <= 0 || cfg.Price != nil {
		return nil
	}
	path, _ := config.Path()
	return fmt.Errorf("-cost-budget needs a price for %s, and none is declared.\n"+
		"pi-go ships no built-in prices: both built-in providers are subscription plans, "+
		"where there is no per-token cost to assert.\n"+
		"Either use -token-budget or -time-budget, which need no price, or declare the rate "+
		"in %s:\n"+
		"    {\"models\": [{\"id\": %q, \"provider\": %q, \"price\": "+
		"{\"input\": 0.5, \"output\": 1.5, \"cache_read\": 0.05}}]}\n"+
		"Rates are per million tokens, in whatever unit you are billed in; -cost-budget is "+
		"compared in that same unit.\n"+
		"Re-declaring a built-in model replaces it wholesale, so copy over its other fields "+
		"too — pi-go will name any it loses",
		cfg.Model, path, cfg.Model, cfg.Provider)
}

func newClient(model string, retries int) (llm.Client, config.Resolved, error) {
	cfg, err := config.Resolve(model)
	if err != nil {
		return nil, config.Resolved{}, err
	}
	return llm.New(llm.Options{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		MaxTokens:  cfg.MaxTokens,
		MaxRetries: retries,
		// Retries are announced so a rate-limited turn looks like waiting rather
		// than hanging.
		OnRetry: func(r llm.RetryInfo) {
			fmt.Fprintf(diagOut, "%s… retry %d/%d in %s: %s%s\n",
				tui.Dim, r.Attempt, r.Max, r.Delay.Round(100*time.Millisecond), tui.Summarize(r.Reason, 120), tui.Reset)
		},
	}), cfg, nil
}

func printModels(current string) {
	for _, m := range config.Catalog() {
		mark := " "
		if m.ID == current {
			mark = "*"
		}
		notes := []string{}
		if len(m.Aliases) > 0 {
			notes = append(notes, strings.Join(m.Aliases, ", "))
		}
		if !config.Configured(m.Provider) {
			notes = append(notes, config.KeyEnv(m.Provider)+" not set")
		}
		if m.SubagentModel != "" {
			// Shown because it is the one catalogue entry that changes what happens
			// without the user naming it at the prompt.
			notes = append(notes, "subagents: "+m.SubagentModel)
		}
		note := ""
		if len(notes) > 0 {
			note = fmt.Sprintf("  %s(%s)%s", tui.Dim, strings.Join(notes, "; "), tui.Reset)
		}
		fmt.Printf(" %s %-26s %-6s ctx %-5s%s\n", mark, m.ID, m.Provider, tui.HumanCtx(m.ContextWindow), note)
	}
	// Where to add one. A list of five built-ins with no hint that the set is
	// extensible is how a tool ends up looking like it only supports two vendors.
	if path, err := config.Path(); err == nil {
		state := "not present"
		if _, err := os.Stat(path); err == nil {
			state = "loaded"
		}
		fmt.Printf("\n%sadd your own providers and models in %s (%s)%s\n", tui.Dim, path, state, tui.Reset)
	}
}

// printSkills implements -skills. It resolves the working directory itself
// because the flag can run without starting a session.
// printMemory answers the two questions -memory exists for: what does the agent
// remember, and where would it look. The second is printed even when the answer to
// the first is "nothing", for the reason printSkills gives: "no notes" is much less
// useful than "no notes, and here is the directory".
func printMemory(o memory.Options) error {
	if o.Cwd == "" {
		var err error
		if o.Cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	mem, diags := memory.Load(o)

	if mem.Empty() {
		fmt.Println("no notes recorded")
	} else {
		// The prompt section itself, verbatim. Showing the model's own view rather than
		// a second rendering of the same data: a listing that can disagree with what the
		// model sees would be a place for the two to drift, and this is the file where
		// the drift would be least visible.
		fmt.Println(mem.PromptSection())
	}

	if user, err := memory.UserDir(); err == nil {
		state := ""
		if !o.User {
			state = " (disabled by -no-memory)"
		}
		fmt.Printf("%suser: %s%s%s\n", tui.Dim, user, state, tui.Reset)
	}
	project := memory.ProjectDir(o.Cwd)
	if o.Project {
		fmt.Printf("%sproject: %s%s\n", tui.Dim, project, tui.Reset)
	} else if _, err := os.Stat(project); err == nil {
		fmt.Printf("%sproject: %s exists but is not loaded; pass -project-memory%s\n",
			tui.Dim, project, tui.Reset)
	}
	for _, d := range diags {
		fmt.Fprintf(os.Stderr, "%s%s%s\n", tui.Yellow, d, tui.Reset)
	}
	return nil
}

func printSkills(o skills.Options, cwd string) error {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	o.Cwd = cwd
	list, diags := skills.Load(o)
	printSkillList(list)

	// The locations are printed even when empty: "no skills" is much less useful
	// than "no skills, and here is where I looked".
	fmt.Printf("%suser: %s%s\n", tui.Dim, skills.UserDir(), tui.Reset)
	project := skills.ProjectDir(cwd)
	if o.IncludeProject {
		fmt.Printf("%sproject: %s%s\n", tui.Dim, project, tui.Reset)
	} else if _, err := os.Stat(project); err == nil {
		fmt.Printf("%sproject: %s exists but is not loaded; pass -project-skills%s\n", tui.Dim, project, tui.Reset)
	}
	reportSkillDiagnostics(diags)
	return nil
}

func printSkillList(list []skills.Skill) {
	if len(list) == 0 {
		fmt.Printf("%sno skills loaded%s\n", tui.Dim, tui.Reset)
		return
	}
	for _, s := range list {
		notes := []string{s.Source}
		if s.DisableModelInvocation {
			notes = append(notes, "manual only")
		}
		fmt.Printf(" %-24s %s(%s)%s\n", s.Name, tui.Dim, strings.Join(notes, "; "), tui.Reset)
		fmt.Printf("   %s%s%s\n", tui.Dim, tui.Summarize(s.Description, 100), tui.Reset)
	}
}

// reportSkillDiagnostics prints load warnings to stderr. They go to stderr so
// that -p stays pipeable: a warning about someone's frontmatter must not end up
// inside the output a script is parsing.
func reportSkillDiagnostics(diags []skills.Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(diagOut, "%sskill %s: %s (%s)%s\n", tui.Dim, d.Kind, d.Message, d.Path, tui.Reset)
	}
}

func printSessions() error {
	dir := session.DefaultDir()
	list, err := session.List(dir)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Printf("%sno sessions in %s%s\n", tui.Dim, dir, tui.Reset)
		return nil
	}
	for _, s := range list {
		when := time.UnixMilli(s.Updated).Format("01-02 15:04")
		title := s.Title
		if title == "" {
			title = "(empty)"
		}
		fmt.Printf(" %s%s  %-8s %3d msg%s  %s\n",
			tui.Dim, when, s.Model, s.Messages, tui.Reset, title)
	}
	fmt.Printf("%s%s%s\n", tui.Dim, dir, tui.Reset)
	return nil
}

func openExisting(resume string) (*session.Store, error) {
	if resume == "last" {
		path, err := session.Latest(session.DefaultDir())
		if err != nil {
			return nil, err
		}
		return session.Open(path)
	}
	return session.Open(resume)
}

// reportResumed announces which transcript a run is continuing.
//
// It writes to diagOut because it is a note to a person, and stdout belongs to
// the data. On stdout it is the first line `pi-go -resume last -p ... | jq`
// reads — a bug in text mode too, not only under -mode json, which is why this
// is unconditional rather than mode-dependent.
func reportResumed(path string, messages int) {
	fmt.Fprintf(diagOut, "%sresumed %s (%d messages)%s\n", tui.Dim, tui.Link(tui.FileURL(path), path), messages, tui.Reset)
}

// reportSessionDiagnostics prints damage found while loading a transcript.
//
// A recovered session that says nothing is the failure this prevents: the count
// in "resumed ... (N messages)" is happily printed whether N is everything or
// what is left after a bad line, and only this tells the two apart.
func reportSessionDiagnostics(store *session.Store) {
	for _, d := range store.Diagnostics() {
		fmt.Fprintf(diagOut, "%ssession: %s%s\n", tui.Yellow, d, tui.Reset)
	}
}

// reportSessionDrift warns when the environment no longer matches what the
// session recorded.
//
// None of these is an error — continuing a session somewhere else is a legitimate
// thing to want. But each one changes behaviour without appearing anywhere in the
// transcript, so resuming into a different one silently is how a session stops
// being reproducible. The model is not checked here because it is not drift: the
// recorded model is used as the default.
func reportSessionDrift(store *session.Store, cwd string, skillNames []string) {
	m := store.Meta()
	if m == nil {
		return
	}
	if m.Cwd != "" && m.Cwd != cwd {
		fmt.Fprintf(diagOut, "%ssession: recorded cwd is %s but running in %s; "+
			"paths in the transcript refer to the old one%s\n", tui.Yellow, m.Cwd, cwd, tui.Reset)
	}
	if added, removed := diffNames(m.Skills, skillNames); len(added)+len(removed) > 0 {
		fmt.Fprintf(diagOut, "%ssession: skills changed since this session was created%s\n", tui.Yellow, tui.Reset)
		if len(removed) > 0 {
			fmt.Fprintf(diagOut, "%s  no longer loaded: %s%s\n", tui.Dim, strings.Join(removed, ", "), tui.Reset)
		}
		if len(added) > 0 {
			fmt.Fprintf(diagOut, "%s  newly loaded: %s%s\n", tui.Dim, strings.Join(added, ", "), tui.Reset)
		}
	}
}

// diffNames reports which names were dropped and which are new.
func diffNames(before, after []string) (added, removed []string) {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	has := make(map[string]bool, len(after))
	for _, n := range after {
		has[n] = true
		if !had[n] {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !has[n] {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// tilde collapses the home directory to ~ for display: a status line should
// cost one glance, not a full path.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~/" + rest
	}
	return p
}

// readPipedStdin returns everything piped into the process, or "" when stdin is
// a terminal. Matching pi: piped content is data, not a prompt to type at.
//
// The char-device check is what keeps this from blocking forever on an
// interactive terminal.
func readPipedStdin() (string, error) {
	if tui.IsCharDevice(os.Stdin) {
		return "", nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// buildPrompt combines piped input with the -p flag. The piped document goes
// first and the instruction last, which is both pi's order and the one models
// follow best: the ask is the freshest thing in the context.
func buildPrompt(piped, flagPrompt string) string {
	switch {
	case piped == "":
		return flagPrompt
	case flagPrompt == "":
		return piped
	default:
		return piped + "\n\n" + flagPrompt
	}
}

// tapEvents forwards every event to the consumer unchanged, letting f see each
// one first. The channel hop is what lets incremental persistence ride the
// stream without the consumer interface growing a method.
func tapEvents(in <-chan agent.Event, f func(agent.Event)) <-chan agent.Event {
	out := make(chan agent.Event)
	go func() {
		defer close(out)
		for e := range in {
			f(e)
			out <- e
		}
	}()
	return out
}

// analyzeSessionFile analyzes a session JSONL file and outputs statistics.
//
// A directory is analysed too, and answers a different question: one file reports
// what a session cost, a directory reports how long runs actually take, which is
// the number a turn cap has to be chosen from. The two are separate because they
// are not the same unit — see analyze.Run.
//
// The literal "sessions" resolves to session.DefaultDir(), because the whole point
// of the aggregate is one's own history and nobody remembers where it is kept.
func analyzeSessionFile(path, format, outputPath string) error {
	if path == "sessions" {
		path = session.DefaultDir()
	}
	config := analyze.Config{
		IncludeTurns: false, // Set to true for verbose mode
	}

	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return analyzeRunLengths(path, format, outputPath, config)
	}

	stats, err := analyze.AnalyzeSession(path, config)
	if err != nil {
		return fmt.Errorf("failed to analyze session: %w", err)
	}

	var output string
	switch format {
	case "json":
		output, err = analyze.FormatJSON(stats)
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}
	case "text":
		output = analyze.FormatText(stats)
	default:
		return fmt.Errorf("invalid format %q: use 'text' or 'json'", format)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Analysis written to %s\n", outputPath)
	} else {
		fmt.Println(output)
	}

	return nil
}

// analyzeRunLengths reports the turn-count distribution across a directory of
// sessions. Shares the format and output flags with the single-session report,
// since the difference is the population, not the plumbing.
func analyzeRunLengths(dir, format, outputPath string, config analyze.Config) error {
	report, err := analyze.AnalyzeRuns(dir, config)
	if err != nil {
		return fmt.Errorf("failed to analyze sessions: %w", err)
	}

	var output string
	switch format {
	case "json":
		output, err = analyze.FormatRunsJSON(report)
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}
	case "text":
		output = analyze.FormatRunsText(report)
	default:
		return fmt.Errorf("invalid format %q: use 'text' or 'json'", format)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Analysis written to %s\n", outputPath)
		return nil
	}
	fmt.Println(output)
	return nil
}
