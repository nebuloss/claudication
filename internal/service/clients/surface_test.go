package clients

import "testing"

func TestSurfaceReadsTheEntrypoint(t *testing.T) {
	cases := []struct{ ua, want string }{
		// Each form dN builds (surface.go).
		{"claude-cli/2.1.304 (external, cli)", "cli"},
		{"claude-cli/2.1.304 (external, sdk-cli)", "sdk-cli"},
		{"claude-cli/2.1.304 (external, sdk-ts, agent-sdk/0.2.304)", "sdk-ts"},
		{"claude-cli/2.1.304 (external, sdk-py, agent-sdk/0.1.9, client-app/nightly-triage)", "sdk-py/nightly-triage"},
		{"claude-cli/2.1.304 (external, sdk-ts, client-app/bot, workload/cron)", "sdk-ts/bot"},
		{"claude-cli/2.1.304 (external, sdk-cli, workload/batch)", "sdk-cli"},
		{"claude-cli/2.1.304 (external, claude-vscode)", "claude-vscode"},
		{"claude-cli/2.1.304 (external, remote_baku)", "remote_baku"},
		{"claude-cli/2.1.304 (external, mcp)", "mcp"},
		// Odd spacing, and an app whose name holds a parenthesis.
		{"  claude-cli/2.1.304   (external,sdk-cli )  ", "sdk-cli"},
		{"claude-cli/2.1.304 (external ,  sdk-ts ,agent-sdk/1 , client-app/ my app (beta) )", "sdk-ts/my app (beta)"},
		// An empty client-app names nothing.
		{"claude-cli/2.1.304 (external, sdk-ts, client-app/)", "sdk-ts"},
		// Not Claude Code's, or not readable as it: nothing rather than a guess.
		{"", ""},
		{"claude-cli/2.1.304", ""},
		{"claude-cli/2.1.304 (external)", ""},
		{"claude-cli/2.1.304 (external, agent-sdk/0.2.1)", ""},
		{"claude-cli/2.1.304 (external, )", ""},
		{"opencode/1.17.13 ai-sdk/provider-utils/4.0.27 runtime/bun", ""},
		{"codex_cli_rs/0.54.0 (Debian 13; x86_64) xterm", ""},
		{"Charm-Crush/0.9 (linux, cli)", ""},
		{"claude-cli-fork/1.0 (external, cli)", ""},
	}
	for _, c := range cases {
		if got := Surface(c.ua); got != c.want {
			t.Errorf("Surface(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

func TestSurfaceKeepsControlCharactersOut(t *testing.T) {
	if got := Surface("claude-cli/2 (external, sdk-ts, client-app/a\x1b[2Jb)"); got != "sdk-ts/a[2Jb" {
		t.Errorf("got %q", got)
	}
}

func TestSurfaceTitle(t *testing.T) {
	cases := []struct{ surface, want string }{
		{"", ""},
		{"cli", "Interactive"},
		{"sdk-cli", "Headless (claude -p)"},
		{"sdk-ts", "Agent SDK (TypeScript)"},
		{"sdk-py/nightly-triage", "Agent SDK (Python) · nightly-triage"},
		{"claude-vscode", "VS Code"},
		{"claude-desktop", "Desktop"},
		{"remote", "Remote"},
		{"remote_mobile", "Remote"},
		{"mcp", "MCP server"},
		{"claude-in-teams", "claude-in-teams"},
		{"bench/x", "bench · x"},
	}
	for _, c := range cases {
		if got := SurfaceTitle(c.surface); got != c.want {
			t.Errorf("SurfaceTitle(%q) = %q, want %q", c.surface, got, c.want)
		}
	}
}
