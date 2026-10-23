# Accounts

The subscriptions the gateway draws on: how each provider's accounts are
connected, what they serve, and how their usage is read.

## Claude accounts

The UI drives the OAuth flow. "Start Claude login" opens Anthropic's consent
screen in a new tab; approve it, and the page shows a code to copy back into the
form. That is the same paste flow `claude auth login` uses, and the reason there
is no callback listener to run: the redirect belongs to Claude Code's own OAuth
client, which is almost never where the gateway runs.

Add as many as you have subscriptions. They form a **priority list**: the gateway
serves from the top and falls through when Anthropic says the one above is out of
room. That order is the operator's rather than something inferred — a personal
subscription and a work one are not interchangeable, and "spread the load evenly"
is the wrong answer when one of them is the account you would rather not spend.

Each account can also turn **models** off, under Models on its card: the list is
what that account's own subscription serves, read with its token, since plans
differ. A request goes to the first account in the list that has its model on;
with it off on every account, the client is told the gateway does not serve it,
and the model leaves `/v1/models` and the configs the gateway hands out. Models
are on until switched off, so one Anthropic releases tomorrow is available
without anyone switching it on.

An account can be **paused** without deleting it. Deleting revokes its refresh
token upstream, so getting it back means going through the consent flow again.

A login does not last forever: Anthropic gives each one a lifetime, and the card
counts down to it. When it is close, or once the refresh token has been refused,
**Reconnect** on that account's card runs the consent flow for that account and
renews it in place — same place in the list, same history. The sign-in page is
pre-filled with the account's address, as `claude auth login --email` does. The
browser still approves as whichever Claude account it is signed in to, so the
gateway checks whose tokens came back: approving as someone else changes nothing
and the tokens are handed back. Sign out of claude.ai first, or open the link the
form offers in a private window. Adding an account that is already connected
renews it too, and says so.

## ChatGPT accounts

A ChatGPT Plus, Pro or Team subscription can serve Codex. **Add account →
ChatGPT** asks OpenAI for a device code; enter it on OpenAI's own device page,
in any browser signed in to the account, and the page notices and adds it. It
is the flow `codex login --device-auth` uses, and it needs nothing to come back
from that browser, so it works for a gateway on another machine.

Each ChatGPT workspace is an account of its own: a personal one and a Team one
under the same address are added separately, and each request names the one it
bills. The account shows the plan the sign-in named.

What a ChatGPT account serves is what a Responses client sends it, as it was
written — the gateway adds the account's credential and nothing that claims to
be Codex. A client on Anthropic Messages — Claude Code, crush, opencode —
whose slot is bound to a GPT model is served there too, its requests
translated into Responses and the answers back, thinking and tool calls
included ([client-apis.md](client-apis.md)). Usage comes
from the backend's own endpoint and from the headers on every answer, in the
same 5-hour and 7-day columns.

The Codex backend is not a public API, and pooling several people's ChatGPT
subscriptions behind shared keys may breach OpenAI's terms and get accounts
flagged — as with Claude, that is the operator's decision to make knowingly.
What was measured against it, and how, is in
`go doc ./internal/provider/openai`.

## Gemini accounts

A Google account — the free tier, Google AI Pro or Ultra — serves Gemini the
way it serves Antigravity, Google's agent IDE: through Code Assist, on the
account's own allowance, not the metered Gemini API and its key. **Add account
→ Gemini** opens Google's consent screen for Antigravity; approve, and your
browser then fails to load a `localhost:51121` page — that is expected, and
its whole address, copied from the address bar, is what you paste back. It
needs nothing to reach the gateway from the browser, so it works for a
gateway on another machine. Signing in also sets the account up on its tier;
an account that would need a Google Cloud project of its own is refused then,
with Google's reasons.

Antigravity, not gemini-cli, because Google decided so: since October 2026 it
refuses gemini-cli's sign-in for personal accounts and serves them only to
Antigravity. It tells the two apart by the User-Agent, so the gateway sends
Antigravity's on every call to Google — the one place it claims another
client's identity, chosen for that reason alone.
`go doc ./internal/provider/google` has the measurements.

A free account was offered Gemini 2.5 Pro, 3 Flash, 3.1 and 3.5 Flash Lite,
and 3.5 and 3.8 Flash at three thinking levels each, with GPT-OSS 120B.
Antigravity also offers Claude Opus and Sonnet; the gateway leaves those out,
because a `claude-` model always goes to the Claude accounts. The quota is
per model and resets weekly — a spent Pro leaves Flash serving — so an
account shows one row per model rather than the 5-hour and 7-day columns.
Its models group as families by tier — Pro, Flash, Flash-Lite — and a name
following one is served the newest the account has, at the highest thinking
level offered.

Every client reaches it: a Messages client translated, a Responses client
through Messages, and gemini-cli with its own request rewrapped — point it at
the gateway with a custom client in direct mode:

    export GOOGLE_GEMINI_BASE_URL=http://your-gateway:8317
    export GEMINI_API_KEY=clc_…

Code Assist is not a public API, and pooling several people's Google accounts
— and presenting the gateway as Antigravity to do it — may breach Google's
terms, as with the other two; that is the operator's decision to make
knowingly.

### Subscription usage

Each Claude account shows what it has spent, as the same rows `/usage` prints
in the client, because they come from the same place — `GET /api/oauth/usage`
(a ChatGPT account's come from the backend's `/wham/usage`, one row per window):

    Current session           29%  resets in 2h
    Current week (all models) 45%  resets in 21h  · binding
    Current week (Fable)       0%

Polled every five minutes and on demand, rather than read off relayed responses.
Headers were the first attempt and were wrong in a way that mattered: an account
showed nothing until it had served a request, and they carry only the two
headline windows, missing the per-model weekly limits.

These are the subscription's own figures, so they count every client on the
account, not only traffic through this gateway.

Quota never reorders the priority list; it only decides whether to skip. If every
account looks exhausted the gateway tries anyway — the figures are a poll, and
being refused by the upstream beats refusing on its behalf.

**Test connection** sends a real minimal request upstream, on the account's own
credential and no client's key, and reports the model, token counts and
latency, or the upstream's own error text unmodified, which is the only thing
that distinguishes an expired token from a plan restriction. The account's
Models page tests each model the same way, two at a time for **Test all**,
keeps each one's last result beside its switch, and offers to switch off the
ones that failed for a reason that lasts — never on its own.

Tokens are sealed with AES-256-GCM before they reach the database. The key lives
beside it as `secret.key` (0600), or comes from `CLAUDICATION_SECRET_KEY`.
