# Claude Code, pointed at a claudication gateway. Source it, or paste it.
#
# No /v1 on the base URL — Claude Code appends the path itself.
# ANTHROPIC_AUTH_TOKEN rather than ANTHROPIC_API_KEY: the token form is the one
# the client documents for a custom base URL.

export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_...

# Optional. Without these Claude Code discovers models through /v1/models,
# which the gateway proxies. Keep the small one small: it runs many times a
# session for titles and summaries.
export ANTHROPIC_MODEL=claude-opus-5
export ANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5-20251001

claude
