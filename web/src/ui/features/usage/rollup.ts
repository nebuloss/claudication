import type { SlotUse } from '../../../api/client'

/**
 * Usage rows added up by one of their columns: a client's, a model's, an
 * account's.
 *
 * Every usage table is one of these over the same SlotUse rows — an account's
 * clients, a client's model names, and Gateway's models, clients and
 * accounts — so the tables add up to one another because they are the same
 * rows folded differently, not because separate queries happen to agree.
 * Pure, so the arithmetic is checked under node (make test-web).
 */
export type Rollup = {
  id: string
  requests: number
  errors: number
  input: number
  output: number
  cache: number
  /** Inside output, as the provider counts it: shown, never added. */
  reasoning: number
  /** The latest request, as the ISO string the rows carry; '' for none. */
  last: string
  /** The rows it was added up from, busiest first as they came. */
  rows: SlotUse[]
}

/** An empty rollup, for an item that exists but sent nothing in the window. */
export function emptyRollup(id: string): Rollup {
  return { id, requests: 0, errors: 0, input: 0, output: 0, cache: 0, reasoning: 0, last: '', rows: [] }
}

/** Add one row into a rollup. */
function add(u: Rollup, r: SlotUse) {
  u.requests += r.requests
  u.errors += r.errors
  u.input += r.input_tokens
  u.output += r.output_tokens
  u.cache += r.cache_tokens
  u.reasoning += r.reasoning_tokens
  // ISO strings in one zone compare as the moments they name.
  if (r.last > u.last) u.last = r.last
  u.rows.push(r)
}

/** The rows added up by `key`, in the order each key first appears. */
export function rollup(rows: readonly SlotUse[], key: (r: SlotUse) => string): Rollup[] {
  const out = new Map<string, Rollup>()
  for (const r of rows) {
    const id = key(r)
    let u = out.get(id)
    if (u === undefined) {
      u = emptyRollup(id)
      out.set(id, u)
    }
    add(u, r)
  }
  return [...out.values()]
}

/** Every token sent and got, as the account list counts them; reasoning is inside output. */
export function tokensOf(u: Pick<Rollup, 'input' | 'output' | 'cache'>): number {
  return u.input + u.output + u.cache
}

/** Busiest first by tokens, then by requests: the order a table is read in. */
export function byTokens(a: Rollup, b: Rollup): number {
  return tokensOf(b) - tokensOf(a) || b.requests - a.requests
}

/**
 * Gateway-wide rows with the account folded out: one row per client, model
 * name and model, as a client's own page has them. The gateway read splits
 * each by the account that served it, which a table of model names has no
 * column for.
 */
export function withoutAccount(rows: readonly SlotUse[]): SlotUse[] {
  const merged = rollup(rows, (r) => JSON.stringify([r.client_id, r.slot, r.model, r.provider]))
  // Busiest first again: folding two accounts' rows together can move one up.
  merged.sort((a, b) => b.requests - a.requests)
  return merged.map((u) => {
    const first = u.rows[0]!
    return {
      client_id: first.client_id,
      client: first.client,
      type: first.type,
      slot: first.slot,
      model: first.model,
      provider: first.provider,
      requests: u.requests,
      errors: u.errors,
      input_tokens: u.input,
      output_tokens: u.output,
      cache_tokens: u.cache,
      reasoning_tokens: u.reasoning,
      last: u.last,
    }
  })
}

/** What part of a whole one figure is, in percent; 0 of nothing is 0. */
export function percentOf(part: number, whole: number): number {
  return whole > 0 ? (part / whole) * 100 : 0
}
