#!/usr/bin/env python3
"""Measure the ChatGPT Codex backend directly, before the gateway relays to it.

Phase 0 of the OpenAI upstream: every fact the provider package will rely on
is checked here against the real backend rather than taken from Codex's or
crush's source. It needs a ChatGPT subscription and a network, so it is a
script run by hand and never part of `make check`.

    scripts/probe-chatgpt.py login            # device-code sign-in, once
    scripts/probe-chatgpt.py probe --out DIR  # run every measurement

`login` prints a code to enter at auth.openai.com/codex/device and keeps the
tokens in ~/.config/claudication-probe/chatgpt.json (mode 600). Treat that
file like a password: it is a live login to the account.

`probe` writes one JSON file per measurement into DIR, with the tokens and
the account id removed, ready to become fixtures under
internal/provider/openai/testdata. What each one answers:

  identity       what the id_token says: account id, plan, email
  models         GET /models — what this account may use, and the shape
  usage          GET /wham/usage — the quota windows and their lengths
  baseline       a minimal streamed turn: status, x-codex-* headers, events, usage
  no-originator  the same with no originator header: does the backend gate on it?
  no-instructions  instructions as a developer message instead of the field
  max-output     max_output_tokens set: crush says the backend refuses it
  surrogate      a lone UTF-16 surrogate in the text, as Anthropic refuses
  replay         a second turn sending back the first turn's encrypted
                 reasoning; with --other-token, under a second account too

Facts come from openai/codex (codex-rs/login, codex-api) and crush
(internal/oauth/openai); see internal/provider/openai/doc.go once it exists.
"""
import argparse
import base64
import json
import os
import pathlib
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ISSUER = "https://auth.openai.com"
CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
CODEX = "https://chatgpt.com/backend-api/codex"
BACKEND = "https://chatgpt.com/backend-api"
DEVICE_REDIRECT = ISSUER + "/deviceauth/callback"
TOKENS = pathlib.Path.home() / ".config/claudication-probe/chatgpt.json"
ORIGINATOR = "claudication"
UA = "claudication-probe/0"


def post(url, body, form=False, headers=None):
    data = (urllib.parse.urlencode(body) if form else json.dumps(body)).encode()
    h = {"Content-Type": "application/x-www-form-urlencoded" if form else "application/json", "User-Agent": UA}
    h.update(headers or {})
    return call(urllib.request.Request(url, data=data, headers=h, method="POST"))


def call(req, stream=False):
    try:
        resp = urllib.request.urlopen(req, timeout=120)
        return resp.status, dict(resp.headers), resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode("utf-8", "replace")


def claims(jwt):
    try:
        payload = jwt.split(".")[1]
        return json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    except (IndexError, ValueError):
        return {}


def account_id(tokens):
    for t in (tokens.get("id_token", ""), tokens.get("access_token", "")):
        c = claims(t)
        aid = c.get("chatgpt_account_id") or c.get("https://api.openai.com/auth", {}).get("chatgpt_account_id")
        if aid:
            return aid
    return ""


def login(_args):
    status, _, body = post(ISSUER + "/api/accounts/deviceauth/usercode", {"client_id": CLIENT_ID})
    if status != 200:
        sys.exit(f"usercode: {status} {body[:400]}")
    start = json.loads(body)
    interval = int(start.get("interval") or 5)
    print(f"Open {ISSUER}/codex/device and enter the code: {start['user_code']}", flush=True)
    deadline = time.time() + 15 * 60
    while time.time() < deadline:
        time.sleep(interval)
        status, _, body = post(ISSUER + "/api/accounts/deviceauth/token",
                               {"device_auth_id": start["device_auth_id"], "user_code": start["user_code"]})
        if status in (403, 404):
            continue
        if status != 200:
            sys.exit(f"device token: {status} {body[:400]}")
        got = json.loads(body)
        status, _, body = post(ISSUER + "/oauth/token", {
            "grant_type": "authorization_code", "client_id": CLIENT_ID,
            "code": got["authorization_code"], "code_verifier": got["code_verifier"],
            "redirect_uri": DEVICE_REDIRECT,
        }, form=True)
        if status != 200:
            sys.exit(f"exchange: {status} {body[:400]}")
        tokens = json.loads(body)
        TOKENS.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(TOKENS, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(tokens, f)
        print(f"Signed in; tokens kept in {TOKENS}", flush=True)
        return
    sys.exit("the code expired before it was entered")


def load(path):
    tokens = json.loads(pathlib.Path(path).read_text())
    # ~/.codex/auth.json keeps them under "tokens".
    return tokens.get("tokens", tokens)


def headers(tokens, originator=True, accept="text/event-stream"):
    h = {"Authorization": "Bearer " + tokens["access_token"], "ChatGPT-Account-ID": account_id(tokens),
         "User-Agent": UA, "Accept": accept, "session-id": SESSION}
    if originator:
        h["originator"] = ORIGINATOR
    return h


SESSION = str(uuid.uuid4())


def sse(body):
    events = []
    for block in body.split("\n\n"):
        name, data = "", ""
        for line in block.splitlines():
            if line.startswith("event:"):
                name = line[6:].strip()
            elif line.startswith("data:"):
                data += line[5:].strip()
        if name or data:
            try:
                events.append({"event": name, "data": json.loads(data) if data else None})
            except ValueError:
                events.append({"event": name, "raw": data[:500]})
    return events


def turn(tokens, model, text, *, originator=True, instructions=True, extra=None, history=None):
    body = {
        "model": model, "stream": True, "store": False, "include": ["reasoning.encrypted_content"],
        "reasoning": {"effort": "low", "summary": "auto"}, "tool_choice": "auto", "parallel_tool_calls": False,
        "tools": [], "prompt_cache_key": SESSION,
        "input": list(history or []) + [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": text}]}],
    }
    if instructions:
        body["instructions"] = "You are a terse assistant."
    else:
        body["input"].insert(0, {"type": "message", "role": "developer",
                                 "content": [{"type": "input_text", "text": "You are a terse assistant."}]})
    body.update(extra or {})
    req = urllib.request.Request(CODEX + "/responses", data=json.dumps(body).encode(),
                                 headers=dict(headers(tokens, originator), **{"Content-Type": "application/json"}), method="POST")
    status, h, raw = call(req)
    events = sse(raw) if status == 200 else []
    done = next((e["data"] for e in events if e["event"] == "response.completed"), None)
    return {
        "status": status,
        "headers": {k: v for k, v in h.items() if k.lower().startswith(("x-codex", "openai", "x-request", "retry", "content-type"))},
        "event_names": [e["event"] for e in events],
        "usage": (done or {}).get("response", {}).get("usage"),
        "output": (done or {}).get("response", {}).get("output"),
        "error_body": raw[:2000] if status != 200 else None,
    }


def scrub(v, secrets):
    s = json.dumps(v)
    for x in secrets:
        if x:
            s = s.replace(x, "REDACTED")
    return json.loads(s)


def probe(args):
    tokens = load(args.tokens)
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    c = claims(tokens.get("id_token", ""))
    auth = c.get("https://api.openai.com/auth", {})
    # Everything that names the person or the account, not only the
    # credentials: these files become fixtures in the repository.
    secrets = [tokens.get("access_token"), tokens.get("refresh_token"), tokens.get("id_token"), account_id(tokens),
               c.get("email"), c.get("name"), c.get("sub"), auth.get("chatgpt_user_id"), auth.get("user_id")]

    def save(name, value):
        (out / f"{name}.json").write_text(json.dumps(scrub(value, secrets), indent=2) + "\n")
        print(f"{name}: {value.get('status', '') if isinstance(value, dict) else ''}", flush=True)

    save("identity", {"plan": auth.get("chatgpt_plan_type"), "has_account_id": bool(account_id(tokens)),
                      "claim_keys": sorted(c), "auth_keys": sorted(auth), "exp_in_s": c.get("exp", 0) - int(time.time())})

    status, h, body = call(urllib.request.Request(CODEX + "/models?client_version=999.0.0", headers=headers(tokens, accept="application/json")))
    models = json.loads(body).get("models", []) if status == 200 else []
    save("models", {"status": status, "body": json.loads(body) if status == 200 else body[:2000]})
    listed = [m["slug"] for m in models if m.get("visibility") == "list"]
    model = args.model or (listed[0] if listed else "gpt-5.5")

    status, h, body = call(urllib.request.Request(BACKEND + "/wham/usage", headers=headers(tokens, accept="application/json")))
    save("usage", {"status": status, "body": json.loads(body) if status == 200 else body[:2000]})

    base = turn(tokens, model, "Reply with the single word: ok")
    save("baseline", dict(base, model=model))
    save("no-originator", turn(tokens, model, "Reply with the single word: ok", originator=False))
    save("no-instructions", turn(tokens, model, "Reply with the single word: ok", instructions=False))
    save("max-output", turn(tokens, model, "Reply with the single word: ok", extra={"max_output_tokens": 256}))
    save("surrogate", turn(tokens, model, "Reply with the single word: ok \ud800"))

    # A trivial prompt is answered without reasoning, so there is nothing
    # encrypted to send back: the replay needs a turn that thinks.
    puzzle = "A bat and a ball cost 1.10 in total; the bat costs 1.00 more than the ball. What does the ball cost? Answer with the number only."
    thought = turn(tokens, model, puzzle, extra={"reasoning": {"effort": "medium", "summary": "auto"}})
    reasoning = [i for i in (thought.get("output") or []) if i.get("type") == "reasoning"]
    save("reasoning", dict(thought, reasoning_items=len(reasoning),
                           encrypted=all(bool(i.get("encrypted_content")) for i in reasoning) if reasoning else None))
    answer = [i for i in (thought.get("output") or []) if i.get("type") == "message"]
    history = [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": puzzle}]}] + reasoning + answer
    again = "Now double it. Number only."
    save("replay", dict(turn(tokens, model, again, history=history), replayed_reasoning=len(reasoning)))
    if args.other_token:
        save("replay-other-account", dict(turn(load(args.other_token), model, again, history=history),
                                          replayed_reasoning=len(reasoning)))


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("login")
    pr = sub.add_parser("probe")
    pr.add_argument("--out", required=True)
    pr.add_argument("--tokens", default=str(TOKENS), help="token file, or a Codex ~/.codex/auth.json")
    pr.add_argument("--other-token", help="a second account's token file, for the cross-account replay")
    pr.add_argument("--model", help="the model to probe; default: the first listed")
    args = p.parse_args()
    {"login": login, "probe": probe}[args.cmd](args)


if __name__ == "__main__":
    main()
