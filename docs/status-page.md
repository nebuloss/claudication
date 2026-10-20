# The status page

A relay's root, when the admin UI is on its own listener (`admin-listen`), is a
public page: anyone who finds the address can open it. It says that this is a
private gateway, what to do if you were or were not given access, and whether
it is up and has been. The admin Overview shows the same history, read from the
same endpoint, so the operator sees exactly what the world does.

The page names no provider, model, client, API or path, links nowhere, and
does not print the build. `make web` and `make check` run
`scripts/check-welcome.mjs`, which follows everything `welcome.html` loads in
the build and fails on any such name, or on a file shared with the admin app.

## What is recorded

Once a minute the gateway notes one of three states in `uptime_samples`:

| State    | Meaning                                                                            |
| -------- | ---------------------------------------------------------------------------------- |
| up       | the gateway switch is on and some account could take a request now               |
| degraded | the switch is on and no account could: none connected, or all cooling or spent    |
| paused   | the operator switched the gateway off                                             |

Down is recorded two ways. A stop the gateway sees — a deploy, a restart, a
shutdown — goes into `uptime_outages` the moment it stops taking requests, and
is closed when it takes them again, to the millisecond. A deploy that waits half
a minute for the last stream to finish refused every request in that half
minute, so it shows: down for exactly that long. (Until 2026-10-20 such a
restart was absorbed by a five-minute tolerance and the page said 100% over
it.) A stop the gateway cannot see — a crash, the machine going away — records
nothing, and is read from the gaps: two samples more than two and a half
minutes apart mean the gateway was not running in between.
Paused is shown as its own state and left out of the uptime figures, the way a
status page leaves out a maintenance window.

Samples are kept for the longer of `usage.retention-days` and 90 days. The
retention bounds the per-request history, which is large and personal; a row
here is a dozen bytes, and a 90-day strip that could only ever fill 30 days of
itself would look like 60 days of missing data.

Nothing about *why* is recorded. A failure rate from the request log would make
degraded sharper, but any such figure says something about traffic — that there
was any, at least — and the page says nothing about traffic.

## `GET /status.json`

On every listener, unauthenticated, limited per address by
`limits.anon-per-minute` like every anonymous route, and rebuilt at most once a
minute (`Cache-Control: public, max-age=60`). The whole answer:

```json
{
  "state": "up",
  "uptime": { "24h": 1, "7d": 0.9993, "90d": 0.9998 },
  "hours": [{ "start": 1760914800000, "state": "up", "up": 1 }],
  "days": [{ "start": 1753056000000, "state": "none", "up": null }],
  "incidents": [{ "start": 1760877720000, "end": 1760879400000, "state": "down" }],
  "generated": 1760965230000
}
```

Each field, and why it is safe to publish:

| Field        | What it is                                                                                     | Why it may be public                                                                                                        |
| ------------ | ---------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `state`      | now: `up`, `degraded` or `paused`                                                             | paused is /health's `accepting: false`, already public; degraded says only that requests would fail, which any key holder learns by trying |
| `uptime`     | share of the last 24 h, 7 d and 90 d that was up, out of the time not paused; null with no data | a fraction of time, derived from the states alone                                                                          |
| `hours`      | the last 24 UTC hours, oldest first: start, worst state, share up                              | the same states, bucketed                                                                                                   |
| `days`       | the last 90 UTC days, the same way                                                             | as above; the first day with data is roughly when the gateway was installed, which is no more than its uptime implies       |
| `incidents`  | the last 90 days' degraded and down stretches, newest first, at most 20; `end` null while ongoing | when it was down, which a monitor of /health learns too                                                                    |
| `generated`  | when the answer was worked out                                                                 | the clock                                                                                                                   |

Not in it, deliberately: providers, models, accounts and how many, clients or
keys, request counts or rates, error kinds or reasons, limits, configuration,
the admin address, and the version. `TestReportFields` and
`TestStatusEndpointPublishesOnlyTheReport` hold the JSON to exactly these keys,
so a field added here is a test changed and a row added to this table.
