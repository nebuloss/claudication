/**
 * The admin UI's page tree, and reading a location against it.
 *
 * Every page has a path of its own, and the path is the tree:
 *
 *   /                                   the gateway's overview
 *   /accounts                           the accounts
 *   /accounts/<id>                      one account's overview
 *   /accounts/<id>/<page>               its traffic and its settings, side by side
 *   /clients … the same shape, for clients
 *   /activity                           the gateway's load: the last hour, and
 *                                       its requests by outcome over days
 *   /activity/<page>                    its traffic by model, by client, by
 *                                       account, and the request log
 *   /settings/<page>                    the gateway's own settings: General
 *                                       (the gateway switch, the admin
 *                                       account), Models, MCP, Configuration
 *
 * So a link, a bookmark and Back all land on a page, never on "whatever
 * sub-tab was open last". Each level's overview is the level itself — the
 * root, an account's own path, /activity — rather than a page named Overview
 * under it, so the trail never says Overview twice over.
 *
 * Pure: string in, string out, no window. Kept apart from the hooks because
 * this is the part that is quietly wrong — a trailing slash from a copied
 * link, an encoded id — and the part worth reading without a DOM.
 */

/** A page in a list of them: its path segment and how the nav names it. */
export type Section = { id: string; label: string }

/**
 * An account's or a client's page, and which half of its pages it is in: what it
 * did (`traffic`), or changing it (`settings`). The half decides more than
 * the order — a settings page holds a form, so it must not poll under it.
 */
export type ItemPage = Section & { kind: 'traffic' | 'settings' }

/** The top-level screens, in the order the app bar lists them. */
export const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'accounts', label: 'Accounts' },
  { id: 'clients', label: 'Clients' },
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
  // Which clients drew on it. No chats: a chat is a client's.
  { id: 'usage', label: 'Usage', kind: 'traffic' },
  { id: 'requests', label: 'Requests', kind: 'traffic' },
  { id: 'models', label: 'Models', kind: 'settings' },
  { id: 'connection', label: 'Connection', kind: 'settings' },
  // Pausing and removing: the two ways out of use.
  { id: 'advanced', label: 'Advanced', kind: 'settings' },
] as const satisfies readonly ItemPage[]

/** An account's page, or '' for its overview — the only values its page dispatches on. */
export type AccountPageId = (typeof ACCOUNT_PAGES)[number]['id'] | ''

/**
 * A client's pages under /clients/<id>, its overview being that path itself:
 * its name, type, switches, key and setup, and a line of what its model names
 * serve. The pages beside it: its usage with its limits beside the figures
 * they bound, its chats, its requests, and its Models — every client's, since
 * each has names to bind and models it may be allowed — where the names are
 * edited and the models it may be served are switched.
 */
export const CLIENT_PAGES = [
  { id: 'usage', label: 'Usage', kind: 'traffic' },
  // Right after Usage: what a client's names serve is what the operator
  // changes most, and it sat last, behind the client's history.
  { id: 'models', label: 'Models', kind: 'settings' },
  { id: 'chats', label: 'Chats', kind: 'traffic' },
  { id: 'requests', label: 'Requests', kind: 'traffic' },
] as const satisfies readonly ItemPage[]

/** A client's page, or '' for its overview. */
export type ClientPageId = (typeof CLIENT_PAGES)[number]['id'] | ''

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
export function pagesOf(kind: 'accounts' | 'clients'): readonly ItemPage[] {
  return kind === 'accounts' ? ACCOUNT_PAGES : CLIENT_PAGES
}

/**
 * The gateway-wide traffic, a page per question, each with its traffic over
 * time drawn above its table: by model, by client, by account — each
 * unfolding to the other side — and the request log, for debugging, under
 * the last hour live and the outcomes over days, filterable by client,
 * provider, account and outcome. Chats are a client's and are on its page.
 *
 * Activity's own path is its overview, as an account's is: the load as it
 * happens. These are the pages under it, each a breakdown of that load.
 */
export const ACTIVITY_PAGES = [
  { id: 'models', label: 'Models' },
  { id: 'clients', label: 'Clients' },
  { id: 'accounts', label: 'Accounts' },
  { id: 'requests', label: 'Requests' },
] as const satisfies readonly Section[]
export type ActivityPageId = (typeof ACTIVITY_PAGES)[number]['id']

/**
 * The gateway's own settings, a page each, General first and where
 * /settings opens. They were one page while all that was left was a switch,
 * an account and a table; with the models' modes and the MCP server's switch
 * beside them it was a scroll past the configuration to find either, and the
 * owner asked for the navigation back (2026-10-22). Configuration last: it
 * is long, read-only, and read least.
 */
// Settings' own path is its general view — the gateway's switch, the MCP
// server's, the admin account — as Activity's is its load: a page named
// General under it was the trail saying Settings twice.
export const SETTINGS_PAGES = [
  { id: 'models', label: 'Models' },
  { id: 'configuration', label: 'Configuration' },
] as const satisfies readonly Section[]
export type SettingsPageId = (typeof SETTINGS_PAGES)[number]['id']

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

/** Where an account's or a client's own page lives. */
export const accountHref = (id: string) => pathOf('accounts', id)
export const clientHref = (id: string) => pathOf('clients', id)

const known = (list: readonly Section[], id: string | undefined) =>
  id !== undefined && list.some((s) => s.id === id)

/**
 * The page a path means, as its canonical path: itself when it already names
 * a page, completed or moved when it does not. Every unknown part falls back
 * to the first page of its branch rather than to a blank screen — a URL is
 * something people type, edit and truncate. Paths from before a screen moved
 * are not kept: a bookmark of one lands on the overview.
 */
export function canonical(pathname: string): string {
  const [top, a, b] = segments(pathname)
  if (top === undefined) return '/'
  switch (top) {
    case 'overview':
      return '/'
    case 'accounts':
    case 'clients': {
      if (a === undefined) return pathOf(top)
      // The item's overview is its own path; /overview under it, and any
      // page that is not one, land there.
      if (!known(pagesOf(top), b)) return pathOf(top, a)
      return pathOf(top, a, b!)
    }
    // Its overview is its own path; a page it does not have lands there.
    case 'activity':
      return known(ACTIVITY_PAGES, a) ? pathOf('activity', a!) : pathOf('activity')
    // A page each; /settings itself, and any page it does not have, open
    // on General.
    case 'settings':
      return known(SETTINGS_PAGES, a) ? pathOf('settings', a!) : pathOf('settings')
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

/** Names an account or a client by id, for the trail and the Back button. */
export type Namer = (kind: 'accounts' | 'clients', id: string) => string

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
  if ((top === 'accounts' || top === 'clients') && id !== undefined) {
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
    // On its overview, no entry: the overview is Activity itself.
    return {
      back: HOME,
      items: links('/activity', ACTIVITY_PAGES),
      current: id === undefined ? pathOf(top) : pathOf(top, id),
    }
  }
  if (top === 'settings') {
    return {
      back: HOME,
      items: links('/settings', SETTINGS_PAGES),
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
  if ((top === 'accounts' || top === 'clients') && id !== undefined) {
    const base = pathOf(top, id)
    out.push({ href: pathOf(top), label: tab.label })
    out.push({ href: base, label: name(top, id) })
    if (page !== undefined) out.push({ href: `${base}/${page}`, label: label(pagesOf(top), page) })
    return out
  }
  out.push({ href: canonical(pathOf(tab.id)), label: tab.label })
  if (top === 'activity' && id !== undefined) {
    out.push({ href: pathOf(top, id), label: label(ACTIVITY_PAGES, id) })
  }
  if (top === 'settings' && id !== undefined) {
    out.push({ href: pathOf(top, id), label: label(SETTINGS_PAGES, id) })
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
