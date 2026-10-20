import type { MouseEvent, ReactNode } from 'react'
import { navigate } from '../route'

/**
 * Links to another screen of this app, and the one rule for when a click on
 * one is ours to handle.
 *
 * Every in-app link is a real <a> with a real href, so middle-click, "open in
 * new tab" and the status bar all work; a plain click is routed in place
 * rather than reloading the app to change a path. There were three copies of
 * that click handler — the navigation's, AppLink's and the clickable rows' —
 * and they disagreed about which modifier keys meant "leave it to the
 * browser", so a shift-click opened a window from the rail and navigated in
 * place from a table.
 */

type Click = Pick<MouseEvent, 'button' | 'metaKey' | 'ctrlKey' | 'shiftKey' | 'altKey'>

/** A primary-button click with no modifier: the only click that is not the browser's to handle. */
export function isPlainLeftClick(e: Click): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey
}

/** Go to an in-app path, from its top. */
function go(href: string) {
  navigate(href)
  window.scrollTo(0, 0)
}

/** Follow an in-app link without a reload; leave modified clicks to the browser. */
export function follow(href: string) {
  return (e: MouseEvent) => {
    if (!isPlainLeftClick(e)) return
    e.preventDefault()
    go(href)
  }
}

/**
 * A click handler that makes a whole row open a page.
 *
 * The row's name stays a real link, which is what a keyboard, a screen reader
 * and "open in new tab" use; this only widens where a pointer can click. A
 * click on anything that is a control of its own — a button, a link, a field —
 * or one that ends a text selection is left alone.
 */
export function openRow(href: string) {
  return rowClick(() => go(href))
}

/**
 * The same rule for a row that does something other than open a page — a
 * table row that unfolds its detail: the row is the wide target, and a link
 * or button inside it keeps its own click instead of also toggling the row.
 */
export function rowClick(act: () => void) {
  return (e: MouseEvent) => {
    if (!isPlainLeftClick(e)) return
    if ((e.target as Element).closest('a, button, input, select, textarea, label')) return
    if ((window.getSelection()?.toString() ?? '') !== '') return
    act()
  }
}

/**
 * How a link inside data reads: dotted, so a figure that opens something is
 * told apart from one that does not without shouting about it.
 *
 * `cell` is for one in a table cell, which gets a hover layer and a little
 * padding so the target is more than the glyphs; `error` for a figure that is
 * a count of failures.
 */
export function appLinkClass({ cell = false, tone }: { cell?: boolean; tone?: 'error' } = {}): string {
  return `underline decoration-dotted underline-offset-2 ${
    cell ? 'state-layer rounded-md3-xs px-1' : ''
  } ${tone === 'error' ? 'text-error' : 'hover:text-primary'}`
}

/** A link to another screen of this app, inside data: a figure, a name in a table. */
export function AppLink({
  href,
  children,
  cell,
  tone,
  strong = false,
}: {
  href: string
  children: ReactNode
  cell?: boolean
  tone?: 'error'
  /** In the body colour, for a name that is the cell's whole content. */
  strong?: boolean
}) {
  return (
    <a
      href={href}
      onClick={follow(href)}
      className={`${appLinkClass({ cell, tone })} ${strong ? 'text-on-surface' : ''}`}
    >
      {children}
    </a>
  )
}

/**
 * A link inside a sentence: a banner's "Reconnect", a note's address.
 *
 * Underlined solid, unlike AppLink's dotted figures, because here the words are
 * the whole affordance. `href` starting with / is an in-app path and is
 * followed in place; anything else opens in a new tab. `primary` draws it in
 * the accent, for a link that is the point of the line it sits in.
 */
export function InlineLink({
  href,
  children,
  primary = false,
}: {
  href: string
  children: ReactNode
  primary?: boolean
}) {
  const internal = href.startsWith('/')
  return (
    <a
      href={href}
      {...(internal
        ? { onClick: follow(href) }
        : { target: '_blank', rel: 'noopener noreferrer' })}
      className={`underline underline-offset-2 ${primary ? 'font-medium text-primary' : ''}`}
    >
      {children}
    </a>
  )
}
