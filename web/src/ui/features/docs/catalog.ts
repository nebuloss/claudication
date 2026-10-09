/**
 * The models the connected accounts serve, as Anthropic lists them, and what
 * each client's config should say about them.
 *
 * The committed files under configs/clients name the models that existed when
 * they were written, and a model released since is missing from every one of
 * them until somebody edits five files by hand. The list they should carry is
 * the one GET /v1/models answers, which the gateway already proxies and which
 * says, per model, everything a client config asks: the context window, the
 * output cap, which effort levels exist, whether it reads images. So a config
 * handed out by this gateway is the committed file with its models rewritten
 * from that answer. The committed file stays complete on its own — it is what
 * the repository links to, and what is shown when the list cannot be read.
 *
 * Measured on the live list (2026-10-09): every field below is present on
 * every model, and the older models genuinely differ — Haiku 4.5 and Sonnet 4.5
 * have no effort setting, Opus 4.5 stops at high, Opus 4.6 and Sonnet 4.6 skip
 * xhigh. The hand-written lists had several of those wrong.
 */

/**
 * The context window crush is told for a model whose real one is larger.
 *
 * crush compacts when the room left in the window it is given falls to 20k, so
 * this number is its compaction point plus 20k; at the model's real 1M it
 * compacted at 980k, and sessions ran there. See docs/clients.md, "When crush
 * compacts", for the measurements behind 600k.
 */
export const CRUSH_WINDOW = 600_000

/** What a model's entry in the upstream list can say. Everything is optional:
 *  a field Anthropic stops sending falls back to the committed file's value. */
type Capability = { supported?: boolean }

export type CatalogModel = {
  id: string
  display_name?: string
  created_at?: string
  line?: string
  max_input_tokens?: number
  max_tokens?: number
  lifecycle?: string
  capabilities?: {
    effort?: Capability & Record<string, Capability | boolean | undefined>
    image_input?: Capability
    thinking?: Capability
  }
}

/** Effort levels in the order a picker should list them. */
const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const

/** The line a model belongs to: opus, sonnet, haiku, fable. */
function lineOf(m: CatalogModel): string {
  return m.line ?? /^claude-([a-z]+)/.exec(m.id)?.[1] ?? ''
}

export class Catalog {
  /** Active Claude models, newest first, as the upstream orders them. */
  readonly models: CatalogModel[]

  // Typed loosely on the way in: the API client's Model says less than this
  // reads, and every field is checked before it is trusted.
  constructor(models: readonly { id: string }[] | null | undefined) {
    // Retired and deprecated models still answer for a while, but a config
    // written today has no business offering one.
    this.models = ((models ?? []) as CatalogModel[]).filter(
      (m) =>
        m.id.startsWith('claude-') && (m.lifecycle === undefined || m.lifecycle === 'active'),
    )
  }

  /** Whether there is anything to write configs from. */
  get known(): boolean {
    return this.models.length > 0
  }

  /** The newest model of a line, or of anything when the line has none. */
  private newest(line: string): string {
    return (this.models.find((m) => lineOf(m) === line) ?? this.models[0]).id
  }

  /** The default for real work: the newest Opus. */
  get large(): string {
    return this.newest('opus')
  }

  /** The default for titles and summaries, run many times a session: the
   *  newest Haiku. */
  get small(): string {
    return this.newest('haiku')
  }

  /** The effort levels a model accepts, in picker order; empty when it has
   *  no effort setting at all. */
  efforts(m: CatalogModel): string[] {
    const e = m.capabilities?.effort
    if (e?.supported !== true) return []
    return EFFORTS.filter((level) => {
      const v = e[level]
      return typeof v === 'object' && v?.supported === true
    })
  }
}

// ── one rewrite per file ────────────────────────────────────────────────────
//
// Each takes the committed file and the catalog and returns the file to hand
// out. They change the model names and the per-model facts and nothing else:
// every other line is the committed file's, comments included.

/** crush.json: the provider's model list, and the large and small defaults. */
export function tailorCrush(body: string, catalog: Catalog): string {
  const json = JSON.parse(body) as {
    providers: Record<string, { models: Record<string, unknown>[] }>
    models?: Record<string, { model?: string }>
  }
  const provider = Object.values(json.providers)[0]
  provider.models = catalog.models.map((m) => {
    const window = m.max_input_tokens ?? 200_000
    const efforts = catalog.efforts(m)
    const entry: Record<string, unknown> = {
      id: m.id,
      name: m.display_name ?? m.id,
      context_window: Math.min(window, CRUSH_WINDOW),
      default_max_tokens: m.max_tokens ?? 64_000,
      can_reason: efforts.length > 0,
      supports_attachments: m.capabilities?.image_input?.supported ?? true,
      // The subscription is the bill; nothing here is charged per token.
      cost_per_1m_in: 0,
      cost_per_1m_out: 0,
      cost_per_1m_in_cached: 0,
      cost_per_1m_out_cached: 0,
    }
    if (efforts.length > 0) {
      entry.reasoning_levels = efforts
      entry.default_reasoning_effort = efforts.includes('medium') ? 'medium' : efforts[0]
    }
    return entry
  })
  if (json.models?.large) json.models.large.model = catalog.large
  if (json.models?.small) json.models.small.model = catalog.small
  return JSON.stringify(json, null, 2) + '\n'
}

/**
 * Codex's model catalog: one entry per model.
 *
 * Every entry in the committed catalog is the same apart from six fields —
 * slug, the two names, the two context windows and the order — so a new
 * model's entry is the first one with those six replaced. Its effort levels
 * are left as they are: the gateway does not forward Codex's reasoning setting
 * to Anthropic (see internal/api/openai), so they only label a picker.
 */
export function tailorCodexCatalog(body: string, catalog: Catalog): string {
  const json = JSON.parse(body) as { models: Record<string, unknown>[] }
  const template = json.models[0]
  json.models = catalog.models.map((m, i) => ({
    ...template,
    slug: m.id,
    display_name: m.display_name ?? m.id,
    description: m.display_name ?? m.id,
    context_window: m.max_input_tokens ?? template.context_window,
    max_context_window: m.max_input_tokens ?? template.max_context_window,
    priority: (i + 1) * 10,
  }))
  return JSON.stringify(json, null, 2) + '\n'
}

/** Replaces the value of a line `name<sep>"value"` or `name<sep>value`. */
function setting(body: string, pattern: RegExp, value: string): string {
  return body.replace(pattern, (_, head: string, quote: string) => `${head}${quote}${value}${quote}`)
}

/** Codex's config.toml: the default model. */
export function tailorCodexConfig(body: string, catalog: Catalog): string {
  return setting(body, /^(model = )(")[^"]*"/m, catalog.large)
}

/** Claude Code's environment: the two model variables. */
export function tailorClaudeCode(body: string, catalog: Catalog): string {
  body = setting(body, /^(export ANTHROPIC_MODEL=)()\S+/m, catalog.large)
  return setting(body, /^(export ANTHROPIC_SMALL_FAST_MODEL=)()\S+/m, catalog.small)
}

/** opencode: the two defaults. Its model list is opencode's own catalog of
 *  the Anthropic provider, which this file overrides only the address of. */
export function tailorOpencode(body: string, catalog: Catalog): string {
  body = setting(body, /^(\s*"model": )(")anthropic\/[^"]*"/m, `anthropic/${catalog.large}`)
  return setting(body, /^(\s*"small_model": )(")anthropic\/[^"]*"/m, `anthropic/${catalog.small}`)
}
