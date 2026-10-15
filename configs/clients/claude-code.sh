# Claude Code, pointed at a claudication gateway. Source it from your shell profile.
#
# No /v1 on the base URL — Claude Code appends the path itself.
# ANTHROPIC_AUTH_TOKEN rather than ANTHROPIC_API_KEY: the token form is the one
# the client documents for a custom base URL.

export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_...

# The gateway's model aliases. Which real model answers each is set in the
# gateway, under Settings › Aliases, not here. The main model and its sub-agents run
# on claude-large, titles, summaries and quick calls on claude-small, and
# picking a tier by name (/model opus, a sub-agent's "haiku") goes to that
# family's alias.
export ANTHROPIC_MODEL=claude-large
export ANTHROPIC_SMALL_FAST_MODEL=claude-small
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-opus
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet
export ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku
export ANTHROPIC_DEFAULT_FABLE_MODEL=claude-fable

# Claude Code does not know these names, so it is told which real models they
# behave like — their context window, output limit and prompt handling.
# Without this it treats them as unknown and caps them at a 200k window.
# A function rather than a command, so sourcing this file — from a shell
# profile, as the one-line installer does — sets things up rather than
# starting Claude Code.
claude() {
  command claude --settings '{"modelPicker":{"options":[{"model":"claude-large","behavesAs":"claude-opus-5-5"},{"model":"claude-small","behavesAs":"claude-haiku-5-5"},{"model":"claude-opus","behavesAs":"claude-opus-5-5"},{"model":"claude-sonnet","behavesAs":"claude-sonnet-5-5"},{"model":"claude-haiku","behavesAs":"claude-haiku-5-5"},{"model":"claude-fable","behavesAs":"claude-fable-5-1"}]}}' "$@"
}
