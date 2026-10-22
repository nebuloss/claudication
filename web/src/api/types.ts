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
  /**
   * What the operator calls it; absent when it is known by its address.
   * accountName (ui/domain/account) is the one to show.
   */
  name?: string
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
  /** Requests it is serving now, streams included. In memory, like cooling_until. */
  in_flight: number
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
  /** The last test of this model on this account, when there was one. */
  test?: ModelTest
}

/** One model's last test on one account: a tiny turn on its own credential. */
export interface ModelTest {
  model: string
  at: string
  ok: boolean
  status: number
  latency_ms: number
  error?: string
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
  /** The same, per provider's pool of accounts: Claude's and ChatGPT's are separate budgets. */
  providers: { provider: string; five_hour: number; seven_day: number; accounts: number }[]
  accounts: {
    account_id: string
    email: string
    /** What the account is called: its name, or its address. */
    name: string
    provider: string
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
  /**
   * How its client was run, from its latest request: Interactive, Headless
   * (claude -p), Agent SDK… Absent when the User-Agent does not say, which is
   * every client but Claude Code. What tells a headless chat, which Claude
   * Code never names, from an interactive one not named yet.
   */
  surface?: string
  key_name: string
  /** What the account is called now: its name, or its address (the field is older than names). */
  account_email: string
  /** In first-seen order. A chat that switched model mid-way shows both. */
  models: string[]
  requests: number
  /** The requests that were the conversation itself, less the ones naming it. */
  turns: number
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
  /** The client that sent it: its id (its key's) and what it is called now. */
  key_id?: string
  key_name?: string
  /** And the account that served it, as it is called now: its name, or its address. */
  account_email?: string
  /** Whose account served it: anthropic, openai. */
  provider?: string
  model?: string
  /** The part of output_tokens spent reasoning, where the provider said. */
  reasoning_tokens?: number
  /** The client's slot name it asked for, when it asked by one. */
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
   * A change the client's own agent made to it through the gateway's MCP
   * server — a model name rebound, an update command made — with the tool as
   * its path. Nothing was relayed or refused, so no usage figure counts it.
   */
  change?: boolean
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
  /** How that was run, where its User-Agent says: Claude Code's Interactive, Headless… */
  surface?: string
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
  /** A client's model name; its other fields are the serving model's. */
  virtual?: boolean
  serving?: string
  /** The slot that name fills. */
  slot?: string
  /** Whose accounts serve it, when not Anthropic's: openai. */
  provider?: string
  /** The provider's own ranking of its models, best first (ChatGPT's). */
  priority?: number
}

export interface GatewayConfig {
  /** The file it was read from, empty when there was none. */
  path: string
  settings: Setting[]
  /** The gateway switch: off, every client-facing route refuses. */
  gateway_enabled: boolean
}

/** What /health answers on every listener, unauthenticated. */
export interface Health {
  status: string
  version: string
  /**
   * Whether client requests are accepted: the Gateway switch. Absent from a
   * gateway from before the switch, which always accepted them.
   */
  accepting?: boolean
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

/**
 * One of a typed client's files as its page shows it, written by the gateway
 * (internal/service/clientconfig) with its names and what each serves, for
 * the address the page asked for. The key is the example's, clc_..., which
 * the page swaps for the client's own while it is offered.
 */
export interface ClientFile {
  /** Where it goes, as a reader writes the path. */
  label: string
  /** What it is called when downloaded. */
  filename: string
  lang: 'json' | 'jsonc' | 'toml' | 'sh'
  body: string
}

export interface SetupStatus {
  needs_setup: boolean
  min_password_len: number
  /** The running build, for the header. */
  version?: string
}


/** One of the gateway's two APIs: Anthropic Messages, or OpenAI Responses. */
export type DialectId = 'anthropic' | 'openai'

/** How a custom client names models: its own slots, or real model ids. */
export type ClientMode = 'binding' | 'direct'

/** One model field of a client type's configuration (internal/service/clients). */
export interface SlotSpec {
  id: string
  label: string
  role: string
  /** Where the client's configuration takes it, as the client spells it. */
  field: string
  /** The name a new slot is given. */
  name: string
  /** The moving target a new slot follows. */
  follows: string
  hint: string
}

/** One kind of client, as the gateway configures it. */
export interface TypeSpec {
  id: string
  label: string
  /** The API it speaks, fixed by the type; absent for a custom client, whose key works on both. */
  api?: DialectId
  slots: SlotSpec[]
  custom: boolean
  /**
   * The role an operator's own slot takes, absent for a type with none: a
   * custom client's, and a typed one's ("picker"), which its files declare
   * so its model picker lists it.
   */
  extra_slots?: string
  /** What such a slot is for. */
  extra_hint?: string
  /** The client names its own sessions and the gateway reads the name: no "Name chats" switch. */
  names_its_chats: boolean
}

/** The table of client types and the APIs. */
export interface ClientTypesWire {
  types: TypeSpec[]
  dialects: { id: DialectId; title: string }[]
  /** Where the gateway's MCP server is on the relay: /mcp. */
  mcp_path: string
}

/**
 * One thing a client's slot can be bound to now — a real model, or a target
 * it follows — and what it serves. The server offers only what is available
 * to that client (internal/service/models, available.go).
 */
export interface BindingChoice {
  model?: string
  follows?: string
  title: string
  serving: string
  provider: string
}

/** One of a client's slots, resolved. */
export interface SlotWire extends SlotSpec {
  /** The model name the client asks for. */
  name: string
  /** The real model it is bound to, or `bound` the target it follows. */
  model: string
  bound?: string
  /** How the slot's menu names its binding. */
  title: string
  /** What the binding points at now, shown even when it cannot be served. */
  serving: string
  provider?: string
  /** Why the binding is not available to this client now; absent when it is. */
  unavailable?: string
  /** One of the operator's own, which can be taken away; a type's own slots stay. */
  removable: boolean
}

/** An install link's state, polled while the client's page is open. */
export interface InstallState {
  state: 'none' | 'waiting' | 'used' | 'expired'
  token?: string
  expires_at: string
  used?: { at: string; ip: string; shell: string }
  /** While it waits: the line that runs it, by shell (sh, ps1), as the gateway writes it. */
  commands?: Record<string, string>
}

/** A client, and the key it holds. */
export interface ClientWire extends ApiKey {
  type: string
  mode: ClientMode
  disabled: boolean
  disabled_at?: string
  /** Whether its chats are named: its switch, or always for a type that names its own. */
  name_chats: boolean
  /**
   * Whether a request of its carrying more than 20 images has each capped at
   * 2000 px when Claude serves it. The one rewrite that changes what the model
   * is shown rather than the shape of the request, so it is off until asked for.
   */
  fit_images: boolean
  /**
   * Whether its own agent may change it through the gateway's MCP server:
   * rebind its names, add and remove its own, make its update command. Off
   * until switched on; the read tools need nothing.
   */
  agent_manage: boolean
  configured_at?: string
  install: InstallState
  /** Its update link's state: the keyless command a configured client's page offers. */
  update: InstallState
  slots: SlotWire[]
  /** What its slots can be bound to now; on its own page, not in the list. */
  choices?: BindingChoice[]
}

/** What a client is doing now, for its Usage page. */
export interface ClientLive {
  per_minute: number[]
  since: string
  period_s: number
  in_period: number
  tokens_day: number
  in_flight: number
  rpm_limit: number
  default_rpm: number
  token_budget: number
  budget_hours: number
  history_recorded: boolean
}

/** What the whole gateway is doing now, for Activity's Requests page: a client's live figures without its limits. */
export type GatewayLive = Pick<ClientLive, 'per_minute' | 'since' | 'in_flight' | 'history_recorded'>

/** Limits read off a client's own history. */
export interface LimitSuggestion {
  days: number
  rate_per_minute: number
  peak_per_minute: number
  daily_tokens: number
  peak_daily: number
  active_days: number
}

/** What a pair of limits would have refused of the last days. */
export interface LimitReplay {
  requests: number
  rate_refused: number
  budget_refused: number
  budget_days: number
}

/** A client's traffic under one model name, served by one model. */
export interface SlotUse {
  client_id: string
  client: string
  type: string
  /** The account that served it, and what it is called: on the gateway-wide rows only. */
  account_id?: string
  account?: string
  slot: string
  model: string
  provider: string
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  cache_tokens: number
  reasoning_tokens: number
  last: string
}

/** One client's share of one account's windows. */
export interface AccountShare {
  account_id: string
  email: string
  provider: string
  five_hour: number
  seven_day: number
  five_hour_share: number
  seven_day_share: number
}
