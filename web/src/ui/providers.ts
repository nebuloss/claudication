/**
 * The upstream providers an account can be on, in the order the UI lists
 * them.
 *
 * One today. Listed rather than assumed, so adding an account starts by
 * choosing one and the accounts are grouped by one — the shape a second
 * provider needs, without a screen to redo when it comes. The id is the
 * gateway's own (store.Account.Provider), which is also models.dev's key for
 * the same provider.
 */
export type Provider = { id: string; label: string; detail: string }

export const PROVIDERS: [Provider, ...Provider[]] = [
  {
    id: 'anthropic',
    label: 'Claude',
    detail: 'A Claude Pro or Max subscription, signed in through Anthropic.',
  },
]

/** How a provider reads in the UI; its id when it is not one listed here. */
export function providerLabel(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.label ?? id
}

/**
 * Accounts grouped by provider: listed providers first in their order, then
 * any other in name order. Within a group the accounts keep the order they
 * came in, which is their priority.
 */
export function byProvider<T extends { provider: string }>(items: T[]): { provider: string; items: T[] }[] {
  const order = (id: string) => {
    const i = PROVIDERS.findIndex((p) => p.id === id)
    return i < 0 ? PROVIDERS.length : i
  }
  const groups = new Map<string, T[]>()
  for (const it of items) {
    const g = groups.get(it.provider)
    if (g === undefined) groups.set(it.provider, [it])
    else g.push(it)
  }
  return [...groups.entries()]
    .map(([provider, items]) => ({ provider, items }))
    .sort((a, b) => order(a.provider) - order(b.provider) || a.provider.localeCompare(b.provider))
}
