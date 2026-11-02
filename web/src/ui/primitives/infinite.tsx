import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Button } from './button'
import { followWindow, openWindow, widen, type Range } from './window-range'

/**
 * Lists that would make a very long page: a chat of hundreds of requests, a
 * log paged on and on. Two pieces, one rule — only what is near the screen is
 * drawn or fetched, and more comes as the reader scrolls to an edge.
 *
 *   Sentinel  calls back while it is within reach of being seen: the end of a
 *             list that pages from the server (the request log).
 *   Windowed  draws a window of a list already in hand and widens it at
 *             either edge (a chat's requests), keeping what is on screen
 *             where it was when rows are added above it.
 *
 * Each edge also has a button: a sentinel is a convenience for a pointer, and
 * a keyboard or a screen reader needs something to press.
 */

/**
 * Calls onReach while it is within `margin` of the viewport. `version` is
 * what changes when a reach was answered — the count loaded — so a sentinel
 * still in view after a page arrived asks again, rather than waiting for a
 * scroll that may never come. Clipped by any scrolling box it is in, as the
 * viewport sees it, so one inside a capped table fires at that table's end.
 */
export function Sentinel({
  onReach,
  version,
  disabled = false,
  margin = '400px',
  row = false,
}: {
  onReach: () => void
  version: number
  disabled?: boolean
  margin?: string
  /** A table row, for the end of a table's body. */
  row?: boolean
}) {
  const ref = useRef<HTMLElement | null>(null)
  const reach = useRef(onReach)
  reach.current = onReach
  useEffect(() => {
    const el = ref.current
    if (disabled || el === null || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) reach.current()
    }, { rootMargin: margin })
    io.observe(el)
    return () => io.disconnect()
  }, [disabled, margin, version])
  if (row) {
    return (
      <tr ref={(el) => void (ref.current = el)} aria-hidden>
        <td colSpan={99} className="h-px p-0" />
      </tr>
    )
  }
  return <div ref={(el) => void (ref.current = el)} aria-hidden className="h-px" />
}

/** The nearest box that scrolls, or the document. */
function scroller(el: HTMLElement): HTMLElement {
  for (let p = el.parentElement; p !== null; p = p.parentElement) {
    const y = getComputedStyle(p).overflowY
    if ((y === 'auto' || y === 'scroll') && p.scrollHeight > p.clientHeight) return p
  }
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement
}

/**
 * A window of `items`, `page` at a time, widened at either edge as the
 * reader nears it. It opens around `focus` (an index) when given, else on
 * the last page — the latest, which is what a log or a chat is opened for —
 * and follows the end as items are added there while it is showing it.
 *
 * Rows added above keep the screen where it was: the first row drawn is
 * measured before and moved back after, so nothing the reader is looking at
 * jumps. The top edge only loads after the reader has moved — at open, the
 * top of a page is in view by nature, and loading on that would draw the
 * whole list upwards before anyone asked.
 *
 * onRange is told the window, for a list whose rows' content is fetched per
 * window: a chat's transcript.
 */
export function Windowed<T>({
  items,
  keyOf,
  render,
  page = 20,
  focus,
  noun = 'items',
  onRange,
}: {
  items: readonly T[]
  keyOf: (item: T) => string
  render: (item: T) => ReactNode
  page?: number
  /** The index to open around, if one was asked for. */
  focus?: number
  /** What the rows are, for the buttons: "requests". */
  noun?: string
  onRange?: (start: number, end: number) => void
}) {
  const total = items.length
  const [range, setRange] = useState<Range>(() => openWindow(total, page, focus))
  const [moved, setMoved] = useState(false)
  const anchor = useRef<{ key: string; top: number } | null>(null)
  const box = useRef<HTMLDivElement>(null)

  // The list grew or shrank: keep following the end if the window was at it,
  // and keep the window inside the list.
  const seen = useRef(total)
  useEffect(() => {
    const before = seen.current
    seen.current = total
    if (before === total) return
    setRange((r) => followWindow(r, before, total, page, focus))
  }, [total, page, focus])

  useEffect(() => onRange?.(range[0], range[1]), [range, onRange])

  // Moving, by any means a person moves a page.
  useEffect(() => {
    if (moved) return
    const arm = () => setMoved(true)
    const events = ['wheel', 'touchmove', 'keydown'] as const
    for (const e of events) window.addEventListener(e, arm, { passive: true, once: true })
    return () => {
      for (const e of events) window.removeEventListener(e, arm)
    }
  }, [moved])

  const earlier = () => {
    const [s, e] = range
    if (s === 0) return
    const first = items[s]
    const el = first === undefined ? null : box.current?.querySelector<HTMLElement>(`[data-window-key="${CSS.escape(keyOf(first))}"]`)
    if (el != null) anchor.current = { key: keyOf(first!), top: el.getBoundingClientRect().top }
    setRange(widen([s, e], total, page, 'start'))
  }
  const later = () => setRange((r) => widen(r, total, page, 'end'))

  useLayoutEffect(() => {
    const a = anchor.current
    anchor.current = null
    if (a === null) return
    const el = box.current?.querySelector<HTMLElement>(`[data-window-key="${CSS.escape(a.key)}"]`)
    if (el == null) return
    const delta = el.getBoundingClientRect().top - a.top
    if (delta !== 0) scroller(el).scrollTop += delta
  }, [range])

  const [start, end] = range
  return (
    <div ref={box} className="space-y-2">
      {start > 0 && (
        <div className="flex items-center gap-3">
          <Sentinel onReach={earlier} version={start} disabled={!moved} />
          <Button variant="text" size="sm" onClick={earlier}>
            {`Show ${String(Math.min(page, start))} earlier`}
          </Button>
          <span className="text-xs text-on-surface-variant">
            {String(start)} {noun} before these
          </span>
        </div>
      )}
      {items.slice(start, end).map((it) => (
        <div key={keyOf(it)} data-window-key={keyOf(it)}>
          {render(it)}
        </div>
      ))}
      {end < total && (
        <div className="flex items-center gap-3">
          <Sentinel onReach={later} version={end} />
          <Button variant="text" size="sm" onClick={later}>
            {`Show ${String(Math.min(page, total - end))} later`}
          </Button>
          <span className="text-xs text-on-surface-variant">
            {String(total - end)} {noun} after these
          </span>
        </div>
      )}
    </div>
  )
}
