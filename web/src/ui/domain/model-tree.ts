/**
 * A list of models as a tree: provider › family › model, newest first at each
 * level. What every list of model switches is drawn from — an account's
 * Models page, a client's Allowed models — so they group alike. Pure: no
 * React, so `make test-web` exercises it.
 *
 * The families:
 *
 *   - Claude by line — Opus, Sonnet, Haiku, Fable — from the model's `line`
 *     when the list carries one, else from its id (claude-opus-5-5 is opus),
 *     the rule the gateway's own claude:<family> targets follow
 *     (internal/service/models, familyOf).
 *   - ChatGPT by generation, from the slug's version: gpt-6.1-sol is GPT-6.1;
 *     gpt-6-astra, gpt-6-sol and gpt-6-luna are GPT-6; gpt-5.6-sol, -terra and
 *     -luna GPT-5.6 (the backend's list, 2026-10-19). Not by the suffix
 *     (sol, luna, terra): a suffix spans generations — there is a gpt-6-sol
 *     and a gpt-5.6-sol — so grouping by it would put the newest model beside
 *     one two releases old, and fold nothing that is out of date. A
 *     generation is what goes out of date as a whole, which is what folding
 *     the older families away is for, and it is what openai:light reads as
 *     "that one's generation".
 *
 * Newest first: a family by its newest model, a model by its release date.
 * The ChatGPT backend lists no dates, so there a generation is ordered by its
 * version and a model within it by the backend's own ranking (priority).
 */

import { providerTitle } from '../clients/specs'

/** What the tree reads off a model: the fields both providers' lists have. */
export interface TreeModel {
  id: string
  display_name?: string
  created_at?: string
  line?: string
  provider?: string
  priority?: number
}

export interface ModelFamily<T> {
  /** provider/family: unique in the tree, and what a fold is remembered by. */
  key: string
  title: string
  /** Newest first. */
  models: T[]
}

export interface ModelProvider<T> {
  key: string
  title: string
  /** Newest first. */
  families: ModelFamily<T>[]
}

const OTHER = 'other'

/** The numbers in a version, for comparing: "6.1" is [6, 1]. */
function version(s: string): number[] {
  return s.split(/[.-]/).filter((p) => /^\d+$/.test(p)).map(Number)
}

/** Positive when a is the later version. A longer one wins a tie: 6.1 after 6. */
function compareVersions(a: number[], b: number[]): number {
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const d = (a[i] ?? -1) - (b[i] ?? -1)
    if (d !== 0) return d
  }
  return 0
}

/** A model's family: its key within the provider, and how it is named. */
export function familyOf(m: TreeModel): { key: string; title: string } {
  const provider = m.provider ?? 'anthropic'
  const id = m.id.toLowerCase()
  if (provider === 'openai') {
    const v = /^gpt-(\d+(?:\.\d+)*)/.exec(id)?.[1]
    return v !== undefined ? { key: `gpt-${v}`, title: `GPT-${v}` } : { key: OTHER, title: 'Other' }
  }
  // A line may be a word or a phrase ("Claude Opus"); its last word is the
  // family either way.
  const line = (m.line ?? '').trim().toLowerCase().split(/\s+/).pop() ?? ''
  const fam = line !== '' ? line : (/^claude-([a-z]+)-/.exec(id)?.[1] ?? '')
  return fam !== '' ? { key: fam, title: fam.charAt(0).toUpperCase() + fam.slice(1) } : { key: OTHER, title: 'Other' }
}

/** Positive when a is the newer model. */
function compareModels(a: TreeModel, b: TreeModel): number {
  const ad = a.created_at ?? ''
  const bd = b.created_at ?? ''
  if (ad !== bd && ad !== '' && bd !== '') return ad < bd ? -1 : 1
  // The backend's ranking, best first, where neither is dated.
  if (a.priority !== undefined || b.priority !== undefined) {
    return (b.priority ?? Infinity) - (a.priority ?? Infinity)
  }
  return compareVersions(version(a.id), version(b.id))
}

/** Positive when family a is the newer, by its newest model; Other last. */
function compareFamilies<T extends TreeModel>(a: ModelFamily<T>, b: ModelFamily<T>): number {
  const ao = a.key.endsWith(`/${OTHER}`)
  const bo = b.key.endsWith(`/${OTHER}`)
  if (ao !== bo) return ao ? -1 : 1
  const an = a.models[0]
  const bn = b.models[0]
  if (an === undefined || bn === undefined) return 0
  const ad = an.created_at ?? ''
  const bd = bn.created_at ?? ''
  if (ad !== bd && ad !== '' && bd !== '') return ad < bd ? -1 : 1
  return compareVersions(version(a.title), version(b.title))
}

const PROVIDER_ORDER = ['anthropic', 'openai']

/** The models as a tree, each level newest first; providers Claude, then ChatGPT, then any other. */
export function modelTree<T extends TreeModel>(models: readonly T[]): ModelProvider<T>[] {
  const byProvider = new Map<string, Map<string, ModelFamily<T>>>()
  for (const m of models) {
    const p = m.provider ?? 'anthropic'
    const f = familyOf(m)
    const families = byProvider.get(p) ?? new Map<string, ModelFamily<T>>()
    byProvider.set(p, families)
    const key = `${p}/${f.key}`
    const family = families.get(key) ?? { key, title: f.title, models: [] }
    families.set(key, family)
    family.models.push(m)
  }
  const rank = (p: string) => {
    const i = PROVIDER_ORDER.indexOf(p)
    return i < 0 ? PROVIDER_ORDER.length : i
  }
  return [...byProvider.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([key, families]) => ({
      key,
      title: providerTitle(key),
      families: [...families.values()]
        .map((f) => ({ ...f, models: [...f.models].sort((a, b) => compareModels(b, a)) }))
        .sort((a, b) => compareFamilies(b, a)),
    }))
}

/** A group's switch: on when every model in it is, off when none is, mixed between. */
export function groupState(on: readonly boolean[]): 'on' | 'off' | 'mixed' {
  const n = on.filter(Boolean).length
  return n === 0 ? 'off' : n === on.length ? 'on' : 'mixed'
}

/**
 * Which groups are open before anyone has folded one: every provider, and in
 * each the newest family alone — the one a model is likely being looked for
 * in; the older ones are a click away rather than a scroll past.
 */
export function openByDefault<T>(tree: readonly ModelProvider<T>[]): Set<string> {
  const open = new Set<string>()
  for (const p of tree) {
    open.add(p.key)
    const first = p.families[0]
    if (first !== undefined) open.add(first.key)
  }
  return open
}
