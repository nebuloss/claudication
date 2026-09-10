# How these findings were obtained, and how to check them

## How to check any of this again

**The measured half** is reproducible with `scripts/bisect-refusal.py` and a
live key, or with a few lines of curl. Nothing here needs the client.

**The bundle half** needs the client binary, which is not in this repository
and should not be. On a machine with Claude Code installed it is a Bun
single-file executable at `~/.local/share/claude/versions/<version>` — one ELF
with the whole JavaScript blob inline, roughly 200 MB. Everything cited below
was read straight out of it:

```sh
python3 - <<'PY'
import re
data = open('/path/to/versions/2.1.263', 'rb').read()
for m in re.finditer(rb'function VB\(', data):        # or any literal
    print(m.start(), data[m.start()-40:m.start()+400].decode('utf8', 'replace'))
PY
```

Two kinds of offset appear below, and they are **not** the same address space:

- `region_NNN @offset` — into a split of the blob into ~25 numbered files, made
  during the first pass. That split was scratch and no longer exists; treat
  these as "somewhere in the JS blob, in the file that held this region" and
  re-find the symbol by name.
- `BIN @offset` — a byte offset into the binary itself, which is stable for
  that exact build and directly usable with the snippet above.

Either way the reliable move is to search for the **literal or function name**,
not to seek to an offset: the offsets rot with every release, the names have
survived several. And a version bump renames the minified identifiers but
rarely the strings, so grep for the string first and read outward.

---

## Gaps, closed and remaining

The first pass split the bundle into region files and several definitions fell
outside them. They are all in the binary; searching it directly closes most.

**Retry backoff.** [client] `@180451885`:

```js
function VB(e, t, r = 32000) {
  let i = Math.min(500 * Math.pow(2, e - 1), r);
  let o = Math.round(i + Math.random() * 0.25 * i);
  if (t) { let u = parseInt(t, 10); if (!isNaN(u)) return Math.max(u * 1000, o) }
  return o
}
```

500 ms doubling per attempt, capped at 32 s, plus 0–25% jitter, and
`retry-after` (in seconds) wins whenever it is larger. Worth noting the earlier
guess from a structurally identical sibling — 5 s base, 20 s cap — was the right
*shape* and the wrong numbers, which is why it was not asserted.

**Endpoints.** [client] `@177161221`: `BASE_API_URL` is
`https://api.anthropic.com`, `TOKEN_URL` is
`https://platform.claude.com/v1/oauth/token`, `CLAUDE_AI_ORIGIN` is
`https://claude.ai`, and the OAuth client ids are literals in the binary.

**The OAuth-vs-key decision.** [client] `@179399142`:
`I9t(e) => e.anthropicAuthEnabled && e.oauthScopes?.includes(<scope>)` — so the
OAuth path additionally requires the stored grant to carry the inference scope,
on top of the precedence rules in the send-path section of
[upstream-request-pipeline.md](upstream-request-pipeline.md).

**MCP output truncation.** [client] The gate reads `MAX_MCP_OUTPUT_TOKENS`, and
when it fires the model is told:

> `[OUTPUT TRUNCATED - exceeded <n> token limit]`
>
> The tool output was truncated. If this MCP server provides pagination or
> filtering tools, use them to retrieve specific portions of the data. If
> pagination is not available, inform the user that you are working with
> truncated output and results may be incomplete.

This is the other half of the MCP story in
[upstream-request-pipeline.md](upstream-request-pipeline.md): the client truncates, tells the
model it truncated, and asks it to paginate. A third-party client that
truncates without saying so leaves the model reading a fragment as though it
were whole — and if it cuts mid-character, produces the surrogate failure in
[refused-requests.md](refused-requests.md).

**Still open**, and only worth chasing if something depends on them: the
default value of `MAX_MCP_OUTPUT_TOKENS` (a validated env var, not a literal),
the media count and byte caps, and microcompaction's `keepRecent`.
