/**
 * One client's configuration files, written from its own model names.
 *
 * The committed files under configs/clients are each type's starting point —
 * complete, readable in the repository, and written with the names a new
 * client of that type is given. A client's files are
 * those with its own names put in every model field, each name described
 * with the facts of the model it serves
 * now: the context window, the output cap, the effort levels. Those facts are
 * read once, when the file is written, which is why switching a slot to a
 * model with other limits means running the install command again.
 *
 * Pure: strings in, strings out, so `make test-web` checks every rewrite
 * against the real committed files.
 */

import type { Model } from '../../api/types'

/** One slot as a file is written from it. */
export interface SlotSetup {
  /** The type's slot id: large, main, review... */
  id: string
  role: string
  /** Where the client's configuration takes it. */
  field: string
  /** The model name the client asks for. */
  name: string
  /** Its entry in the client's model list: the serving model's facts. */
  served?: Model
}

/**
 * What a client's files are written from. No API: each type's files point at
 * the one its type speaks.
 */
export interface Setup {
  slots: SlotSetup[]
}

/**
 * The context window crush is told for a model whose real one is larger.
 *
 * crush compacts when the room left in the window it is given falls to 20k, so
 * this number is its compaction point plus 20k; at the model's real 1M it
 * compacted at 980k, and sessions ran there. See docs/clients.md, "When crush
 * compacts", for the measurements behind 600k.
 */
export const CRUSH_WINDOW = 600_000

/** Effort levels in the order a picker should list them. */
const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'] as const

/** The effort levels a model accepts, in picker order; empty when it has none. */
export function efforts(m: Model | undefined): string[] {
  const e = m?.capabilities?.effort
  if (e?.supported !== true) return []
  return EFFORTS.filter((level) => {
    const v = e[level]
    return typeof v === 'object' && v?.supported === true
  })
}

/** The real model a slot serves, or '' when its list entry was not read. */
function servingOf(s: SlotSetup): string {
  return s.served?.serving ?? ''
}

/** Each name once, in slot order: two slots may share a name on a custom client. */
function unique(slots: SlotSetup[]): SlotSetup[] {
  const seen = new Set<string>()
  return slots.filter((s) => (seen.has(s.name) ? false : (seen.add(s.name), true)))
}

/** The slot of a role, or the first. */
function byRole(setup: Setup, role: string): SlotSetup | undefined {
  return setup.slots.find((s) => s.role === role) ?? setup.slots[0]
}

// ── Claude Code ──────────────────────────────────────────────────────────────

/** The variables that name a model, whichever of them a client has a slot for. */
const CLAUDE_CODE_MODEL_VAR = /^(ANTHROPIC_MODEL|ANTHROPIC_SMALL_FAST_MODEL|ANTHROPIC_DEFAULT_[A-Z]+_MODEL|CLAUDE_CODE_AUTO_MODE_MODEL)$/

/**
 * claude-code.sh: each model variable set to its slot's name, and every name
 * the picker lists told which real model it behaves like. The auto-mode
 * classifier's name is not in the picker: nobody picks it.
 */
export function claudeCodeSh(body: string, setup: Setup): string {
  const byField = new Map(setup.slots.map((s) => [s.field, s.name]))
  const written = new Set<string>()
  const lines: string[] = []
  let last = -1
  for (const line of body.split('\n')) {
    const m = /^export ([A-Z_]+)=(.*)$/.exec(line)
    if (m === null || !CLAUDE_CODE_MODEL_VAR.test(m[1] ?? '')) {
      lines.push(line)
      continue
    }
    const name = byField.get(m[1] ?? '')
    if (name === undefined) continue
    lines.push(`export ${m[1]}=${name}`)
    written.add(m[1] ?? '')
    last = lines.length - 1
  }
  const missing = setup.slots.filter((s) => !written.has(s.field) && CLAUDE_CODE_MODEL_VAR.test(s.field))
  if (missing.length > 0 && last >= 0) {
    lines.splice(last + 1, 0, ...missing.map((s) => `export ${s.field}=${s.name}`))
  }
  const out = lines.join('\n')
  return out.replace(/--settings '(\{.*\})'/, (whole, json: string) => {
    let committed: { modelPicker?: { options?: { model: string; behavesAs: string }[] } }
    try {
      committed = JSON.parse(json)
    } catch {
      return whole
    }
    const before = new Map((committed.modelPicker?.options ?? []).map((o) => [o.model, o.behavesAs]))
    const options = unique(setup.slots)
      .filter((s) => s.role !== 'classifier')
      .map((s) => ({ model: s.name, behavesAs: servingOf(s) || before.get(s.name) || '' }))
      .filter((o) => o.behavesAs !== '')
    return `--settings '${JSON.stringify({ ...committed, modelPicker: { ...committed.modelPicker, options } })}'`
  })
}

// ── Codex ────────────────────────────────────────────────────────────────────

/**
 * config.toml: each model key — model, review_model, and the ones in the
 * [agents] and [memories] tables — set to its slot's name. A field naming two
 * keys (memories) sets both.
 */
export function codexToml(body: string, setup: Setup): string {
  let out = body
  for (const s of setup.slots) {
    for (const field of s.field.split(',').map((f) => f.trim())) {
      const key = field.split('.').at(-1) ?? field
      out = out.replace(new RegExp(`^(${key} = )"[^"]*"`, 'm'), `$1"${s.name}"`)
    }
  }
  return out
}

/**
 * The model catalog Codex reads instead of asking: one entry per name, the
 * committed first entry with the name and the serving model's window put in,
 * and the review slot named as the reviewer every entry defers to.
 */
export function codexCatalog(body: string, setup: Setup): string {
  const json = JSON.parse(body) as { models: Record<string, unknown>[] }
  const template = json.models[0]
  if (template === undefined) return body
  const review = setup.slots.find((s) => s.role === 'review')?.name ?? null
  json.models = unique(setup.slots).map((s, i) => {
    const window = s.served?.max_input_tokens ?? template.context_window
    return {
      ...template,
      slug: s.name,
      display_name: s.name,
      description: s.served?.display_name || servingOf(s) || s.name,
      context_window: window,
      max_context_window: window,
      priority: (i + 1) * 10,
      auto_review_model_override: review,
    }
  })
  return JSON.stringify(json, null, 2) + '\n'
}

// ── crush ────────────────────────────────────────────────────────────────────

/**
 * crush.json: its anthropic provider at the bare address (crush adds /v1),
 * its model list the client's names with their models' facts, and
 * models.large and models.small naming the two slots, the large one thinking.
 */
export function crushJson(body: string, setup: Setup, base: string): string {
  const json = JSON.parse(body) as {
    providers: Record<string, Record<string, unknown>>
    models?: Record<string, { model?: string; think?: boolean }>
  }
  const provider = Object.values(json.providers)[0]
  if (provider === undefined) return body
  provider.type = 'anthropic'
  provider.base_url = base
  provider.models = unique(setup.slots).map((s) => {
    const m = s.served
    const levels = efforts(m)
    const entry: Record<string, unknown> = {
      id: s.name,
      name: s.name,
      context_window: Math.min(m?.max_input_tokens ?? 200_000, CRUSH_WINDOW),
      default_max_tokens: m?.max_tokens ?? 64_000,
      can_reason: levels.length > 0,
      supports_attachments: m?.capabilities?.image_input?.supported ?? true,
      // The subscription is the bill; nothing here is charged per token.
      cost_per_1m_in: 0,
      cost_per_1m_out: 0,
      cost_per_1m_in_cached: 0,
      cost_per_1m_out_cached: 0,
    }
    if (levels.length > 0) {
      entry.reasoning_levels = levels
      entry.default_reasoning_effort = levels.includes('medium') ? 'medium' : levels[0]
    }
    return entry
  })
  const large = byRole(setup, 'main')
  const small = setup.slots.find((s) => s.role === 'fast') ?? large
  if (json.models?.large && large) json.models.large.model = large.name
  if (json.models?.small && small) json.models.small.model = small.name
  if (json.models?.large) json.models.large.think = true
  return JSON.stringify(json, null, 2) + '\n'
}

// ── opencode ─────────────────────────────────────────────────────────────────

/** JSONC to JSON: comments out, strings (and the // in their URLs) left alone. */
export function stripComments(body: string): string {
  let out = ''
  for (let i = 0; i < body.length; i++) {
    const c = body[i]
    if (c === '"') {
      let j = i + 1
      while (j < body.length && body[j] !== '"') j += body[j] === '\\' ? 2 : 1
      out += body.slice(i, j + 1)
      i = j
    } else if (c === '/' && body[i + 1] === '/') {
      while (i < body.length && body[i] !== '\n') i++
      out += '\n'
    } else {
      out += c
    }
  }
  return out
}

/**
 * opencode.jsonc: its built-in anthropic provider overridden, at /v1, with
 * each name declared at its model's limits, and model and small_model naming
 * the two slots. The file's opening comment is kept; the rest is written
 * whole. A reasoning model gets opencode's adaptive thinking modes, as current
 * Claude models take them.
 */
export function opencodeJsonc(body: string, setup: Setup): string {
  const head = body.split('\n').filter((l, i, all) => i < all.findIndex((x) => x.includes('"$schema"')) && l.trim().startsWith('//'))
  const json = JSON.parse(stripComments(body)) as {
    provider: Record<string, { options?: Record<string, unknown>; models?: Record<string, unknown> }>
    model?: string
    small_model?: string
  }
  const committed = Object.values(json.provider)[0] ?? {}
  const id = 'anthropic'
  const models: Record<string, unknown> = {}
  for (const s of unique(setup.slots)) {
    const m = s.served
    const levels = efforts(m)
    const entry: Record<string, unknown> = {
      name: s.name,
      limit: { context: m?.max_input_tokens ?? 200_000, output: m?.max_tokens ?? 64_000 },
      reasoning: levels.length > 0,
      attachment: m?.capabilities?.image_input?.supported ?? true,
      tool_call: true,
      temperature: false,
      modalities: { input: ['text', 'image', 'pdf'], output: ['text'] },
    }
    if (levels.length > 0) {
      entry.variants = Object.fromEntries(levels.map((l) => [l, { thinking: { type: 'adaptive' }, effort: l }]))
    }
    models[s.name] = entry
  }
  json.provider = { [id]: { options: committed.options ?? {}, models } }
  const large = byRole(setup, 'main')
  const small = setup.slots.find((s) => s.role === 'fast') ?? large
  if (large) json.model = `${id}/${large.name}`
  if (small) json.small_model = `${id}/${small.name}`
  const text = JSON.stringify(json, null, 2)
  return head.length > 0 ? `${text.replace(/^\{\n/, `{\n${head.join('\n')}\n`)}\n` : `${text}\n`
}
