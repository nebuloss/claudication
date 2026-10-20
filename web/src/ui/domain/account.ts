import type { Account } from '../../api/client'
import { relative } from '../format'
import type { Tone } from '../styles/tone'

/**
 * What an account is called: the name the operator gave it, or its address
 * when it has none — the gateway's Account.DisplayName, and what the names
 * ledger labels its history with, so a row in the request log and the
 * account's own page say the same.
 */
export function accountName(a: Pick<Account, 'name' | 'email'>): string {
  return a.name !== undefined && a.name !== '' ? a.name : a.email
}

/** Whether the account is named, so its address is worth showing beside the name. */
export function isNamed(a: Pick<Account, 'name'>): boolean {
  return a.name !== undefined && a.name !== ''
}

/**
 * What state an account is in: one colour, the chip's words (`label`), and
 * the same state as a headline with what to know about it underneath
 * (`headline`, `detail`), for the tile on its Usage page. The chip on its row
 * in the list, the one on its own page and that tile must never disagree,
 * which is why all three come from here.
 *
 * The order is the answer to "why is this account not serving", most urgent
 * first, so an account in two states shows the one an operator acts on.
 */
export function accountStatus(a: Account): { tone: Tone; label: string; headline: string; detail?: string } {
  if (a.disabled) return { tone: 'error', label: 'disabled', headline: 'Disabled', detail: 'switched off here' }
  // Terminal, and the only state a human has to act on: nothing the gateway
  // does brings a refused refresh token back.
  if (a.needs_reauth === true) {
    return { tone: 'error', label: 'reconnect this account', headline: 'Reconnect', detail: 'its sign-in was refused' }
  }
  // Cooling down comes first among the recoverable states, because it is the
  // only one that answers "why is this account not being used right now" —
  // and it is the one the card could not show at all until the pool started
  // reporting it.
  if (a.cooling_until !== undefined && a.cooling_until !== '') {
    const back = `back ${relative(a.cooling_until)}`
    return { tone: 'warn', label: `cooling down, ${back}`, headline: 'Cooling down', detail: back }
  }
  // A subscription window the upstream is refusing outranks a stale token:
  // refreshing fixes one and does nothing at all for the other.
  if (a.quota !== undefined && !a.quota.allowed) {
    return { tone: 'error', label: 'limit reached', headline: 'Limit reached', detail: 'the provider is refusing it' }
  }
  if (a.expired) return { tone: 'warn', label: 'token expired', headline: 'Token expired', detail: 'refreshed on next use' }
  if (a.last_error) return { tone: 'warn', label: 'last call failed', headline: 'Last call failed', detail: 'still in rotation' }
  // Ready, and the one serving is the account the next request goes to: the
  // chip beside this one says so on the page; the headline says it alone.
  return a.serving
    ? { tone: 'ok', label: 'ready', headline: 'Serving', detail: 'takes the next request' }
    : { tone: 'ok', label: 'ready', headline: 'Ready', detail: 'serves when its turn comes' }
}
