# Model names

There are no gateway-wide model names. Each client has **slots**, one per model
setting its tool has, each with a name the tool asks for:

| Client | Slots |
|---|---|
| Claude Code | `large` (`ANTHROPIC_MODEL`), `small` (`ANTHROPIC_SMALL_FAST_MODEL`), `opus`, `sonnet`, `haiku`, `fable` (the `/model` tiers), `auto` (auto mode's classifier), and any more the operator adds, for `/model` |
| Codex | `large` (`model`), `review` (`review_model`), `agents` (sub-agents), `small` (memories), and any more the operator adds, for `/model` and `-m` |
| opencode | `large` (`model`), `small` (`small_model`), and any more the operator adds, for its model list |
| crush | `large` (`models.large`), `small` (`models.small`), and any more the operator adds, for its picker |
| Custom | `large`, and any the operator adds — or real model ids, in direct mode |

A name is the client's own, so two clients can both have a `large` on different
models. Each slot is bound to a family — Claude Opus, Sonnet, Haiku or Fable,
GPT Sol, Astra, Luna or Terra — or, where a provider offers every model, to
one model. Settings › Models sets which version each family serves: the
latest, each account serving the newest it has — so an account on a plan
without the latest Opus still serves Opus with the one it has, and accounts
with the newest are preferred while one can take the request — or one version
for the whole gateway; and, per provider, whether clients are offered each
family once (the default) or every version. Moving a client to a new model is one
change on its page, with no config to edit; the client is answered in its own
name, and the request log records both. A typed client may ask for its slot
names and nothing else, which is why every model it can ask for of its own
accord has one. `go doc ./internal/service/clients` has each type's slots and
the sources they were read from.

What the gateway knows about models comes from [models.dev](https://models.dev),
every provider's: names, release dates, context windows, output limits. Its
model list is that catalogue matched against what the connected accounts list,
less what each account's switches turn off. A model an account lists that the
catalogue does not know yet is listed anyway, after the rest. Until an account
can be asked, the gateway lists the catalogue's Anthropic models as they stand,
for the admin screens; no client is offered any of them until an account serves it.
`models-catalog: off` never asks, and the list is then the accounts' own.

Each client's files also carry what each name's model can do — Claude Code's
`behavesAs`, opencode's declared limits, the crush and Codex model lists —
because a client sizes its requests from those. Rebinding a slot to a model
with a smaller window means running the client's install command again, or its
keyless update command once it is set up. They
also set what a client would otherwise read off a real model's id — opencode's
tool-call streaming, crush's thinking display — so a plain name behaves as the
model it serves ([clients.md](clients.md), "What a client reads off
a name").

Rewriting the model is the relay's seventh exception to sending the caller's
bytes as they are; see `go doc ./internal/relay/passes`.
