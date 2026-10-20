package clients

import (
	"strings"
)

// How Claude Code was run, read off its User-Agent: the one place it says
// whether a person was at the prompt. It matters for naming chats — Claude Code
// names its interactive sessions itself and never a claude -p one (ClaudeCode,
// above) — so a headless chat is otherwise a bare session id that nothing
// tells apart from an interactive chat that has not been named yet.
//
// Read from the 2.1.304 binary (~/.local/share/claude/versions/2.1.304, byte
// offsets into it):
//
//	User-Agent  claude-cli/<version> (external, <entrypoint><sdk><app><workload>)
//	            built by dN @239800626, the literal @239801356:
//	            (external, ${TY()??a.CLAUDE_CODE_ENTRYPOINT??"cli"}${e}${n}${s})
//	            e = ", agent-sdk/<CLAUDE_AGENT_SDK_VERSION>" when set
//	            n = ", client-app/<CLAUDE_AGENT_SDK_CLIENT_APP>" when set
//	            s = ", workload/<w>" when the request has one
//	TY()        the host's launchOptions.entrypointOnRequests() @236414964,
//	            which a host embedding Claude Code sets; otherwise the env
//	cli         the default: the interactive REPL. At start-up an entrypoint
//	            of cli, or none, becomes sdk-cli when the session is not
//	            interactive — claude -p, print mode —
//	            a.set("CLAUDE_CODE_ENTRYPOINT",e?"sdk-cli":"cli") @236971436;
//	            `claude mcp serve` sets mcp @236971295
//	sdk-ts      the TypeScript Agent SDK sets it before spawning the CLI
//	            when the caller has not (@262945533, @263000737)
//	sdk-py      the Python SDK, by the same rule; the CLI lists it among its
//	            known entrypoints (@236962621) and files sdk-cli, sdk-ts and
//	            sdk-py together as claude_code_sdk (@236218163)
//	others      the same list: claude-vscode, claude-desktop(-3p), remote,
//	            remote_baku, remote_cowork, remote_desktop, remote_mobile,
//	            remote_projects, claude-in-teams, claude-in-slack, bench,
//	            claude-code-github-action, local-agent, ssh-remote, ...
//
// Stored per request (usage_events.surface) as the entrypoint, with the
// client-app after a slash when there is one: "sdk-ts/my-app". The User-Agent
// is not kept, so this is the only record of it; the label is SurfaceTitle's,
// decided on read, so it can be reworded without touching history.

// maxSurfacePart bounds what a client puts in its User-Agent before it is
// stored. Generous for an entrypoint, which is a short word, and for an app's
// name.
const maxSurfacePart = 64

// Surface is how Claude Code was run, from its User-Agent, as it is stored:
// the entrypoint, then "/" and the client-app when one is named. Empty for any
// other client's User-Agent, and for one too damaged to read — never a guess,
// since an interactive chat filed as headless is the confusion this exists to
// end.
func Surface(userAgent string) string {
	ua := strings.TrimSpace(userAgent)
	product, rest, ok := strings.Cut(ua, "/")
	if !ok || product != "claude-cli" {
		return ""
	}
	_, details, ok := strings.Cut(rest, "(")
	if !ok {
		return ""
	}
	// The last ")" rather than the first: an app's name is the caller's own
	// text and may hold one.
	if i := strings.LastIndex(details, ")"); i >= 0 {
		details = details[:i]
	}
	parts := strings.Split(details, ",")
	// The first is the build's user type (external); the entrypoint is always
	// second, defaulting to cli, so a User-Agent without one is not Claude
	// Code's.
	if len(parts) < 2 {
		return ""
	}
	entry := cleanPart(parts[1])
	if entry == "" || strings.ContainsAny(entry, "/ ") {
		return ""
	}
	for _, p := range parts[2:] {
		if app, ok := strings.CutPrefix(strings.TrimSpace(p), "client-app/"); ok {
			if app = cleanPart(app); app != "" {
				return entry + "/" + app
			}
		}
	}
	return entry
}

// cleanPart trims a part of the User-Agent and makes it safe to store and
// print, as api.CleanIdentifier does for the other identifiers a client
// sends: no control characters, bounded.
func cleanPart(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(v))
	if len(v) > maxSurfacePart {
		v = v[:maxSurfacePart]
	}
	return v
}

// surfaceTitles are the entrypoints worth a word of their own. The remote_*
// family is folded into Remote below: one product, several hosts.
var surfaceTitles = map[string]string{
	"cli":                       "Interactive",
	"sdk-cli":                   "Headless (claude -p)",
	"sdk-ts":                    "Agent SDK (TypeScript)",
	"sdk-py":                    "Agent SDK (Python)",
	"claude-vscode":             "VS Code",
	"claude-desktop":            "Desktop",
	"claude-desktop-3p":         "Desktop",
	"mcp":                       "MCP server",
	"claude-code-github-action": "GitHub Action",
}

// SurfaceTitle is how the UI names a stored surface: a short label, with the
// Agent SDK's client-app after it when it named one. An entrypoint it has no
// word for is shown as it came, which is how a new one is noticed. Empty for
// none.
func SurfaceTitle(surface string) string {
	if surface == "" {
		return ""
	}
	entry, app, _ := strings.Cut(surface, "/")
	title, ok := surfaceTitles[entry]
	switch {
	case ok:
	case entry == "remote" || strings.HasPrefix(entry, "remote_"):
		title = "Remote"
	default:
		title = entry
	}
	if app != "" {
		return title + " · " + app
	}
	return title
}
