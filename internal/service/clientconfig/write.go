package clientconfig

import (
	"encoding/json"
	"regexp"
	"strings"

	"claudication/internal/service/clients"
)

// The rewrites: each committed file with one client's names put in every
// model field, and each name described with the facts of the model it serves
// now — the context window, the output cap, the effort levels. Each takes the
// committed file and gives back the client's; one that cannot read the file
// gives an error, and the caller hands out the committed file instead, which
// still works for the type's default names.

// CrushWindow is the context window crush is told for a model whose real one
// is larger.
//
// crush compacts when the room left in the window it is given falls to 20k,
// so this number is its compaction point plus 20k; at the model's real 1M it
// compacted at 980k, and sessions ran there. See docs/clients.md, "When crush
// compacts", for the measurements behind 600k.
const CrushWindow = 600_000

// effortLevels are the levels in the order a picker lists them.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// Efforts are the effort levels a model accepts, in picker order; none when
// it has no effort setting or was not read. What every file declares of a
// model, and what the MCP server's list_models says of one.
func Efforts(m *Model) []string { return efforts(m) }

func efforts(m *Model) []string {
	if m == nil {
		return nil
	}
	e := m.Capabilities.Effort
	var supported bool
	if json.Unmarshal(e["supported"], &supported) != nil || !supported {
		return nil
	}
	var out []string
	for _, l := range effortLevels {
		var c struct {
			Supported bool `json:"supported"`
		}
		if json.Unmarshal(e[l], &c) == nil && c.Supported {
			out = append(out, l)
		}
	}
	return out
}

// unique is each name once, in slot order: two slots may share a name on a
// custom client.
func unique(slots []Slot) []Slot {
	seen := map[string]bool{}
	var out []Slot
	for _, s := range slots {
		if !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	return out
}

// byRole is the slot of a role, or the first.
func (s Setup) byRole(role clients.Role) (Slot, bool) {
	for _, sl := range s.Slots {
		if sl.Role == role {
			return sl, true
		}
	}
	if len(s.Slots) > 0 {
		return s.Slots[0], true
	}
	return Slot{}, false
}

// find is the slot of a role, without the fallback.
func (s Setup) find(role clients.Role) (Slot, bool) {
	for _, sl := range s.Slots {
		if sl.Role == role {
			return sl, true
		}
	}
	return Slot{}, false
}

// ── Claude Code ─────────────────────────────────────────────────────────────

// claudeCodeModelVar are the variables that name a model, whichever of them a
// client has a slot for.
var claudeCodeModelVar = regexp.MustCompile(`^(ANTHROPIC_MODEL|ANTHROPIC_SMALL_FAST_MODEL|ANTHROPIC_DEFAULT_[A-Z]+_MODEL|CLAUDE_CODE_AUTO_MODE_MODEL)$`)

var exportLine = regexp.MustCompile(`^export ([A-Z_]+)=(.*)$`)

// envExport is any variable's export line, as an update reads an env file.
var envExport = regexp.MustCompile(`^export ([A-Za-z_][A-Za-z0-9_]*)=`)

// settingsArg is the --settings JSON in the wrapper. Single quotes end it: no
// JSON the file carries has one, since a model name cannot, and a greedy
// match up to the last brace would run on into the --mcp-config argument
// beside it.
var settingsArg = regexp.MustCompile(`--settings '(\{[^']*\})'`)

// claudeCodeSh writes claude-code.sh: each model variable set to its slot's
// name, and every name the picker lists told which real model it behaves
// like. The auto-mode classifier's name is not in the picker: nobody picks
// it. A picker name of the operator's own has no variable and only its row,
// which is what lets /model <name> take it without a probe and handle it as
// its model (internal/service/clients, ClaudeCode).
func claudeCodeSh(body string, setup Setup) (string, error) {
	byField := map[string]string{}
	for _, s := range setup.Slots {
		byField[s.Field] = s.Name
	}
	written := map[string]bool{}
	var lines []string
	last := -1
	for _, line := range strings.Split(body, "\n") {
		m := exportLine.FindStringSubmatch(line)
		if m == nil || !claudeCodeModelVar.MatchString(m[1]) {
			lines = append(lines, line)
			continue
		}
		name, ok := byField[m[1]]
		if !ok {
			continue
		}
		lines = append(lines, "export "+m[1]+"="+name)
		written[m[1]] = true
		last = len(lines) - 1
	}
	var missing []string
	for _, s := range setup.Slots {
		if !written[s.Field] && claudeCodeModelVar.MatchString(s.Field) {
			missing = append(missing, "export "+s.Field+"="+s.Name)
		}
	}
	if len(missing) > 0 && last >= 0 {
		lines = append(lines[:last+1], append(missing, lines[last+1:]...)...)
	}
	out := strings.Join(lines, "\n")

	loc := settingsArg.FindStringSubmatchIndex(out)
	if loc == nil {
		return out, nil
	}
	committed, err := parseObject(out[loc[2]:loc[3]])
	if err != nil {
		return "", err
	}
	picker := committed.obj("modelPicker")
	before := map[string]string{}
	var fallback []string
	if picker != nil {
		opts, _ := picker.get("options").([]any)
		for _, o := range opts {
			oo, _ := o.(*object)
			model, _ := oo.get("model").(string)
			as, _ := oo.get("behavesAs").(string)
			if _, seen := before[model]; !seen && isClaude(as) {
				fallback = append(fallback, as)
			}
			before[model] = as
		}
	}
	options := []any{}
	for _, s := range unique(setup.Slots) {
		if s.Role == clients.RoleClassifier {
			continue
		}
		as := behavesAs(s, setup, fallback)
		if as == "" {
			as = before[s.Name]
		}
		if as == "" {
			continue
		}
		options = append(options, newObject().set("model", s.Name).set("behavesAs", as))
	}
	settings := committed.clone()
	settings.set("modelPicker", picker.clone().set("options", options))
	return out[:loc[0]] + "--settings '" + compact(settings) + "'" + out[loc[1]:], nil
}

func isClaude(id string) bool { return strings.HasPrefix(id, "claude-") }

// behavesAs is which model Claude Code should take a name to be. Its own id,
// for a Claude model. A name served by a GPT model needs a Claude model in
// its place: behavesAs is how Claude Code picks the prompt, the thinking and
// effort it sends and the window it compacts at, and an id it does not know
// left it on an unrecognised model's 32k cap with a warning. The stand-in is
// the largest Claude model this client serves whose window and output both
// fit inside the GPT model's, so Claude Code never plans for more room than
// there is; the smallest when none fits; and, with no Claude model's figures
// to hand, the middle of the committed rows (the gateway clamps max_tokens to
// the real cap whatever Claude Code sends).
func behavesAs(s Slot, setup Setup, fallback []string) string {
	serving := s.serving()
	if serving == "" || isClaude(serving) {
		return serving
	}
	var claude []*Model
	for _, x := range setup.Slots {
		if m := x.Served; m != nil && isClaude(m.Serving) && m.MaxInputTokens != nil && m.MaxTokens != nil {
			claude = append(claude, m)
		}
	}
	size := func(m *Model) float64 { return float64(*m.MaxInputTokens)*1e6 + float64(*m.MaxTokens) }
	fitsIn := func(m *Model) bool {
		if s.Served == nil {
			return true
		}
		return (s.Served.MaxInputTokens == nil || *m.MaxInputTokens <= *s.Served.MaxInputTokens) &&
			(s.Served.MaxTokens == nil || *m.MaxTokens <= *s.Served.MaxTokens)
	}
	var largest, smallest *Model
	for _, m := range claude {
		if fitsIn(m) && (largest == nil || size(m) > size(largest)) {
			largest = m
		}
		if smallest == nil || size(m) < size(smallest) {
			smallest = m
		}
	}
	switch {
	case largest != nil:
		return largest.Serving
	case smallest != nil:
		return smallest.Serving
	case len(fallback) > 0:
		return fallback[len(fallback)/2]
	}
	return ""
}

// ── Codex ───────────────────────────────────────────────────────────────────

// codexToml writes config.toml: each model key — model, review_model, and
// the ones in the [agents] and [memories] tables — set to its slot's name. A
// field naming two keys (memories) sets both. A picker name has no key here:
// it is a catalog entry only (codexCatalog).
func codexToml(body string, setup Setup) (string, error) {
	out := body
	for _, s := range setup.Slots {
		for _, field := range strings.Split(s.Field, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			key := field[strings.LastIndex(field, ".")+1:]
			re := regexp.MustCompile(`(?m)^(` + regexp.QuoteMeta(key) + ` = )"[^"]*"`)
			done := false
			out = re.ReplaceAllStringFunc(out, func(m string) string {
				if done {
					return m
				}
				done = true
				return re.ReplaceAllString(m, `${1}"`+s.Name+`"`)
			})
		}
	}
	return out, nil
}

// codexCatalog writes the model catalog Codex reads instead of asking: one
// entry per name — a picker name of the operator's own included, which
// /model then lists and -m takes — the committed first entry with the name
// and the serving model's window put in, and the review slot named as the
// reviewer every entry defers to.
func codexCatalog(body string, setup Setup) (string, error) {
	doc, err := parseObject(body)
	if err != nil {
		return "", err
	}
	models, _ := doc.get("models").([]any)
	if len(models) == 0 {
		return body, nil
	}
	template, ok := models[0].(*object)
	if !ok {
		return body, nil
	}
	var review any
	if r, ok := setup.find(clients.RoleReview); ok {
		review = r.Name
	}
	entries := []any{}
	for i, s := range unique(setup.Slots) {
		window := template.get("context_window")
		if s.Served != nil && s.Served.MaxInputTokens != nil {
			window = *s.Served.MaxInputTokens
		}
		desc := s.Name
		if s.Served != nil && s.Served.DisplayName != "" {
			desc = s.Served.DisplayName
		} else if v := s.serving(); v != "" {
			desc = v
		}
		e := template.clone()
		e.set("slug", s.Name).set("display_name", s.Name).set("description", desc).
			set("context_window", window).set("max_context_window", window).
			set("priority", (i+1)*10).set("auto_review_model_override", review)
		entries = append(entries, e)
	}
	doc.set("models", entries)
	return indented(doc) + "\n", nil
}

// ── crush ───────────────────────────────────────────────────────────────────

// crushName is what crush's picker shows for a slot: its name, and the model
// it serves now. ctrl+l lists every model of the file by this alone, in both
// of its tabs, so a row saying only "deep" would not say what picking it
// runs.
func crushName(s Slot) string {
	what := s.serving()
	if s.Served != nil && s.Served.DisplayName != "" {
		what = s.Served.DisplayName
	}
	if what == "" {
		return s.Name
	}
	return s.Name + " · " + what
}

// crushJSON writes crush.json: its anthropic provider at the bare address
// (crush adds /v1), its model list every one of the client's names with their
// models' facts — the operator's own picker names beside large and small —
// and models.large and models.small naming the two slots, the large one
// thinking.
//
// Those two are a first run's defaults and no more: crush saves each pick from
// its picker to its data file (~/.local/share/crush/crush.json), which loads
// after this one and wins. What the file decides for good is the list the
// picker offers, and the facts each entry carries.
//
// The provider asks for summarized thinking. crush's SDK asks for it only by
// a real model's id (fantasy, providers/anthropic, thinkingDisplay), which a
// slot name is not, and Opus, Sonnet and Fable 5 return empty thinking text
// without it (internal/service/clients, Crush).
func crushJSON(body string, setup Setup) (string, error) {
	doc, err := parseObject(body)
	if err != nil {
		return "", err
	}
	providers := doc.obj("providers")
	if providers == nil || len(providers.keys) == 0 {
		return body, nil
	}
	provider, ok := providers.get(providers.keys[0]).(*object)
	if !ok {
		return body, nil
	}
	provider.set("type", "anthropic").set("base_url", ExampleBase)
	provider.set("provider_options", provider.obj("provider_options").clone().set("thinking_display", "summarized"))
	models := []any{}
	for _, s := range unique(setup.Slots) {
		m := s.Served
		levels := efforts(m)
		window, output, images := 200_000, 64_000, true
		if m != nil {
			if m.MaxInputTokens != nil {
				window = *m.MaxInputTokens
			}
			if m.MaxTokens != nil {
				output = *m.MaxTokens
			}
			if m.Capabilities.ImageInput != nil && m.Capabilities.ImageInput.Supported != nil {
				images = *m.Capabilities.ImageInput.Supported
			}
		}
		e := newObject().set("id", s.Name).set("name", crushName(s)).
			set("context_window", min(window, CrushWindow)).set("default_max_tokens", output).
			set("can_reason", len(levels) > 0).set("supports_attachments", images).
			// The subscription is the bill; nothing here is charged per token.
			set("cost_per_1m_in", 0).set("cost_per_1m_out", 0).
			set("cost_per_1m_in_cached", 0).set("cost_per_1m_out_cached", 0)
		if len(levels) > 0 {
			def := levels[0]
			for _, l := range levels {
				if l == "medium" {
					def = l
				}
			}
			e.set("reasoning_levels", levels).set("default_reasoning_effort", def)
		}
		models = append(models, e)
	}
	provider.set("models", models)
	large, hasLarge := setup.byRole(clients.RoleMain)
	small, hasSmall := setup.find(clients.RoleFast)
	if !hasSmall {
		small, hasSmall = large, hasLarge
	}
	if picks := doc.obj("models"); picks != nil {
		if l := picks.obj("large"); l != nil {
			if hasLarge {
				l.set("model", large.Name)
			}
			l.set("think", true)
		}
		if sm := picks.obj("small"); sm != nil && hasSmall {
			sm.set("model", small.Name)
		}
	}
	return indented(doc) + "\n", nil
}

// ── opencode ────────────────────────────────────────────────────────────────

// opencodeJSONC writes opencode.jsonc: its built-in anthropic provider
// overridden, at /v1, with each name declared at its model's limits — a
// picker name of the operator's own too, which opencode's model list then
// offers — and model and small_model naming the two slots. The file's opening
// comment is kept; the rest is written whole. A reasoning model gets
// opencode's adaptive thinking modes, as current Claude models take them.
//
// Each name turns tool-call streaming on in its own options: opencode starts
// every model on this provider without it unless its id contains "claude",
// and a name's options are merged over that default (internal/service/clients,
// Opencode).
func opencodeJSONC(body string, setup Setup) (string, error) {
	lines := strings.Split(body, "\n")
	schemaAt := -1
	for i, l := range lines {
		if strings.Contains(l, `"$schema"`) {
			schemaAt = i
			break
		}
	}
	var head []string
	for i, l := range lines {
		if i < schemaAt && strings.HasPrefix(strings.TrimSpace(l), "//") {
			head = append(head, l)
		}
	}
	doc, err := parseObject(stripComments(body))
	if err != nil {
		return "", err
	}
	var committed *object
	if p := doc.obj("provider"); p != nil && len(p.keys) > 0 {
		committed, _ = p.get(p.keys[0]).(*object)
	}
	const id = "anthropic"
	models := newObject()
	for _, s := range unique(setup.Slots) {
		m := s.Served
		levels := efforts(m)
		window, output, images := 200_000, 64_000, true
		if m != nil {
			if m.MaxInputTokens != nil {
				window = *m.MaxInputTokens
			}
			if m.MaxTokens != nil {
				output = *m.MaxTokens
			}
			if m.Capabilities.ImageInput != nil && m.Capabilities.ImageInput.Supported != nil {
				images = *m.Capabilities.ImageInput.Supported
			}
		}
		e := newObject().set("name", s.Name).
			set("limit", newObject().set("context", window).set("output", output)).
			set("reasoning", len(levels) > 0).set("attachment", images).
			set("tool_call", true).set("temperature", false).
			set("modalities", newObject().set("input", []string{"text", "image", "pdf"}).set("output", []string{"text"})).
			set("options", newObject().set("toolStreaming", true))
		if len(levels) > 0 {
			variants := newObject()
			for _, l := range levels {
				variants.set(l, newObject().set("thinking", newObject().set("type", "adaptive")).set("effort", l))
			}
			e.set("variants", variants)
		}
		models.set(s.Name, e)
	}
	options := committed.obj("options")
	if options == nil {
		options = newObject()
	}
	doc.set("provider", newObject().set(id, newObject().set("options", options).set("models", models)))
	if large, ok := setup.byRole(clients.RoleMain); ok {
		doc.set("model", id+"/"+large.Name)
		small, ok := setup.find(clients.RoleFast)
		if !ok {
			small = large
		}
		doc.set("small_model", id+"/"+small.Name)
	}
	text := indented(doc)
	if len(head) > 0 {
		text = strings.Replace(text, "{\n", "{\n"+strings.Join(head, "\n")+"\n", 1)
	}
	return text + "\n", nil
}
