/*
 * The admin API, as one object per resource.
 *
 * It was one flat literal of forty-odd functions, with positional arguments,
 * some list endpoints handing back `null` for an empty list and others `[]`,
 * and the encoding of every path segment written out at each call. Now:
 *
 *  - every request goes through one HttpTransport (transport.ts), which owns
 *    the timeout, cancellation and the lapsed-session report;
 *  - paths are built with the `path` tag, which encodes what it interpolates;
 *  - a list always comes back as an array, so no screen has to remember which
 *    endpoint answers null, and an endpoint with nothing to say is
 *    Promise<void>;
 *  - a key's limits travel as one KeyLimits value rather than three numbers
 *    in a row, where a rate period and a budget are one transposition apart.
 */
import { HttpTransport, path, sessionExpiry } from './transport'
import type {
  Account,
  AccountList,
  AccountModel,
  AccountShare,
  ApiKey,
  ChatDetail,
  ClientLive,
  ClientMode,
  ClientTypesWire,
  ClientWire,
  LimitReplay,
  LimitSuggestion,
  SlotUse,
  SubscriptionShare,
  Chats,
  GatewayConfig,
  Health,
  InstallFile,
  Model,
  OAuthResult,
  OAuthStart,
  Overview,
  ProbeResult,
  RequestFacets,
  RequestRow,
  Session,
  SetupStatus,
  Usage,
  UsageScope,
} from './types'

export * from './types'
export { ApiError, SessionExpiry, messageOf, path, sessionExpiry } from './transport'

const http = new HttpTransport(sessionExpiry)

interface AccountListWire {
  accounts: Account[] | null
  window_days: number
  usage_poll_s?: number
}

function toAccountList(res: AccountListWire): AccountList {
  return {
    accounts: res.accounts ?? [],
    windowDays: res.window_days,
    usagePollS: res.usage_poll_s ?? 0,
  }
}

function scopedQuery(days: number | undefined, scope: UsageScope): string {
  const q = new URLSearchParams()
  if (days !== undefined) q.set('days', String(days))
  if (scope.account) q.set('account', scope.account)
  if (scope.key) q.set('key', scope.key)
  const s = q.toString()
  return s === '' ? '' : `?${s}`
}

/**
 * What a key may spend, as the editor holds it. 0 means the gateway's own
 * limit for the rate, and no ceiling at all for the budget.
 */
export interface KeyLimits {
  /** Requests per `ratePeriodS`; 0 for the gateway's default. */
  rpmLimit: number
  /** What rpmLimit is per, in seconds. */
  ratePeriodS: number
  /** Tokens per rolling day; 0 for unlimited. */
  tokenBudget: number
}

/** A key's name and limits as the API spells them. */
function toKeyBody(name: string, limits: KeyLimits) {
  return {
    name,
    rpm_limit: limits.rpmLimit,
    rate_period_s: limits.ratePeriodS,
    token_budget: limits.tokenBudget,
  }
}

/** The models one account serves or one key may ask for, each with its switch. */
type ModelSwitches = { models: AccountModel[] }

/** For the calls whose 401 is their answer; see CallOptions.answersAuth. */
const AUTH = { answersAuth: true }

/** The admin account and the session: signing in, and the password. */
const session = {
  /** Unauthenticated: tells a fresh install apart from a forgotten password. */
  setupStatus: () => http.get<SetupStatus>('/admin/setup', undefined, AUTH),

  /** Claims a gateway nobody has set up yet, and signs in as part of it. */
  setup: (password: string) => http.post<Session>('/admin/setup', { password }, AUTH),

  signIn: (password: string) => http.post<Session>('/admin/session', { password }, AUTH),
  signOut: () => http.send('DELETE', '/admin/session', AUTH),
  whoAmI: () => http.get<Session>('/admin/me', undefined, AUTH),

  /** Ends every other session, and hands this one a fresh cookie. */
  changePassword: (current: string, next: string) =>
    http.post<Session>(
      '/admin/password',
      { current_password: current, new_password: next },
      AUTH,
    ),

  /** Returns the gateway to its first-run state. Upstream accounts survive. */
  deleteAdmin: (password: string) =>
    http.send('POST', '/admin/account/delete', { ...AUTH, body: { password } }),
}

/** The upstream accounts the pool draws on. */
const accounts = {
  list: async (signal?: AbortSignal): Promise<AccountList> =>
    toAccountList(await http.get<AccountListWire>('/admin/accounts', signal)),

  /** Sets the whole priority order; the first account with room serves. */
  reorder: async (ids: string[]): Promise<AccountList> =>
    toAccountList(await http.post<AccountListWire>('/admin/accounts/order', { ids })),

  /** Begins a consent flow: for a new account, or to reconnect `accountId`. */
  startOAuth: (provider: string, accountId?: string) =>
    http.post<OAuthStart>('/admin/accounts/oauth/start', {
      provider,
      ...(accountId !== undefined ? { account_id: accountId } : {}),
    }),

  /**
   * Asks once whether a device code has been approved: null while it has not,
   * the stored account once it has.
   */
  pollOAuth: async (provider: string, state: string): Promise<OAuthResult | null> => {
    const res = await http.post<{ account?: Account; renewed?: boolean; status?: string }>(
      '/admin/accounts/oauth/poll',
      { provider, state },
    )
    if (res.account === undefined) return null
    return { account: res.account, renewed: res.renewed === true }
  },

  completeOAuth: async (provider: string, state: string, redirectUrl: string): Promise<OAuthResult> => {
    const res = await http.post<{ account: Account; renewed?: boolean }>(
      '/admin/accounts/oauth/complete',
      { provider, state, redirect_url: redirectUrl },
    )
    return { account: res.account, renewed: res.renewed === true }
  },

  /** The models one account serves, each with whether it may serve it. */
  models: async (id: string, signal?: AbortSignal): Promise<ModelSwitches> => {
    const res = await http.get<{ models: AccountModel[] | null }>(path`/admin/accounts/${id}/models`, signal)
    return { models: res.models ?? [] }
  },

  setModel: (id: string, model: string, enabled: boolean) =>
    http.send('POST', path`/admin/accounts/${id}/models`, { body: { model, enabled } }),

  /** One tiny turn on the account's own credential: on a model, or on the provider's default. */
  test: (id: string, model = '') => http.post<ProbeResult>(path`/admin/accounts/${id}/test`, { model }),

  /** Re-reads the subscription usage now, rather than waiting for the poll. */
  refreshUsage: async (id: string): Promise<Account> =>
    (await http.post<{ account: Account }>(path`/admin/accounts/${id}/usage`)).account,

  refreshToken: async (id: string): Promise<Account> =>
    (await http.post<{ account: Account }>(path`/admin/accounts/${id}/refresh`)).account,

  /**
   * Pause an account without destroying it. Deleting revokes the refresh
   * token upstream and costs a trip through the consent flow to undo.
   */
  setDisabled: (id: string, disabled: boolean) =>
    http.post<Account>(path`/admin/accounts/${id}/disabled`, { disabled }),

  remove: (id: string) => http.send('DELETE', path`/admin/accounts/${id}`),

  /** Which clients drew on this account, by model name and model, and each one's share of it. */
  clients: (id: string, days: number, signal?: AbortSignal) =>
    http.get<{ days: number; rows: SlotUse[] | null; shares: Record<string, AccountShare> }>(
      path`/admin/accounts/${id}/clients?days=${String(days)}`,
      signal,
    ),
}

/** The clients, each holding one key; the key's own calls take the client's id. */
const clients = {
  /** The table of client types, the APIs and the targets a slot can follow. */
  types: (signal?: AbortSignal) => http.get<ClientTypesWire>('/admin/client-types', signal),

  list: async (
    signal?: AbortSignal,
  ): Promise<{ clients: ClientWire[]; windowDays: number; budgetHours: number; defaultRpm: number }> => {
    const res = await http.get<{
      clients: ClientWire[] | null
      window_days: number
      budget_hours: number
      default_rpm: number
    }>('/admin/clients', signal)
    return {
      clients: res.clients ?? [],
      windowDays: res.window_days,
      budgetHours: res.budget_hours,
      defaultRpm: res.default_rpm,
    }
  },

  get: async (id: string, signal?: AbortSignal): Promise<ClientWire> =>
    (await http.get<{ client: ClientWire }>(path`/admin/clients/${id}`, signal)).client,

  /** The plaintext comes back once from here; secret() reads it again until first use. */
  create: (name: string, type: string, limits: KeyLimits) =>
    http.post<{ client: ClientWire; plaintext: string }>('/admin/clients', {
      ...toKeyBody(name, limits),
      type,
    }),

  /** Rename a client or change its limits. Its key is untouched. */
  update: (id: string, name: string, limits: KeyLimits) =>
    http.patch<{ key: ApiKey }>(path`/admin/keys/${id}`, toKeyBody(name, limits)),

  remove: (id: string) => http.send('DELETE', path`/admin/keys/${id}`),

  setMode: async (id: string, mode: ClientMode): Promise<ClientWire> =>
    (await http.post<{ client: ClientWire }>(path`/admin/clients/${id}/mode`, { mode })).client,

  setDisabled: async (id: string, disabled: boolean): Promise<ClientWire> =>
    (await http.post<{ client: ClientWire }>(path`/admin/clients/${id}/disabled`, { disabled })).client,

  setNameChats: async (id: string, enabled: boolean): Promise<ClientWire> =>
    (await http.post<{ client: ClientWire }>(path`/admin/clients/${id}/name-chats`, { enabled })).client,

  setFitImages: async (id: string, enabled: boolean): Promise<ClientWire> =>
    (await http.post<{ client: ClientWire }>(path`/admin/clients/${id}/fit-images`, { enabled })).client,

  /** Rename a slot and bind it: to a real model, or to a target it follows. */
  setSlot: async (id: string, slot: string, name: string, binding: { model?: string; follows?: string }) =>
    (await http.send('PUT', path`/admin/clients/${id}/slots/${slot}`, { body: { name, ...binding } })),

  addSlot: (id: string, name: string, binding: { model?: string; follows?: string } = {}) =>
    http.post<{ client: ClientWire }>(path`/admin/clients/${id}/slots`, { name, ...binding }),

  removeSlot: (id: string, slot: string) => http.send('DELETE', path`/admin/clients/${id}/slots/${slot}`),

  /** Its key, until the client is first set up with it. 409 configured after. */
  secret: (id: string, signal?: AbortSignal) =>
    http.get<{ plaintext: string }>(path`/admin/keys/${id}/secret`, signal),

  /** A new key: the old one stops at once, and the client is to set up again. */
  rotate: (id: string) => http.post<{ key: ApiKey; plaintext: string }>(path`/admin/keys/${id}/rotate`),

  /** Keeps the client's files for one install command; the token fetches them once. */
  createInstall: (id: string, files: InstallFile[]) =>
    http.post<{ token: string; expires_at: string }>(path`/admin/keys/${id}/install`, { files }),

  /** Every model the gateway serves, each with whether a direct-mode client may ask for it. */
  models: async (id: string, signal?: AbortSignal): Promise<ModelSwitches> => {
    const res = await http.get<{ models: AccountModel[] | null }>(path`/admin/keys/${id}/models`, signal)
    return { models: res.models ?? [] }
  },

  setModel: (id: string, model: string, enabled: boolean) =>
    http.send('POST', path`/admin/keys/${id}/models`, { body: { model, enabled } }),

  /** /v1/models as this client sees it: its slots, each with its model's facts. */
  offeredModels: async (id: string, signal?: AbortSignal): Promise<Model[]> =>
    (await http.get<{ data: Model[] | null }>(path`/admin/keys/${id}/offered-models`, signal)).data ?? [],

  usage: (id: string, days: number, signal?: AbortSignal) =>
    http.get<{ days: number; rows: SlotUse[] | null; share?: SubscriptionShare }>(
      path`/admin/clients/${id}/usage?days=${String(days)}`,
      signal,
    ),

  live: (id: string, signal?: AbortSignal) => http.get<ClientLive>(path`/admin/clients/${id}/limits`, signal),

  suggest: async (id: string, signal?: AbortSignal): Promise<LimitSuggestion[]> =>
    (await http.get<{ suggestions: LimitSuggestion[] }>(path`/admin/clients/${id}/limits/suggest`, signal))
      .suggestions,

  preview: (id: string, limits: KeyLimits) =>
    http.post<{ days: number; replay: LimitReplay }>(
      path`/admin/clients/${id}/limits/preview`,
      toKeyBody('', limits),
    ),
}

/** The models the accounts serve. */
const models = {
  list: async (signal?: AbortSignal): Promise<Model[]> =>
    (await http.get<{ data: Model[] | null }>('/admin/models', signal)).data ?? [],
}

/** The gateway's own state and switches. */
const gateway = {
  overview: (signal?: AbortSignal) => http.get<Overview>('/admin/overview', signal),
  config: (signal?: AbortSignal) => http.get<GatewayConfig>('/admin/config', signal),

  setServing: (enabled: boolean) => http.send('POST', '/admin/gateway', { body: { enabled } }),

  /**
   * Liveness and the build, as every listener answers them to anyone: all the
   * welcome page reads, so it can say no more than a monitor already learns.
   */
  health: (signal?: AbortSignal) => http.get<Health>('/health', signal),
}

/** What went through the gateway: the usage report, the chats, the request log. */
const traffic = {
  /** The usage report, gateway-wide or narrowed to one account or one key. */
  usage: (days?: number, scope: UsageScope = {}, signal?: AbortSignal) =>
    http.get<Usage>(`/admin/usage${scopedQuery(days, scope)}`, signal),

  chats: (days?: number, scope: UsageScope = {}, signal?: AbortSignal) =>
    http.get<Chats>(`/admin/chats${scopedQuery(days, scope)}`, signal),

  /** One conversation's latest requests, oldest first — the drill-down behind a row. */
  chat: (id: string, signal?: AbortSignal) => http.get<ChatDetail>(path`/admin/chats/${id}`, signal),

  /** A title typed by hand, held against automatic renames. */
  setChatTitle: (id: string, title: string) =>
    http.send('PUT', path`/admin/chats/${id}/title`, { body: { title } }),

  /** The column menus' values, or null while usage history is off. */
  requestFacets: async (): Promise<RequestFacets | null> =>
    (await http.get<{ enabled: boolean; facets?: RequestFacets }>('/admin/requests/facets')).facets ?? null,

  requests: async (
    limit: number,
    after: string,
    filter: Record<string, string | readonly string[]>,
    signal?: AbortSignal,
  ): Promise<{ rows: RequestRow[]; nextCursor: string }> => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (after !== '') query.set('after', after)
    // Passed straight through: the server decides what each one narrows, and
    // an unknown one is ignored rather than being a client-side allowlist that
    // has to be kept in step with it.
    //
    // A set becomes a repeated parameter rather than one comma-joined value:
    // a model name or an address is not ours to reserve punctuation in. An
    // empty member is appended too, because "the rows with nothing here" is a
    // choice the menus offer and `?model=` is how it travels.
    for (const [k, v] of Object.entries(filter)) {
      if (typeof v !== 'string') {
        for (const one of v) query.append(k, one)
      } else if (v !== '') {
        query.set(k, v)
      }
    }
    const res = await http.get<{ requests: RequestRow[] | null; next_cursor?: string }>(
      `/admin/requests?${query.toString()}`,
      signal,
    )
    return { rows: res.requests ?? [], nextCursor: res.next_cursor ?? '' }
  },
}

export const api = { session, accounts, clients, models, gateway, traffic }
