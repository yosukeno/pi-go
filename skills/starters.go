package skills

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Starter cards are what a deployment puts on an empty conversation: a few
// concrete openings that show what this particular agent is for.
//
// They live in the skill directory rather than in the UI or a flag, for the same
// reason the instructions do: the domain belongs to the skill. pi-go itself must
// stay ignorant of what its deployment analyses — it renders cards, it does not
// know what a sample or a Yara rule is. A skill mounted over the container's
// skill path therefore changes the empty state too, with no rebuild.
//
// The file is read per request rather than at load time, which is what makes an
// edited starters.json take effect without a restart (Invocation does the same
// for the instructions).
const StartersFile = "starters.json"

// Limits are shaped by the layout, not by the format. Cards are a row of
// buttons: past six they stop being a glance and start being a menu, which is
// also where the suggestion APIs in comparable products land.
const (
	MaxStarterCards   = 6
	MaxStarterHeading = 60
	MaxStarterTitle   = 40
	MaxStarterLabel   = 30
	MaxStarterPrompt  = 2000
	// Follow-ups render as chips on one line rather than as cards, so they run
	// out of room sooner. Groups are capped only to keep a runaway file bounded.
	MaxFollowupChips  = 4
	MaxFollowupGroups = 8
)

// starterIcons is a closed set because the alternative is letting a file hand the
// page markup. The names are intents rather than pictures, so the UI can restyle
// them without every deployment's file going stale.
var starterIcons = map[string]bool{
	"search": true, "code": true, "shield": true, "graph": true,
	"file": true, "terminal": true, "spark": true, "book": true,
}

// StarterCard is one card. It carries exactly one action:
//
//   - Prompt: put this text in the composer.
//   - Panel (+ optional At): open this dock panel, optionally at a hash route.
//
// Two verbs is the whole vocabulary. Keeping it that small is what stops this
// from growing into a way for a skill to drive the UI.
type StarterCard struct {
	Icon   string `json:"icon,omitempty"`
	Title  string `json:"title"`
	Label  string `json:"label,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Panel  string `json:"panel,omitempty"`
	At     string `json:"at,omitempty"`
}

// Starters is one skill's contribution to the empty state.
type Starters struct {
	Heading string `json:"heading,omitempty"`
	// Send makes a card's prompt go out on click instead of landing in the
	// composer. Default false: pi-go's rule elsewhere is that it never speaks for
	// the user, and a deployment that wants one-click demos opts in explicitly.
	Send  bool          `json:"send,omitempty"`
	Cards []StarterCard `json:"cards"`
	// Followups offer the next step once a turn has finished. They are
	// deliberately deterministic: the alternative is asking the model what to
	// suggest, which spends a call and adds latency on every turn, and the worst
	// thing a follow-up can do is make the answer feel slower than it was.
	Followups []FollowupGroup `json:"followups,omitempty"`
	// Skill names the contributor, for diagnostics and for the UI to attribute.
	Skill string `json:"skill,omitempty"`
}

// FollowupGroup is a set of chips and the condition for showing them.
//
// When is a list of substrings matched against what the last turn actually did —
// its tool calls and its reply. That keeps the suggestion relevant without a
// model call: "the agent just ran mal-decompile" is a fact already on screen.
// No match means no chips, which is the point. A fixed row shown after every
// turn regardless of context would be interface noise, and half of it would be
// suggesting a step that makes no sense for what just happened.
//
// pi-go does not know what any of these strings mean. It matches text.
type FollowupGroup struct {
	When  []string      `json:"when"`
	Chips []StarterCard `json:"chips"`
}

// LoadStarters reads every loaded skill's starters.json.
//
// A skill without the file contributes nothing, which is the common case and not
// a problem. A malformed one is reported and skipped: the empty state falls back
// to its built-in hint, so a bad file costs a feature rather than the page.
func LoadStarters(list []Skill) ([]Starters, []Diagnostic) {
	var out []Starters
	var diags []Diagnostic
	for _, sk := range list {
		if sk.Dir == "" {
			continue
		}
		path := filepath.Join(sk.Dir, StartersFile)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				diags = append(diags, Diagnostic{Kind: "warning", Message: err.Error(), Path: path})
			}
			continue
		}
		st, d := parseStarters(data, path)
		diags = append(diags, d...)
		if st == nil {
			continue
		}
		st.Skill = sk.Name
		out = append(out, *st)
	}
	return out, diags
}

func parseStarters(data []byte, path string) (*Starters, []Diagnostic) {
	var diags []Diagnostic
	warn := func(msg string) {
		diags = append(diags, Diagnostic{Kind: "warning", Message: msg, Path: path})
	}

	// Unknown fields are an error rather than ignored: a typo'd "prompts" key
	// would otherwise produce a card that silently does nothing, and the author
	// would have no way to tell that from a UI bug.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw Starters
	if err := dec.Decode(&raw); err != nil {
		warn(StartersFile + " is not valid: " + err.Error())
		return nil, diags
	}

	if n := len([]rune(raw.Heading)); n > MaxStarterHeading {
		warn("heading exceeds " + strconv.Itoa(MaxStarterHeading) + " characters; dropped")
		raw.Heading = ""
	}

	kept := make([]StarterCard, 0, len(raw.Cards))
	for i, c := range raw.Cards {
		if len(kept) == MaxStarterCards {
			warn("more than " + strconv.Itoa(MaxStarterCards) + " cards; the rest are ignored")
			break
		}
		if msg := validateStarterCard(c); msg != "" {
			warn("card " + strconv.Itoa(i+1) + ": " + msg + "; skipped")
			continue
		}
		c.Title = strings.TrimSpace(c.Title)
		c.Label = strings.TrimSpace(c.Label)
		c.Prompt = strings.TrimSpace(c.Prompt)
		kept = append(kept, c)
	}
	groups := make([]FollowupGroup, 0, len(raw.Followups))
	for i, g := range raw.Followups {
		if len(groups) == MaxFollowupGroups {
			warn("more than " + strconv.Itoa(MaxFollowupGroups) + " followup groups; the rest are ignored")
			break
		}
		g, msg := validateFollowupGroup(g)
		if msg != "" {
			warn("followup " + strconv.Itoa(i+1) + ": " + msg + "; skipped")
			continue
		}
		groups = append(groups, g)
	}

	// A file may contribute an empty state, follow-ups, or both — but one of
	// them, or it is doing nothing and the author should hear about it.
	if len(kept) == 0 && len(groups) == 0 {
		warn(StartersFile + " has no usable cards or followups")
		return nil, diags
	}
	raw.Cards = kept
	raw.Followups = groups
	return &raw, diags
}

// validateFollowupGroup returns the cleaned group, or the reason it cannot be
// shown. Trimming happens here so an author's stray whitespace in `when` does
// not quietly make a group unmatchable.
func validateFollowupGroup(g FollowupGroup) (FollowupGroup, string) {
	when := make([]string, 0, len(g.When))
	for _, w := range g.When {
		if w = strings.TrimSpace(w); w != "" {
			when = append(when, w)
		}
	}
	if len(when) == 0 {
		return g, `"when" needs at least one non-empty string`
	}
	if len(g.Chips) == 0 {
		return g, "needs at least one chip"
	}
	if len(g.Chips) > MaxFollowupChips {
		return g, "more than " + strconv.Itoa(MaxFollowupChips) + " chips"
	}
	chips := make([]StarterCard, 0, len(g.Chips))
	for i, c := range g.Chips {
		if msg := validateStarterCard(c); msg != "" {
			return g, "chip " + strconv.Itoa(i+1) + ": " + msg
		}
		c.Title = strings.TrimSpace(c.Title)
		c.Label = strings.TrimSpace(c.Label)
		c.Prompt = strings.TrimSpace(c.Prompt)
		chips = append(chips, c)
	}
	return FollowupGroup{When: when, Chips: chips}, ""
}

// validateStarterCard returns the reason a card cannot be shown, or "".
func validateStarterCard(c StarterCard) string {
	title := strings.TrimSpace(c.Title)
	if title == "" {
		return "title is required"
	}
	if len([]rune(title)) > MaxStarterTitle {
		return "title exceeds " + strconv.Itoa(MaxStarterTitle) + " characters"
	}
	if len([]rune(strings.TrimSpace(c.Label))) > MaxStarterLabel {
		return "label exceeds " + strconv.Itoa(MaxStarterLabel) + " characters"
	}
	if c.Icon != "" && !starterIcons[c.Icon] {
		return "unknown icon " + strconv.Quote(c.Icon)
	}

	prompt := strings.TrimSpace(c.Prompt)
	panel := strings.TrimSpace(c.Panel)
	switch {
	case prompt == "" && panel == "":
		return "needs either a prompt or a panel"
	case prompt != "" && panel != "":
		return "has both a prompt and a panel; pick one"
	}

	if panel != "" {
		if c.At != "" && !strings.HasPrefix(c.At, "#/") {
			return `"at" must start with "#/"`
		}
		return ""
	}

	if c.At != "" {
		return `"at" only applies to a panel card`
	}
	if len([]rune(prompt)) > MaxStarterPrompt {
		return "prompt exceeds " + strconv.Itoa(MaxStarterPrompt) + " characters"
	}
	// A slash command must never become conversation content — that guard exists
	// so one injected line cannot disable the approval gate, and it holds
	// regardless of who wrote the file. /skill:name is the exception because it
	// is a prompt that expands, not a command that acts.
	if strings.HasPrefix(prompt, "/") {
		if _, _, ok := ParseCommand(prompt); !ok {
			return "prompt looks like a slash command; commands are not prompts"
		}
	}
	return ""
}
