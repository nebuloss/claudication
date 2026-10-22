# Claude Code, pointed at a claudication gateway. Source it from your shell profile.
#
# No /v1 on the base URL — Claude Code appends the path itself.
# ANTHROPIC_AUTH_TOKEN rather than ANTHROPIC_API_KEY: the token form is the one
# the client documents for a custom base URL.

export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_...

# This client's model names, one per model setting Claude Code has. Each is
# the client's own: what it serves, a Claude model or a GPT one, is chosen on
# the client's page in the gateway, not here, and these are the names a new
# Claude Code client starts with. A tier's name is Claude Code's own word for
# it, so /model opus, or a sub-agent asking for opus, asks for "opus".
export ANTHROPIC_MODEL=large
export ANTHROPIC_SMALL_FAST_MODEL=small
export ANTHROPIC_DEFAULT_OPUS_MODEL=opus
export ANTHROPIC_DEFAULT_SONNET_MODEL=sonnet
export ANTHROPIC_DEFAULT_HAIKU_MODEL=haiku
export ANTHROPIC_DEFAULT_FABLE_MODEL=fable
export CLAUDE_CODE_AUTO_MODE_MODEL=auto

# Claude Code does not know these names, so it is told which real models they
# behave like — their context window, output limit, prompt profile, thinking
# and effort defaults. That is all it reads off a model's name: with this, a
# plain name is handled as the model it serves. Without it, it treats
# them as unknown and caps them at a 200k window. The gateway writes what each
# serves when it hands out this file; switching one to a model with other
# limits means running the install command (or, once set up, the update
# command) again. The auto-mode classifier's name is not listed: nobody
# picks it. A name added on the client's page has a row and no variable:
# /model <name> switches a session to it.
#
# The gateway's MCP server goes in beside it, as "claudication": tools for
# Claude Code's own agent about this client (mcp__claudication__list_models
# and the rest; see docs/clients.md, "Agent tools (MCP)"). --mcp-config adds
# it to whatever servers you have, it replaces none. Written with = because
# the flag takes several values: spaced, it would swallow the prompt after it.
# The header names a variable rather than the key, so the key is in this file
# once; and not ANTHROPIC_AUTH_TOKEN, which Claude Code never expands toward
# a remote server, so the function hands it over under a name of its own, to
# this one command.
#
# A function rather than a command, so sourcing this file — from a shell
# profile, as the one-line installer does — sets things up rather than
# starting Claude Code.
claude() {
  CLAUDICATION_MCP_KEY="$ANTHROPIC_AUTH_TOKEN" command claude --settings '{"modelPicker":{"options":[{"model":"large","behavesAs":"claude-opus-5-5"},{"model":"small","behavesAs":"claude-haiku-5-5"},{"model":"opus","behavesAs":"claude-opus-5-5"},{"model":"sonnet","behavesAs":"claude-sonnet-5-5"},{"model":"haiku","behavesAs":"claude-haiku-5-5"},{"model":"fable","behavesAs":"claude-fable-5-1"}]}}' \
    '--mcp-config={"mcpServers":{"claudication":{"type":"http","url":"https://claudication.example.com/mcp","headers":{"Authorization":"Bearer ${CLAUDICATION_MCP_KEY}"}}}}' \
    "$@"
}
