/**
 * The upstream providers an account can be on, in the order the UI lists
 * them.
 *
 * Listed rather than assumed, so adding an account starts by choosing one and
 * the accounts are grouped by one. The id is the gateway's own
 * (store.Account.Provider), which is also models.dev's key for the same
 * provider. `signIn` is how its accounts connect: Claude and Gemini by
 * consenting and pasting back the code Anthropic or Google shows, ChatGPT by
 * entering a device code on OpenAI's own page — neither needs anything to
 * come back from the browser, so both work the same for a gateway on
 * another machine.
 */
export type Provider = {
  id: string
  label: string
  detail: string
  signIn: 'paste' | 'device'
  /** Who runs the sign-in, as a sentence names them: "Anthropic shows you a code". */
  vendor: string
  /** Where the browser is signed in, which decides the account that approves. */
  site: string
  /** What consent ends on, for the paste flow's step: what to copy back. */
  pasteHint?: string
  /** Whose figures the usage endpoint reports, and which client shows the same ones. */
  usage: { source: string; client: string }
}

export const PROVIDERS: [Provider, ...Provider[]] = [
  {
    id: 'anthropic',
    label: 'Claude',
    detail: 'A Claude Pro or Max subscription, signed in through Anthropic.',
    signIn: 'paste',
    vendor: 'Anthropic',
    site: 'claude.ai',
    usage: { source: 'Anthropic', client: '/usage in Claude Code' },
  },
  {
    id: 'openai',
    label: 'ChatGPT',
    detail: 'A ChatGPT Plus, Pro or Team subscription, signed in with a device code.',
    signIn: 'device',
    vendor: 'OpenAI',
    site: 'chatgpt.com',
    usage: { source: 'OpenAI', client: '/status in Codex' },
  },
  {
    id: 'google',
    label: 'Gemini',
    detail: 'A Google account — the free tier, Google AI Pro or Ultra — signed in through Antigravity.',
    signIn: 'paste',
    vendor: 'Google',
    site: 'accounts.google.com',
    pasteHint:
      'Your browser then fails to load a localhost:51121 page. That is expected: copy the whole address from its address bar.',
    usage: { source: 'Google', client: 'the model picker in Antigravity' },
  },
]

/** A provider by id, when it is one listed here. */
export function providerOf(id: string): Provider | undefined {
  return PROVIDERS.find((p) => p.id === id)
}

/** How a provider reads in the UI; its id when it is not one listed here. */
export function providerLabel(id: string): string {
  return providerOf(id)?.label ?? id
}

/**
 * A subscription plan as the provider's sign-in named it — "plus", "pro",
 * "team" — the way the provider writes it on its own pages.
 */
export function planLabel(plan: string | undefined): string {
  if (plan === undefined || plan === '') return ''
  return plan
    .split(/[_\s-]+/)
    .filter((w) => w !== '')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
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
