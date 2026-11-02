# The admin UI, clients and limits

Five screens, at the gateway's own address. Each page has a path of its own, so
a link to one opens on it.

**Overview** — whether the gateway can serve a request: its status and
today's traffic, its availability (the 90 days, and every incident behind
History), and its process's memory.

**Notifications** — the bell in the header, on every page, counts what needs
the operator now in the colour of the worst, and opens the notification
centre: an account to sign in again, a model failing everywhere, a client
whose requests keep failing, an incident of the last day — filtered by how
bad, grouped by what about, each a link to where it is dealt with. Nothing to
dismiss: a notification goes once its problem does.

**Accounts** — the Claude, ChatGPT and Google accounts the pool draws on, each
provider's in its own priority order, each showing its 5-hour and 7-day
subscription usage, which clients drew on it, and a switch to pause it.

**Clients** — the tools that use the gateway: each one's key and setup, its
model names, its usage with its limits beside it, its chats and its requests,
a switch to turn it off, one to shrink images Claude would refuse, and one to
record its content.

**Recording content.** A client's *Record content* switch keeps, for an hour,
everything its requests carry: each request as the client sent it, every
attempt as the gateway sent it upstream (translated, where it was), and both
answers, each part of a stream timed. A recorded request opens from the
request log with a Content section: each body as a conversation — read into
Anthropic Messages, the form every API is translated through, so Claude Code,
Codex and gemini-cli read alike — or raw, and downloadable as HAR for browser
developer tools, Charles or Fiddler. A chat has pages of
its own, from its name in the client's Chats: its overview
(`/clients/<id>/chats/<chat>`) — its figures, the models it ran on by the
names it asked by, and its context curve — then **Requests**, every request a
line each, and **Transcript**, each recorded request as what it added to the
conversation — the messages new since the turn before — and its answer. Both
lists scroll in a box of their own and draw only what is in view;
`?request=<row>` opens them on one request, which is where a request's dialog
links with *Open in its chat*. The chat's crumb lists the client's other
chats.
It is held in memory
(`internal/service/capture`), credentials taken out of every header, each body
cut at 8 MB and 256 MB held in all, the oldest dropped first, and never in the
database or a backup. By default a restart or an update clears it.

**Keeping recordings on disk.** With `capture.retention-days` above 0 (at most
`usage.retention-days`), each recorded request is also kept on disk once it has
its request log row, and read back from there after a restart or a deploy,
exactly as it passed: every leg, header, body and the timing of each streamed
part. It is kept in a file of its own beside the database
(`capture-v1.db`, `internal/store/recordings`), sealed under keys derived from
the gateway's own, so the file alone is unreadable without `secret.key`. It is
never in a backup, and a restore discards it. Each body is cut where its content says and
each piece stored once, so a conversation resent on every turn costs about
what each turn added, whatever its API or provider. It is written in the
background, never on a request's way, and dropped rather than waited for when
the writer is behind or the disk has under 100 MiB free; pruned every ten
minutes to its retention and to `capture.disk-budget` (200 MiB by default),
oldest first. The recording switch itself is not kept: a deploy stops a
recording in progress, and what it recorded stays.

**Traffic** — the last hour live; where the traffic went, by model, by client
and by account, each with its traffic over days above its table; and the
requests: their outcomes over days above the request log, every client's,
with the upstream's own error text, paged back as far as retention keeps it.

**Settings** — a page each: **General**, the Gateway switch and the admin
password and sessions; **Models**, how each provider's models are offered (by
family, the default, or every model), which version each family serves (the
latest, per account, or one for the gateway) and which account serves which; **MCP**, the MCP server's switch and address;
**Configuration**, the configuration as loaded; **Diagnostics**, how changes
reach the screens.

## Clients, keys and limits

Every client holds one key. A key authenticates by `sha256(key)` and a lookup
prefix, and is also kept sealed with the same key that seals the account
tokens, so the client's page can show it, and write it into the client's
files, until the client is configured: the first time its install command runs
or its key is used. From then on neither the key nor the install command is
offered; regenerating the key starts over. A copy of the state directory
therefore holds working keys as well as working account tokens, which it
already did.

Each client can carry two limits, both off by default, set on its Usage page
beside the figures they bound, with suggestions from its own last 7 or 30 days
and a preview of what a limit would have refused:

- **A request rate** — a count and the period it is measured over, minute, hour
  or day. The whole allowance is available at once and refills over the period,
  so 200 an hour permits a burst of 200 and then runs dry. Off means the
  gateway's own `limits.requests-per-minute`.
- **A token budget** — tokens per rolling 24 hours, counting input, output and
  both cache columns, because all four are billed. Rolling rather than
  calendar-aligned, so it frees up continuously instead of everything becoming
  possible again at midnight. Off means no ceiling.

A client can exceed its budget by one request, necessarily: what a request costs is
only known once it has been served. The refusal carries `Retry-After` computed
from when the oldest counted request ages out.

### Per-request history

One row per proxied request: which client, which of its names, which account,
which model, how many tokens, how long, and what failed. It survives the client
or account it refers to being deleted — otherwise the record you go looking for
after removing a client is precisely the one that disappears.

`usage.retention-days` bounds it (30 by default; 0 records nothing) and a daily
prune enforces it, returning the freed pages to the filesystem.
