# How data moves between the gateway and the admin UI

A map of every way the admin UI gets its data and keeps it fresh, where that
data changes on the server, and what the gateway does on its own schedule —
written to redesign refreshing around one event channel. Audited 2026-10-30
at `79d7d21` from the source; paths are from the repository root.

## In short

- The UI keeps itself fresh by **polling**: 18 timers, from 2 s to 60 s, each
  panel its own, paused by a global Live toggle and a hidden tab; 9 screens
  have a Refresh button. Nothing is pushed: there is no EventSource or
  WebSocket anywhere.
- Several polled endpoints are **expensive** — the client page every 4 s,
  `/admin/clients`, `/admin/usage`, `/admin/usage/slots`, `/admin/chats` —
  and an account's model list, polled every 3 s during a check, **calls the
  upstream on every load**.
- **Any admin request speeds the usage poller up 15×** (5 min → 20 s), so
  merely having a tab open costs upstream calls on every account.
- Server state changes in about 50 places that fall into **10 topics**; only
  five change per client request, and those must be coalesced.
- Proposed: one **server-sent event stream per tab** carrying small "this
  changed" notices (and, later, events with data), the UI re-reading only
  what a notice concerns, the poller's fast rate driven by connected
  subscribers. Section 5.

## 1. How the UI gets its data today

Hooks (`web/src/ui/hooks.ts`): `useLoader` fetches on mount and when its
`deps` change; `refreshMs` polls through `usePoll`, which runs only while Live
is on and the tab is visible, and fires at once when the tab becomes visible;
one poll in flight per loader, a failed one keeps the last data.

| Screen | Endpoint | Refresh |
|---|---|---|
| Overview | `/admin/overview` | 10 s |
| Overview › availability, welcome page | `/status.json` | 60 s, own timer, ignores Live |
| Accounts list | `/admin/accounts` | server's `usage_poll_s` (20 s); paused while editing |
| Account page | `/admin/accounts` (whole list) | 20 s |
| Account › models | `/admin/accounts/{id}/models` | 3 s while a check runs, else once — **upstream call each time** |
| Account › families | `/admin/model-families` | once, and after each model switch |
| Account › usage › clients | `/admin/accounts/{id}/clients` | once |
| Clients list | `/admin/clients` | 20 s |
| Client page | `/admin/clients/{id}` | **4 s** on its overview tab, 20 s on others |
| Client › limits (live) | `/admin/clients/{id}/limits` | 5 s |
| Client › setup | `/admin/keys/{id}/secret` (twice), `/admin/clients/{id}/files` | once |
| Client › chats, chat detail | `/admin/chats`, `/admin/chats/{id}` | 20 s |
| Requests log | `/admin/requests` (first page merged) + `/admin/requests/facets` after every poll | 10 s |
| Running requests | `/admin/requests/running` | 2 s |
| Activity › overview | `/admin/usage/live` 5 s; `/admin/usage` 60 s | |
| Activity › models/clients/accounts | `/admin/usage/slots` + `/admin/usage` | 30 s; Activity › Accounts adds `/admin/accounts` at 20 s |
| Settings | the shell's `/admin/config`; `/admin/model-families` | once; reloaded after each switch |
| Shell | `/` (new UI bundle?) | 60 s |

**Mutations** never update selectively: every write ends in a full re-read of
the page's loader through `onChanged`/`reload()` — often re-reading what the
write's own answer already returned (`POST /admin/accounts/order`, most client
POSTs, `setVersion`).

**Local clocks** (`useNow`): Freshness labels, running requests' durations,
sign-in and install countdowns. Relative times (`relative()`: "resets in",
"last worked", token expiry) have no timer of their own and move only when a
poll re-renders the panel; with polling gone they need one.

**Duplicate fetching**: `/admin/overview` twice on the Overview; the client
key's secret twice on Setup; `/admin/client-types` once per `ClientTable` —
on Activity › Models, once per model shown; facets re-read in full on every
10 s log poll.

## 2. The admin API, by cost

67 routes (`internal/httpapi/admin/admin.go:150-214`). Cheap: one indexed
query or memory. Medium: aggregation or several queries. Expensive: upstream
calls, full scans, per-account loops.

**Polled and expensive:**

| Endpoint | Why | Polled |
|---|---|---|
| `/admin/clients/{id}` | usage of the key ×2, subscription shares (two GROUP BYs per account), each slot resolved (each a `ListAccounts`) | **every 4 s** |
| `/admin/clients` | the same, for every client and slot | 20 s |
| `/admin/usage` | one grouped pass over the window (measured 0.5–2.2 s) | 60 s, and 30 s on Activity pages |
| `/admin/usage/slots` | GROUP BY over the window + subscription shares | 30 s |
| `/admin/chats` | rolls up the window per conversation | 20 s |
| `/admin/accounts/{id}/models` | **upstream model list, uncached** (ChatGPT's is ~573 KB) | 3 s during a check |
| `/admin/overview` | totals + two percentiles over 24 h | 10 s |

**Not polled, but upstream on every load, uncached:** `/admin/models`,
`/admin/keys/{id}/models`, `/admin/clients/{id}/files`, install/update
commands — each reads **every enabled account's** model list
(`Catalog.List`, `internal/service/models/list.go`); so does a client's
`/v1/models`. Only the families built from those lists are cached (10 min).

**Cached today:** request facets (60 s, single-flight); the models.dev
catalogue (6 h); families and who serves what (10 min, background refresh);
each account's last list (as a fallback, not in front of `List`). **Not
cached:** subscription shares, pool status, usage aggregations, chats.

`usage_events` is indexed on `at`, `(conversation_id, at)`, `(error_code,
at)` and refused rows — **not on `key_id` or `account_id`**, which every
per-client and per-account aggregation filters by.

## 3. Where the data changes

| Topic | What it covers | Changed by | Rate |
|---|---|---|---|
| `accounts` | row, order, name, disabled, plan, tokens and expiry, refresh-dead, last error, last used, deleted | admin, token refresh, relay (error / used), poller (plan) | rare; last error per failed request; last used ≤ 1/min |
| `quota` | an account's usage windows | poller (`SetAccountQuota`), relay (`NoteQuota`, ≤ 1 per 15 s per account) | medium |
| `pool` | cooldowns, model holds, leases in flight | every request (lease up/down), failures, availability | **per request** (memory) |
| `models` | models off (account/key), model tests, failing/scanning, family pins and modes, lists refreshed | availability checker, relay outcomes (≤ 1/min per pair), admin, catalogue refresh | medium |
| `clients` | clients, keys, slots, limits, switches, configured, rotate, delete, install links, key last used | admin, **MCP agent** (slots), install command, request (last used ≤ 1/min) | rare |
| `running` | running requests, in flight per key, budgets | every request, twice | **2N/s** (memory) |
| `requests` | request log, usage totals, chat activity | every request (`Recorder.Record`), refusals, titler, agent; daily prune | **N/s** |
| `chats` | titles | titler (async), admin, agent | low |
| `settings` | gateway / MCP / model-check switches | admin (`settings.Switch.Set`, one choke point) | rare |
| `uptime` | samples, outages | every minute, start/stop | 1/min |

Choke points that make publishing simple: `Recorder.Record` for the log,
`Switch.Set` for switches, `Checker.Record` for model results. Account writes
come from four packages, so `accounts` is best published from the store.

**What an in-process channel would not see:** writes by the CLI from another
process (`clients add/delete`, `passwd`, `login-url`, `vacuum`, `restore`);
the old worker's writes while it drains during a reload; and **time passing**
— a cooldown or hold ending, a token expiring, an install link lapsing —
which writes nothing.

## 4. What the gateway does on its own

| Loop | Interval | Upstream | Faster when watched? |
|---|---|---|---|
| Usage poller | 5 min idle, **20 s watched** (`usage.poll-*`) | each account's usage endpoint (Google: two calls) | **yes** |
| Availability checker | 1 min tick; failing models every 15 min | one tiny turn per due model | no |
| Startup scan | once, accounts with no results | model list + a turn per model | no |
| Family refresh | on read, 10 min (1 min after a partial read) | every account's model list | no |
| Catalogue | 6 h | models.dev | no |
| Pruner, sweepers, trimmer, uptime | 24 h, 1–10 min, 5 s, 1 min | none | no |

**"Someone is watching"** is the time of the last admin request
(`Poller.NoteActivity`, called by `RequireAdmin`), valid for 2 minutes. Any
open tab polling anything keeps it — watching the request log alone keeps
every account's usage polled every 20 s.

The per-request path touches UI state at: in flight per key
(`gateway/inference.go:92`), the running list (`:320`/`:331`), the pool's
lease and cooldowns (`relay/passthrough.go`), quota between headers and body,
availability outcomes, the request log (`Recorder.Record`, synchronous after
the response), chat titles (async).

## 5. Proposed design: one event channel

### Channel

- `GET /admin/events`: Server-Sent Events, one stream per tab, behind the
  admin session. Plain HTTP — through the operator's proxy as the client
  streams already are, with the keepalive and `X-Accel-Buffering: no` those
  use — reconnecting by itself. Nothing flows the other way that a POST does
  not already carry.
- Envelope: `id: <seq>`, `event: <topic>`, `data: {"at": …, "data": …}`.
  `id` lets a reconnecting tab say what it saw last (`Last-Event-ID`); one
  the server can no longer serve from gets `resync`.
- One package, `internal/events`, holds the hub and the topic list — each
  topic a constant with when it fires — and the UI mirrors the list with
  each topic's data type. A new event: a constant, a `Publish` call where it
  happens, a subscriber in the UI. Unknown topics are ignored by an older UI,
  so the server can add them freely.

### Two kinds of event

- **Notices** — "topic X changed", no data. Coalesced per tab: at most one
  per topic per second, so 50 requests in a second are one `requests`
  notice. What panels reload on.
- **Events with data** — for what no endpoint holds: a toast ("switched
  gemini-2.5-pro off: failing for 30 min"), a check's progress. A small
  bounded queue per tab; a tab that falls behind loses its queue and gets one
  `resync` (reload everything). A slow tab can never hold server memory.

Snapshots of what the API already serves (request rows, account lists) are
not pushed: reloading through the API keeps one source of truth.

### Publishing

- After the change is stored and its locks released, never under a lock, and
  never blocking: `Publish` drops into each subscriber's coalescing set. The
  hot paths (`NoteQuota`, lease, running) only mark a topic dirty.
- High-frequency topics (`running`, `requests`, `pool`, `quota`) are dirty
  flags, flushed by the hub's one-second tick; the rest flush at once.
- A "request finished" notice belongs where the request ends
  (`inference.go:331`), not in the recorder, which is off when history is.

### The UI

- One provider owns the `EventSource`; `useEvent(topic, handler)` for
  anything that reacts; `useLoader(load, { on: ['requests', 'running'] })`
  replaces `refreshMs`. Several notices for one panel are one reload; a
  hidden tab reloads nothing and catches up once when shown.
- While the stream is down: polling at 30 s, as a floor.
- Relative times get `useNow`, so they keep moving without polls.
- Removed: Refresh buttons, the Live toggle; Freshness says whether the live
  stream is connected.

### Watching

`Poller.Watching()` becomes "a tab is subscribed to a topic that shows usage
(`accounts`/`quota`) and visible" — the stream tells the server when its tab
hides — not "an admin request came in the last 2 minutes". Watching the
request log no longer polls every account every 20 s.

### What it does not see, and how it copes

- **Other processes** (CLI, the draining old worker): the stream ends on
  handover (`Release`), the UI reconnects to the new worker and resyncs; CLI
  writes are rare and admin-initiated — a reconnect or the 30 s floor picks
  them up.
- **Time**: the UI already has the end times (cooldowns, expiries, link
  lapses) and its own clocks; the server publishes `pool` when a hold it set
  ends only if a panel needs the server's word.

### Cost

About 30 bytes per notice, at most one per topic per second per tab, a
keepalive comment every 15 s, one goroutine and a small set per connected
tab.

## 6. To fix regardless of the channel

1. **Adding a model name does not reload the client page**
   (`clients/slots.tsx` → `onAdded` → `onNamed`, never `onChanged`): the new
   name appears only at the next 20 s poll.
2. **Upstream model lists are uncached** in front of `Catalog.List` and
   `ListFor`: every load of `/admin/models`, a key's or an account's models,
   a client's files and `/v1/models` reads every account's list. Serve them
   from the remembered lists (refreshed every 10 min), reading the upstream
   only on an explicit refresh.
3. **`usage_events` needs `(key_id, at)` and `(account_id, at)` indexes** for
   every per-client and per-account aggregation.
4. **Subscription shares and pool status are recomputed on every read**
   (`keyshare.go`, `ListAccounts` once per provider and once per slot): one
   read per request, a short cache.
5. **Writes re-read what they returned**: use the answer
   (`accounts/order`, client POSTs, `setVersion`).
6. **Duplicates**: the Overview's second `/admin/overview`, the second secret
   read, `/admin/client-types` once per table, facets on every poll.

## 7. Order of work

1. The fixes in section 6 — they cut most of the cost before anything else.
2. `internal/events` (hub, topics, coalescing, queue, resync) and
   `/admin/events`, with tests for coalescing and a slow subscriber.
3. Publish from the choke points: `requests`, `running`, `settings`,
   `models`, `chats`, then `accounts`, `quota`, `pool`, `clients`, `uptime`.
4. The UI provider and `useLoader({ on })`; move each screen off its timer;
   `useNow` for relative times.
5. Watching from subscribers; remove Refresh buttons and the Live toggle.
6. Events with data, when the first one is wanted (toasts).
