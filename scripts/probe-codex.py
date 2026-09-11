#!/usr/bin/env python3
"""Replay a Codex request through a running gateway and judge the answer.

This is the half the unit tests cannot reach: the live subscription backend,
with its content checks, judging a request this gateway synthesised rather than
relayed. It needs credentials and a network, so it is a script run by hand and
never part of `make check`.

    scripts/probe-codex.py --key clc_... --url http://127.0.0.1:8317
    scripts/probe-codex.py --key clc_... --prompt 'list the files here' --tool
    scripts/probe-codex.py --key clc_... --roundtrip

The checks are the ones Codex itself applies, and each failure below is a turn
Codex would report as broken:

  - the stream opens with response.created
  - it ends with response.completed, which is the ONLY terminal event Codex
    accepts; anything else fails the turn whatever preceded it
  - response.completed carries an id, and usage with a non-zero output count
  - no response.failed anywhere
  - a function_call item, if one arrives, has arguments that parse as JSON

With --tool it asks for something that should make the model call a tool, which
exercises the part of the mapping with the most in it: arguments buffered
across deltas, handed over as a JSON string rather than an object, and a
namespace put back on a name that went out flattened.

With --roundtrip it then hands the tool's output back and takes a second turn,
which is the path every Codex session after its first turn takes and the only
one that exercises the mapping the other way: a function_call becomes an
assistant tool_use block and a function_call_output becomes a user tool_result.
Anthropic rejects the whole request if either the pairing or the role
alternation is wrong, so this either works or fails loudly.
"""

import argparse
import json
import pathlib
import subprocess
import sys

FIXTURE = (pathlib.Path(__file__).resolve().parent.parent
           / 'internal/api/openai/testdata/codex-responses-request.json')


def build_body(prompt, want_tool):
    """The captured Codex request, with its last user turn replaced."""
    body = json.loads(FIXTURE.read_text())
    if want_tool and prompt is None:
        prompt = ('Use the shell tool to run `echo hello`. '
                  'Call the tool, do not describe it.')
    if prompt is not None:
        for item in reversed(body['input']):
            if item.get('role') == 'user':
                item['content'] = [{'type': 'input_text', 'text': prompt}]
                break
    return json.dumps(body)


def fetch(url, key, payload):
    """POST it and return the raw SSE text.

    Headers go to a separate file: curl -i would interleave them with the body,
    and Python's universal-newline decoding rewrites the \\r\\n that separates
    the two, so there is nothing left to split on. That cost half an hour once.
    """
    headers = pathlib.Path('/tmp/probe-codex-headers.txt')
    proc = subprocess.run(
        ['curl', '-sS', '-m', '300', '-N', '-D', str(headers),
         url.rstrip('/') + '/v1/responses',
         '-H', 'Authorization: Bearer ' + key,
         '-H', 'Content-Type: application/json',
         '-H', 'Accept: text/event-stream',
         # The headers Codex itself sends. The gateway drops them rather than
         # passing them upstream; sending them here is what proves it.
         '-H', 'originator: codex_exec',
         '-H', 'session_id: 00000000-0000-0000-0000-000000000000',
         '--data-binary', '@-'],
        input=payload, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.exit('curl failed: ' + proc.stderr.strip())
    status = headers.read_text().splitlines()[0] if headers.exists() else '?'
    return status, proc.stdout


def parse(sse):
    out = []
    for block in sse.split('\n\n'):
        name = data = None
        for line in block.split('\n'):
            if line.startswith('event: '):
                name = line[7:]
            elif line.startswith('data: '):
                data = line[6:]
        if name is not None:
            out.append((name, json.loads(data) if data else {}))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--key', required=True, help='a clc_ API key')
    ap.add_argument('--url', default='http://127.0.0.1:8317')
    ap.add_argument('--prompt', default=None)
    ap.add_argument('--tool', action='store_true',
                    help='ask for something that should call a tool')
    ap.add_argument('--roundtrip', action='store_true',
                    help='call a tool, hand back its output, and take a second turn')
    args = ap.parse_args()

    want_tool = args.tool or args.roundtrip
    body = build_body(args.prompt, want_tool)
    status, sse = fetch(args.url, args.key, body)
    print(status)
    events = parse(sse)
    if not events:
        print(sse[:2000])
        sys.exit('no SSE events at all')

    counts = {}
    for name, _ in events:
        counts[name] = counts.get(name, 0) + 1
    for name in sorted(counts):
        print('  %-40s %d' % (name, counts[name]))

    text = ''.join(e['delta'] for n, e in events
                   if n == 'response.output_text.delta')
    if text:
        print('\ntext: ' + text[:300])

    calls = [e['item'] for n, e in events
             if n == 'response.output_item.done'
             and e.get('item', {}).get('type') == 'function_call']
    for c in calls:
        print('\ncall: %s%s(%s)' % (
            c.get('namespace', '') + '.' if c.get('namespace') else '',
            c.get('name'), c.get('arguments')))

    # The second turn: the call comes back as history alongside its output,
    # which is the path every Codex session after the first turn takes. It
    # exercises the mapping in the other direction — a function_call becomes an
    # assistant tool_use block and a function_call_output becomes a user
    # tool_result, and Anthropic rejects the request outright if either the
    # pairing or the role alternation is wrong.
    second = []
    if args.roundtrip and calls:
        call = calls[0]
        turn = json.loads(body)
        turn['input'].append({
            'type': 'function_call',
            'name': call['name'],
            'call_id': call['call_id'],
            'arguments': call['arguments'],
            **({'namespace': call['namespace']} if call.get('namespace') else {}),
        })
        turn['input'].append({
            'type': 'function_call_output',
            'call_id': call['call_id'],
            'output': 'hello\n',
        })
        print('\n--- second turn, with the tool output handed back ---')
        status2, sse2 = fetch(args.url, args.key, json.dumps(turn))
        print(status2)
        second = parse(sse2)
        text2 = ''.join(e['delta'] for n, e in second
                        if n == 'response.output_text.delta')
        if text2:
            print('text: ' + text2[:300])
        for n, _ in second:
            if n in ('response.failed', 'response.incomplete'):
                print('event: ' + n)

    print()
    bad = []
    if events[0][0] != 'response.created':
        bad.append('opens with %s, not response.created' % events[0][0])
    if events[-1][0] != 'response.completed':
        bad.append('ends with %s — Codex accepts only response.completed'
                   % events[-1][0])
    for name, payload in events:
        if name == 'response.failed':
            bad.append('response.failed: %s' % payload.get('response', {}).get('error'))
        if name == 'response.completed':
            resp = payload.get('response', {})
            if not resp.get('id'):
                bad.append('response.completed has no id')
            usage = resp.get('usage') or {}
            if not usage.get('output_tokens'):
                bad.append('response.completed reports no output tokens')
    for c in calls:
        try:
            json.loads(c.get('arguments', ''))
        except Exception as err:
            bad.append('%s has unparseable arguments: %s' % (c.get('name'), err))
    if want_tool and not calls:
        bad.append('asked for a tool call and got none '
                   '(the model may just have declined — try again)')
    if args.roundtrip:
        if not second:
            bad.append('the second turn produced nothing')
        else:
            if second[-1][0] != 'response.completed':
                bad.append('second turn ends with %s' % second[-1][0])
            for name, payload in second:
                if name == 'response.failed':
                    bad.append('second turn failed: %s'
                               % payload.get('response', {}).get('error'))

    for b in bad:
        print('FAIL  ' + b)
    if not bad:
        print('PASS  a stream Codex would accept')
    sys.exit(1 if bad else 0)


if __name__ == '__main__':
    main()
