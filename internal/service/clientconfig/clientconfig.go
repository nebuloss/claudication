// Package clientconfig writes a client's configuration files: the committed
// ones under configs/clients, with its own model names put in every model
// field and each name described with the facts of the model it serves now.
//
// The committed files are each type's starting point — complete, readable in
// the repository, and written with the names a new client of that type is
// given. A client's files are those with its names in, which is why switching
// a name to a model with other limits means running the install command
// again or, once the client is set up, its update command, which carries no
// key.
//
// The gateway writes them, and only the gateway: the admin UI shows what this
// returns (GET /admin/clients/{id}/files), the install and update commands
// carry it, and the MCP server's update_config makes the same update command
// for a client's own agent (internal/httpapi/agent). Until the agent could
// ask for one, the UI wrote the files itself and handed them over; a second
// writer here would have been a second copy to keep in step, so the one
// writer moved to where every caller can reach it.
//
// Every typed client's files also register the gateway's MCP server under
// MCPServer, with the key the file already carries or the variable it is in;
// what each file owns of a merged config is that one entry, so the user's
// other MCP servers stay (see the recipes, and docs/clients.md, "Agent tools
// (MCP)").
package clientconfig

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"claudication/configs"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/store"
)

// ExampleBase is the address inside the committed files, and ExampleKey the
// key: plain, complete files that work after one edit, and swapped for a
// gateway's own with a string replace rather than a template language.
const (
	ExampleBase = "https://claudication.example.com"
	ExampleKey  = "clc_..."
)

// MCPServer is the name every client's config registers the gateway's MCP
// server under, and so the one entry each merge owns of the client's MCP
// servers. Claude Code and Codex name its tools after it (mcp__claudication__…).
const MCPServer = "claudication"

// MCPPath is where the MCP server is on the relay.
const MCPPath = "/mcp"

// baseURL is an address a client can be pointed at: http or https, a host,
// maybe a port and a path, and nothing a shell, a TOML string or a JSON one
// would read as anything but text — it is written into every file and into
// the command that fetches them.
var baseURL = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)

// ErrBase is an address Base refuses.
var ErrBase = errors.New("the gateway's address is http(s)://host[:port][/path], with nothing else in it")

// Base is the address files are written for, its trailing slashes dropped;
// ErrBase for one that is not an address, or not safe to write.
func Base(raw string) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(raw), "/")
	if !baseURL.MatchString(b) {
		return "", ErrBase
	}
	return b, nil
}

// Model is one entry of the model list a client is offered (/v1/models as
// its key sees it, internal/service/models): a name of the client's,
// carrying the facts of the model it serves.
type Model struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	Serving        string `json:"serving"`
	Provider       string `json:"provider"`
	MaxInputTokens *int   `json:"max_input_tokens"`
	MaxTokens      *int   `json:"max_tokens"`
	Capabilities   struct {
		// Effort is {"supported": bool, "<level>": {"supported": bool}...}.
		Effort     map[string]json.RawMessage `json:"effort"`
		ImageInput *struct {
			Supported *bool `json:"supported"`
		} `json:"image_input"`
	} `json:"capabilities"`
}

// Slot is one slot as a file is written from it.
type Slot struct {
	// ID is the type's slot id: large, main, review...
	ID   string
	Role clients.Role
	// Field is where the client's configuration takes it.
	Field string
	// Name is the model name the client asks for.
	Name string
	// Served is its entry in the client's model list, nil when it was not
	// read.
	Served *Model
}

func (s Slot) serving() string {
	if s.Served == nil {
		return ""
	}
	return s.Served.Serving
}

// Setup is what a client's files are written from.
type Setup struct {
	Slots []Slot
}

// For is a client's setup: its slots, each with the entry of its name in
// list, the model list its key is offered. A list that cannot be read leaves
// every slot without its facts, and each file then falls back on the
// defaults it gives a model it knows nothing of.
func For(c store.Client, list []byte) Setup {
	var l struct {
		Data []Model `json:"data"`
	}
	_ = json.Unmarshal(list, &l)
	t := c.Spec()
	out := Setup{Slots: make([]Slot, 0, len(c.Slots))}
	for _, sl := range c.Slots {
		spec, _ := t.SlotFor(sl.Slot)
		s := Slot{ID: sl.Slot, Role: spec.Role, Field: spec.Field, Name: sl.Name}
		for i := range l.Data {
			if l.Data[i].ID == sl.Name {
				s.Served = &l.Data[i]
				break
			}
		}
		out.Slots = append(out.Slots, s)
	}
	return out
}

// File is one file of a client's, as its page shows it.
type File struct {
	// Label is where it goes, as a reader writes the path.
	Label string `json:"label"`
	// Filename is what it is called when downloaded.
	Filename string `json:"filename"`
	// Lang is how the page highlights it.
	Lang string `json:"lang"`
	Body string `json:"body"`
}

// updateRule is how an update writes a file once the client is set up,
// without its key (install.Bundle, Update): a JSON config names the paths
// that carry the key, taken out of the update's copy, and the paths it
// rewrites, which leave those out; an env file names the variables whose
// lines stay as they are on disk. A file with none holds no key and goes as an
// install's would.
type updateRule struct {
	strip, owns []string
	keep        []string
}

// fileSpec is one file of a type's: the committed one, how it is written for
// a client, and where the installer puts it.
type fileSpec struct {
	label, filename, lang string
	// committed is its name under configs/clients.
	committed string
	write     func(body string, s Setup) (string, error)
	// target is where it goes, as the installer takes it; Body and the
	// client's names are filled in per client.
	target install.File
	update *updateRule
}

// recipe is one type's files.
type recipe struct {
	id    string
	files []fileSpec
}

// recipes are the typed clients' files. A custom client has none: its page
// gives the addresses and the header, and it is pointed here by hand.
var recipes = []recipe{
	{id: "claude-code", files: []fileSpec{{
		label: "claudication.sh", filename: "claude-code.sh", lang: "sh",
		committed: "claude-code.sh", write: claudeCodeSh,
		// The file is the gateway's whole, key and all; an update rewrites
		// it keeping the key's line from the one on disk. The MCP server is
		// in the wrapper's --mcp-config, its header naming the variable the
		// key is in rather than the key, so nothing else holds it.
		target: install.File{Path: ".config/claudication/claude-code.sh", Env: true},
		update: &updateRule{keep: []string{"ANTHROPIC_AUTH_TOKEN"}},
	}}},
	{id: "codex", files: []fileSpec{{
		label: "~/.codex/config.toml", filename: "config.toml", lang: "toml",
		committed: "codex.toml", write: codexToml,
		// Neither Codex file holds the key — config.toml names the variable
		// it is in, for the provider and the MCP server alike — so an update
		// writes both as an install does. A TOML merge owns what the file
		// defines, so [mcp_servers.claudication] is the gateway's and every
		// other [mcp_servers.*] table stays the user's.
		target: install.File{Path: ".codex/config.toml", Merge: &install.Merge{Format: "toml"}},
	}, {
		label: "~/.codex/claudication-models.json", filename: "claudication-models.json", lang: "json",
		committed: "codex-models.json", write: codexCatalog,
		target: install.File{Path: ".codex/claudication-models.json"},
	}}},
	{id: "opencode", files: []fileSpec{{
		label: "~/.config/opencode/opencode.jsonc", filename: "opencode.jsonc", lang: "jsonc",
		committed: "opencode.jsonc", write: opencodeJSONC,
		// The anthropic provider is the gateway's to own, and the default
		// models, since they name its slots, and its own MCP server's entry
		// — the user's other servers under mcp stay. An openai override left
		// by a configuration from when opencode could be pointed at
		// Responses stays as it was: it names nothing the defaults use.
		target: install.File{Path: ".config/opencode/opencode.jsonc", Merge: &install.Merge{Format: "json",
			Owns: []string{"provider.anthropic", "model", "small_model", "mcp." + MCPServer}}},
		// An update owns the declared models and the two defaults, and never
		// the provider's options or the MCP server's entry, both of which
		// carry the key (mcpUpdate).
		update: &updateRule{
			strip: []string{"provider.anthropic.options.apiKey", "mcp"},
			owns:  []string{"provider.anthropic.models", "model", "small_model"},
		},
	}}},
	{id: "crush", files: []fileSpec{{
		label: "~/.config/crush/crush.json", filename: "crush.json", lang: "json",
		committed: "crush.json", write: crushJSON,
		// ~/.config/crush/crush.json on every OS, Windows included: crush
		// reads its config from XDG_CONFIG_HOME or <home>/.config
		// (internal/home/home.go:26, config/load.go:1210 at 76cc5c57).
		// %LOCALAPPDATA%\crush is its data file, where the picker saves picks
		// (load.go:1266) — writing there reset them on every install. Its own
		// provider, the large and small picks, which name it, and its own MCP
		// server's entry; the operator's other providers and servers stay.
		target: install.File{Path: ".config/crush/crush.json", Merge: &install.Merge{Format: "json",
			Owns: []string{"providers.claudication", "models.large", "models.small", "mcp." + MCPServer}},
			// Installs before 2026-10-22 wrote it there on Windows. Only the
			// provider is taken back: models.large and models.small in that
			// file are the user's own picks now, which crush reads over ours.
			Stale: &install.Stale{WindowsPath: "AppData/Local/crush/crush.json", Owns: []string{"providers.claudication"}}},
		// An update owns the provider's model list and the two picks, and
		// never its api_key or the MCP server's entry, both of which carry
		// the key (mcpUpdate).
		update: &updateRule{
			strip: []string{"providers.claudication.api_key", "mcp"},
			owns:  []string{"providers.claudication.models", "models.large", "models.small"},
		},
	}}},
}

// mcpUpdate: why an update leaves crush's and opencode's MCP entry alone.
//
// Their entry carries the key in its header, as their provider does — neither
// client has the key in a variable for a header to name — and an update
// carries no key. Owning part of the entry would not help: a machine set up
// before the MCP server existed has no entry, and an update writing one
// without its header would leave an entry every call to which is a 401. So
// the entry is the install's to write and an update's to keep: such a
// machine gets the MCP server by being set up again (a new key), and every
// other keeps what its install wrote. Claude Code's and Codex's entries name
// the variable the key is in, so their updates write them whole.

func recipeOf(typeID string) (recipe, bool) {
	for _, r := range recipes {
		if r.id == typeID {
			return r, true
		}
	}
	return recipe{}, false
}

// Writes reports whether a type has files: every typed client.
func Writes(typeID string) bool {
	_, ok := recipeOf(typeID)
	return ok
}

// write is each of a type's files for one client, at base, with key in it
// where the committed file has the example's; "" leaves the example's.
func (r recipe) write(s Setup, base, key string) []string {
	out := make([]string, len(r.files))
	for i, f := range r.files {
		committed := configs.Client(f.committed)
		body, err := f.write(committed, s)
		if err != nil {
			// A committed file the rewrite cannot read is still a working
			// file for the type's default names: hand it out rather than
			// nothing.
			body = committed
		}
		body = strings.ReplaceAll(body, ExampleBase, base)
		if key != "" {
			body = strings.ReplaceAll(body, ExampleKey, key)
		}
		out[i] = body
	}
	return out
}

// Files are a type's files for one client, at base, as its page shows them:
// the example's key where the key goes, which the page swaps for the client's
// own while the key is still offered. None for a custom client.
func Files(typeID string, s Setup, base string) []File {
	r, ok := recipeOf(typeID)
	if !ok {
		return nil
	}
	bodies := r.write(s, base, "")
	out := make([]File, len(r.files))
	for i, f := range r.files {
		out[i] = File{Label: f.label, Filename: f.filename, Lang: f.lang, Body: bodies[i]}
	}
	return out
}

// Install is a type's files as the install command takes them, key and all.
// label names the client in the script's messages.
func Install(typeID, label string, s Setup, base, key string) []install.File {
	r, ok := recipeOf(typeID)
	if !ok {
		return nil
	}
	bodies := r.write(s, base, key)
	out := make([]install.File, len(r.files))
	for i, f := range r.files {
		out[i] = r.installFile(f, label, bodies[i])
	}
	return out
}

// Update is a type's files as the update command takes them: written without
// the key — the example's stands where it would go — and each file that
// holds the key told to keep the one on disk, so the key goes nowhere.
func Update(typeID, label string, s Setup, base string) []install.File {
	r, ok := recipeOf(typeID)
	if !ok {
		return nil
	}
	bodies := r.write(s, base, "")
	out := make([]install.File, len(r.files))
	for i, f := range r.files {
		out[i] = forUpdate(r.installFile(f, label, bodies[i]), f.update)
	}
	return out
}

func (r recipe) installFile(f fileSpec, label, body string) install.File {
	out := f.target
	out.ClientID = r.id
	out.Client = label
	out.Body = body
	if f.target.Merge != nil {
		m := *f.target.Merge
		m.Owns = append([]string(nil), m.Owns...)
		out.Merge = &m
	}
	return out
}

// forUpdate is a file as an update hands it to the installer: one with no
// rule holds no key and goes as an install's would. A JSON config loses its
// key's paths and owns only the model ones; an env file keeps its key's lines
// from the file on disk, and its own copy of them is emptied, so the key
// could not ride along even if the body had it. Either must already be on the
// machine: written fresh, it would configure the client without a key.
func forUpdate(f install.File, rule *updateRule) install.File {
	if rule == nil {
		return f
	}
	if len(rule.keep) > 0 {
		kept := map[string]bool{}
		for _, k := range rule.keep {
			kept[k] = true
		}
		lines := strings.Split(f.Body, "\n")
		for i, l := range lines {
			if m := envExport.FindStringSubmatch(l); m != nil && kept[m[1]] {
				lines[i] = "export " + m[1] + "="
			}
		}
		f.Body = strings.Join(lines, "\n")
		f.Merge = &install.Merge{Format: "env", Keep: append([]string(nil), rule.keep...)}
		f.Existing = true
		return f
	}
	if body, err := withoutPaths(f.Body, rule.strip); err == nil {
		f.Body = body
	}
	f.Merge = &install.Merge{Format: "json", Owns: append([]string(nil), rule.owns...)}
	f.Existing = true
	return f
}

// withoutPaths is a JSON or JSONC body with the dotted paths taken out;
// comments go too. A path the body does not have is no error.
func withoutPaths(body string, paths []string) (string, error) {
	doc, err := parseObject(stripComments(body))
	if err != nil {
		return "", err
	}
	for _, p := range paths {
		keys := strings.Split(p, ".")
		at := doc
		for _, k := range keys[:len(keys)-1] {
			at = at.obj(k)
		}
		if at != nil {
			at.del(keys[len(keys)-1])
		}
	}
	return indented(doc) + "\n", nil
}
