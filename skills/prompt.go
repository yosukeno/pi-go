package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PromptSection renders the skills for the system prompt.
//
// Only names, descriptions and locations go in; the instructions stay on disk
// until the model reads them. That is the whole idea, and it is what keeps twenty
// skills costing a few hundred tokens a turn instead of a few hundred thousand.
//
// The XML shape and the first two instruction lines are pi's, verbatim, so a
// skill written for either harness behaves the same in both. The third and fourth
// lines are pi-go's: pi has no path guard, so it never had to say that a skill
// directory is readable, and it has no ls tool to point at.
//
// Skills with disable-model-invocation are left out. They are still reachable
// through /skill:name, which is the point of the flag.
func PromptSection(list []Skill) string {
	var visible []Skill
	for _, s := range list {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("The following skills provide specialized instructions for specific tasks.\n")
	b.WriteString("Use the read tool to load a skill's file when the task matches its description.\n")
	b.WriteString("When a skill file references a relative path, resolve it against the skill directory " +
		"(the directory containing SKILL.md) and use that absolute path in tool commands.\n")
	b.WriteString("Skill directories are outside the working directory: you can read and ls inside them, " +
		"but you cannot write to them.\n\n")
	b.WriteString("<available_skills>\n")
	for _, s := range visible {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", escapeXML(s.Name))
		fmt.Fprintf(&b, "    <description>%s</description>\n", escapeXML(s.Description))
		fmt.Fprintf(&b, "    <location>%s</location>\n", escapeXML(s.Path))
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// Invocation reads a skill and formats it as the text of a user message, for
// /skill:name. The wrapper is pi's format down to the wording, because the block
// ends up in the transcript and a transcript that reads the same in both harnesses
// is worth more than a nicer sentence.
//
// Reading here rather than at load time is what makes an edited skill take effect
// without restarting.
func Invocation(s Skill, extra string) (string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return "", err
	}
	_, body := parseFrontmatter(string(data))
	block := fmt.Sprintf("<skill name=%q location=%q>\nReferences are relative to %s.\n\n%s\n</skill>",
		s.Name, s.Path, s.Dir, strings.TrimSpace(body))
	if extra = strings.TrimSpace(extra); extra != "" {
		return block + "\n\n" + extra, nil
	}
	return block, nil
}

// ParseCommand splits a /skill:name form into its parts. It reports false for any
// other line so the caller can treat it as an ordinary prompt.
func ParseCommand(line string) (name, args string, ok bool) {
	const prefix = "/skill:"
	if !strings.HasPrefix(line, prefix) {
		return "", "", false
	}
	rest := line[len(prefix):]
	name, args, _ = strings.Cut(rest, " ")
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}

// MatchRead reports which skill, if any, a read call was loading.
//
// It matches on the call's *arguments*, not on the tool's result details. The
// original reason was that details never reached disk, so a badge derived from them
// vanished on reload; that premise is gone, because llm.Block.Details persists now.
// The choice stands on a simpler reason: which file was read is part of the call
// itself, so there is no cause to detour through the tool's return value to ask.
//
// cwd is needed because the model may pass a relative path.
func MatchRead(list []Skill, cwd string, args json.RawMessage) (Skill, bool) {
	if len(list) == 0 || len(args) == 0 {
		return Skill{}, false
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil || a.Path == "" {
		return Skill{}, false
	}
	path := a.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	target := canonical(filepath.Clean(path))
	for _, s := range list {
		if canonical(s.Path) == target {
			return s, true
		}
	}
	return Skill{}, false
}

func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
