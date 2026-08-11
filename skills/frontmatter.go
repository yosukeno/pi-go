package skills

import "strings"

// parseFrontmatter splits a YAML frontmatter block from a markdown body.
//
// This is a hand-written parser for the small subset skills actually use:
// top-level scalars, optionally quoted, plus `>` and `|` block scalars because a
// 1024-character description will be wrapped by anyone writing one by hand.
// Nested maps and lists are recognised only well enough to be skipped.
//
// The reason not to pull in a YAML library is that pi-go has no third-party
// dependencies at all, and one frontmatter block is a poor first reason to
// acquire one. The cost is real and worth stating: `metadata:` sub-keys are
// dropped, and anything relying on YAML aliases, flow mappings, or multi-document
// files will not parse the way its author expects.
//
// A missing or unterminated block is not an error. It yields no fields and the
// whole input as the body, which matches pi and means a malformed file still
// produces a diagnostic about the *missing description* rather than a parse error
// the author cannot act on.
func parseFrontmatter(content string) (map[string]string, string) {
	text := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	text = strings.TrimPrefix(text, "\ufeff")

	if !strings.HasPrefix(text, "---\n") && text != "---" {
		return nil, text
	}
	rest := text[3:]
	rest = strings.TrimPrefix(rest, "\n")

	end := -1
	lines := strings.Split(rest, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "---" || strings.TrimRight(line, " \t") == "..." {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, text
	}

	fields := map[string]string{}
	body := strings.Join(lines[end+1:], "\n")
	head := lines[:end]

	for i := 0; i < len(head); i++ {
		line := head[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Anything indented at this point belongs to a structure we already
		// decided to skip; consuming it as a top-level key is the one way this
		// parser could invent fields that are not there.
		if indentOf(line) > 0 {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}

		switch {
		case value == "":
			// Either an empty scalar or the header of a nested map or list. Both
			// are stored as empty; the following indented lines are skipped by the
			// indent check above.
			fields[key] = ""
		case value[0] == '>' || value[0] == '|':
			text, next := readBlockScalar(head, i+1, value[0] == '|')
			fields[key] = text
			i = next - 1
		default:
			fields[key] = unquote(value)
		}
	}
	return fields, body
}

// readBlockScalar consumes the indented lines that follow a `>` or `|` marker.
// Folded joins lines with a space and keeps blank lines as paragraph breaks;
// literal keeps the line structure. Trailing whitespace is always chomped, so the
// chomping indicators are accepted and ignored.
func readBlockScalar(lines []string, start int, literal bool) (string, int) {
	var block []string
	i := start
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			block = append(block, "")
			continue
		}
		if indentOf(line) == 0 {
			break
		}
		block = append(block, strings.TrimSpace(line))
	}
	// Drop the trailing blank lines a block picks up from the separator line.
	for len(block) > 0 && block[len(block)-1] == "" {
		block = block[:len(block)-1]
	}
	if literal {
		return strings.Join(block, "\n"), i
	}
	var b strings.Builder
	for j, line := range block {
		switch {
		case line == "":
			b.WriteString("\n")
		case j > 0 && block[j-1] != "":
			b.WriteString(" ")
			b.WriteString(line)
		default:
			b.WriteString(line)
		}
	}
	return b.String(), i
}

func indentOf(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// unquote strips one layer of matching quotes and undoes the escapes those
// quotes make necessary. Unquoted values also lose a trailing comment, which is
// the one piece of YAML syntax a hand-written description is likely to hit by
// accident.
func unquote(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			inner := s[1 : len(s)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			return strings.ReplaceAll(inner, `\\`, `\`)
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		}
	}
	if idx := strings.Index(s, " #"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
