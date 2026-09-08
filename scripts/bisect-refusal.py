#!/usr/bin/env python3
"""Find the smallest part of a request the upstream refuses.

Anthropic's subscription backend refuses some requests on their *content*, and
says so in a message about billing that names nothing:

    Third-party apps now draw from your extra usage, not your plan limits.
    Add more at claude.ai/settings/usage and keep going.

Two triggers have been found this way — a tool named `mcp_x` rather than
`mcp__x`, and Claude Code's own `Is directory a git repo:` line inside a
foreign system prompt — and both were found by bisecting a captured body by
hand, which took hours each time. This does that automatically.

It is not limited to that one error. Anything that makes a request fail
reproducibly can be narrowed with it.

Usage:

    scripts/bisect-refusal.py --key clc_... captured-body.json
    scripts/bisect-refusal.py --key clc_... --url http://gw:8317 body.json

Capture a body by pointing the client's base URL at a logging proxy, or take
one from whatever the client writes to disk. Only `model`, `system`, `tools`
and `messages` are used; sampling parameters are replaced so each probe is
small.

This spends real requests against a real account. Every probe asks for 16
output tokens, and --max-calls bounds the run.
"""

import argparse
import json
import sys
import urllib.error
import urllib.request

PROBE_MAX_TOKENS = 16
CONTROL = "say OK"


class Budget:
    """Bounds how much of the account this run is allowed to spend."""

    def __init__(self, limit):
        self.limit = limit
        self.used = 0

    def spend(self):
        self.used += 1
        if self.used > self.limit:
            raise SystemExit(
                f"\nstopped: {self.limit} calls used. Re-run with --max-calls "
                f"to go further."
            )


class Gateway:
    def __init__(self, url, key, budget, verbose):
        self.url = url.rstrip("/") + "/v1/messages"
        self.key = key
        self.budget = budget
        self.verbose = verbose
        self.cache = {}

    def accepts(self, body):
        """True when the upstream takes this body."""
        payload = json.dumps(body, sort_keys=True)
        if payload in self.cache:
            return self.cache[payload]

        self.budget.spend()
        request = urllib.request.Request(
            self.url,
            data=payload.encode(),
            headers={
                "x-api-key": self.key,
                "anthropic-version": "2023-06-01",
                "content-type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                ok, detail = response.status == 200, ""
        except urllib.error.HTTPError as err:
            ok = False
            try:
                detail = json.load(err)["error"]["message"]
            except Exception:
                detail = err.reason or ""
        except Exception as err:  # network, DNS, timeout
            raise SystemExit(f"could not reach {self.url}: {err}")

        if self.verbose:
            print(f"    [{self.budget.used:3}] {'accept' if ok else 'REFUSE'}"
                  f"  {detail[:70]}")
        self.cache[payload] = ok
        return ok


def probe_body(original, **overrides):
    """A small request built from the captured one."""
    body = {
        "model": original.get("model", "claude-opus-5"),
        "max_tokens": PROBE_MAX_TOKENS,
        "messages": [{"role": "user", "content": CONTROL}],
    }
    for key, value in overrides.items():
        if value is None:
            body.pop(key, None)
        else:
            body[key] = value
    return body


def minimise(items, fails, note):
    """Delta-debugging: the shortest subsequence that still fails.

    Standard ddmin — halve, then quarter, and so on, keeping any subset that
    still reproduces. Cheaper than removing one item at a time, which matters
    because every test is a billed request.
    """
    granularity = 2
    while len(items) >= 2:
        chunk = max(1, len(items) // granularity)
        parts = [items[i:i + chunk] for i in range(0, len(items), chunk)]

        for part in parts:
            if part and fails(part):
                items, granularity = part, 2
                note(len(items))
                break
        else:
            # Nothing on its own reproduces it; try each complement.
            for i, part in enumerate(parts):
                rest = [x for j, p in enumerate(parts) if j != i for x in p]
                if rest and fails(rest):
                    items = rest
                    granularity = max(granularity - 1, 2)
                    note(len(items))
                    break
            else:
                if granularity >= len(items):
                    break
                granularity = min(len(items), granularity * 2)
    return items


def system_text(system):
    """The system field as one string, whichever shape it is in."""
    if isinstance(system, str):
        return system
    if isinstance(system, list):
        return "\n".join(b.get("text", "") for b in system if isinstance(b, dict))
    return ""


def main():
    parser = argparse.ArgumentParser(
        description="Narrow a refused request to its smallest failing part.")
    parser.add_argument("body", help="captured request body, as JSON")
    parser.add_argument("--key", required=True, help="an API key for the gateway")
    parser.add_argument("--url", default="http://127.0.0.1:8317",
                        help="gateway base URL (default %(default)s)")
    parser.add_argument("--max-calls", type=int, default=80,
                        help="stop after this many requests (default %(default)s)")
    parser.add_argument("-v", "--verbose", action="store_true",
                        help="print every probe")
    args = parser.parse_args()

    with open(args.body) as handle:
        original = json.load(handle)

    gateway = Gateway(args.url, args.key, Budget(args.max_calls), args.verbose)

    print("== is anything wrong at all")
    if gateway.accepts(probe_body(original)):
        print("  a trivial request is accepted, so the account is fine")
    else:
        raise SystemExit(
            "  a trivial request is ALSO refused — this is the account or the "
            "key, not the body. Check the Claude accounts tab.")

    full = probe_body(original,
                      system=original.get("system"),
                      tools=original.get("tools"),
                      messages=original.get("messages"))
    if gateway.accepts(full):
        raise SystemExit(
            "  the captured body is accepted as it stands.\n"
            "  Either the trigger is gone, or the gateway already normalises "
            "it — see docs/upstream-request-pipeline.md.")
    print("  the captured body is refused, as expected")

    print("\n== which part carries it")
    parts = {
        "system": original.get("system"),
        "tools": original.get("tools"),
        "messages": original.get("messages"),
    }
    culprits = []
    for name, value in parts.items():
        if value is None:
            continue
        alone = probe_body(original, **{name: value})
        verdict = "REFUSED" if not gateway.accepts(alone) else "accepted"
        print(f"  {name:<10} on its own: {verdict}")
        if verdict == "REFUSED":
            culprits.append(name)

    if not culprits:
        print("\n  No single part reproduces it; the trigger needs a "
              "combination.\n  Re-run with -v and narrow by hand from there.")
        return

    print("\n== narrowing")
    for name in culprits:
        if name == "tools":
            tools = original["tools"]
            smallest = minimise(
                tools,
                lambda subset: not gateway.accepts(probe_body(original, tools=subset)),
                lambda n: print(f"  tools: down to {n}"))
            print(f"\n  SMALLEST FAILING TOOL SET ({len(smallest)}):")
            for tool in smallest:
                print(f"    {tool.get('name')}")
            if len(smallest) == 1:
                name_only = dict(smallest[0])
                name_only["name"] = "probe_tool"
                if gateway.accepts(probe_body(original, tools=[name_only])):
                    print(f"    -> it is the NAME: {smallest[0].get('name')!r}")
                else:
                    print("    -> it is the schema or description, not the name")

        elif name == "system":
            lines = system_text(original["system"]).split("\n")
            smallest = minimise(
                lines,
                lambda subset: not gateway.accepts(
                    probe_body(original, system=[{"type": "text",
                                                  "text": "\n".join(subset)}])),
                lambda n: print(f"  system: down to {n} lines"))
            print(f"\n  SMALLEST FAILING SYSTEM PROMPT ({len(smallest)} lines):")
            for line in smallest:
                print(f"    {line[:100]}")

        elif name == "messages":
            messages = original["messages"]
            smallest = minimise(
                messages,
                lambda subset: not gateway.accepts(probe_body(original, messages=subset)),
                lambda n: print(f"  messages: down to {n}"))
            print(f"\n  SMALLEST FAILING MESSAGE LIST ({len(smallest)}):")
            for message in smallest:
                content = json.dumps(message.get("content"))[:100]
                print(f"    {message.get('role')}: {content}")

    print(f"\n{gateway.budget.used} requests used.")


if __name__ == "__main__":
    sys.exit(main())
