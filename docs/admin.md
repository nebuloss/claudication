# The admin UI, clients and limits

Five screens, at the gateway's own address. Each page has a path of its own, so
a link to one opens on it.

**Overview** — whether the gateway can serve a request, and today's traffic.

**Accounts** — the Claude, ChatGPT and Google accounts the pool draws on, each
provider's in its own priority order, each showing its 5-hour and 7-day
subscription usage, which clients drew on it, and a switch to pause it.

**Clients** — the tools that use the gateway: each one's key and setup, its
model names, its usage with its limits beside it, its chats and its requests,
a switch to turn it off, and one to shrink images Claude would refuse.

**Activity** — where the traffic went, by model, by client and by account,
each with its traffic over days above its table; and the requests: the last
hour live, the outcomes over days, and the request log, every client's, with
the upstream's own error text, paged back as far as retention keeps it.

**Settings** — a page each: **General**, the Gateway switch and the admin
password and sessions; **Models**, how each provider's models are offered (by
family, the default, or every model), which version each family serves (the
latest, per account, or one for the gateway) and which account serves which; **MCP**, the MCP server's switch and address;
**Configuration**, the configuration as loaded.

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
