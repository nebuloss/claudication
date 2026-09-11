#!/usr/bin/env python3
"""Render configs/clients.json into docs/clients.md.

The client recipes are shared: the admin UI renders them with the gateway's own
address and live model list, and this renders the same templates into the
document with example values. They were written out twice before, and two
copies of a config stanza drift — silently, because the one nobody is looking
at is the one that goes wrong.

    scripts/gen-client-docs.py            # rewrite the generated regions
    scripts/gen-client-docs.py --check    # fail if they are stale

`make check` runs the second form, so editing a stanza without regenerating is
a build failure rather than a documentation bug found months later.

Only the regions between the GENERATED markers are touched. Everything else in
docs/clients.md is prose that belongs to the document.
"""

import argparse
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
RECIPES = ROOT / 'configs' / 'clients.json'
DOC = ROOT / 'docs' / 'clients.md'

BEGIN = '<!-- GENERATED FROM configs/clients.json — edit that file, then run scripts/gen-client-docs.py -->'
END = '<!-- END GENERATED -->'

# Two models are enough to show the shape of crush's array. The document says
# so, and the Setup tab generates the real thing from the live list.
SAMPLE_MODELS = [
    ('claude-opus-5', 'Claude Opus 5'),
    ('claude-haiku-4-5-20251001', 'Claude Haiku 4.5'),
]


def model_facts(model_id):
    """The same rule the UI applies; see setup.tsx for why it is a guess."""
    small = model_id.startswith('claude-opus-4-5') or model_id.startswith('claude-haiku-4-5')
    reasons = not model_id.startswith('claude-haiku') and not model_id.startswith(
        'claude-sonnet-4-5'
    )
    big_out = model_id.startswith('claude-opus-5') or model_id.startswith('claude-sonnet-5')
    return {
        'context': 200_000 if small else 1_000_000,
        'maxTokens': 128_000 if big_out else 64_000,
        'reasons': reasons,
    }


def render(template, variables):
    out = template
    for key, value in variables.items():
        out = out.replace('{{%s}}' % key, value)
    return out


def crush_models(recipe):
    entry = recipe.get('modelEntry')
    if entry is None:
        return ''
    reasoning = recipe.get('modelEntryReasoning', '')
    blocks = []
    for model_id, name in SAMPLE_MODELS:
        f = model_facts(model_id)
        blocks.append(
            render(
                entry,
                {
                    'id': model_id,
                    'name': name,
                    'context': str(f['context']),
                    'maxTokens': str(f['maxTokens']),
                    'reasons': 'true' if f['reasons'] else 'false',
                    'reasoning': reasoning if f['reasons'] else '',
                },
            )
        )
    return ',\n'.join(blocks)


def catalog_entries(recipe):
    """Codex's catalog, one entry per sample model."""
    entry = recipe.get('catalogEntry')
    if entry is None:
        return ''
    blocks = []
    for i, (model_id, name) in enumerate(SAMPLE_MODELS):
        blocks.append(
            render(
                entry,
                {
                    'id': model_id,
                    'name': name,
                    'description': name,
                    'context': str(model_facts(model_id)['context']),
                    'priority': str((i + 1) * 10),
                },
            )
        )
    return ',\n'.join(blocks)


def sentences(text):
    """A note, as a markdown paragraph. Backticks already mean what they mean."""
    return text


def build(recipes):
    ex = recipes['examples']
    out = []

    for recipe in recipes['clients']:
        variables = {
            'base': ex['base'],
            'model': ex['model'],
            'smallModel': ex['smallModel'],
            'models': crush_models(recipe),
            'catalogEntries': catalog_entries(recipe),
        }
        out.append('## %s' % recipe['label'])
        out.append('')
        out.append(sentences(recipe['lead']))
        out.append('')
        for snippet in recipe['snippets']:
            out.append('`%s`:' % snippet['label'] if _is_path(snippet['label']) else snippet['label'] + ':')
            out.append('')
            out.append('```%s' % snippet['lang'])
            out.append(render(snippet['template'], variables))
            out.append('```')
            out.append('')
        for note in recipe['notes']:
            out.append(sentences(note))
            out.append('')
        for download in recipe.get('downloads', []):
            # The admin UI serves these from the binary; a reader of the
            # document has the repository, so point them at the copy there.
            out.append('**`scripts/%s`** — %s' % (download['name'], download['what']))
            out.append('')
            out.append('The admin UI offers it as a download under Setup, which is the only')
            out.append('way to get it on a machine that installed a release binary and has no')
            out.append('checkout.')
            out.append('')

    out.append('## From the shell')
    out.append('')
    out.append('On the machine running the gateway. Shell access to the state directory is')
    out.append('already the higher privilege, so none of these asks for the admin password.')
    out.append('')
    out.append('| Command | What it does |')
    out.append('|---|---|')
    for command in recipes['shell']:
        out.append('| `%s` | %s |' % (command['cmd'], command['what']))
    out.append('')

    out.append('## When it does not work')
    out.append('')
    out.append('| What you see | What it usually is |')
    out.append('|---|---|')
    for row in recipes['troubleshooting']:
        out.append('| %s | %s |' % (_cell(row['symptom']), _cell(row['cause'])))
    out.append('')

    return '\n'.join(out).rstrip() + '\n'


def _is_path(label):
    return label.startswith('~') or label.startswith('/') or label.endswith('.json')


def _cell(text):
    """Markdown tables end a cell at an unescaped pipe."""
    return text.replace('|', '\\|')


def splice(document, generated):
    if BEGIN not in document or END not in document:
        sys.exit('docs/clients.md has no GENERATED markers; add them first')
    head, rest = document.split(BEGIN, 1)
    _, tail = rest.split(END, 1)
    return '%s%s\n\n%s\n%s%s' % (head, BEGIN, generated, END, tail)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--check', action='store_true',
                    help='exit non-zero if the document is out of date')
    args = ap.parse_args()

    recipes = json.loads(RECIPES.read_text())
    document = DOC.read_text()
    updated = splice(document, build(recipes))

    if args.check:
        if updated != document:
            print('docs/clients.md is stale — run scripts/gen-client-docs.py', file=sys.stderr)
            sys.exit(1)
        print('docs/clients.md is up to date')
        return

    if updated == document:
        print('docs/clients.md already current')
        return
    DOC.write_text(updated)
    print('rewrote docs/clients.md from configs/clients.json')


if __name__ == '__main__':
    main()
