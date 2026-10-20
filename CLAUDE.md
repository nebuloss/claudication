# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Working on claudication

A gateway that proxies Claude and ChatGPT subscription accounts over OAuth,
with an embedded admin UI. Go, no CGO; the UI is built into the binary with
`go:embed`.

It serves two client-facing APIs — Anthropic Messages and OpenAI Responses,
which is what Codex CLI speaks — and has two upstreams: Anthropic, and
ChatGPT's Codex backend. One Gateway switch in Settings turns client traffic
on and off at runtime; each client and each account has its own switch too.

A client is a top-level thing — a type (Claude Code, Codex, opencode, crush,
custom), one API key, its own model names ("slots"), limits and switches;
`go doc ./internal/service/clients` holds each type's spec with the sources it
was read from. There are no gateway-wide aliases: a request's model is one of
its client's names, bound to a model or to a target that follows the newest
of a family, or — for a custom client in direct mode — a real model id. It
then goes to the provider whose accounts serve that model
(`go doc ./internal/service/models`, route.go). Responses reaches ChatGPT as it
came and Claude translated; Messages reaches Claude only, and is refused a
ChatGPT-served model rather than translated.

## Where code goes

`go doc ./internal/arch` is the map: every package on a layer, importing only
from layers below, and the few rules layering alone does not say. Its test
fails the build when an import crosses a layer or a new package is not on the
map, so place a new package there when you add it.

The HTTP surface is `internal/httpapi`, which only puts things together, with
one package per surface below it: `httpx` (shared plumbing), `gateway` (the
client-facing APIs), `admin` (the admin API) and `web` (the embedded UI, and
the welcome page at the root of a relay-only listener). They do not import one another; the root hands across what
one needs from another.

## Read these before changing the relay

1. `go doc ./internal/relay/passes` — the rule the relay follows, its seven
   exceptions, and why each exists; `passes.OpenAI` is the ChatGPT wire's
   pipeline, which runs one of them. `go doc ./internal/relay` is the relay
   itself: accounts, retries, streaming, the stall retry. There is one relay
   per provider. `go doc ./internal/provider` is the line between the relay
   and the upstreams — `Wire` for the relay, `Accounts` for the pool, the
   poller and the admin API — and `internal/provider/anthropic` and
   `internal/provider/openai` the only places each one's URLs, headers, event
   names and error shapes appear. `go doc ./internal/provider/openai` is the
   measurement record for the ChatGPT backend.
2. [`docs/`](docs/) — `refused-requests.md` first: every refusal with the
   measurements behind it. Then `upstream-request-pipeline.md` for what the
   official client sends, and `reversing.md` for how to check any of it.

## Read these before adding or changing a client-facing API

1. `go doc ./internal/api` — what a dialect has to provide, and what it gets
   for free. Anthropic is an implementation of that interface rather than a
   special case, which is what keeps the relay free of per-client branches.
2. `go doc ./internal/api/openai` — the complete record of what Codex sends,
   what it does with the answer, and the measurement behind each mapping
   decision. Kept in the code so nobody has to reverse Codex twice.
3. [`docs/client-apis.md`](docs/client-apis.md) — the operator's view: the
   surfaces, the Gateway switch, and how to add another.
4. [`configs/clients/`](configs/clients/) — a complete, commented config file
   per client, and the one copy of each: the admin UI imports them as text and
   substitutes the gateway's address, and [`docs/clients.md`](docs/clients.md)
   links to them rather than repeating them. Change a stanza there, nowhere
   else.
5. `scripts/probe-codex.py` replays a real Codex request against a live gateway
   and judges the answer the way Codex does.

One package per surface under `internal/api/`, with its own translation beside
it. A dialect's mapping is only defensible next to the evidence for it, and the
evidence is per dialect.

The relay's rule is that the caller's bytes go upstream unchanged. There are
seven exceptions, each named in `internal/relay/passes`, each because the
request cannot otherwise succeed — six repairs in the default pipeline, and the
model-role rewrite the gateway applies when a request names one. Do not add
an eighth on a hunch — see below.

## When a request fails for no visible reason

Anthropic's subscription backend refuses some requests on their *content* and
answers with a message about billing that names nothing:

    Third-party apps now draw from your extra usage, not your plan limits.

Three triggers have been found. All three were found by bisecting a captured
request body; none was found by reasoning about it. `scripts/bisect-refusal.py`
does that automatically — point it at a captured body and a live key and it
narrows to the smallest failing part in a dozen requests.

The gateway labels this refusal in the request log (`ClassifyRefusal`), because
it costs hours every time someone meets it fresh.

## Commands

    make check        # fmt-check, vet, test, typecheck, test-web, web build — what CI runs
    make build        # binary at dist/claudication, UI embedded
    make test         # Go tests only, with -race (needs cgo)
    make test-norace  # the same without cgo, for musl or scratch toolchains
    make test-web     # the chart scales and the UI's pure modules, under node
    make typecheck    # tsc over web/ — vite strips types without checking them
    make repro        # build twice and prove the bytes match; CI runs it too

One Go test: `go test ./internal/relay/passes -run TestName -race -count=1`.

The Makefile pins `GOTOOLCHAIN` to the `go` directive in `go.mod`, so a plain
`go` command may resolve a different compiler than `make` does. CI also fails
on `go mod tidy -diff`.

The UI is embedded from `internal/httpapi/webdist`, which only `make web`
fills (git holds just a `.gitkeep`); a bare `go build` ships no UI. For UI
work, `make dev-api` runs the gateway with debug logging and `make dev` runs
Vite on :5173 proxying the API to it.

The binary's subcommands are `serve`, `clients`, `login-url`, `passwd`,
`vacuum`, `backup` and `restore` (`cmd/claudication`).

The tests do not need credentials or a network. Anything that probes the real
API is a script under `scripts/`, run by hand, and says so.

## Conventions

- Comments say *why*, and record what was measured. The measurement tables in
  `internal/relay/passes`, `internal/relay`, `internal/provider/anthropic` and
  `internal/provider/openai` are the reason nobody has to reverse the client
  again; keep them accurate or delete them, but do not let them drift.
- Commit messages are imperative and explain the reasoning, not the diff.
  Commits are authored by the repository owner and carry no co-author or
  generated-by trailers.
- The UI is Tailwind 4 with an MD3-flavoured token set in
  `web/src/ui/styles/index.css`. Use the tokens (`text-on-surface`,
  `bg-surface-container`, `state-layer`), not raw colours.

## Deploying

`scripts/install.sh` installs or updates a systemd or OpenRC service and writes
`/etc/claudication/config.yaml` on first run. State — the database and the
sealing key — lives in the state directory and never in config.
`claudication backup FILE` takes a consistent snapshot without stopping the
service; that file holds the sealing key and every stored token together, so it
is exactly as sensitive as the state directory.
