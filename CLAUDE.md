# Working on claudication

A gateway that proxies Claude subscription accounts over OAuth, with an
embedded admin UI. Go, no CGO; the UI is built into the binary with
`go:embed`.

It serves two client-facing APIs — Anthropic Messages and OpenAI Responses,
which is what Codex CLI speaks — and speaks only Anthropic upstream. Both can
be switched on and off at runtime from Settings.

## Read these before changing the relay

1. `go doc ./internal/upstream` — the rule the relay follows, its five
   exceptions, and why each exists.
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
   surfaces, their switches, and how to add another.
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
five exceptions, all in `internal/upstream`, each because the request cannot
otherwise succeed. Do not add a sixth on a hunch — see below.

## When a request fails for no visible reason

The subscription backend refuses some requests on their *content* and answers
with a message about billing that names nothing:

    Third-party apps now draw from your extra usage, not your plan limits.

Three triggers have been found. All three were found by bisecting a captured
request body; none was found by reasoning about it. `scripts/bisect-refusal.py`
does that automatically — point it at a captured body and a live key and it
narrows to the smallest failing part in a dozen requests.

The gateway labels this refusal in the request log (`ClassifyRefusal`), because
it costs hours every time someone meets it fresh.

## Commands

    make check        # fmt-check, vet, test, typecheck, web build — run this
    make build        # binary at dist/claudication, UI embedded
    make test         # Go tests only
    make typecheck    # tsc over web/

The tests do not need credentials or a network. Anything that probes the real
API is a script under `scripts/`, run by hand, and says so.

## Conventions

- Comments say *why*, and record what was measured. The measurement tables in
  `internal/upstream` are the reason nobody has to reverse the client again;
  keep them accurate or delete them, but do not let them drift.
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
