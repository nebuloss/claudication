# Endpoints

| Endpoint                                      | Auth  | Notes                                      |
| --------------------------------------------- | ----- | ------------------------------------------ |
| `GET /health`                                 | no    | Liveness. Leaks no operational detail.     |
| `HEAD /api/hello`                             | no    | Claude Code's connection-warming probe.    |
| `GET /v1/models`                              | key   | Discovery, proxied from the upstream.      |
| `POST /v1/messages`                           | key   | The relay, streaming and not.              |
| `POST /v1/messages/count_tokens`              | key   | So counting does not spend inference.      |
| `POST /v1/responses`                          | key   | The OpenAI Responses surface, for Codex.   |
| `POST /mcp`                                   | key   | The MCP server: the client's agent's tools.|
| `GET /install?token=`                         | link  | One install or update command's script.    |
| `GET /`                                       | —     | Admin UI. `?token=` spends a sign-in link. |
| `GET,POST /admin/setup`                       | —     | Fresh install, and claiming it.            |
| `POST /admin/session`                         | —     | Password for a session cookie.             |
| `POST /admin/password`                        | admin | Change it; ends every other session.       |
| `GET /admin/overview`                         | admin | Status, counts, last 24h.                  |
| `GET /admin/accounts`                         | admin | Upstream accounts.                         |
| `POST /admin/accounts/order`                  | admin | Set the priority list.                     |
| `POST /admin/accounts/oauth/{start,complete}` | admin | The login flow.                            |
| `POST /admin/accounts/{id}/{test,refresh}`    | admin | Probe or refresh.                          |
| `POST /admin/accounts/{id}/disabled`          | admin | Pause or resume.                           |
| `GET,POST /admin/clients`                     | admin | Clients, and making one with its key.      |
| `GET /admin/clients/{id}`, `…/usage`          | admin | One client, and what it used.              |
| `PUT,DELETE /admin/clients/{id}/slots/{slot}` | admin | Bind, rename or remove a model name.       |
| `GET /admin/clients/{id}/files?base=`         | admin | Its files, as the gateway writes them.     |
| `POST /admin/clients/{id}/agent-manage`       | admin | Whether its agent may change it over MCP.  |
| `POST /admin/keys/{id}/{install,update}`      | admin | One install or update command.             |
| `PATCH,DELETE /admin/keys/{id}`               | admin | Edit a client's limits, or delete it.      |
| `POST /admin/gateway`                         | admin | The Gateway switch.                        |
| `GET /admin/usage`                            | admin | Totals and breakdowns over a window.       |
| `GET /admin/requests`                         | admin | The request log, paged.                    |

Model discovery is pinned by the contract: `GET /v1/models?limit=1000`, a
3-second timeout, **any redirect counts as failure**, and Claude Code keeps only
ids containing `claude` or `anthropic` — when it asks at all, which is only
with `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` set. The files the gateway
writes do not set it: they name every model the client may use.
