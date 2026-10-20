/* The admin API's shapes, as the client hands them to the screens. */

/** One row of the usage display, titled as the client titles it. */
export interface UsageLimitRow {
  title: string
  kind: string
  /** Percentage, 0-100. */
  percent: number
  severity: string
  resets_at?: string
  is_active: boolean
  /** The window's length, when the provider reports one (ChatGPT does). */
  window_seconds?: number
}

/**
 * What the provider last reported — for Claude the same figures `/usage`
 * prints, for ChatGPT the backend's own windows. The 5-hour window is what
 * bites during a working session; the 7-day one is what runs out over a heavy
 * week. Each provider's windows land in these two by their length.
 * Percentages are 0-100.
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
  /** The subscription the sign-in named (ChatGPT's plus, pro, team). */
  plan?: string
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
  /** Models turned off for this account, as they were switched. */
  models_off?: string[]
}

export interface AccountList {
  accounts: Account[]
  windowDays: number
  /**
   * How often the gateway re-reads each subscription's usage, in seconds. The
   * accounts panel polls at this rate: asking faster returns the same figures,
   * and asking slower shows stale ones for no reason.
   */
  usagePollS: number
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

/**
 * A sign-in begun. `flow` says which kind: a consent page whose code is
 * pasted back (Claude), or a device code entered on the provider's own page
 * while this one asks whether it has been (ChatGPT).
 */
export interface OAuthStart {
  provider: string
  flow: 'paste' | 'device'
  state: string
  /** The consent page, for a paste sign-in. */
  auth_url?: string
  redirect_uri?: string
  /** The code and the page to enter it on, for a device sign-in. */
  user_code?: string
  verification_uri?: string
  /** How often to ask whether it has been approved, in seconds. */
  interval_s?: number
  expires_at: string
  instructions: string
  /** Set on a reconnect: the account being renewed, and who to sign in as. */
  account_id?: string
  email?: string
}

/** One model as one account's subscription serves it, with its switch. */
export interface AccountModel extends Model {
  /** Whether this account may serve it. */
  enabled: boolean
  /** False for a model switched off that the upstream no longer lists. */
  listed: boolean
}

/** What a finished consent flow stored. */
export interface OAuthResult {
  account: Account
  /** True when an account already connected was renewed rather than added. */
  renewed: boolean
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
  /**
   * The key's estimated part of the Claude subscription's two windows, as a
   * percent of every account's capacity together. Absent when usage history
   * is off or no account has reported its utilisation.
   */
  subscription?: SubscriptionShare
  /** Models this key may not ask for; empty allows every model. */
  models_off: string[]
  /** The secret is stored sealed, so the key and its configs can be shown again. */
  secret_kept: boolean
}

/** One key's estimated use of the subscription; see the server's keyshare.go. */
export interface SubscriptionShare {
  five_hour: number
  seven_day: number
  accounts: {
    account_id: string
    email: string
    /** Percentage points of this account's window. */
    five_hour: number
    seven_day: number
    /** The key's fraction of the gateway's weighted traffic to this account. */
    five_hour_share: number
    seven_day_share: number
  }[]
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
  /** An account bucket's provider. */
  provider?: string
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

/** What a report is narrowed to: one account's page, one key's, or neither. */
export interface UsageScope {
  account?: string
  key?: string
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
  /** The operator typed it, so the gateway will not rename it on its own. */
  title_manual: boolean
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
  /** Where the conversation's context stands. Absent on the unattributed row. */
  context?: ChatContext
}

/**
 * A chat's context: the prompt its client resends each turn, counted on the
 * model that carried its largest one, and how often it fell by more than half
 * — compacted. See internal/store/context.go for the rule.
 */
export interface ChatContext {
  /** Empty when no request in the window succeeded. */
  model: string
  now: number
  peak: number
  compactions: number
  /** When it compacted, oldest first. Only on one chat's detail. */
  compacted_at?: string[]
}

export interface ChatDetail {
  enabled: boolean
  id: string
  /** The latest requests, oldest first. */
  requests: RequestRow[]
  context: ChatContext
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
  /** The row's id, which only grows: what tells a polled row from a held one. */
  id: number
  at: string
  key_name?: string
  account_email?: string
  /** Whose account served it: anthropic, openai. */
  provider?: string
  model?: string
  /** The part of output_tokens spent reasoning, where the provider said. */
  reasoning_tokens?: number
  /** What the client asked for, when that was a virtual model. */
  virtual_model?: string
  path: string
  status: number
  streaming: boolean
  input_tokens: number
  output_tokens: number
  cache_tokens: number
  duration_ms: number
  /**
   * How long a streamed request waited for its first token, retries included.
   * Absent when not measured: not a stream, no content, or an older row.
   */
  first_token_ms?: number
  error?: string
  /** Set when the refusal does not mean what its message says. */
  error_kind?: string
  /**
   * What went wrong, in one word, decided when the request was recorded: the
   * upstream's own type, content_check, no_answer, or absent for one that
   * worked. What the Message column shows and what its filter matches.
   */
  error_code?: string
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
  /**
   * The chat this request belongs to, as its client named it. Empty for a
   * client that names none, which is one bucket rather than one chat.
   */
  conversation_id?: string
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
  /** Labelled with each chat's name where it has one, since an id is not a thing anyone recognises. */
  chats: FacetValue[]
  /** The stored outcome classes — the Message column's values. */
  messages: FacetValue[]
  /** Forwarded to the upstream, or refused here. */
  kinds: FacetValue[]
  /** Account ids, labelled with each one's email. */
  accounts: FacetValue[]
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

/** Whether a model has a feature, as the upstream's model list says it. */
export type Capability = { supported?: boolean }

/**
 * One model the connected accounts can serve, as the upstream lists it: one
 * entry of GET /v1/models.
 *
 * Read live rather than hardcoded: the gateway has no model allowlist — it
 * relays whatever name a client sends — so any list written into the UI would
 * be a second opinion that quietly goes stale.
 *
 * Only id is guaranteed. Everything else is optional because a field
 * Anthropic stops sending must fall back to the committed config's value
 * rather than break it; ui/clients/catalog.ts reads these and checks each one
 * before trusting it. The catalog used to declare its own fuller copy of this
 * type and cast the list into it.
 */
export interface Model {
  id: string
  display_name?: string
  created_at?: string
  line?: string
  max_input_tokens?: number
  max_tokens?: number
  lifecycle?: string
  capabilities?: {
    /** Each level by name — low, medium, … — beside whether effort exists at all. */
    effort?: Capability & Record<string, Capability | boolean | undefined>
    image_input?: Capability
    thinking?: Capability
  }
  /** A gateway model role; its other fields are the serving model's. */
  virtual?: boolean
  serving?: string
  role?: string
  /** Whose accounts serve it, when not Anthropic's: openai. */
  provider?: string
  /** The provider's own ranking of its models, best first (ChatGPT's). */
  priority?: number
}

/** A model role — claude-large, claude-small — for the gateway or one key. */
export interface ModelRole {
  role: string
  /**
   * What this scope binds it to. For the gateway, empty is automatic (the
   * newest of family); for a key, empty is "follow the gateway".
   */
  model: string
  /** What a built-in Claude alias follows while unbound; empty otherwise. */
  family: string
  builtin: boolean
  /** Whose models a built-in alias follows while unbound: anthropic, openai. */
  provider?: string
  /** What a request for the role gets now, and why not when it cannot. */
  serving: string
  problem?: string
  /** Its per-client bindings; on the gateway's view only. */
  clients?: ClientRole[]
}

/**
 * One alias bound for one client, the client named as the request log names
 * it, and what that client is served for it by a key that binds nothing.
 */
export interface ClientRole {
  client: string
  model: string
  serving: string
  problem?: string
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
}

/** What /health answers on every listener, unauthenticated. */
export interface Health {
  status: string
  version: string
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
  /** The gateway process's own memory, read when the overview was asked for. */
  memory?: MemorySnapshot
}

/** internal/memlimit's Snapshot. Byte counts; 0 means unknown or unset. */
export interface MemorySnapshot {
  /** Resident memory: what a container's limit counts. 0 off Linux. */
  rss_bytes: number
  /** The process ceiling from memory-limit or the cgroup. */
  limit_bytes: number
  /** The Go runtime's own target, a share of the ceiling. */
  heap_limit_bytes: number
  heap_in_use_bytes: number
  /** Held from the system but unused; the trimmer gives it back. */
  heap_idle_bytes: number
  goroutines: number
  gc_cycles: number
}

/** One file the installer writes; see internal/service/install. */
export interface InstallFile {
  client_id: string
  client: string
  path: string
  windows_path?: string
  env?: boolean
  body: string
  merge?: InstallMerge
}

/**
 * Folds a file into the client's own config instead of replacing it: JSON
 * names the dotted paths the gateway owns; TOML owns what its file defines.
 */
export type InstallMerge = { format: 'json'; owns: string[] } | { format: 'toml' }

export interface SetupStatus {
  needs_setup: boolean
  min_password_len: number
  /** The running build, for the header. */
  version?: string
}

