package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/wangy/pi-go/tui"
)

// flagOrder groups the help output logically instead of alphabetically. Any flag
// missing from this list still gets printed, so adding one cannot silently drop
// it from the help.
var flagOrder = []string{
	"p", "model", "models", "sessions", "C", "resume", "mode", "quiet",
	"max-turns", "soft-turns", "max-runs", "evaluate", "retries",
	"skills", "skill", "no-skills", "project-skills",
	"worktrees", "worktrees-prune",
	"web", "listen", "web-dev", "gate-timeout",
}

// usage prints bilingual help. Flag rows are generated from the actual flag set
// rather than hand-written, so the help cannot describe a flag that no longer
// exists.
func usage() {
	w := flag.CommandLine.Output()

	fmt.Fprint(w, `pi-go — a minimal coding agent: one loop, seven tools, one protocol.
pi-go — 极简 coding agent：一个循环、七个工具、一种协议。

USAGE / 用法
  pi-go [flags]                 interactive mode / 交互模式
  pi-go [flags] -p "<prompt>"   run once and exit / 跑一次就退出
  <stdin> | pi-go [flags]       piped input becomes the prompt / 管道内容作为 prompt
  pi-go -web [flags]            serve the browser UI / 启动浏览器界面

FLAGS / 参数
`)
	printFlags(w)

	fmt.Fprintln(w)
	printCommands(w)

	fmt.Fprint(w, `
ENVIRONMENT / 环境变量
  KIMI_API_KEY      Required for kimi models. / 使用 kimi 模型时必需
  ZHIPU_API_KEY     Required for zhipu models. / 使用 zhipu 模型时必需
  KIMI_BASE_URL     Override the kimi endpoint (proxy or mirror).
                    覆盖 kimi 端点（代理或镜像）
  ZHIPU_BASE_URL    Override the zhipu endpoint.
                    覆盖 zhipu 端点
  PIGO_WEB_TOKEN    Pin the -web token instead of generating one per run.
                    固定 -web 的访问 token，否则每次启动随机生成
  PIGO_SESSION_DIR  Where sessions are stored. Default ~/.pi-go/sessions
                    会话存放目录，默认 ~/.pi-go/sessions

  Credentials are read from the environment only; no config file is consulted.
  密钥只从环境变量读取，不读任何配置文件。

EXAMPLES / 示例
  pi-go -p "count the lines of Go code here"
  pi-go -model glm-5.2 -p "这个项目的入口在哪"
  cat notes.md | pi-go -p "extract the action items"
  git diff | pi-go -p "review 这个 diff，只说有问题的地方"
  pi-go -resume last -p "carry on from where we stopped"

  Piped input is placed before the -p instruction, so the ask stays last.
  管道内容置于 -p 指令之前，指令留在最后。
`)
}

// printCommands lists the interactive commands. Shared by -h and /help so the
// two can never disagree. The command data itself lives in tui.Commands — the
// editor's Tab completion matches against the same table.
func printCommands(w io.Writer) {
	fmt.Fprintln(w, "COMMANDS (interactive mode) / 交互模式命令")
	for _, c := range tui.Commands {
		fmt.Fprintf(w, "  %-16s %s\n", c.Usage, c.En)
		fmt.Fprintf(w, "  %-16s %s\n", "", c.Zh)
	}
	fmt.Fprintln(w, "  Tab completes commands and model names; candidates appear live as you type,"+
		" with descriptions.\n                    Tab 补全命令和模型名，候选随输入实时显示、带说明")
	fmt.Fprintln(w, `  Enter submits. A literal newline: trailing \ + Enter, Ctrl-J, or Alt+Enter;`+
		" pasted newlines are kept.\n                    Enter 提交；换行用行尾 \\ + Enter、Ctrl-J 或 Alt+Enter；粘贴的换行原样保留")
}

// resolveCommand expands a slash-command prefix to the first command the live
// completion would list, so Enter on "/e" runs /exit instead of erroring — the
// candidate was right there on screen. Exact names and non-matching input pass
// through unchanged (a typo still hits the caller's unknown-command branch),
// and a bare "/" is not treated as a prefix: every command would match. The
// exact pass runs before the prefix pass so a full name can never be hijacked
// by an earlier entry that merely starts with it.
//
// Commands marked NoAbbrev are skipped by the prefix pass: expanding a guess into
// something that cannot be undone is a different trade from expanding it into /exit.
// They still Tab-complete, which is the version of the convenience that shows you
// the full name before you commit to it.
// commandWord reports the slash command a prompt is, or "".
//
// Only an exact leading word counts, so "/usr/local/bin is on PATH?" stays a prompt
// — the same rule the web layer applies for the same reason. No prefix expansion
// here on purpose: a scripted `-p` has no completion list to have shown the user
// what a guess would become.
func commandWord(prompt string) string {
	word, _, _ := strings.Cut(strings.TrimSpace(prompt), " ")
	for _, c := range tui.Commands {
		if c.Name == word {
			return word
		}
	}
	return ""
}

func resolveCommand(line string) string {
	word, arg, _ := strings.Cut(line, " ")
	if len(word) < 2 || !strings.HasPrefix(word, "/") {
		return line
	}
	for _, c := range tui.Commands {
		if c.Name == word {
			return line
		}
	}
	for _, c := range tui.Commands {
		if c.NoAbbrev {
			continue
		}
		if strings.HasPrefix(c.Name, word) {
			if arg == "" {
				return c.Name
			}
			return c.Name + " " + arg
		}
	}
	return line
}

// printFlags renders one row per flag: English on the first line, Chinese on the
// second, with the default value appended when it is meaningful.
func printFlags(w io.Writer) {
	const indent = "  "
	const descCol = 22 // width of the name column, sized to the longest label

	seen := map[string]bool{}
	render := func(f *flag.Flag) {
		if f == nil || seen[f.Name] {
			return
		}
		seen[f.Name] = true

		argName, desc := flag.UnquoteUsage(f)
		label := "-" + f.Name
		if argName != "" {
			label += " <" + argName + ">"
		}

		en, zh, _ := strings.Cut(desc, "\n")
		if d := defaultOf(f); d != "" {
			en += " Default: " + d
		}

		pad := max(descCol-len(label), 1)
		fmt.Fprintf(w, "%s%s%s%s\n", indent, label, strings.Repeat(" ", pad), en)
		if zh != "" {
			fmt.Fprintf(w, "%s%s%s\n", indent, strings.Repeat(" ", descCol), zh)
		}
	}

	for _, name := range flagOrder {
		render(flag.Lookup(name))
	}
	// Anything not in flagOrder still shows up.
	flag.VisitAll(render)
}

// defaultOf returns a printable default, skipping the zero values where showing
// "false" or an empty string would be noise.
func defaultOf(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0":
		return ""
	}
	return f.DefValue
}
