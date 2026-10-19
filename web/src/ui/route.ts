/**
 * The admin UI's page tree, and reading a location against it.
 *
 * Every page has a path of its own, and the path is the tree:
 *
 *   /                                   the gateway's overview
 *   /accounts                           the accounts
 *   /accounts/<id>                      one account's overview
 *   /accounts/<id>/<page>               its traffic and its settings, side by side
 *   /keys … the same shape, for keys
 *   /activity/chats|requests            all traffic
 *   /settings                           the admin account and its security
 *   /settings/<section>                 each of the others
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

/**
 * An account's or a key's page, and which half of its pages it is in: what it
 * did (`traffic`), or changing it (`settings`). The half decides more than
 * the order — a settings page holds a form, so it must not poll under it.
 */
export type ItemPage = Section & { kind: 'traffic' | 'settings' }

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
 * An account's pages under /accounts/<id>, its overview being that path
 * itself. Its traffic first, then its settings — on the same level, not a
 * Settings page with sections under it: three levels of navigation for three
 * settings was a click and a Back more than they are worth.
 */
export const ACCOUNT_PAGES = [
  { id: 'usage', label: 'Usage', kind: 'traffic' },
  { id: 'chats', label: 'Chats', kind: 'traffic' },
  { id: 'requests', label: 'Requests', kind: 'traffic' },
  { id: 'models', label: 'Models', kind: 'settings' },
  { id: 'connection', label: 'Connection', kind: 'settings' },
  // Pausing and removing: the two ways out of use.
  { id: 'advanced', label: 'Advanced', kind: 'settings' },
] as const satisfies readonly ItemPage[]

/** An account's page, or '' for its overview — the only values its page dispatches on. */
export type AccountPageId = (typeof ACCOUNT_PAGES)[number]['id'] | ''

/**
 * A key's pages under /keys/<id>, the same way. Clients comes first of its
 * settings: handing the key to a client is what a key is for. The models it
 * may use and what its aliases serve are one page.
 */
export const KEY_PAGES = [
  { id: 'usage', label: 'Usage', kind: 'traffic' },
  { id: 'chats', label: 'Chats', kind: 'traffic' },
  { id: 'requests', label: 'Requests', kind: 'traffic' },
  { id: 'clients', label: 'Clients', kind: 'settings' },
  { id: 'models', label: 'Models', kind: 'settings' },
  { id: 'general', label: 'General', kind: 'settings' },
  { id: 'advanced', label: 'Advanced', kind: 'settings' },
] as const satisfies readonly ItemPage[]

/** A key's page, or '' for its overview. */
export type KeyPageId = (typeof KEY_PAGES)[number]['id'] | ''

/**
 * A path segment as one of a registry's page ids, or '' — the item's overview
 * — for anything else. canonical() has already completed the path, so '' here
 * is the overview and never a page that went missing.
 */
export function pageIdOf<P extends readonly Section[]>(pages: P, segment: string): P[number]['id'] | '' {
  return pages.find((p) => p.id === segment)?.id ?? ''
}

/** Whether a page is one of an item's settings pages. */
export function isSettingsPage(pages: readonly ItemPage[], id: string): boolean {
  return pages.some((p) => p.id === id && p.kind === 'settings')
}

/** The pages of one kind of item. */
export function pagesOf(kind: 'accounts' | 'keys'): readonly ItemPage[] {
  return kind === 'accounts' ? ACCOUNT_PAGES : KEY_PAGES
}

export const ACTIVITY_PAGES = [
  { id: 'chats', label: 'Chats' },
  { id: 'requests', label: 'Requests' },
] as const satisfies readonly Section[]
export type ActivityPageId = (typeof ACTIVITY_PAGES)[number]['id']

export const GATEWAY_SETTINGS = [
  { id: 'aliases', label: 'Aliases' },
  { id: 'apis', label: 'APIs' },
  { id: 'titles', label: 'Titles' },
  { id: 'images', label: 'Images' },
  { id: 'documentation', label: 'Documentation' },
  { id: 'configuration', label: 'Configuration' },
] as const satisfies readonly Section[]
/** A Settings section, or '' for /settings itself: the admin account. */
export type SettingsSectionId = (typeof GATEWAY_SETTINGS)[number]['id'] | ''

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
  keys: { limits: 'general', aliases: 'models', configs: 'clients' },
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

/** Where an account's or a key's own page lives. */
export const accountHref = (id: string) => pathOf('accounts', id)
export const keyHref = (id: string) => pathOf('keys', id)

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
      // Settings were a level of their own: /settings/<section> is the
      // section's page now, and /settings alone the first of them.
      const raw = b === 'settings' ? (c ?? 'models') : b
      const page = (raw !== undefined && RENAMED[top]?.[raw]) || raw
      // The item's overview is its own path; /overview under it, and any
      // page that is not one, land there.
      if (!known(pagesOf(top), page)) return pathOf(top, a)
      return pathOf(top, a, page!)
    }
    case 'activity':
      return pathOf('activity', known(ACTIVITY_PAGES, a) ? a! : ACTIVITY_PAGES[0].id)
    case 'settings': {
      // /settings itself is the admin account — what was its Advanced
      // section — and where any section that is not one lands.
      const section = (a !== undefined && RENAMED.settings[a]) || a
      return known(GATEWAY_SETTINGS, section) ? pathOf('settings', section!) : pathOf('settings')
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
 * however deep the page is — an account shows its pages, not the whole tree
 * above them. The level above is one Back away, and the
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
export function navLevel(pathname: string): NavLevel {
  const [top, id, page] = segments(pathname)
  if ((top === 'accounts' || top === 'keys') && id !== undefined) {
    const base = pathOf(top, id)
    const list = TABS.find((t) => t.id === top)!
    return {
      back: { href: pathOf(top), label: list.label },
      items: links(base, pagesOf(top)),
      // On the item's overview, no entry: the overview is the item itself.
      current: page === undefined ? base : `${base}/${page}`,
    }
  }
  if (top === 'activity') {
    return { back: HOME, items: links('/activity', ACTIVITY_PAGES), current: pathOf(top, id ?? '') }
  }
  if (top === 'settings') {
    return {
      back: HOME,
      items: links('/settings', GATEWAY_SETTINGS),
      current: id === undefined ? pathOf(top) : pathOf(top, id),
    }
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
  const [top, id, page] = segments(pathname)
  const out: NavLink[] = [ROOT_CRUMB]
  const tab = TABS.find((t) => t.id === tabOf(pathname))!
  if (tab.id === 'overview') return out
  const label = (list: readonly Section[], x: string | undefined) =>
    list.find((s) => s.id === x)?.label ?? x ?? ''
  if ((top === 'accounts' || top === 'keys') && id !== undefined) {
    const base = pathOf(top, id)
    out.push({ href: pathOf(top), label: tab.label })
    out.push({ href: base, label: name(top, id) })
    if (page !== undefined) out.push({ href: `${base}/${page}`, label: label(pagesOf(top), page) })
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
