/**
 * Reading a location, as arithmetic.
 *
 * The screen is in the path and the panel within it is in the hash, so
 * `/usage#chats` is a link straight to one sub-tab. These are the pure half of
 * that: string in, choice out, no `window`. Kept apart from the hooks because
 * this is the part that is quietly wrong — a stray leading slash, a trailing
 * one from a copied link, an encoded hash — and the part worth testing without
 * standing up a DOM to do it.
 *
 * Every unknown value falls back rather than erroring. A URL is something
 * people type, edit and truncate, and a bookmark to a tab that has since been
 * renamed should land somewhere sensible instead of on a blank screen.
 */

/** The screen named by the first path segment. */
export function tabFromPath<T extends string>(
  pathname: string,
  tabs: readonly T[],
  fallback: T,
): T {
  const first = pathname.replace(/^\/+/, '').split('/')[0]
  return (tabs as readonly string[]).includes(first) ? (first as T) : fallback
}

/**
 * The path for a screen.
 *
 * The default screen lives at `/`, not `/overview`. Two URLs for one page is
 * how a bookmark and a nav link end up disagreeing about which tab is current,
 * and the root is the one people already have.
 */
export function pathForTab<T extends string>(tab: T, fallback: T): string {
  return tab === fallback ? '/' : `/${tab}`
}

/**
 * The panel named by the hash.
 *
 * Decoded first: a hash that has been through a mail client or a chat window
 * comes back percent-encoded, and `%23chats` should still open Chats. Split on
 * `&` because a hash can carry more than one thing and the panel is the first.
 */
export function panelFromHash<T extends string>(
  hash: string,
  panels: readonly T[],
  fallback: T,
): T {
  let raw = hash.replace(/^#/, '')
  try {
    raw = decodeURIComponent(raw)
  } catch {
    // A malformed escape is not a panel name; fall through with the raw text
    // rather than throwing inside a render.
  }
  // Again after decoding: a link that has been through something which encoded
  // its `#` arrives as `%23chats`, which unescapes to a `#` still attached to
  // the name.
  const first = raw.replace(/^#/, '').split('&')[0]
  return (panels as readonly string[]).includes(first) ? (first as T) : fallback
}
