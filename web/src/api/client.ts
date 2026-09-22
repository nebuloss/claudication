/* The admin API: its shapes, and the single place that talks to it. */

/** One row of the usage display, titled as the client titles it. */
export interface UsageLimitRow {
  title: string
  kind: string
  /** Percentage, 0-100. */
  percent: number
  severity: string
  resets_at?: string
  is_active: boolean
}

/**
 * What /api/oauth/usage reported — the same figures `/usage` prints. The
 * 5-hour window is what bites during a working session; the 7-day one is what
 * runs out over a heavy week. Percentages are 0-100.
 */
export interface Quota {
  updated_at: string
  five_hour_util: number
  five_hour_reset?: string
  five_hour_status?: string
  seven_day_util: number
  seven_day_reset?: string
  seven_day_status?: string
  allowed: boolean
  /** The server's own normalised rows, including per-model weekly limits. */
  limits?: UsageLimitRow[]
}

export interface Account {
  id: string
  provider: string
  email: string
  expires_at: string
  created_at: string
  last_refresh_at?: string
  last_used_at?: string
  last_error?: string
  expired: boolean
  disabled: boolean
  refresh_expires_at?: string
  reauth_days_left?: number
  needs_reauth_soon?: boolean
  /** Terminal: the refresh token was refused for good. Only a browser fixes it. */
  needs_reauth?: boolean
  refresh_dead_at?: string
  /** Absent until the account has served a request. */
  quota?: Quota
  /** What this gateway put through the account over the report window. */
  requests: number
  tokens: number
  /** Priority order, and whether this one would serve the next request. */
  position: number
  serving: boolean
  /**
   * When a sidelined account comes back. The pool holds cooldowns in memory,
   * so this is empty for a healthy account and after a restart.
   */
  cooling_until?: string
}

export interface ProbeResult {
  ok: boolean
  model?: string
  stop_reason?: string
  input_tokens?: number
  output_tokens?: number
  reply?: string
  latency_ms: number
  status: number
  request_id?: string
  error?: string
}

export interface OAuthStart {
  provider: string
  state: string
  auth_url: string
  redirect_uri: string
  expires_at: string
  instructions: string
}

export interface Session {
  authenticated: boolean
  /** How the session was opened: "password", "setup", "password change". */
  via?: string
  expires_at?: string
  /** When the password last changed. Absent if the account is gone. */
  password_updated_at?: string
}

export interface ApiKey {
  id: string
  name: string
  display: string
  created_at: string
  last_used_at?: string
  rpm_limit: number
  /** What rpm_limit is per, in seconds. 60 unless an operator chose otherwise. */
  rate_period_s: number
  /** Tokens allowed per rolling day; 0 means unlimited. */
  token_budget: number
  /** What has been counted against that budget so far. */
  spent_today: number
  requests: number
  tokens: number
  /**
   * The key the gateway issued to itself — today, the one chat titling bills
   * to. The row shows no Delete, because deleting it never stopped anything:
   * the next chat needing a name just gets another issued. Its limits stay
   * editable, and a token budget is how you actually cap it.
   */
  managed: boolean
}

export interface UsageTotals {
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  median_ms: number
  p95_ms: number
}

export interface UsageBucket {
  label: string
  id?: string
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_tokens: number
}

/**
 * One name on one day: a model, a key, an account, a status code.
 *
 * The gateway sends the cross-tabs long-form because that is the shape its
 * grouped query already produces; pivoting into a grid is the chart's job.
 */
export interface UsageCell {
  day: string
  name: string
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_tokens: number
}

export interface UsageReport {
  since: string
  totals: UsageTotals
  by_day: UsageBucket[]
  by_model: UsageBucket[]
  by_key: UsageBucket[]
  by_account: UsageBucket[]
  by_status: UsageBucket[]
  /** Each breakdown crossed with the day, keyed by dimension. */
  cross?: Record<string, UsageCell[]>
}

export interface Usage {
  enabled: boolean
  days?: number
  retention_days?: number
  report?: UsageReport
}

/**
 * One conversation's totals.
 *
 * The id is the one the client itself sent — Claude Code's session uuid,
 * opencode's `ses_…`, Codex's session id. Nothing here is derived from the
 * messages, which is why there is no title: see the Chats screen.
 */
export interface Chat {
  id: string
  /**
   * What the gateway asked a model to call this conversation, or empty when it
   * never did — titling is off, the chat predates it, or the model declined.
   * Empty is normal, so the row falls back to the client and the id.
   */
  title: string
  client: string
  key_name: string
  account_email: string
  /** In first-seen order. A chat that switched model mid-way shows both. */
  models: string[]
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_tokens: number
  first: string
  last: string
}

export interface ChatReport {
  chats: Chat[]
  /**
   * Every request whose client named no conversation, as one bucket. Shown
   * rather than dropped: without it the totals here would not add up to the
   * ones on the Overview, and it is how you find out something is talking to
   * the gateway unlabelled.
   */
  unattributed: Chat
  /** Distinct chats in the window, which is not chats.length once limit bites. */
  total: number
}

export interface Chats {
  enabled: boolean
  days?: number
  retention_days?: number
  report?: ChatReport
}

export interface RequestRow {
  at: string
  key_name?: string
  account_email?: string
  model?: string
  path: string
  status: number
  streaming: boolean
  input_tokens: number
  output_tokens: number
  cache_tokens: number
  duration_ms: number
  error?: string
  /** Set when the refusal does not mean what its message says. */
  error_kind?: string
  /**
   * True for a request this gateway refused before it reached the upstream —
   * no key, or one it does not recognise. Those spent nothing and have no key,
   * model or account, so every usage figure steps over them; this log is the
   * only place they appear.
   */
  rejected?: boolean
  /**
   * Which machine made the request. For a refused one it is the only identity
   * there is; for a relayed one it answers what a key and a User-Agent cannot,
   * which is where it ran.
   */
  ip?: string
  /**
   * What was running, read from the User-Agent. Not the same question as the
   * key, which names whoever is paying and is whatever they typed when they
   * minted it.
   */
  client?: string
}

/** One line of a column's filter menu. */
export interface FacetValue {
  /** What to filter on. Empty means the rows with nothing in that column. */
  value: string
  /** What to show, when that is not the value itself — a key id is not a name. */
  label?: string
  count: number
}

/**
 * What each filterable column of the request log holds.
 *
 * Counted over the whole log rather than the page on screen, and deliberately
 * not narrowed by the filter in force: a menu that hid the values the current
 * filter excludes would be a filter you cannot widen.
 */
export interface RequestFacets {
  models: FacetValue[]
  clients: FacetValue[]
  keys: FacetValue[]
  ips: FacetValue[]
  statuses: FacetValue[]
}

/**
 * Where a value came from. The four are ranked: database beats environment
 * beats file beats default, and every one of them is silent about losing.
 */
export type Origin = 'default' | 'file' | 'env' | 'database'

/** One configuration value, and which source supplied it. */
export interface Setting {
  key: string
  value: string
  /** The environment wins over the file, silently. */
  origin: Origin
  /** The variable that overrides this one, absent when none does. */
  env?: string
  doc: string
}

/**
 * One client-facing API, and whether it is being served.
 *
 * Runtime state rather than configuration: the switch takes effect on the next
 * request, with no restart and nothing to drain.
 */
export interface Surface {
  id: string
  title: string
  /** The paths it answers on. */
  routes: string[]
  enabled: boolean
  /** 'database' once someone has set it, 'default' while nobody has. */
  origin: Origin
}

/**
 * One model the connected accounts can serve, as the upstream lists it.
 *
 * Read live rather than hardcoded: the gateway has no model allowlist — it
 * relays whatever name a client sends — so any list written into the UI would
 * be a second opinion that quietly goes stale.
 */
export interface Model {
  id: string
  display_name?: string
}

export interface GatewayConfig {
  /** The file it was read from, empty when there was none. */
  path: string
  settings: Setting[]
  surfaces: Surface[]
  /**
   * Whether the gateway may name conversations by asking a model. The only
   * thing it does that spends the operator's subscription on its own behalf,
   * which is why it is a switch and why it is off until turned on.
   */
  chat_titles: boolean
  /**
   * Whether the gateway reads names that clients generate for themselves.
   * Costs nothing — opencode and crush ask a model to name their own
   * conversations, and that answer already passes through here.
   */
  chat_titles_capture: boolean
  /**
   * Whether the relay caps oversized images in a many-image request. The one
   * pass that changes what the model is shown rather than the shape of the
   * envelope, so it is off until an operator asks for it.
   */
  fit_images: boolean
  /**
   * Where the public setup page is published, or empty when there is none.
   * The gateway cannot see its own outside name, so this is configured.
   */
  docs_url: string
  /** Whether the public setup page is being served right now. */
  docs_enabled: boolean
}

/**
 * Everything the public setup page is told, and nothing else.
 *
 * Deliberately not the admin overview or the configuration: those carry the
 * listen addresses, the state directory and every limit. This is four facts —
 * what to point a client at, which APIs are on, whether an account is
 * connected, and which models exist.
 */
export interface DocsInfo {
  /** The relay's address. Empty when nobody has configured public-url. */
  public_url: string
  surfaces: Surface[]
  ready: boolean
  /** Cached upstream list; absent when the upstream could not be reached. */
  models?: { data: Model[] | null }
}

export interface Overview {
  version: string
  commit: string
  listen: string
  /** What clients should point at, when the operator has said. */
  public_url?: string
  /** True when the admin UI is on its own listener, so this origin is not it. */
  admin_split?: boolean
  started_at: string
  uptime_s: number
  usage_enabled: boolean
  ready: boolean
  accounts: {
    total: number
    usable: number
    cooling?: number
    needs_reauth_soon: number
    needs_reauth?: number
  }
  keys: { total: number }
  last_24h?: UsageTotals
}

export interface SetupStatus {
  needs_setup: boolean
  min_password_len: number
}

/** An error carrying the status and machine-readable type from the server. */
export class ApiError extends Error {
  readonly status: number
  readonly type: string

  constructor(status: number, type: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.type = type
  }

  /** True when the session is missing or expired and the user must sign in. */
  get isUnauthenticated(): boolean {
    return this.status === 401
  }
}

/** Anything thrown at us, as a sentence worth showing. */
export function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/**
 * How long a call to the admin API may hang before it is reported as failed.
 *
 * fetch has no timeout of its own, so a gateway that accepts a connection and
 * then stops answering — mid-restart, or reachable over a tunnel that has gone
 * away — leaves every panel spinning for ever with nothing to click. Generous,
 * because a few of these do real work: probing an account talks to Anthropic.
 */
const REQUEST_TIMEOUT_MS = 30_000

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let response: Response
  const abort = new AbortController()
  const timer = window.setTimeout(() => abort.abort(), REQUEST_TIMEOUT_MS)
  try {
    response = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? null : JSON.stringify(body),
      credentials: 'same-origin',
      signal: abort.signal,
    })
  } catch (cause) {
    if (abort.signal.aborted) {
      throw new ApiError(
        0,
        'timeout',
        `The gateway did not answer within ${REQUEST_TIMEOUT_MS / 1000} seconds.`,
      )
    }
    throw new ApiError(0, 'network_error', `Could not reach the gateway: ${String(cause)}`)
  } finally {
    window.clearTimeout(timer)
  }

  const text = await response.text()
  let payload: unknown = null
  if (text.length > 0) {
    try {
      payload = JSON.parse(text)
    } catch {
      // A non-JSON body is reported verbatim below.
    }
  }

  if (!response.ok) {
    const envelope = payload as { error?: { message?: string; type?: string } } | null
    // `||`, not `??`: text.trim() is always a string, so `??` never reaches
    // the status fallback and an empty error body renders as an empty message —
    // which the panels then show as their "nothing here yet" empty state.
    throw new ApiError(
      response.status,
      envelope?.error?.type ?? 'http_error',
      envelope?.error?.message || text.trim() || `HTTP ${response.status}`,
    )
  }
  return payload as T
}

export const api = {
  /** Unauthenticated: tells a fresh install apart from a forgotten password. */
  setupStatus: () => request<SetupStatus>('GET', '/admin/setup'),

  /** Claims a gateway nobody has set up yet, and signs in as part of it. */
  setup: (password: string) => request<Session>('POST', '/admin/setup', { password }),

  signIn: (password: string) => request<Session>('POST', '/admin/session', { password }),
  signOut: () => request<unknown>('DELETE', '/admin/session'),
  whoAmI: () => request<Session>('GET', '/admin/me'),

  /** Ends every other session, and hands this one a fresh cookie. */
  changePassword: (current: string, next: string) =>
    request<Session>('POST', '/admin/password', {
      current_password: current,
      new_password: next,
    }),

  /** Returns the gateway to its first-run state. Upstream accounts survive. */
  deleteAdminAccount: (password: string) =>
    request<unknown>('POST', '/admin/account/delete', { password }),

  listAccounts: async (): Promise<{ accounts: Account[]; windowDays: number }> => {
    const res = await request<{ accounts: Account[] | null; window_days: number }>(
      'GET',
      '/admin/accounts',
    )
    return { accounts: res.accounts ?? [], windowDays: res.window_days }
  },

  /** Sets the whole priority order; the first account with room serves. */
  reorderAccounts: async (ids: string[]): Promise<{ accounts: Account[]; windowDays: number }> => {
    const res = await request<{ accounts: Account[] | null; window_days: number }>(
      'POST',
      '/admin/accounts/order',
      { ids },
    )
    return { accounts: res.accounts ?? [], windowDays: res.window_days }
  },

  startOAuth: (provider: string) =>
    request<OAuthStart>('POST', '/admin/accounts/oauth/start', { provider }),

  completeOAuth: async (provider: string, state: string, redirectUrl: string): Promise<Account> => {
    const res = await request<{ account: Account }>('POST', '/admin/accounts/oauth/complete', {
      provider,
      state,
      redirect_url: redirectUrl,
    })
    return res.account
  },

  testAccount: (id: string) =>
    request<ProbeResult>('POST', `/admin/accounts/${encodeURIComponent(id)}/test`, { model: '' }),

  /** Re-reads the subscription usage now, rather than waiting for the poll. */
  refreshUsage: async (id: string): Promise<Account> => {
    const res = await request<{ account: Account }>(
      'POST',
      `/admin/accounts/${encodeURIComponent(id)}/usage`,
    )
    return res.account
  },

  refreshAccount: async (id: string): Promise<Account> => {
    const res = await request<{ account: Account }>(
      'POST',
      `/admin/accounts/${encodeURIComponent(id)}/refresh`,
    )
    return res.account
  },

  /**
   * Pause an account without destroying it. Deleting revokes the refresh
   * token upstream and costs a trip through the consent flow to undo.
   */
  setAccountDisabled: (id: string, disabled: boolean) =>
    request<Account>('POST', `/admin/accounts/${encodeURIComponent(id)}/disabled`, { disabled }),

  deleteAccount: (id: string) =>
    request<unknown>('DELETE', `/admin/accounts/${encodeURIComponent(id)}`),

  overview: () => request<Overview>('GET', '/admin/overview'),

  config: () => request<GatewayConfig>('GET', '/admin/config'),

  models: () => request<{ data: Model[] | null }>('GET', '/admin/models'),
  setSurface: (id: string, enabled: boolean) =>
    request<{ surfaces: Surface[] }>('POST', `/admin/surfaces/${encodeURIComponent(id)}`, {
      enabled,
    }),

  setFitImages: (enabled: boolean) =>
    request<{ fit_images: boolean }>('POST', '/admin/fit-images', { enabled }),

  setChatTitles: (change: { enabled?: boolean; capture?: boolean }) =>
    request<{ chat_titles: boolean; chat_titles_capture: boolean }>(
      'POST',
      '/admin/chat-titles',
      change,
    ),

  listKeys: async (): Promise<{
    keys: ApiKey[]
    windowDays: number
    budgetHours: number
    defaultRpm: number
  }> => {
    const res = await request<{
      keys: ApiKey[] | null
      window_days: number
      budget_hours: number
      default_rpm: number
    }>('GET', '/admin/keys')
    return {
      keys: res.keys ?? [],
      windowDays: res.window_days,
      budgetHours: res.budget_hours,
      defaultRpm: res.default_rpm,
    }
  },

  /** The plaintext comes back exactly once; nothing can retrieve it later. */
  createKey: (name: string, rpmLimit: number, ratePeriodS: number, tokenBudget: number) =>
    request<{ key: ApiKey; plaintext: string }>('POST', '/admin/keys', {
      name,
      rpm_limit: rpmLimit,
      rate_period_s: ratePeriodS,
      token_budget: tokenBudget,
    }),

  /** Rename a key or change its limit. The secret is untouched. */
  updateKey: (
    id: string,
    name: string,
    rpmLimit: number,
    ratePeriodS: number,
    tokenBudget: number,
  ) =>
    request<{ key: ApiKey }>('PATCH', `/admin/keys/${encodeURIComponent(id)}`, {
      name,
      rpm_limit: rpmLimit,
      rate_period_s: ratePeriodS,
      token_budget: tokenBudget,
    }),

  deleteKey: (id: string) => request<unknown>('DELETE', `/admin/keys/${encodeURIComponent(id)}`),

  usage: (days?: number) =>
    request<Usage>('GET', days === undefined ? '/admin/usage' : `/admin/usage?days=${days}`),

  chats: (days?: number) =>
    request<Chats>('GET', days === undefined ? '/admin/chats' : `/admin/chats?days=${days}`),

  /** One conversation's requests, oldest first — the drill-down behind a row. */
  chat: (id: string) =>
    request<{ enabled: boolean; id: string; requests: RequestRow[] }>(
      'GET',
      `/admin/chats/${encodeURIComponent(id)}`,
    ),

  docsInfo: () => request<DocsInfo>('GET', '/api/docs'),

  setDocs: (enabled: boolean) =>
    request<{ docs_enabled: boolean }>('POST', '/admin/docs', { enabled }),

  requestFacets: () =>
    request<{ enabled: boolean; facets?: RequestFacets }>('GET', '/admin/requests/facets'),

  recentRequests: async (
    limit = 50,
    after = '',
    filter: Record<string, string | string[]> = {},
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
      if (Array.isArray(v)) {
        for (const one of v) query.append(k, one)
      } else if (v !== undefined && v !== '') {
        query.set(k, v)
      }
    }
    const res = await request<{ requests: RequestRow[] | null; next_cursor?: string }>(
      'GET',
      `/admin/requests?${query.toString()}`,
    )
    return { rows: res.requests ?? [], nextCursor: res.next_cursor ?? '' }
  },
}
