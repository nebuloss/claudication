/**
 * The admin UI's page tree, and reading a location against it.
 *
 * Every page has a path of its own, and the path is the tree:
 *
 *   /                                   overview
 *   /accounts                           the accounts
 *   /accounts/<id>/usage|chats|requests one account's traffic
 *   /accounts/<id>/settings/<section>   one of its settings
 *   /keys … the same shape, for keys
 *   /activity/chats|requests            all traffic
 *   /settings/<section>                 the gateway's settings
 *
 * So a link, a bookmark and Back all land on a page, never on "whatever
 * sub-tab was open last". A path that names a branch rather than a page —
 * /accounts/<id>, /settings — is completed to the branch's first page by
 * canonical(), and old paths are moved to where their screen went.
 *
 * Pure: string in, string out, no window. Kept apart from the hooks because
 * this is the part that is quietly wrong — a trailing slash from a copied
 * link, an encoded id — and the part worth reading without a DOM.
 */

/** A page in a list of them: its path segment and how the nav names it. */
export type Section = { id: string; label: string }

/** The top-level screens, in the order the app bar lists them. */
export const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'accounts', label: 'Claude accounts' },
  { id: 'keys', label: 'API keys' },
  { id: 'activity', label: 'Activity' },
  { id: 'settings', label: 'Settings' },
] as const satisfies readonly Section[]
export type Tab = (typeof TABS)[number]['id']

/** An account's or a key's pages, under /accounts/<id> and /keys/<id>. */
export const ITEM_PAGES = [
  { id: 'usage', label: 'Usage' },
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
  { id: 'settings', label: 'Settings' },
] as const satisfies readonly Section[]

export const ACCOUNT_SETTINGS = [
  { id: 'models', label: 'Models' },
  { id: 'connection', label: 'Connection' },
  { id: 'availability', label: 'Availability' },
  { id: 'remove', label: 'Remove' },
] as const satisfies readonly Section[]

export const KEY_SETTINGS = [
  { id: 'models', label: 'Models' },
  { id: 'aliases', label: 'Model aliases' },
  { id: 'limits', label: 'Name and limits' },
  { id: 'configs', label: 'Client configurations' },
  { id: 'delete', label: 'Delete' },
] as const satisfies readonly Section[]

export const ACTIVITY_PAGES = [
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
] as const satisfies readonly Section[]

export const GATEWAY_SETTINGS = [
  { id: 'models', label: 'Model aliases' },
  { id: 'apis', label: 'API surfaces' },
  { id: 'chats', label: 'Chat names' },
  { id: 'images', label: 'Oversized images' },
  { id: 'docs', label: 'Public docs page' },
  { id: 'security', label: 'Admin account' },
  { id: 'configuration', label: 'Configuration' },
] as const satisfies readonly Section[]

/** Where screens used to be, so a bookmark still lands on its screen. */
const MOVED: Record<string, string> = {
  models: '/settings/models',
  chats: '/activity/chats',
  requests: '/activity/requests',
  usage: '/',
}

/** The path's segments, decoded. A malformed escape is kept as it came. */
export function segments(pathname: string): string[] {
  return pathname
    .split('/')
    .filter((s) => s !== '')
    .map((s) => {
      try {
        return decodeURIComponent(s)
      } catch {
        return s
      }
    })
}

/** A path from segments, each encoded. */
export function pathOf(...parts: string[]): string {
  return '/' + parts.map(encodeURIComponent).join('/')
}

const known = (list: readonly Section[], id: string | undefined) =>
  id !== undefined && list.some((s) => s.id === id)

/**
 * The page a path means, as its canonical path: itself when it already names
 * a page, completed or moved when it does not. Every unknown part falls back
 * to the first page of its branch rather than to a blank screen — a URL is
 * something people type, edit and truncate.
 */
export function canonical(pathname: string): string {
  const [top, a, b, c] = segments(pathname)
  if (top === undefined) return '/'
  if (top in MOVED && a === undefined) return MOVED[top]
  switch (top) {
    case 'overview':
      return '/'
    case 'accounts':
    case 'keys': {
      if (a === undefined) return pathOf(top)
      const page = known(ITEM_PAGES, b) ? b! : 'usage'
      if (page !== 'settings') return pathOf(top, a, page)
      const list = top === 'accounts' ? ACCOUNT_SETTINGS : KEY_SETTINGS
      return pathOf(top, a, 'settings', known(list, c) ? c! : list[0].id)
    }
    case 'activity':
      return pathOf('activity', known(ACTIVITY_PAGES, a) ? a! : ACTIVITY_PAGES[0].id)
    case 'settings':
      return pathOf('settings', known(GATEWAY_SETTINGS, a) ? a! : GATEWAY_SETTINGS[0].id)
  }
  return '/'
}

/** The top-level screen a canonical path is on. */
export function tabOf(pathname: string): Tab {
  const top = segments(pathname)[0]
  return TABS.some((t) => t.id === top) ? (top as Tab) : 'overview'
}

/**
 * Go somewhere, query string and all, without reloading the app.
 *
 * pushState alone is invisible to everything watching the location: the
 * listeners are on popstate, which the browser fires only for Back and
 * Forward. Dispatching one says "the location changed" in the vocabulary every
 * screen is already listening in.
 */
export function navigate(url: string) {
  window.history.pushState(null, '', url)
  window.dispatchEvent(new PopStateEvent('popstate'))
}
