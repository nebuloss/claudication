# Claude Code, pointed at a claudication gateway. Source it, or paste it.
#
# No /v1 on the base URL — Claude Code appends the path itself.
# ANTHROPIC_AUTH_TOKEN rather than ANTHROPIC_API_KEY: the token form is the one
# the client documents for a custom base URL.

export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_...

# The gateway's two model roles. Every model Claude Code picks goes through
# its opus and haiku tiers: the main model and its sub-agents through opus,
# titles, summaries and quick calls through haiku. Which real model answers
# is set in the gateway, on its Models tab, not here.
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-large
export ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-small

# Claude Code does not know these two names, so it is told which real models
# they behave like — their context window, output limit and prompt handling.
# Without this it treats them as unknown and caps them at a 200k window.
claude --settings '{"modelPicker":{"options":[{"model":"claude-large","behavesAs":"claude-opus-5-5"},{"model":"claude-small","behavesAs":"claude-haiku-5-5"}]}}' "$@"
