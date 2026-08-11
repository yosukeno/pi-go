// Package skills discovers on-demand capability packages and renders them for
// the system prompt.
//
// A skill is a directory with a SKILL.md: frontmatter naming and describing it,
// plus freeform instructions and whatever scripts or reference documents those
// instructions point at. Only the name, description and location go into the
// prompt; the body is loaded by the model with the read tool when a task matches.
// That is the whole mechanism, and it is pi's (see agentskills.io) so that a skill
// directory can be shared between the two.
//
// This package deliberately does not depend on agent or tools. The prompt section
// is a string the caller passes to agent.SystemPrompt, and the readable roots are
// a []string the caller passes to tools.Default. That keeps skills out of the loop
// and out of the path guard, both of which stay unaware the concept exists.
package skills

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Limits from the Agent Skills specification. Exceeding them is a warning, not a
// refusal: a skill with a 70-character name is still a usable skill, and refusing
// to load it would punish the user for the author's mistake.
const (
	MaxNameLength        = 64
	MaxDescriptionLength = 1024
	// MaxBodyBytes is not in the specification. It exists because the read tool
	// truncates at 50KB, and a SKILL.md over that limit would be silently
	// half-loaded — the model gets a "use offset to continue" note it often
	// ignores. Warning at load time turns a confusing runtime behaviour into a
	// message the author can act on.
	MaxBodyBytes = 40 * 1024
)

var nameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// Skill is one discovered skill. The body is not held in memory: it is read from
// Path on demand, which is what makes progressive disclosure actually save
// context rather than just delay it.
type Skill struct {
	Name        string
	Description string
	// Path is the absolute path to SKILL.md, and what goes into the prompt.
	Path string
	// Dir is the directory relative references inside the skill resolve against.
	Dir string
	// Source is "user", "project" or "path", for listings and diagnostics.
	Source string
	// DisableModelInvocation hides the skill from the prompt. It stays reachable
	// through /skill:name, which is the point: some skills are destructive enough
	// that a person should be the one deciding.
	DisableModelInvocation bool
}

// Diagnostic is a problem found while loading. Everything here is a warning;
// skills that cannot be used at all are simply not returned.
type Diagnostic struct {
	// Kind is "warning" or "collision".
	Kind    string
	Message string
	Path    string
}

type Options struct {
	// Cwd is the project directory, used for the project location and to resolve
	// relative entries in Paths.
	Cwd string
	// Paths are explicit --skill entries, files or directories. They load even
	// when Disabled is set: an explicit request is not a default.
	Paths []string
	// Disabled skips the default locations.
	Disabled bool
	// IncludeProject enables <cwd>/.pi-go/skills.
	//
	// Off by default, which is a deliberate deviation from pi. pi loads project
	// skills once the project is trusted, and pi-go has no trust store. Since a
	// skill is a file that rewrites the system prompt, loading one from a
	// freshly cloned repository would be prompt injection with no prompt: the
	// first turn would already be running someone else's instructions.
	IncludeProject bool
	// UserDir overrides the user location. Empty means ~/.pi-go/skills.
	UserDir string
}

// UserDir is the default user-level location.
func UserDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi-go", "skills")
}

// ProjectDir is the project-level location for a working directory.
func ProjectDir(cwd string) string { return filepath.Join(cwd, ".pi-go", "skills") }

// Load discovers skills from the configured locations.
//
// Order is user, then project, then explicit paths, and the first skill to claim a
// name keeps it. That makes a user-level skill win over a project-level one of the
// same name, which is both what pi does and the safer direction: a repository
// cannot quietly shadow a skill you wrote yourself.
func Load(o Options) ([]Skill, []Diagnostic) {
	l := &loader{byName: map[string]Skill{}, seen: map[string]bool{}}

	if !o.Disabled {
		userDir := o.UserDir
		if userDir == "" {
			userDir = UserDir()
		}
		if userDir != "" {
			l.scan(userDir, true, "user")
		}
		if o.IncludeProject && o.Cwd != "" {
			l.scan(ProjectDir(o.Cwd), true, "project")
		}
	}

	for _, p := range o.Paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) && o.Cwd != "" {
			abs = filepath.Join(o.Cwd, abs)
		}
		info, err := os.Stat(abs)
		if err != nil {
			l.warn(abs, "skill path does not exist")
			continue
		}
		switch {
		case info.IsDir():
			l.scan(abs, true, "path")
		case strings.HasSuffix(abs, ".md"):
			l.loadFile(abs, "path")
		default:
			l.warn(abs, "skill path is not a directory or a .md file")
		}
	}

	out := make([]Skill, 0, len(l.byName))
	for _, s := range l.byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// Collisions come last so the actionable warnings are not buried under them.
	return out, append(l.diags, l.collisions...)
}

// Roots returns the directories that must become readable for these skills to be
// usable, deduplicated.
//
// Per-skill directories rather than the scan roots: a skill needs to reach its own
// scripts and references and nothing else, and the narrower set is the one worth
// handing to the path guard.
func Roots(list []Skill) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		if s.Dir == "" || seen[s.Dir] {
			continue
		}
		seen[s.Dir] = true
		out = append(out, s.Dir)
	}
	return out
}

// Names returns the loaded skill names, for the session record.
func Names(list []Skill) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

// Find looks a skill up by name.
func Find(list []Skill, name string) (Skill, bool) {
	for _, s := range list {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

type loader struct {
	byName     map[string]Skill
	seen       map[string]bool // canonical paths, so a symlinked skill loads once
	diags      []Diagnostic
	collisions []Diagnostic
}

func (l *loader) warn(path, msg string) {
	l.diags = append(l.diags, Diagnostic{Kind: "warning", Message: msg, Path: path})
}

// scan walks a directory looking for skills.
//
// A directory containing SKILL.md is a skill root and is not descended into: the
// scripts and references inside it are its private business, and recursing would
// turn a bundled example into a second skill. Root-level .md files count as skills
// too, which is what makes a one-file skill possible without a directory each.
func (l *loader) scan(dir string, includeRootFiles bool, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			l.warn(dir, err.Error())
		}
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if e.Name() != "SKILL.md" || e.IsDir() {
			continue
		}
		l.loadFile(filepath.Join(dir, e.Name()), source)
		return
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		full := filepath.Join(dir, name)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			st, err := os.Stat(full)
			if err != nil {
				continue // broken link
			}
			isDir = st.IsDir()
		}
		if isDir {
			l.scan(full, false, source)
			continue
		}
		if includeRootFiles && strings.HasSuffix(name, ".md") {
			l.loadFile(full, source)
		}
	}
}

func (l *loader) loadFile(path, source string) {
	real := canonical(path)
	if l.seen[real] {
		return // same file reached twice through a symlink
	}

	data, err := os.ReadFile(path)
	if err != nil {
		l.warn(path, err.Error())
		return
	}
	fields, body := parseFrontmatter(string(data))
	dir := filepath.Dir(path)

	description := strings.TrimSpace(fields["description"])
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		name = filepath.Base(dir)
	}

	// A skill with no description cannot be chosen: the description is the entire
	// basis on which the model decides. This is the one hard failure.
	if description == "" {
		l.warn(path, "description is required; skill not loaded")
		return
	}
	if len(description) > MaxDescriptionLength {
		l.warn(path, "description exceeds "+strconv.Itoa(MaxDescriptionLength)+" characters")
	}
	for _, msg := range validateName(name) {
		l.warn(path, msg)
	}
	if len(body) > MaxBodyBytes {
		l.warn(path, "SKILL.md body is larger than "+strconv.Itoa(MaxBodyBytes/1024)+
			"KB and will be truncated when the model reads it; move detail into reference files")
	}

	if existing, dup := l.byName[name]; dup {
		l.collisions = append(l.collisions, Diagnostic{
			Kind:    "collision",
			Message: "name " + name + " already loaded from " + existing.Path + "; this one is ignored",
			Path:    path,
		})
		return
	}
	l.seen[real] = true
	l.byName[name] = Skill{
		Name:                   name,
		Description:            description,
		Path:                   path,
		Dir:                    dir,
		Source:                 source,
		DisableModelInvocation: truthy(fields["disable-model-invocation"]),
	}
}

// validateName reports specification violations. pi-go follows pi in not
// requiring the name to match the parent directory, because that rule makes a
// skill directory shared between harnesses harder to keep valid than it is worth.
func validateName(name string) []string {
	var out []string
	if len(name) > MaxNameLength {
		out = append(out, "name exceeds "+strconv.Itoa(MaxNameLength)+" characters")
	}
	if !nameRE.MatchString(name) {
		out = append(out, "name "+name+" has invalid characters (lowercase a-z, 0-9 and hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		out = append(out, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		out = append(out, "name must not contain consecutive hyphens")
	}
	return out
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// canonical resolves symlinks so that the same file reached two ways is
// recognised as one skill rather than loaded twice or reported as a collision
// with itself.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
