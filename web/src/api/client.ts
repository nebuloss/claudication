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
  /** Absent until the account has served a request. */
  quota?: Quota
  /** What this gateway put through the account over the report window. */
  requests: number
  tokens: number
  /** Priority order, and whether this one would serve the next request. */
  position: number
  serving: boolean
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
  revoked_at?: string
  revoked: boolean
  rpm_limit: number
  requests: number
  tokens: number
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

export interface UsageReport {
  since: string
  totals: UsageTotals
  by_day: UsageBucket[]
  by_model: UsageBucket[]
  by_key: UsageBucket[]
  by_account: UsageBucket[]
  by_status: UsageBucket[]
}

export interface Usage {
  enabled: boolean
  days?: number
  retention_days?: number
  report?: UsageReport
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
}

export interface Overview {
  version: string
  commit: string
  listen: string
  started_at: string
  uptime_s: number
  usage_enabled: boolean
  ready: boolean
  accounts: { total: number; usable: number; needs_reauth_soon: number }
  keys: { total: number; active: number }
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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let response: Response
  try {
    response = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? null : JSON.stringify(body),
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new ApiError(0, 'network_error', `Could not reach the gateway: ${String(cause)}`)
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
    throw new ApiError(
      response.status,
      envelope?.error?.type ?? 'http_error',
      envelope?.error?.message ?? text.trim() ?? `HTTP ${response.status}`,
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

  deleteAccount: (id: string) =>
    request<unknown>('DELETE', `/admin/accounts/${encodeURIComponent(id)}`),

  overview: () => request<Overview>('GET', '/admin/overview'),

  listKeys: async (): Promise<{ keys: ApiKey[]; windowDays: number }> => {
    const res = await request<{ keys: ApiKey[] | null; window_days: number }>('GET', '/admin/keys')
    return { keys: res.keys ?? [], windowDays: res.window_days }
  },

  /** The plaintext comes back exactly once; nothing can retrieve it later. */
  createKey: (name: string, rpmLimit: number) =>
    request<{ key: ApiKey; plaintext: string }>('POST', '/admin/keys', {
      name,
      rpm_limit: rpmLimit,
    }),

  revokeKey: (id: string) =>
    request<unknown>('POST', `/admin/keys/${encodeURIComponent(id)}/revoke`),

  deleteKey: (id: string) => request<unknown>('DELETE', `/admin/keys/${encodeURIComponent(id)}`),

  usage: (days?: number) =>
    request<Usage>('GET', days === undefined ? '/admin/usage' : `/admin/usage?days=${days}`),

  recentRequests: async (limit = 50): Promise<RequestRow[]> => {
    const res = await request<{ requests: RequestRow[] | null }>(
      'GET',
      `/admin/requests?limit=${limit}`,
    )
    return res.requests ?? []
  },
}
