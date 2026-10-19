import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react'
import { createPortal } from 'react-dom'
import { useEscapeKey } from '../hooks'

/**
 * Everything that opens over the page — a column's menu, a right-click menu, a
 * select's list — is one of these, so how a popup behaves is decided once.
 *
 * It was decided three times before, and each copy relearnt the same lesson
 * separately: a popup that closes on scroll closes when its own list is
 * scrolled, because a scroll listener on the window in the capture phase sees
 * the panel's scroll too. HeaderMenu fixed that by following its anchor
 * instead of closing; the Select written after it closed on scroll again, and
 * its list could not be scrolled. A fix in one copy is no fix — hence here.
 *
 * The rules, in one place:
 *
 *  - Portalled to the body in fixed coordinates, because a capped table or a
 *    card clips anything positioned inside it.
 *  - Anchored panels follow their anchor while the page scrolls, and close
 *    only when the anchor leaves the screen. Scrolling inside the panel moves
 *    no anchor, so it changes nothing.
 *  - Opened below the anchor, or above it when there is more room there, and
 *    never taller than the room: a list longer than that scrolls inside.
 *  - Scrolling over a panel stays in the panel (see useContainedScroll).
 *  - A press outside the panel and its anchor closes it, and so does Escape.
 */

/** The gap between an anchor and its panel, and a panel and the window edge. */
const GAP = 4
const MARGIN = 8

/** Where an anchored panel sits: below its anchor (top) or above it (bottom). */
export type Placement = {
  left: number
  top?: number
  bottom?: number
  width: number
  maxHeight: number
}

export type AnchoredPanelOptions = {
  /** The panel's width; at least the anchor's when matchAnchor is set. */
  width: number
  matchAnchor?: boolean
  /** The tallest the panel may be. Less when the window has less room. */
  maxHeight?: number
}

export type AnchoredPanel<A extends HTMLElement> = {
  anchor: RefObject<A>
  panel: RefObject<HTMLDivElement>
  /** Where the panel is; null while it is closed. */
  at: Placement | null
  open: boolean
  show: () => void
  close: () => void
  toggle: () => void
}

/** A panel opened from an element: a menu button, a select, a heading. */
export function useAnchoredPanel<A extends HTMLElement = HTMLButtonElement>({
  width,
  matchAnchor = false,
  maxHeight = 352,
}: AnchoredPanelOptions): AnchoredPanel<A> {
  const anchor = useRef<A>(null)
  const panel = useRef<HTMLDivElement>(null)
  const [at, setAt] = useState<Placement | null>(null)
  const open = at !== null
  const close = useCallback(() => setAt(null), [])

  const place = useCallback(() => {
    const r = anchor.current?.getBoundingClientRect()
    if (r === undefined) return
    // A menu belonging to something scrolled out of sight is a box floating
    // in the page.
    if (r.bottom < 0 || r.top > window.innerHeight) {
      setAt(null)
      return
    }
    const w = matchAnchor ? Math.max(r.width, width) : width
    const left = Math.max(MARGIN, Math.min(r.left, window.innerWidth - w - MARGIN))
    const below = window.innerHeight - r.bottom - GAP - MARGIN
    const above = r.top - GAP - MARGIN
    const up = below < Math.min(maxHeight, 200) && above > below
    const room = Math.max(96, Math.min(maxHeight, up ? above : below))
    const next: Placement = up
      ? { left, bottom: window.innerHeight - r.top + GAP, width: w, maxHeight: room }
      : { left, top: r.bottom + GAP, width: w, maxHeight: room }
    // The same place keeps the same object, so scrolling the panel's own
    // list — which moves no anchor — re-renders nothing.
    setAt((prev) =>
      prev !== null &&
      prev.left === next.left &&
      prev.top === next.top &&
      prev.bottom === next.bottom &&
      prev.width === next.width &&
      prev.maxHeight === next.maxHeight
        ? prev
        : next,
    )
  }, [width, matchAnchor, maxHeight])

  useEscapeKey(close, open)
  useDismiss(open, [anchor, panel], close)
  useContainedScroll(panel, open)

  // Follow the anchor. Capture, to see scrolling in every container between
  // the anchor and the document; throttled to a frame, because measuring on
  // every scroll event measures far more often than the screen is redrawn.
  useEffect(() => {
    if (!open) return
    let frame = 0
    const follow = () => {
      if (frame !== 0) return
      frame = requestAnimationFrame(() => {
        frame = 0
        place()
      })
    }
    window.addEventListener('scroll', follow, true)
    window.addEventListener('resize', follow)
    return () => {
      if (frame !== 0) cancelAnimationFrame(frame)
      window.removeEventListener('scroll', follow, true)
      window.removeEventListener('resize', follow)
    }
  }, [open, place])

  const toggle = useCallback(() => (open ? close() : place()), [open, close, place])
  return { anchor, panel, at, open, show: place, close, toggle }
}

/** A press outside every one of refs closes the popup. */
export function useDismiss(
  enabled: boolean,
  refs: RefObject<HTMLElement>[],
  onClose: () => void,
) {
  useEffect(() => {
    if (!enabled) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (refs.some((r) => r.current?.contains(t) === true)) return
      onClose()
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
    // refs are stable objects; their contents are read at the press.
  }, [enabled, onClose])
}

/**
 * Scrolling over a panel stays in the panel.
 *
 * overscroll-contain stops a scroll chaining to the page once the list
 * reaches its end, but only while the list has something to scroll: a short
 * one has no scroll of its own, so the wheel goes straight to the document
 * and the page slides away under a menu still being read. A wheel or a drag
 * over a panel that cannot scroll is swallowed. Non-passive, because
 * preventDefault on a passive listener does nothing.
 */
export function useContainedScroll(panel: RefObject<HTMLElement>, enabled: boolean) {
  useEffect(() => {
    if (!enabled) return
    const el = panel.current
    if (el === null) return
    const swallow = (e: Event) => {
      if (el.scrollHeight <= el.clientHeight) e.preventDefault()
    }
    el.addEventListener('wheel', swallow, { passive: false })
    el.addEventListener('touchmove', swallow, { passive: false })
    return () => {
      el.removeEventListener('wheel', swallow)
      el.removeEventListener('touchmove', swallow)
    }
  }, [panel, enabled])
}

/**
 * An MD3 menu surface: surface-container, elevation 2, a large rounded
 * corner, with its items inset so their own rounded state layers sit inside
 * it.
 */
export const menuSurface =
  'fixed z-50 overflow-y-auto overscroll-contain rounded-md3-l bg-surface-container p-1.5 text-sm normal-case elevation-2'

/** One row of a menu or a list: a rounded state layer, a 40px target. */
export function menuItem({ active = false, selected = false } = {}): string {
  const tone = selected
    ? 'bg-secondary-container text-on-secondary-container'
    : active
      ? 'bg-on-surface/8 text-on-surface'
      : 'text-on-surface'
  return `state-layer flex min-h-10 w-full cursor-pointer items-center gap-3 rounded-md3-m px-3 py-2 text-left text-sm font-normal tracking-normal ${tone}`
}

/** A line between groups of a menu's items. */
export function MenuDivider() {
  return <div role="separator" className="-mx-1.5 my-1.5 border-t border-outline-variant" />
}

/** A popup's surface, portalled, where a Placement or a point puts it. */
export function Popover({
  panelRef,
  at,
  role,
  label,
  id,
  className = '',
  children,
}: {
  panelRef: RefObject<HTMLDivElement>
  at: Placement
  role: 'menu' | 'listbox' | 'dialog' | 'tooltip'
  label: string
  id?: string
  className?: string
  children: ReactNode
}) {
  return createPortal(
    <div
      ref={panelRef}
      id={id}
      role={role}
      aria-label={label}
      style={{ left: at.left, top: at.top, bottom: at.bottom, width: at.width, maxHeight: at.maxHeight }}
      className={`${menuSurface} ${className}`}
    >
      {children}
    </div>,
    document.body,
  )
}
