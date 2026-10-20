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
# limits means running the install command again. The auto-mode classifier's
# name is not listed: nobody picks it.
# A function rather than a command, so sourcing this file — from a shell
# profile, as the one-line installer does — sets things up rather than
# starting Claude Code.
claude() {
  command claude --settings '{"modelPicker":{"options":[{"model":"large","behavesAs":"claude-opus-5-5"},{"model":"small","behavesAs":"claude-haiku-5-5"},{"model":"opus","behavesAs":"claude-opus-5-5"},{"model":"sonnet","behavesAs":"claude-sonnet-5-5"},{"model":"haiku","behavesAs":"claude-haiku-5-5"},{"model":"fable","behavesAs":"claude-fable-5-1"}]}}' "$@"
}
