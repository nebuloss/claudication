import type { RequestRow } from '../../../api/client'
import { compact } from '../../format'
import { providerLabel } from '../../providers'
import type { Tone } from '../../styles/tone'

/**
 * What to call a stored outcome class.
 *
 * The upstream's own words with the underscores taken out, which is what it
 * calls them in its own documentation, plus the two this gateway names itself:
 * a content check, and nothing having come back at all.
 */
const MESSAGE_LABELS: Record<string, string> = {
  content_check: 'refused on content',
  no_answer: 'no answer',
  other: 'failed',
}

export function messageLabel(code: string | undefined): string {
  if (code === undefined || code === '') return '—'
  return MESSAGE_LABELS[code] ?? code.replace(/_/g, ' ')
}

function dash(v: string | undefined): string {
  return v === undefined || v === '' ? '—' : v
}

/**
 * The columns worth ordering by.
 *
 * Three, not ten. Sorting a categorical column — a model, a client, an address
 * — groups rows that the column's own menu can simply select, and it only ever
 * reorders the rows already loaded, so on a filtered log it answers a question
 * about this page rather than about the log. Where the menu is the better
 * tool, the heading offers the menu and nothing else.
 *
 * What is left is the three where order is the whole question: when, how much,
 * how long.
 */
export type SortKey = 'at' | 'tokens' | 'duration'
export type Sort = { key: SortKey; dir: 'asc' | 'desc' }

/**
 * One request as the log shows it: the row the server sent, and every reading
 * of it the table, the dialog and the copied record share.
 *
 * The status chip was worked out twice — once in the row and once in the
 * dialog — and the record's "outcome" a third time; a reading that is a
 * getter here is one the three cannot disagree about.
 */
export class RequestRecord {
  constructor(readonly row: RequestRow) {}

  /** Did it work, at a glance: an error or no answer is a failure, a 4xx a warning. */
  get tone(): Exclude<Tone, 'neutral'> {
    const r = this.row
    if (r.error !== undefined && r.error !== '') return 'error'
    if (r.status === 0 || r.status >= 500) return 'error'
    if (r.status >= 400) return 'warn'
    return 'ok'
  }

  /** The status as its chip says it; a dot marks a stream that answered. */
  get statusLabel(): string {
    const r = this.row
    return `${r.status === 0 ? 'failed' : r.status}${r.streaming && r.status === 200 ? ' ·' : ''}`
  }

  get failed(): boolean {
    return this.row.error !== undefined && this.row.error !== ''
  }

  get refused(): boolean {
    return this.row.rejected === true
  }

  /** The Message column's word, or "no error". */
  get outcome(): string {
    const label = messageLabel(this.row.error_code)
    return label === '—' ? 'no error' : label
  }

  /** "12k / 3k" in a cell, or a dash for a request that spent nothing. */
  get tokensShort(): string {
    const r = this.row
    return r.input_tokens + r.output_tokens > 0 ? `${compact(r.input_tokens)} / ${compact(r.output_tokens)}` : '—'
  }

  /** The same, spelt out, with the cache when there was any. */
  get tokensLong(): string {
    const r = this.row
    if (r.input_tokens + r.output_tokens === 0) return '—'
    return (
      `${compact(r.input_tokens)} in / ${compact(r.output_tokens)} out` +
      // Part of the output, not beside it: the provider bills it as output.
      (r.reasoning_tokens !== undefined && r.reasoning_tokens > 0
        ? ` (${compact(r.reasoning_tokens)} reasoning)`
        : '') +
      (r.cache_tokens > 0 ? ` / ${compact(r.cache_tokens)} cached` : '')
    )
  }

  /**
   * The account, with whose it is: two providers' accounts can share an
   * address, and the address alone does not say which served.
   */
  get accountLabel(): string {
    const r = this.row
    if (r.account_email === undefined || r.account_email === '') return '—'
    return r.provider !== undefined && r.provider !== ''
      ? `${r.account_email} · ${providerLabel(r.provider)}`
      : r.account_email
  }

  /** The model, and the slot name the client asked for when it asked by one. */
  get modelLabel(): string {
    const r = this.row
    return r.virtual_model ? `${r.virtual_model} → ${dash(r.model)}` : dash(r.model)
  }

  /** A field, or a dash when the row has none. */
  field(v: 'conversation_id' | 'client' | 'key_name' | 'account_email' | 'ip' | 'model'): string {
    return dash(this.row[v])
  }

  /** What a row is worth for a given column. */
  sortValue(key: SortKey): number {
    const r = this.row
    switch (key) {
      case 'at':
        return new Date(r.at).getTime()
      case 'tokens':
        return r.input_tokens + r.output_tokens
      case 'duration':
        return r.duration_ms
    }
  }

  /** The row as something worth pasting somewhere else. */
  asText(): string {
    const r = this.row
    // Both spellings of the instant: the ISO one to read, the epoch to grep a
    // log with.
    const epoch = Math.round(new Date(r.at).getTime() / 1000)
    const lines = [
      `timestamp: ${r.at}`,
      `epoch:     ${Number.isNaN(epoch) ? '—' : epoch}`,
      `status:    ${r.status === 0 ? 'failed (no response)' : r.status}`,
      `outcome:   ${this.outcome}`,
      `forwarded: ${this.refused ? 'no, refused by the gateway' : 'yes'}`,
      `model:     ${dash(r.model)}`,
      `chat:      ${dash(r.conversation_id)}`,
      `client:    ${dash(r.key_name)}`,
      `agent:     ${dash(r.client)}${r.surface ? ` (${r.surface})` : ''}`,
      `account:   ${this.accountLabel}`,
      `ip:        ${dash(r.ip)}`,
      `path:      ${r.path}`,
      `streaming: ${r.streaming ? 'yes' : 'no'}`,
      `tokens:    ${r.input_tokens} in / ${r.output_tokens} out / ${r.cache_tokens} cache` +
        (r.reasoning_tokens !== undefined && r.reasoning_tokens > 0 ? ` / ${r.reasoning_tokens} reasoning` : ''),
      `duration:  ${r.duration_ms}ms`,
      `first tok: ${r.first_token_ms === undefined ? '—' : `${r.first_token_ms}ms`}`,
    ]
    // Only when there is one: a blank heading followed by nothing reads as
    // something having gone missing.
    if (this.failed) lines.push('', r.error ?? '')
    return lines.join('\n')
  }

  /**
   * The rows in a column's order. Sorted here rather than by the server: the
   * cursor is a keyset on (at, id), so a different order would need a
   * different cursor. Ties always fall back to newest first, whichever way the
   * column runs; without it, rows sharing a value would shuffle on every
   * re-render.
   */
  static sorted(rows: readonly RequestRecord[], sort: Sort): RequestRecord[] {
    return [...rows].sort((a, b) => {
      const cmp = a.sortValue(sort.key) - b.sortValue(sort.key)
      if (cmp !== 0) return sort.dir === 'asc' ? cmp : -cmp
      return b.sortValue('at') - a.sortValue('at')
    })
  }
}
