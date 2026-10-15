/**
 * The admin UI's page tree, and reading a location against it.
 *
 * Every page has a path of its own, and the path is the tree:
 *
 *   /                                   the gateway's overview
 *   /accounts                           the accounts
 *   /accounts/<id>                      one account's overview
 *   /accounts/<id>/usage|chats|requests its traffic
 *   /accounts/<id>/settings/<section>   one of its settings
 *   /keys … the same shape, for keys
 *   /activity/chats|requests            all traffic
 *   /settings/<section>                 the gateway's settings
 *
 * So a link, a bookmark and Back all land on a page, never on "whatever
 * sub-tab was open last". Each level's overview is the level itself — the
 * root, an account's own path — rather than a page named Overview under it,
 * so the trail never says Overview twice over. A path that names a branch
 * with no page of its own — /settings, /activity — is completed to its first
 * page by canonical(), and old paths are moved to where their screen went.
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
  { id: 'accounts', label: 'Accounts' },
  { id: 'keys', label: 'API keys' },
  { id: 'activity', label: 'Activity' },
  { id: 'settings', label: 'Settings' },
] as const satisfies readonly Section[]
export type Tab = (typeof TABS)[number]['id']

/**
 * An account's pages under /accounts/<id>. Its overview is that path itself,
 * not one of these.
 */
export const ACCOUNT_PAGES = [
  { id: 'usage', label: 'Usage' },
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
  { id: 'settings', label: 'Settings' },
] as const satisfies readonly Section[]

/**
 * A key's pages under /keys/<id>. Clients is a page of its own rather than a
 * setting: handing a key to a client is what a key is for, and it was the
 * fourth of five sections under Settings.
 */
export const KEY_PAGES = [
  { id: 'usage', label: 'Usage' },
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
  { id: 'clients', label: 'Clients' },
  { id: 'settings', label: 'Settings' },
] as const satisfies readonly Section[]

/** The pages of one kind of item. */
export function pagesOf(kind: 'accounts' | 'keys'): readonly Section[] {
  return kind === 'accounts' ? ACCOUNT_PAGES : KEY_PAGES
}

/** Pausing is under Advanced, beside removing: both take the account out of use. */
export const ACCOUNT_SETTINGS = [
  { id: 'models', label: 'Models' },
  { id: 'connection', label: 'Connection' },
  { id: 'advanced', label: 'Advanced' },
] as const satisfies readonly Section[]

/** The models a key may use and what its aliases serve are one page. */
export const KEY_SETTINGS = [
  { id: 'models', label: 'Models' },
  { id: 'general', label: 'General' },
  { id: 'advanced', label: 'Advanced' },
] as const satisfies readonly Section[]

export const ACTIVITY_PAGES = [
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
] as const satisfies readonly Section[]

export const GATEWAY_SETTINGS = [
  { id: 'aliases', label: 'Aliases' },
  { id: 'apis', label: 'APIs' },
  { id: 'titles', label: 'Titles' },
  { id: 'images', label: 'Images' },
  { id: 'documentation', label: 'Documentation' },
  { id: 'advanced', label: 'Advanced' },
  { id: 'configuration', label: 'Configuration' },
] as const satisfies readonly Section[]

/** Where screens used to be, so a bookmark still lands on its screen. */
const MOVED: Record<string, string> = {
  models: '/settings/aliases',
  chats: '/activity/chats',
  requests: '/activity/requests',
  usage: '/',
}

/**
 * Sections whose path has since been renamed to say what the navigation
 * calls them, so a link from before still opens the same section.
 */
const RENAMED: Record<string, Record<string, string>> = {
  settings: {
    models: 'aliases',
    chats: 'titles',
    docs: 'documentation',
    security: 'advanced',
  },
  accounts: { availability: 'advanced' },
  keys: { limits: 'general', aliases: 'models' },
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
      // The item's overview is its own path; /overview under it, and any
      // page that is not one, land there.
      if (!known(pagesOf(top), b)) return pathOf(top, a)
      const page = b!
      if (page !== 'settings') return pathOf(top, a, page)
      // A key's clients were a settings section before they were a page.
      if (top === 'keys' && (c === 'clients' || c === 'configs')) return pathOf(top, a, 'clients')
      const list = top === 'accounts' ? ACCOUNT_SETTINGS : KEY_SETTINGS
      const section = (c !== undefined && RENAMED[top]?.[c]) || c
      return pathOf(top, a, 'settings', known(list, section) ? section! : list[0].id)
    }
    case 'activity':
      return pathOf('activity', known(ACTIVITY_PAGES, a) ? a! : ACTIVITY_PAGES[0].id)
    case 'settings': {
      const section = (a !== undefined && RENAMED.settings[a]) || a
      return pathOf('settings', known(GATEWAY_SETTINGS, section) ? section! : GATEWAY_SETTINGS[0].id)
    }
  }
  return '/'
}

/** The top-level screen a canonical path is on. */
export function tabOf(pathname: string): Tab {
  const top = segments(pathname)[0]
  return TABS.some((t) => t.id === top) ? (top as Tab) : 'overview'
}

/** A place to go: where, and what the navigation calls it. */
export type NavLink = { href: string; label: string }

/**
 * The navigation for one page: the choices on its own level, which one is
 * this page, and the way up a level.
 *
 * Only the last level is listed, so the rail stays a handful of short entries
 * however deep the page is — an account's settings show its four sections,
 * not the whole tree above them. The level above is one Back away, and the
 * breadcrumb (trailOf) shows every level at once.
 */
export type NavLevel = { back?: NavLink; items: NavLink[]; current: string }

/** Names an account or a key by id, for the trail and the Back button. */
export type Namer = (kind: 'accounts' | 'keys', id: string) => string

const links = (base: string, list: readonly Section[]): NavLink[] =>
  list.map((s) => ({ href: `${base}/${s.id}`, label: s.label }))

// The overview is the root itself, reached by the trail's home step, so it
// is not one of the root's entries.
const ROOT: NavLink[] = TABS.filter((t) => t.id !== 'overview').map((t) => ({
  href: `/${t.id}`,
  label: t.label,
}))
const HOME: NavLink = { href: '/', label: 'Main menu' }

/** The first step of every trail: the gateway's overview, at the root. */
export const ROOT_CRUMB: NavLink = { href: '/', label: 'Overview' }

/** The navigation level of a canonical path. */
export function navLevel(pathname: string, name: Namer): NavLevel {
  const [top, id, page, section] = segments(pathname)
  if ((top === 'accounts' || top === 'keys') && id !== undefined) {
    const base = pathOf(top, id)
    if (page === 'settings') {
      const list = top === 'accounts' ? ACCOUNT_SETTINGS : KEY_SETTINGS
      return {
        back: { href: base, label: name(top, id) },
        items: links(`${base}/settings`, list),
        current: `${base}/settings/${section}`,
      }
    }
    const list = TABS.find((t) => t.id === top)!
    return {
      back: { href: pathOf(top), label: list.label },
      // Settings opens on its first section; its link says so, so it is the
      // same href the page will end up at and Back history stays clean.
      items: links(base, pagesOf(top)).map((l) =>
        l.href === `${base}/settings` ? { ...l, href: canonical(l.href) } : l,
      ),
      // On the item's overview, no entry: the overview is the item itself.
      current: page === undefined ? base : `${base}/${page}`,
    }
  }
  if (top === 'activity') {
    return { back: HOME, items: links('/activity', ACTIVITY_PAGES), current: pathOf(top, id ?? '') }
  }
  if (top === 'settings') {
    return { back: HOME, items: links('/settings', GATEWAY_SETTINGS), current: pathOf(top, id ?? '') }
  }
  // The root's own level: the screens, each opening on its first page.
  return {
    items: ROOT.map((l) => ({ ...l, href: canonical(l.href) })),
    current: canonical(top === undefined ? '/' : pathOf(top)),
  }
}

/**
 * Every level from the root down to this page, each but the last a link. It
 * starts at the root — the gateway's overview, drawn as a home mark — and an
 * item's step is its overview, so no level is named twice.
 */
export function trailOf(pathname: string, name: Namer): NavLink[] {
  const [top, id, page, section] = segments(pathname)
  const out: NavLink[] = [ROOT_CRUMB]
  const tab = TABS.find((t) => t.id === tabOf(pathname))!
  if (tab.id === 'overview') return out
  const label = (list: readonly Section[], x: string | undefined) =>
    list.find((s) => s.id === x)?.label ?? x ?? ''
  if ((top === 'accounts' || top === 'keys') && id !== undefined) {
    const base = pathOf(top, id)
    out.push({ href: pathOf(top), label: tab.label })
    out.push({ href: base, label: name(top, id) })
    if (page !== undefined) {
      out.push({ href: canonical(`${base}/${page}`), label: label(pagesOf(top), page) })
    }
    if (page === 'settings') {
      const list = top === 'accounts' ? ACCOUNT_SETTINGS : KEY_SETTINGS
      out.push({ href: pathOf(top, id, 'settings', section ?? ''), label: label(list, section) })
    }
    return out
  }
  out.push({ href: canonical(pathOf(tab.id)), label: tab.label })
  if (top === 'activity' && id !== undefined) {
    out.push({ href: pathOf(top, id), label: label(ACTIVITY_PAGES, id) })
  } else if (top === 'settings' && id !== undefined) {
    out.push({ href: pathOf(top, id), label: label(GATEWAY_SETTINGS, id) })
  }
  return out
}

let reloadOnNavigate = false

/** From now on, the next navigation loads the page afresh: a newer UI is out. */
export function reloadOnNextNavigation() {
  reloadOnNavigate = true
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
  // A newer UI has been deployed since this one loaded: moving to another page
  // is the moment nothing is lost by loading it, so go there by a real load.
  if (reloadOnNavigate) {
    window.location.assign(url)
    return
  }
  window.history.pushState(null, '', url)
  window.dispatchEvent(new PopStateEvent('popstate'))
}
