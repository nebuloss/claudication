/**
 * Testing an account's models: one tiny turn per model on the account's own
 * credential, a few at a time, each result shown as it arrives.
 *
 * Pure, so `make test-web` checks the bound and the order.
 */

import type { ModelTest, ProbeResult } from '../../api/types'

/**
 * Runs fn over items, at most `limit` at once, in order of starting, calling
 * done with each result as it lands. Two at a time is enough to make a dozen
 * models quick and few enough not to look like a burst to the provider.
 */
export async function eachBounded<T, R>(
  items: readonly T[],
  limit: number,
  fn: (item: T) => Promise<R>,
  done: (item: T, result: R) => void,
): Promise<void> {
  let next = 0
  const worker = async () => {
    while (next < items.length) {
      const item = items[next++] as T
      done(item, await fn(item))
    }
  }
  await Promise.all(Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, worker))
}

/** A probe's result as an account's model row keeps it. */
export function testOf(model: string, p: ProbeResult, at = new Date()): ModelTest {
  return { model, at: at.toISOString(), ok: p.ok, status: p.status, latency_ms: p.latency_ms, error: p.error }
}

/**
 * What a failed test means, in the operator's words where the provider's are
 * known: the model is not in this account's plan, the account cannot use it at
 * all, or the upstream's own text.
 */
export function explain(t: ModelTest): string {
  if (t.ok) return `ok · ${t.latency_ms} ms`
  const text = (t.error ?? '').toLowerCase()
  if (text.includes('usage_not_included') || text.includes('not included')) return 'not in this plan'
  if (t.status === 404 || text.includes('not_found') || text.includes('does not exist')) return 'not served to this account'
  if (t.status === 401 || t.status === 403) return 'refused: the account may not use it'
  if (t.status === 429) return 'rate limited just now'
  return t.error !== undefined && t.error !== '' ? t.error : `failed (HTTP ${t.status})`
}

/**
 * What fits in the chip at the end of a model's row: explain's words when
 * they are short, else just the failure's kind. An upstream's own text can
 * run to a whole URL and a socket error, which pushed the row past the edge
 * of its list; it stays on the chip's tooltip, whole.
 */
export function brief(t: ModelTest): string {
  const said = explain(t)
  if (said.length <= 32) return said
  return t.status > 0 ? `failed (HTTP ${t.status})` : 'could not reach it'
}

/** The models to suggest switching off: on, tested, and failed for a reason that lasts. */
export function failing(models: readonly { id: string; enabled: boolean; test?: ModelTest }[]): string[] {
  return models
    .filter((m) => m.enabled && m.test !== undefined && !m.test.ok && m.test.status !== 429)
    .map((m) => m.id)
}
