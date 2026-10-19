import type { Account } from '../../api/client'
import { relative } from '../format'
import type { Tone } from '../styles/tone'

/**
 * What state an account is in, as one word and a colour: the chip on its row
 * in the list and the one on its own page, which must never disagree.
 *
 * The order is the answer to "why is this account not serving", most urgent
 * first, so an account in two states shows the one an operator acts on.
 */
export function accountStatus(a: Account): { tone: Tone; label: string } {
  if (a.disabled) return { tone: 'error', label: 'disabled' }
  // Terminal, and the only state a human has to act on: nothing the gateway
  // does brings a refused refresh token back.
  if (a.needs_reauth === true) return { tone: 'error', label: 'reconnect this account' }
  // Cooling down comes first among the recoverable states, because it is the
  // only one that answers "why is this account not being used right now" —
  // and it is the one the card could not show at all until the pool started
  // reporting it.
  if (a.cooling_until !== undefined && a.cooling_until !== '') {
    return { tone: 'warn', label: `cooling down, back ${relative(a.cooling_until)}` }
  }
  // A subscription window the upstream is refusing outranks a stale token:
  // refreshing fixes one and does nothing at all for the other.
  if (a.quota !== undefined && !a.quota.allowed) return { tone: 'error', label: 'limit reached' }
  if (a.expired) return { tone: 'warn', label: 'token expired' }
  if (a.last_error) return { tone: 'warn', label: 'last call failed' }
  return { tone: 'ok', label: 'ready' }
}
