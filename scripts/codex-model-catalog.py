#!/usr/bin/env python3
"""Write a Codex model catalog describing the Claude models the gateway serves.

Codex looks its model up in a catalog compiled into its own binary, and warns
when it cannot find one:

    warning: Model metadata for `claude-sonnet-5` not found. Defaulting to
    fallback metadata; this can degrade performance and cause issues.

The fallback is conservative, so a million-token model gets auto-compacted as
if it were far smaller — the session starts losing context long before it has
to. Pointing `model_catalog_json` at the file this writes fixes that and
silences the warning.

    scripts/codex-model-catalog.py                 # writes ~/.codex/claude-models.json
    scripts/codex-model-catalog.py --codex ~/bin/codex --out /tmp/models.json

Then in ~/.codex/config.toml:

    model_catalog_json = "/home/you/.codex/claude-models.json"

# Why it clones rather than writes an entry

A catalog entry is not a few numbers. It carries Codex's entire system-prompt
template and several dozen behaviour switches — which shell tool to offer, how
to truncate, whether to use the apply_patch tool, what reasoning levels exist.
Writing one from scratch means silently choosing different values for all of
them, and the failures would look like the model behaving oddly rather than
like a configuration mistake.

So this reads the catalog out of the Codex binary you are actually running,
clones a real entry, and changes only the fields that genuinely differ. It
therefore stays correct across Codex versions on its own: run it again after an
upgrade.

# What it changes, and why

  - slug, display_name, description, context_window, priority — the identity.
  - visibility "list", so the model appears in the picker.
  - upgrade: dropped. The template model has a retirement notice pointing at
    an OpenAI model; inherited, it would tell you to migrate away.
  - prefer_websockets, use_responses_lite, supports_experimental_context:
    false. The gateway serves HTTP SSE and the plain Responses shape, and
    nothing else.

web_search_tool_type is deliberately left as the template has it. A null there
is rejected outright — the field is an untagged enum of string-or-map — and it
costs nothing to keep: the translator drops web_search tools on the way out
because they have no schema and cannot become Anthropic functions, so the model
never sees one whatever this says.
"""

import argparse
import json
import os
import sys

# The template to clone: a plain, current model with no special tool mode.
TEMPLATE = 'gpt-5.4'

# What the gateway serves, and the context each model has.
MODELS = [
    ('claude-opus-5', 'Claude Opus 5',
     'Most capable, for complex work.', 1_000_000, 10),
    ('claude-sonnet-5', 'Claude Sonnet 5',
     'Strong model for everyday coding.', 1_000_000, 20),
    ('claude-haiku-4-5-20251001', 'Claude Haiku 4.5',
     'Fast and cheap, for simple work.', 200_000, 30),
]


def embedded_catalog(path):
    """Pull the catalog JSON out of the Codex binary."""
    data = open(path, 'rb').read()
    start = data.find(b'{\n  "models": [\n')
    if start < 0:
        sys.exit('no model catalog found in %s — has the format changed?' % path)

    depth, in_str, esc, end = 0, False, False, None
    for i in range(start, len(data)):
        c = data[i]
        if esc:
            esc = False
        elif c == 0x5C:  # backslash
            esc = True
        elif c == 0x22:  # quote
            in_str = not in_str
        elif not in_str:
            if c == 0x7B:
                depth += 1
            elif c == 0x7D:
                depth -= 1
                if depth == 0:
                    end = i + 1
                    break
    if end is None:
        sys.exit('the catalog in %s does not terminate' % path)
    return json.loads(data[start:end].decode('utf-8'))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--codex', default=os.path.expanduser('~/bin/codex'),
                    help='the codex binary to read the template from')
    ap.add_argument('--out', default=os.path.expanduser('~/.codex/claude-models.json'))
    ap.add_argument('--template', default=TEMPLATE)
    args = ap.parse_args()

    catalog = embedded_catalog(args.codex)
    slugs = [m['slug'] for m in catalog['models']]
    template = next((m for m in catalog['models'] if m['slug'] == args.template), None)
    if template is None:
        sys.exit('no %s in this Codex build; pick one of --template %s'
                 % (args.template, ', '.join(slugs)))

    out = []
    for slug, name, description, window, priority in MODELS:
        m = json.loads(json.dumps(template))
        m['slug'] = slug
        m['display_name'] = name
        m['description'] = description
        m['context_window'] = window
        m['max_context_window'] = window
        m['priority'] = priority
        m['visibility'] = 'list'
        m['upgrade'] = None
        m['availability_nux'] = None
        m['prefer_websockets'] = False
        m['use_responses_lite'] = False
        m['supports_experimental_context'] = False
        out.append(m)

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, 'w') as f:
        json.dump({'models': out}, f, indent=2)
    print('wrote %s from %s: %s'
          % (args.out, args.template, ', '.join(m['slug'] for m in out)))
    print('add to ~/.codex/config.toml:\n    model_catalog_json = "%s"' % args.out)


if __name__ == '__main__':
    main()
