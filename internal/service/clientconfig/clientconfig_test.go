package clientconfig

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"claudication/configs"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/store"
)

// A client's files are the committed ones with its own names put in. Checked
// against the real committed files, because the rewrite is only as good as
// its reading of them.

func ptr[T any](v T) *T { return &v }

func effort(levels ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{"supported": json.RawMessage("true")}
	for _, l := range effortLevels {
		out[l] = json.RawMessage(`{"supported": ` + map[bool]string{true: "true", false: "false"}[slices.Contains(levels, l)] + `}`)
	}
	return out
}

func model(display, serving, provider string, in, out int, levels []string, images *bool) *Model {
	m := &Model{ID: "x", DisplayName: display, Serving: serving, Provider: provider,
		MaxInputTokens: ptr(in), MaxTokens: ptr(out)}
	if levels != nil {
		m.Capabilities.Effort = effort(levels...)
	} else {
		m.Capabilities.Effort = map[string]json.RawMessage{"supported": json.RawMessage("false")}
	}
	if images != nil {
		m.Capabilities.ImageInput = &struct {
			Supported *bool `json:"supported"`
		}{Supported: images}
	}
	return m
}

var (
	opus   = model("Claude Opus 5.5", "claude-opus-5-5", "", 1000000, 128000, []string{"low", "medium", "high", "xhigh", "max"}, ptr(true))
	sonnet = model("Claude Sonnet 4.5", "claude-sonnet-4-5-20250929", "", 200000, 64000, nil, ptr(true))
	gpt    = model("GPT-5.5", "gpt-5.5", "openai", 400000, 128000, []string{"low", "medium", "high"}, nil)
)

func committed(name string) string {
	body := configs.Client(name)
	if body == "" {
		panic("no committed " + name)
	}
	return body
}

// must is a rewrite's file, failing the test on its error:
// must(t)(crushJSON(...)).
func must(t *testing.T) func(string, error) string {
	return func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func obj(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(stripComments(text)), &out); err != nil {
		t.Fatalf("%v in\n%s", err, text)
	}
	return out
}

func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func check(t *testing.T, what string, got, want any) {
	t.Helper()
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("%s: got %s, want %s", what, gj, wj)
	}
}

var crushSlots = []Slot{
	{ID: "large", Role: clients.RoleMain, Field: "models.large", Name: "large", Served: opus},
	{ID: "small", Role: clients.RoleFast, Field: "models.small", Name: "small", Served: sonnet},
}

func TestCrush(t *testing.T) {
	crush := obj(t, must(t)(crushJSON(committed("crush.json"), Setup{Slots: crushSlots})))
	p := get(crush, "providers", "claudication")
	models := get(p, "models").([]any)
	check(t, "crush lists the client's names", []any{get(models, 0, "id"), get(models, 1, "id")}, []string{"large", "small"})
	check(t, "a 1M model is declared at the compaction window", get(models, 0, "context_window"), CrushWindow)
	check(t, "a 200k model keeps its own window", get(models, 1, "context_window"), 200000)
	check(t, "a model with no effort setting does not reason", []any{get(models, 1, "can_reason"), get(models, 1, "reasoning_levels")}, []any{false, nil})
	check(t, "the output cap is the served model's", get(models, 0, "default_max_tokens"), 128000)
	check(t, "the anthropic type, the bare address and thinking on",
		[]any{get(p, "type"), get(p, "base_url"), get(crush, "models", "large", "think")}, []any{"anthropic", ExampleBase, true})
	check(t, "summarized thinking for every name", get(p, "provider_options"), map[string]any{"thinking_display": "summarized"})
	check(t, "each name says the model it serves", []any{get(models, 0, "name"), get(models, 1, "name")},
		[]string{"large · Claude Opus 5.5", "small · Claude Sonnet 4.5"})

	unread := obj(t, must(t)(crushJSON(committed("crush.json"), Setup{Slots: []Slot{{ID: "large", Role: clients.RoleMain, Name: "large"}}})))
	check(t, "a name whose model was not read is its name", get(unread, "providers", "claudication", "models", 0, "name"), "large")

	g := obj(t, must(t)(crushJSON(committed("crush.json"), Setup{Slots: []Slot{
		{ID: "large", Role: clients.RoleMain, Name: "large", Served: gpt}, {ID: "small", Role: clients.RoleFast, Name: "small", Served: gpt}}})))
	pg := get(g, "providers", "claudication")
	check(t, "a GPT model, on the same provider with its own facts",
		[]any{get(pg, "type"), get(pg, "models", 0, "context_window"), get(pg, "models", 0, "reasoning_levels")},
		[]any{"anthropic", 400000, []string{"low", "medium", "high"}})

	x := obj(t, must(t)(crushJSON(committed("crush.json"), Setup{Slots: append(slices.Clone(crushSlots),
		Slot{ID: "s-0a1b2c", Role: clients.RolePicker, Name: "deep", Served: gpt})})))
	deep := get(x, "providers", "claudication", "models", 2)
	check(t, "a picker name is declared after the two, with its own model's facts",
		[]any{get(deep, "id"), get(deep, "name"), get(deep, "context_window"), get(deep, "reasoning_levels"), get(deep, "supports_attachments")},
		[]any{"deep", "deep · GPT-5.5", 400000, []string{"low", "medium", "high"}, true})
	check(t, "and the first-run picks stay large and small",
		[]any{get(x, "models", "large", "model"), get(x, "models", "small", "model")}, []string{"large", "small"})
	check(t, "the MCP server's entry passes through", get(x, "mcp", "claudication", "url"), ExampleBase+"/mcp")
}

// The committed file is what a new crush client is handed, so writing it with
// the default names and the committed facts changes nothing, byte for byte:
// the ordered writer formats as the file was written.
func TestCommittedCrushIsANewClients(t *testing.T) {
	slots := []Slot{
		{ID: "large", Role: clients.RoleMain, Name: "large", Served: opus},
		{ID: "small", Role: clients.RoleFast, Name: "small", Served: opus},
	}
	if got := must(t)(crushJSON(committed("crush.json"), Setup{Slots: slots})); got != committed("crush.json") {
		t.Errorf("the committed crush.json is not a new client's:\n%s", got)
	}
}

var codexSlots = []Slot{
	{ID: "large", Role: clients.RoleMain, Field: "model", Name: "big", Served: opus},
	{ID: "review", Role: clients.RoleReview, Field: "review_model", Name: "rev", Served: gpt},
	{ID: "agents", Role: clients.RoleSubagent, Field: "agents.default_subagent_model", Name: "sub", Served: sonnet},
	{ID: "small", Role: clients.RoleMemories, Field: "memories.extract_model, memories.consolidation_model", Name: "mem", Served: sonnet},
}

func TestCodex(t *testing.T) {
	toml := must(t)(codexToml(committed("codex.toml"), Setup{Slots: codexSlots}))
	for _, re := range []string{`(?m)^model = "big"$`, `(?m)^review_model = "rev"$`, `(?m)^default_subagent_model = "sub"$`,
		`(?m)^extract_model = "mem"$`, `(?m)^consolidation_model = "mem"$`} {
		if !regexp.MustCompile(re).MatchString(toml) {
			t.Errorf("codex.toml lacks %s", re)
		}
	}
	if strings.Count(toml, "\n") != strings.Count(committed("codex.toml"), "\n") {
		t.Error("something else in codex.toml moved")
	}
	if !strings.Contains(toml, "[mcp_servers.claudication]\nurl = \""+ExampleBase+"/mcp\"\nbearer_token_env_var = \"CLAUDICATION_API_KEY\"") {
		t.Errorf("codex.toml does not register the MCP server with the key's variable:\n%s", toml)
	}

	cat := obj(t, must(t)(codexCatalog(committed("codex-models.json"), Setup{Slots: codexSlots})))
	models := get(cat, "models").([]any)
	var slugs, windows, reviews []any
	for _, m := range models {
		slugs = append(slugs, []any{get(m, "slug"), get(m, "priority")})
		windows = append(windows, get(m, "context_window"))
		reviews = append(reviews, get(m, "auto_review_model_override"))
	}
	check(t, "the catalog has one entry per name, in order", slugs, [][]any{{"big", 10}, {"rev", 20}, {"sub", 30}, {"mem", 40}})
	check(t, "each at its served model's window", windows, []int{1000000, 400000, 200000, 200000})
	check(t, "each defers its reviews to the review name", reviews, []string{"rev", "rev", "rev", "rev"})
	template := get(obj(t, committed("codex-models.json")), "models", 0)
	check(t, "everything else in an entry is the committed one", get(models, 2, "base_instructions"), get(template, "base_instructions"))

	x := append(slices.Clone(codexSlots), Slot{ID: "s-0a1b2c", Role: clients.RolePicker, Name: "deep", Served: gpt})
	catX := obj(t, must(t)(codexCatalog(committed("codex-models.json"), Setup{Slots: x})))
	deep := get(catX, "models", 4)
	check(t, "a picker name is a catalog entry after the four, at its own model's window",
		[]any{get(deep, "slug"), get(deep, "context_window"), get(deep, "max_context_window"), get(deep, "description"), get(deep, "auto_review_model_override"), get(deep, "priority")},
		[]any{"deep", 400000, 400000, "GPT-5.5", "rev", 50})
	if got := must(t)(codexToml(committed("codex.toml"), Setup{Slots: x})); got != toml {
		t.Error("a picker name changed config.toml")
	}
}

var ccSlots = []Slot{
	{ID: "large", Role: clients.RoleMain, Field: "ANTHROPIC_MODEL", Name: "large", Served: opus},
	{ID: "small", Role: clients.RoleFast, Field: "ANTHROPIC_SMALL_FAST_MODEL", Name: "small", Served: sonnet},
	{ID: "opus", Role: clients.RoleOpus, Field: "ANTHROPIC_DEFAULT_OPUS_MODEL", Name: "opus", Served: opus},
	{ID: "sonnet", Role: clients.RoleSonnet, Field: "ANTHROPIC_DEFAULT_SONNET_MODEL", Name: "sonnet-mine", Served: sonnet},
	{ID: "auto", Role: clients.RoleClassifier, Field: "CLAUDE_CODE_AUTO_MODE_MODEL", Name: "auto", Served: sonnet},
}

func picker(t *testing.T, sh string) []string {
	t.Helper()
	m := settingsArg.FindStringSubmatch(sh)
	if m == nil {
		t.Fatalf("no --settings in\n%s", sh)
	}
	var s struct {
		ModelPicker struct {
			Options []struct{ Model, BehavesAs string }
		}
	}
	if err := json.Unmarshal([]byte(m[1]), &s); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range s.ModelPicker.Options {
		out = append(out, o.Model+"="+o.BehavesAs)
	}
	return out
}

func TestClaudeCode(t *testing.T) {
	sh := must(t)(claudeCodeSh(committed("claude-code.sh"), Setup{Slots: ccSlots}))
	for _, re := range []string{`(?m)^export ANTHROPIC_MODEL=large$`, `(?m)^export ANTHROPIC_DEFAULT_SONNET_MODEL=sonnet-mine$`,
		`(?m)^export CLAUDE_CODE_AUTO_MODE_MODEL=auto$`} {
		if !regexp.MustCompile(re).MatchString(sh) {
			t.Errorf("claude-code.sh lacks %s", re)
		}
	}
	if strings.Contains(sh, "ANTHROPIC_DEFAULT_FABLE_MODEL") {
		t.Error("a variable with no slot was kept")
	}
	check(t, "the picker says what each name behaves like, and leaves the classifier out", picker(t, sh), []string{
		"large=claude-opus-5-5", "small=claude-sonnet-4-5-20250929", "opus=claude-opus-5-5", "sonnet-mine=claude-sonnet-4-5-20250929"})

	x := append(slices.Clone(ccSlots), Slot{ID: "s-0a1b2c", Role: clients.RolePicker, Name: "deep", Served: opus},
		Slot{ID: "s-1", Role: clients.RolePicker, Name: "quick", Served: sonnet})
	shX := must(t)(claudeCodeSh(committed("claude-code.sh"), Setup{Slots: x}))
	px := picker(t, shX)
	check(t, "each picker name has its row, after the built-ins", px[len(px)-2:], []string{"deep=claude-opus-5-5", "quick=claude-sonnet-4-5-20250929"})
	if regexp.MustCompile(`(?m)=deep$|=quick$`).MatchString(shX) || strings.Count(shX, "\n") != strings.Count(sh, "\n") {
		t.Error("a picker name got a variable")
	}

	g := append(slices.Clone(ccSlots), Slot{ID: "s-g", Role: clients.RolePicker, Name: "gpt", Served: gpt})
	pg := picker(t, must(t)(claudeCodeSh(committed("claude-code.sh"), Setup{Slots: g})))
	check(t, "a GPT-served name behaves as the largest Claude model that fits inside it", pg[len(pg)-1], "gpt=claude-sonnet-4-5-20250929")
	only := picker(t, must(t)(claudeCodeSh(committed("claude-code.sh"), Setup{Slots: []Slot{
		{ID: "large", Role: clients.RoleMain, Field: "ANTHROPIC_MODEL", Name: "large", Served: gpt}}})))
	if len(only) != 1 || !strings.HasPrefix(strings.SplitN(only[0], "=", 2)[1], "claude-") {
		t.Errorf("with no Claude model to measure against, not a committed Claude row: %v", only)
	}

	cc := committed("claude-code.sh")
	var vars []string
	for _, m := range regexp.MustCompile(`(?m)^export [A-Z_]+_MODEL=(.*)$`).FindAllStringSubmatch(cc, -1) {
		vars = append(vars, m[1])
	}
	check(t, "the committed file names a new client's slots", vars, []string{"large", "small", "opus", "sonnet", "haiku", "fable", "auto"})
	for _, row := range picker(t, cc) {
		if !strings.Contains(row, "=claude-") {
			t.Errorf("a committed row behaves as no real model: %s", row)
		}
	}
}

// Claude Code's MCP server is in its wrapper: --mcp-config written with = so
// the variadic flag cannot swallow a prompt, the header naming a variable
// that is not ANTHROPIC_AUTH_TOKEN (which Claude Code blanks toward a remote
// server), and that variable handed to this one command from the token.
func TestClaudeCodeMCP(t *testing.T) {
	sh := must(t)(claudeCodeSh(committed("claude-code.sh"), Setup{Slots: ccSlots}))
	m := regexp.MustCompile(`'--mcp-config=(\{[^']*\})'`).FindStringSubmatch(sh)
	if m == nil {
		t.Fatalf("no --mcp-config= in\n%s", sh)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type, URL string
			Headers   map[string]string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg.MCPServers[MCPServer]
	check(t, "the server", []any{srv.Type, srv.URL, srv.Headers["Authorization"]},
		[]any{"http", ExampleBase + "/mcp", "Bearer ${CLAUDICATION_MCP_KEY}"})
	if !strings.Contains(sh, `CLAUDICATION_MCP_KEY="$ANTHROPIC_AUTH_TOKEN" command claude --settings`) {
		t.Error("the wrapper does not hand the key over to the one command")
	}
	if strings.Count(sh, ExampleKey) != 1 {
		t.Errorf("the key is in the file %d times, not once", strings.Count(sh, ExampleKey))
	}
}

var ocSlots = []Slot{
	{ID: "large", Role: clients.RoleMain, Field: "model", Name: "large", Served: opus},
	{ID: "small", Role: clients.RoleFast, Field: "small_model", Name: "small", Served: sonnet},
}

func TestOpencode(t *testing.T) {
	oc := must(t)(opencodeJSONC(committed("opencode.jsonc"), Setup{Slots: ocSlots}))
	m := obj(t, oc)
	check(t, "the anthropic provider and the client's names",
		[]any{len(get(m, "provider").(map[string]any)), get(m, "model"), get(m, "small_model")}, []any{1, "anthropic/large", "anthropic/small"})
	check(t, "each name at its model's limits", get(m, "provider", "anthropic", "models", "small", "limit"), map[string]int{"context": 200000, "output": 64000})
	check(t, "and none for one without effort", get(m, "provider", "anthropic", "models", "small", "variants"), nil)
	check(t, "tool-call streaming on for every name",
		[]any{get(m, "provider", "anthropic", "models", "large", "options"), get(m, "provider", "anthropic", "models", "small", "options")},
		[]any{map[string]bool{"toolStreaming": true}, map[string]bool{"toolStreaming": true}})
	if !strings.Contains(oc, "// opencode, pointed at a claudication gateway.") {
		t.Error("its opening comment was lost")
	}
	if !strings.Contains(oc, "\"low\": {\n              \"thinking\"") {
		t.Errorf("adaptive thinking variants are not in effort order:\n%s", oc)
	}
	committedOC := obj(t, committed("opencode.jsonc"))
	check(t, "and the committed baseURL", get(m, "provider", "anthropic", "options"), get(committedOC, "provider", "anthropic", "options"))
	check(t, "the MCP server, remote, OAuth off, the key as the header",
		[]any{get(m, "mcp", "claudication", "type"), get(m, "mcp", "claudication", "url"), get(m, "mcp", "claudication", "oauth"),
			get(m, "mcp", "claudication", "headers", "Authorization")},
		[]any{"remote", ExampleBase + "/mcp", false, "Bearer " + ExampleKey})

	x := obj(t, must(t)(opencodeJSONC(committed("opencode.jsonc"), Setup{Slots: append(slices.Clone(ocSlots),
		Slot{ID: "s-0a1b2c", Role: clients.RolePicker, Name: "deep", Served: gpt})})))
	deep := get(x, "provider", "anthropic", "models", "deep")
	check(t, "a picker name at its own model's limits, streaming tool calls",
		[]any{get(deep, "limit"), get(deep, "options")}, []any{map[string]int{"context": 400000, "output": 128000}, map[string]bool{"toolStreaming": true}})
	check(t, "and the two defaults stay", []any{get(x, "model"), get(x, "small_model")}, []string{"anthropic/large", "anthropic/small"})
	check(t, "a URL's // is not a comment", stripComments(`{"u": "https://x"} // c`), "{\"u\": \"https://x\"} \n")
}

// What each type's install owns of a merged config is the gateway's sections
// and, of the MCP servers, only its own entry: the user's other servers stay.
func TestInstallOwnsOnlyItsMCPEntry(t *testing.T) {
	const key = "clc_0123456789abcdefSECRET"
	byType := map[string]Setup{
		"claude-code": {Slots: ccSlots}, "codex": {Slots: codexSlots}, "opencode": {Slots: ocSlots}, "crush": {Slots: crushSlots},
	}
	for typ, s := range byType {
		files := Install(typ, "x", s, "https://gw.example", key)
		if len(files) == 0 {
			t.Fatalf("%s: no files", typ)
		}
		var mcp bool
		for _, f := range files {
			if strings.Contains(f.Body, "https://gw.example/mcp") {
				mcp = true
			}
			if strings.Contains(f.Body, ExampleBase) {
				t.Errorf("%s: %s still has the example address", typ, f.Path)
			}
			if f.Merge != nil && f.Merge.Format == "json" {
				var mcpOwned []string
				for _, o := range f.Merge.Owns {
					if o == "mcp" || strings.HasPrefix(o, "mcp.") {
						mcpOwned = append(mcpOwned, o)
					}
				}
				check(t, typ+": the merge owns only its own MCP entry", mcpOwned, []string{"mcp." + MCPServer})
			}
		}
		if !mcp {
			t.Errorf("%s: no file registers the MCP server", typ)
		}
	}
}

// The update command's files: written as an install's, holding no key even
// when the install's would, owning only the model paths, and each file that
// holds the key on disk required to be there.
func TestUpdate(t *testing.T) {
	const base = "https://gw.example"
	byType := map[string]Setup{
		"claude-code": {Slots: ccSlots}, "codex": {Slots: codexSlots}, "opencode": {Slots: ocSlots}, "crush": {Slots: crushSlots},
	}
	for typ, s := range byType {
		for _, f := range Update(typ, "x", s, base) {
			// Where an install's file has the key, an update's has nothing:
			// not even the placeholder, which would be written over the key
			// on disk. Codex's files hold no key, only a comment showing the
			// variable's shape.
			if typ != "codex" && strings.Contains(f.Body, ExampleKey) {
				t.Errorf("%s: %s carries the key's place", typ, f.Path)
			}
			if err := (install.Bundle{ClientID: "c", Files: []install.File{f}, Update: true}).Validate(); err != nil {
				t.Errorf("%s: %s: %v", typ, f.Path, err)
			}
		}
	}

	crush := Update("crush", "x", Setup{Slots: crushSlots}, base)[0]
	check(t, "crush: an update owns the models, needs the file", []any{crush.Merge, crush.Existing},
		[]any{&install.Merge{Format: "json", Owns: []string{"providers.claudication.models", "models.large", "models.small"}}, true})
	cj := obj(t, crush.Body)
	var keys []string
	for k := range get(cj, "providers", "claudication").(map[string]any) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	check(t, "and keeps the rest of the provider", keys, []string{"base_url", "models", "name", "provider_options", "type"})
	check(t, "and leaves the MCP servers, whose entry holds the key, as they are on disk", get(cj, "mcp"), nil)

	oc := Update("opencode", "x", Setup{Slots: ocSlots}, base)[0]
	oj := obj(t, oc.Body)
	check(t, "opencode: an update holds no key, and parses", get(oj, "provider", "anthropic", "options", "baseURL"), base+"/v1")
	check(t, "and leaves its MCP servers alone", get(oj, "mcp"), nil)
	if slices.ContainsFunc(oc.Merge.Owns, func(o string) bool { return strings.HasPrefix(o, "mcp") }) {
		t.Errorf("opencode's update owns an MCP path: %v", oc.Merge.Owns)
	}

	cc := Update("claude-code", "x", Setup{Slots: ccSlots}, base)[0]
	check(t, "claude code: an update empties the key's line and keeps it from disk",
		[]any{regexp.MustCompile(`(?m)^export ANTHROPIC_AUTH_TOKEN=$`).MatchString(cc.Body), cc.Merge, cc.Existing},
		[]any{true, &install.Merge{Format: "env", Keep: []string{"ANTHROPIC_AUTH_TOKEN"}}, true})
	if !strings.Contains(cc.Body, `"url":"`+base+`/mcp"`) {
		t.Error("claude code: the update does not carry the MCP server, which names no key")
	}
	installed := Install("claude-code", "x", Setup{Slots: ccSlots}, base, "clc_k")[0]
	notKey := func(body string) []string {
		var out []string
		for _, l := range strings.Split(body, "\n") {
			if !strings.HasPrefix(l, "export ANTHROPIC_AUTH_TOKEN") {
				out = append(out, l)
			}
		}
		return out
	}
	check(t, "and rewrites every other line", notKey(cc.Body), notKey(installed.Body))

	codex := Update("codex", "x", Setup{Slots: codexSlots}, base)
	if codex[0].Existing || codex[0].Merge.Format != "toml" || !strings.Contains(codex[0].Body, "[mcp_servers.claudication]") {
		t.Errorf("codex: a file holding no key goes as an install's: %+v", codex[0].Merge)
	}
	if got := must(t)(withoutPaths(`{"a": {"b": 1}} // c`, []string{"a.x.y", "q", "a.b"})); got != "{\n  \"a\": {}\n}\n" {
		t.Errorf("a path missing from the body: %q", got)
	}
}

type secretIs string

func (s secretIs) KeySecret(context.Context, string) (string, error) {
	if s == "" {
		return "", store.ErrSecretNotKept
	}
	return string(s), nil
}

// MakeUpdate refuses files that carry the key, by its secret or its prefix,
// however they came to: here a "secret" every crush file has in it.
func TestMakeUpdateRefusesTheKey(t *testing.T) {
	c := store.Client{APIKey: store.APIKey{ID: "c1", Name: "laptop", Prefix: "abcd"}, Type: "crush"}
	links := install.New(time.Minute)
	if _, _, err := MakeUpdate(context.Background(), secretIs(`"models"`), links, c, false, Setup{Slots: crushSlots}, "https://gw.example"); err == nil ||
		!strings.Contains(err.Error(), "carries the client's key") {
		t.Errorf("an update carrying the secret: %v", err)
	}
	if links.UpdateStateOf("c1").State != install.None {
		t.Error("a refused update made a link")
	}
	token, _, err := MakeUpdate(context.Background(), secretIs(""), links, c, false, Setup{Slots: crushSlots}, "https://gw.example")
	if err != nil || token == "" || links.UpdateStateOf("c1").State != install.Waiting {
		t.Errorf("an update with no secret kept: %q %v", token, err)
	}
	c.Type = "custom"
	if _, _, err := MakeUpdate(context.Background(), secretIs(""), links, c, false, Setup{}, "https://gw.example"); err == nil {
		t.Error("a custom client's update")
	}
}

func TestBase(t *testing.T) {
	for raw, want := range map[string]string{
		"https://gw.example.com/":       "https://gw.example.com",
		"http://127.0.0.1:18403":        "http://127.0.0.1:18403",
		"https://x.example/claude":      "https://x.example/claude",
		"http://[::1]:8317":             "http://[::1]:8317",
		"https://[2001:db8::7]/gw/":     "https://[2001:db8::7]/gw",
		"http://my_host.internal:8317":  "http://my_host.internal:8317",
		"https://gw.example/a%20b/x-y_": "https://gw.example/a%20b/x-y_",
	} {
		if got, err := Base(raw); err != nil || got != want {
			t.Errorf("Base(%q) = %q, %v", raw, got, err)
		}
	}
	for _, bad := range []string{"", "gw.example.com", "https://gw'.example", "https://gw.example/a b", "javascript:x",
		"https://gw.example/$(id)", "ftp://gw.example", "https://user@gw.example", "https://gw.example/?q=1",
		"https://gw.example/#x", "http://[::1", "http://[nothost]:80", "http://gw.example:99999", "https://", "http://:80"} {
		if _, err := Base(bad); err == nil {
			t.Errorf("Base(%q) took it", bad)
		}
	}
}

func TestOrderedJSONRoundTrips(t *testing.T) {
	for _, name := range []string{"crush.json", "codex-models.json"} {
		doc, err := parseObject(committed(name))
		if err != nil {
			t.Fatal(err)
		}
		if got := indented(doc) + "\n"; got != committed(name) {
			t.Errorf("%s does not round-trip", name)
		}
	}
	if got := compact(newObject().set("b", 1).set("a", []any{"<&>", nil, true})); got != `{"b":1,"a":["<&>",null,true]}` {
		t.Errorf("compact: %s", got)
	}
	if !reflect.DeepEqual(efforts(gpt), []string{"low", "medium", "high"}) || efforts(sonnet) != nil || efforts(nil) != nil {
		t.Error("efforts")
	}
}
