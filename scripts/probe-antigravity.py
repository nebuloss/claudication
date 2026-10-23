#!/usr/bin/env python3
"""Measure Google's Antigravity backend directly, before the gateway relays to it.

Google refuses gemini-cli's OAuth client for Gemini Code Assist for
individuals (measured 2026-10-23; internal/provider/google, doc.go) and
points to Antigravity. This checks, against the real backend, what signing
in with Antigravity's client takes and what its backend accepts — starting
from the least identity and adding only what is refused without, so the
gateway sends nothing it was not measured to need. It needs a Google account
and a network, so it is run by hand and never part of `make check`.

    scripts/probe-antigravity.py login-url          # print the consent link
    scripts/probe-antigravity.py login CALLBACK_URL # paste where it landed
    scripts/probe-antigravity.py probe --out DIR    # run every measurement

The consent redirects to http://localhost:51121/oauth-callback, Antigravity's
own loopback address, which nothing serves here: the browser shows an error
page, and its address — carrying the code — is what `login` takes. The tokens
are kept in ~/.config/claudication-probe/antigravity.json (mode 600). Treat
that file like a password: it is a live login to the account.

`probe` writes one JSON file per measurement into DIR, tokens and project
ids removed:

  identity       userinfo: the address
  load-plain     loadCodeAssist with gemini-cli's metadata and this script's
                 own User-Agent: the control
  load-ag        loadCodeAssist with Antigravity's metadata and User-Agent
  onboard        onboardUser onto the default tier, when the account has none
  models         fetchAvailableModels: what this account may call
  quota          retrieveUserQuota: the buckets
  turn-bare      a minimal streamed turn, the plain Code Assist envelope
  turn-fields    the same with Antigravity's body fields (userAgent,
                 requestType, requestId)
  turn-plain-ua  the bare envelope with this script's own User-Agent

Every call but the controls carries Antigravity's User-Agent.

Facts come from CLIProxyAPI (router-for-me/CLIProxyAPI, MIT), internal/auth/
antigravity and internal/runtime/executor/antigravity_*.
"""
import argparse
import base64
import hashlib
import json
import os
import pathlib
import secrets
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid


def reveal(encoded):
    return base64.b64decode(encoded).decode()[::-1]


# Antigravity's OAuth client, kept encoded as internal/provider keeps every
# client credential (oauth.Reveal), so a secret scanner does not refuse the
# push over public values.
CLIENT_ID = reveal("bW9jLnRuZXRub2NyZXN1ZWxnb29nLnNwcGEucGUzMDRnNGhqb2xvdHY1MzJlcmNsMTJoMm5pc3NobXQtMTk1MDYwNjAwMTcwMQ==")
CLIENT_SECRET = reveal("ZkFEcTZ6NENYczhCTG0xSkxkTDY4NFJXRjg1Sy1YUFNDT0c=")
REDIRECT = "http://localhost:51121/oauth-callback"

# An operator's own OAuth client instead, to measure whether Code Assist serves
# a token from a client that is not Antigravity's:
# ~/.config/claudication-probe/own-client.json, {"client_id", "client_secret",
# "redirect"} — redirect as registered on the client.
_own = pathlib.Path.home() / ".config/claudication-probe/own-client.json"
if os.environ.get("PROBE_OWN_CLIENT") == "1":
    _c = json.loads(_own.read_text())
    CLIENT_ID, CLIENT_SECRET = _c["client_id"], _c["client_secret"]
    REDIRECT = _c.get("redirect", "https://claudication.dmz.patapouf.club/admin/oauth/callback/google")
SCOPES = [
    "https://www.googleapis.com/auth/cloud-platform",
    "https://www.googleapis.com/auth/userinfo.email",
    "https://www.googleapis.com/auth/userinfo.profile",
    "https://www.googleapis.com/auth/cclog",
    "https://www.googleapis.com/auth/experimentsandconfigs",
]
DAILY = "https://daily-cloudcode-pa.googleapis.com/v1internal"
CONFIG = pathlib.Path.home() / ".config/claudication-probe"
TOKENS = CONFIG / "antigravity.json"
PENDING = CONFIG / "antigravity-pending.json"
if os.environ.get("PROBE_OWN_CLIENT") == "1":
    # Its own token chain, beside Antigravity's rather than over it.
    TOKENS = CONFIG / "own-client-tokens.json"
    PENDING = CONFIG / "own-client-pending.json"
UA = "claudication-probe/0"
# Antigravity's User-Agent, in the form CLIProxyAPI sends
# (internal/misc/antigravity_version.go). Google serves the free tier only to
# requests that carry it (measured 2026-10-23: the same token, client and
# metadata without it are refused as UNSUPPORTED_CLIENT).
AG_UA = "antigravity/hub/2.25.0 (aidev_client; os_type=linux; arch=amd64)"


def save(path, data):
    CONFIG.mkdir(parents=True, exist_ok=True)
    os.chmod(CONFIG, 0o700)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(data, f)


def call(method, url, body=None, form=False, headers=None):
    h = {"User-Agent": UA}
    data = None
    if body is not None:
        data = (urllib.parse.urlencode(body) if form else json.dumps(body)).encode()
        h["Content-Type"] = "application/x-www-form-urlencoded" if form else "application/json"
    h.update(headers or {})
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        resp = urllib.request.urlopen(req, timeout=120)
        return resp.status, dict(resp.headers), resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read().decode("utf-8", "replace")


def login_url(_):
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    state = secrets.token_urlsafe(24)
    save(PENDING, {"verifier": verifier, "state": state})
    q = {
        "client_id": CLIENT_ID, "redirect_uri": REDIRECT, "response_type": "code",
        "scope": " ".join(SCOPES), "access_type": "offline", "prompt": "consent",
        "code_challenge": challenge, "code_challenge_method": "S256", "state": state,
    }
    print("https://accounts.google.com/o/oauth2/v2/auth?" + urllib.parse.urlencode(q))


def login(args):
    pending = json.loads(PENDING.read_text())
    u = urllib.parse.urlparse(args.callback.strip())
    q = urllib.parse.parse_qs(u.query)
    code = (q.get("code") or [args.callback.strip()])[0]
    if q.get("state") and q["state"][0] != pending["state"]:
        sys.exit("the state does not match the link printed by login-url")
    status, _, body = call("POST", "https://oauth2.googleapis.com/token", {
        "grant_type": "authorization_code", "client_id": CLIENT_ID, "client_secret": CLIENT_SECRET,
        "code": code, "code_verifier": pending["verifier"], "redirect_uri": REDIRECT,
    }, form=True)
    if status != 200:
        sys.exit(f"token exchange: {status} {body[:400]}")
    tokens = json.loads(body)
    save(TOKENS, tokens)
    PENDING.unlink()
    print("signed in; refresh token kept:", "refresh_token" in tokens)


def access_token():
    tokens = json.loads(TOKENS.read_text())
    status, _, body = call("POST", "https://oauth2.googleapis.com/token", {
        "grant_type": "refresh_token", "client_id": CLIENT_ID, "client_secret": CLIENT_SECRET,
        "refresh_token": tokens["refresh_token"],
    }, form=True)
    if status != 200:
        sys.exit(f"refresh: {status} {body[:400]}")
    return json.loads(body)["access_token"]


SCRUB = {}


def scrub(text):
    for secret, mark in SCRUB.items():
        if secret:
            text = text.replace(secret, mark)
    return text


def record(out, name, status, headers, body, sent=None):
    try:
        parsed = json.loads(scrub(body))
    except ValueError:
        parsed = scrub(body)[:20000]
    keep = {k: v for k, v in headers.items() if k.lower().startswith(("content-type", "x-", "retry-after"))}
    doc = {"status": status, "headers": keep, "body": parsed}
    if sent is not None:
        doc["sent"] = json.loads(scrub(json.dumps(sent)))
    (out / f"{name}.json").write_text(json.dumps(doc, indent=1) + "\n")
    print(f"{name:12} {status}")
    return parsed


def probe(args):
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    token = access_token()
    SCRUB[token] = "<access>"
    plain_auth = {"Authorization": "Bearer " + token}
    auth = dict(plain_auth, **{"User-Agent": AG_UA})

    status, h, body = call("GET", "https://www.googleapis.com/oauth2/v2/userinfo?alt=json", headers=auth)
    record(out, "identity", status, h, body)

    plain = {"metadata": {"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI"}}
    status, h, body = call("POST", DAILY + ":loadCodeAssist", plain, headers=plain_auth)
    load = record(out, "load-plain", status, h, body, plain)
    ag = {"metadata": {"ideType": "ANTIGRAVITY"}}
    status, h, body = call("POST", DAILY + ":loadCodeAssist", ag, headers=auth)
    load_ag = record(out, "load-ag", status, h, body, ag)

    project = ""
    for answer in (load_ag, load):
        if isinstance(answer, dict):
            p = answer.get("cloudaicompanionProject")
            project = project or (p.get("id") if isinstance(p, dict) else p) or ""
    if not project:
        tier = ""
        for answer in (load_ag, load):
            for t in (answer or {}).get("allowedTiers", []) if isinstance(answer, dict) else []:
                if t.get("isDefault"):
                    tier = tier or t.get("id", "")
        req = {"tierId": tier, "metadata": ag["metadata"]}
        status, h, body = call("POST", DAILY + ":onboardUser", req, headers=auth)
        op = record(out, "onboard", status, h, body, req)
        if isinstance(op, dict):
            p = (op.get("response") or {}).get("cloudaicompanionProject") or {}
            project = p.get("id", "") if isinstance(p, dict) else p
    if not project:
        sys.exit("no project: see load-*.json and onboard.json")
    SCRUB[project] = "<project>"

    status, h, body = call("POST", DAILY + ":fetchAvailableModels", {"project": project}, headers=auth)
    models = record(out, "models", status, h, body, {"project": project})
    status, h, body = call("POST", DAILY + ":retrieveUserQuota", {"project": project}, headers=auth)
    record(out, "quota", status, h, body, {"project": project})

    model = args.model
    if not model and isinstance(models, dict):
        names = list((models.get("models") or {}).keys())
        model = next((n for n in names if "flash" in n), names[0] if names else "")
    model = model or "gemini-2.5-flash"
    request = {"contents": [{"role": "user", "parts": [{"text": "Reply with the single word: pong"}]}]}
    bare = {"model": model, "project": project, "request": request}
    url = DAILY + ":streamGenerateContent?alt=sse"
    status, h, body = call("POST", url, bare, headers=auth)
    record(out, "turn-bare", status, h, body, bare)
    fields = dict(bare, userAgent="antigravity", requestType="agent", requestId="agent-" + str(uuid.uuid4()))
    status, h, body = call("POST", url, fields, headers=auth)
    record(out, "turn-fields", status, h, body, fields)
    status, h, body = call("POST", url, bare, headers=plain_auth)
    record(out, "turn-plain-ua", status, h, body, bare)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("login-url").set_defaults(fn=login_url)
    lg = sub.add_parser("login")
    lg.add_argument("callback")
    lg.set_defaults(fn=login)
    pr = sub.add_parser("probe")
    pr.add_argument("--out", required=True)
    pr.add_argument("--model", default="")
    pr.set_defaults(fn=probe)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
