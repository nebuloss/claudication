# How the admin UI stays current

The rules every piece of data the admin UI shows follows: how it is read,
how it changes, and how a change reaches the screen. They replace per-panel
polling, which [data-flow.md](data-flow.md) maps as it was. Each rule is
enforced by a test or by the code's shape, so a new screen or a new write
cannot quietly break it.

## The rules

1. **Every change says what it changed.** Every write to the database, and
   every change to state kept in memory that a screen shows, marks a
   **topic** changed. The topics are one list, `internal/live`.
2. **One stream per tab says what changed.** A tab opens one server-sent
   event stream, naming the topics its screen shows. The gateway sends a
   **notice** when one of them changes — at most one per topic per its
   interval — and nothing else.
3. **A notice is answered by reading again, through the API.** The stream
   carries no data the API serves, so there is one source of truth; a panel
   reads again when a topic it names changes, and not otherwise.
4. **A read never calls an upstream.** Reads answer from the database and
   memory. Asking a provider — a model list read now, an account's usage now,
   a model tried — is an action: a POST, or `?fresh=1`, that stores what it
   learns, which then changes a topic like any other write.
5. **An expensive read is computed once per change.** Its answer is kept,
   tagged with the versions of the topics it reads, and served again — or
   answered `304 Not Modified` — until one of them changes.
6. **Clocks tick in the browser.** Durations, countdowns and "resets in" are
   computed from times the API gives, so they move without a read.

An idle tab makes no requests at all: its stream is open, and nothing
changes.

## Topics

| Topic | Covers | Interval |
|---|---|---|
| `accounts` | accounts: row, order, name, switch, plan, tokens, errors, last use | 1 s |
| `quota` | accounts' usage windows | 1 s |
| `pool` | cooldowns, model holds, leases in flight | 2 s |
| `models` | models switched off, model checks and scans, families, model lists | 1 s |
| `clients` | clients, keys, model names, limits, switches, install links | 1 s |
| `running` | requests running now, in flight per client | 1 s |
| `requests` | the request log and everything counted from it | 2 s |
| `chats` | chat titles | 1 s |
| `settings` | the gateway's switches | at once |
| `uptime` | uptime samples and outages | 10 s |

The interval bounds how often a topic is announced, whatever its rate of
change: at a hundred requests a second, `requests` is one notice every two
seconds.

## How it is built

### The hub — `internal/live`

A leaf package, so the store can use it.

- `Hub.Changed(topics...)` marks topics changed: one atomic add each and a
  non-blocking wake, so it is safe on the hottest path and under the caller's
  own lock. A nil hub marks nothing, so a command run beside the gateway
  needs none.
- Each topic has a **version**: the count of its changes. Raised by every
  `Changed`, announced or not, so an answer tagged with it is stale exactly
  when a change came after it — including one made a moment ago that the
  interval has not let through yet.
- `Hub.Run` wakes every 250 ms (at once for a topic with no interval) and
  announces each topic whose version moved since its last notice and whose
  interval has passed. Every stream subscribed to it is told; a send that
  would block marks that stream behind instead.
- A stream that fell behind is sent `resync` once its buffer has room: read
  everything again. A slow tab never holds the hub or its memory.
- `Hub.Watched(topics...)` is whether an open stream names any of them. The
  usage poller works at its fast rate while one names `quota`
  (`accounts.Poller.Viewed`), since a tab on its stream asks nothing while
  nothing changes.
- When `Run` stops — on a reload's release, with the other background work —
  it ends every stream, and any opened after. Tabs move to the new worker at
  once instead of holding the old one through its drain.

### Where changes are marked

- **The store**: every function that writes defers `s.changed(topics...)` at
  its top, so it marks once it returns, whichever way it returns; a write
  that failed costs a tab one read for nothing. `TestEveryWriteMarksATopic`
  reads the store's source and fails when a function that runs `INSERT`,
  `UPDATE`, `DELETE` or `REPLACE` marks none — or says `//live:none` and why
  (the admin password, sign-in links, a helper whose caller marks).
- **Memory**: the pool (cooldowns, holds, leases), the running list, the
  availability checker's scans and the model lists read from an upstream mark
  their topics where their state changes (`Announce`). Install links and the
  in-flight counts sit on the lowest layer, below `internal/live`, so they
  take an `OnChange` hook and the composition root names the topic.
- **Settings**: a switch is a settings row, so the store marks it — `settings`,
  and `models` with it, since families and model checks are switches.
- **Not seen**: writes by another process — the command line, or the old
  worker while a reload drains. A reload ends every stream and tabs read
  again on reconnecting; the command line's writes show at the next change
  of the same topic.
- **Time**: a cooldown ending or a token expiring writes nothing. Screens
  have the end times and count them down; a cached read's `maxAge` bounds
  what time alone changes in an answer computed on the gateway.

### The stream — `GET /admin/events?topics=…`

- Server-sent events, behind the admin session. Each message is
  `id: <seq>`, `event: <topic>`, `data: {"v": <version>}`, or
  `event: resync`. `retry: 1000` first, a keepalive comment every 15 s, and
  `X-Accel-Buffering: no`, as the client-facing streams do.
- A stream lasts at most 30 minutes; the browser opens the next at once, and
  the session is checked again then.

### Cached reads

`Admin.cached` wraps an expensive GET. Its answer is kept per URL with the
versions of the topics it names, read *before* it is computed, and an ETag
over its bytes. A read while none of those topics has changed and younger
than its `maxAge` is the kept answer, or `304` to a browser that has it; one
computed again to the same bytes keeps its ETag, so the browser still gets
`304`. Concurrent reads of one URL compute it once. Only a 200 is kept, and
at most 256 URLs.

The wrapped routes, their topics and their `maxAge` are listed together where
the routes are registered (`internal/httpapi/admin`, `Routes`): accounts,
overview, clients, a client, a client's usage, usage, slots, chats, a chat.

### The browser

- `api/live.ts` holds the topics (`TOPICS`, which `TestTopicsMirrored` holds
  to the hub's), the tab's one `EventSource`, and `READS`: what each admin
  read is computed from, the same lists the gateway tags its cached reads
  with.
- A panel reads with `useLoader(load, { on: READS.x })`. The stream is open on
  the union of the topics the mounted panels name, and opened again when that
  set changes. A notice makes each panel on its topic read again — once,
  however many of its topics changed (notices are gathered for 30 ms), and
  once more if one arrived while it was reading.
- A hidden tab closes its stream, and every panel reads once when it is
  shown. A stream that dropped and came back reads every panel once, since
  what changed meanwhile was told to nobody. A stream refused — a 401, a
  502 — has every panel read at once, so a lapsed session is the sign-in
  screen, and it is tried again every 5 s.
- While the stream is down, panels read every 30 s, as a floor.
- A panel that must not move under a hand — the accounts list mid-drag —
  passes `on: []` meanwhile, and reads once when it names its topics again.
- No Refresh button and no Live switch: `Freshness` says `live` while the
  stream is open, and `offline` with the figures' age while it is not.
- Duplicate reads of one URL by two panels cost the gateway nothing to
  answer: the cached read serves both, and the browser revalidates with the
  ETag.
- Whether a new UI was deployed is checked when the stream comes back — a
  deploy ends every stream — and when the tab is shown.
- The public status page has no session and no stream; it keeps its own
  timer.

### Diagnostics — Settings › Diagnostics

`GET /admin/live`: per topic, its changes, notices and the tabs listening;
the open streams; per cached read, how often it was kept or computed and the
last cost. What to look at when a screen is late, or the gateway busy.

## Adding something

- **A new screen**: `useLoader(load, { on: READS.x })`, adding the read to
  `READS` with every topic its endpoint reads.
- **A new expensive read**: register it with `cached(...)` and the same
  topics.
- **A new write**: defer `s.changed(topic)` at its top; the test fails until
  it does.
- **New state in memory a screen shows**: mark its topic where it changes.
- **A new topic**: a constant and its interval in `internal/live`, the same
  name in `web/src/api/live.ts`; `TestTopicsMirrored` checks the two agree.
- **Something only a provider knows**: an action that stores it, never a
  read that asks.
