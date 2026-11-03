import { useCallback, useEffect, useLayoutEffect, useMemo, useReducer, useRef, useState, type ReactNode } from 'react'
import { offsetsOf, rowAt, visibleRange } from './virtual-range'

/** How far past the view rows are drawn, so a quick scroll meets drawn rows. */
const OVERSCAN = 600

/**
 * A long list in one scrolling box of its own height, drawing only the rows
 * in view: a chat of hundreds of requests, each of which can be a screenful
 * of transcript.
 *
 * Rows differ in height and the list cannot know a row's until it is drawn,
 * so each starts at `estimate` and is measured as it appears (a
 * ResizeObserver), and its real height is kept. Whatever is at the top of
 * the view stays there while rows above it are measured, grow or shrink —
 * the transcript switched on, a row's content arriving — so the reader is
 * never moved by the list settling. Scrolled to its end, it follows the end
 * as rows are added, as a log does.
 *
 * It opens at `focus` (an index) when given, else at its end: the latest is
 * what a chat or a log is opened for. onRange is told which rows are drawn,
 * for a list whose rows' content is fetched for what is on screen.
 */
export function VirtualList<T>({
  items,
  keyOf,
  render,
  estimate = 64,
  gap = 8,
  height = 'min(75vh, 56rem)',
  focus,
  label,
  onRange,
  fill = false,
}: {
  items: readonly T[]
  keyOf: (item: T) => string
  render: (item: T) => ReactNode
  /** A row's height before it is measured. */
  estimate?: number
  /** The space between rows. */
  gap?: number
  /** The box's height, as CSS. */
  height?: string
  /** The index to open at. */
  focus?: number
  /** What the list is, for a screen reader. */
  label: string
  onRange?: (start: number, end: number) => void
  /**
   * As tall as what is left of the window, so the page itself does not
   * scroll and the list is the one thing that does; `height` is then only
   * its first guess.
   */
  fill?: boolean
}) {
  const box = useRef<HTMLDivElement>(null)
  const sizes = useRef(new Map<string, number>())
  const [, remeasured] = useReducer((n: number) => n + 1, 0)
  const [view, setView] = useState({ top: 0, height: 0 })
  const [fitted, setFitted] = useState<number | null>(null)

  const keys = useMemo(() => items.map(keyOf), [items, keyOf])
  const index = useMemo(() => new Map(keys.map((k, i) => [k, i])), [keys])
  const offsets = offsetsOf(keys.map((k) => (sizes.current.get(k) ?? estimate) + gap))
  const total = Math.max(0, offsets[offsets.length - 1]! - gap)
  const [start, end] = visibleRange(offsets, view.top, view.height, OVERSCAN)

  // Where the reader is: the row at the view's top and how far into it, or
  // the end. Kept across renders, and put back after each one.
  const anchor = useRef<{ key: string; into: number } | null>(null)
  const following = useRef(false)
  const placed = useRef(false)
  const latest = useRef({ offsets, keys })
  latest.current = { offsets, keys }

  const note = useCallback(() => {
    const el = box.current
    if (el === null) return
    const { offsets: o, keys: k } = latest.current
    following.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 4
    const i = rowAt(o, el.scrollTop)
    anchor.current = k[i] === undefined ? null : { key: k[i]!, into: el.scrollTop - o[i]! }
    setView({ top: el.scrollTop, height: el.clientHeight })
  }, [])

  // Each drawn row measured as it is drawn and as it changes.
  const observer = useRef<ResizeObserver | null>(null)
  useEffect(() => {
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      let changed = false
      for (const e of entries) {
        const el = e.target as HTMLElement
        const key = el.dataset.key
        if (key === undefined) continue
        const h = e.borderBoxSize?.[0]?.blockSize ?? el.offsetHeight
        if (Math.abs((sizes.current.get(key) ?? -1) - h) > 0.5) {
          sizes.current.set(key, h)
          changed = true
        }
      }
      if (changed) remeasured()
    })
    observer.current = ro
    // The box itself, for its own height.
    const el = box.current
    const self = new ResizeObserver(() => note())
    if (el !== null) self.observe(el)
    return () => {
      ro.disconnect()
      self.disconnect()
      observer.current = null
    }
  }, [note])

  // The drawn rows, observed afresh each render: the set changes as the
  // view moves, and observing a row again costs nothing.
  useLayoutEffect(() => {
    const ro = observer.current
    const el = box.current
    if (ro === null || el === null) return
    ro.disconnect()
    for (const row of el.querySelectorAll<HTMLElement>('[data-key]')) ro.observe(row)
  })

  // Opened once there is something to open on: at the focus, or the end.
  useLayoutEffect(() => {
    if (placed.current || keys.length === 0 || box.current === null) return
    placed.current = true
    if (focus !== undefined && focus >= 0 && focus < keys.length) {
      anchor.current = { key: keys[focus]!, into: -16 }
      // The row asked for is the reason the page was opened: the list
      // itself in view too, not below whatever heads the page.
      box.current.scrollIntoView({ block: 'nearest' })
    } else {
      following.current = true
    }
  }, [keys, focus])

  // After every render: back to the end when following it, else the row the
  // reader was on back where it was.
  useLayoutEffect(() => {
    const el = box.current
    if (el === null || !placed.current) return
    let want = el.scrollTop
    if (following.current) {
      // The list's own end, not the box's scroll height: a row drawn taller
      // than its estimate is clipped until it is measured, so the two agree.
      want = total - el.clientHeight
    } else if (anchor.current !== null) {
      const i = index.get(anchor.current.key)
      if (i !== undefined) want = offsets[i]! + anchor.current.into
    }
    want = Math.max(0, Math.min(want, el.scrollHeight - el.clientHeight))
    if (Math.abs(want - el.scrollTop) > 1) el.scrollTop = want
    if (view.height === 0 || Math.abs(view.top - el.scrollTop) > 1) note()
  })

  useEffect(() => onRange?.(start, end), [start, end, onRange])

  // Filling the window: the box takes back whatever the page overflows by,
  // or grows into what it leaves, on opening and whenever the window or
  // the page around it changes size.
  useLayoutEffect(() => {
    if (!fill) return
    const fit = () => {
      const el = box.current
      if (el === null) return
      const over = document.documentElement.scrollHeight - window.innerHeight
      const h = Math.max(240, Math.floor(el.getBoundingClientRect().height - over))
      setFitted((was) => (was !== null && Math.abs(was - h) < 2 ? was : h))
    }
    fit()
    window.addEventListener('resize', fit)
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(fit)
    ro?.observe(document.body)
    return () => {
      window.removeEventListener('resize', fit)
      ro?.disconnect()
    }
  }, [fill])

  return (
    <div
      ref={box}
      onScroll={note}
      tabIndex={0}
      role="list"
      aria-label={label}
      className="relative overflow-y-auto overscroll-contain rounded-md3-l outline-none focus-visible:ring-2 focus-visible:ring-primary"
      style={{ height: fitted !== null ? `${String(fitted)}px` : height }}
    >
      {/* Clipped to the list's height: a row taller than its estimate must not
          spill past it before it is measured, or the box's scroll height would
          change with what is drawn, and following the end would chase it —
          drawing other rows, changing it again — without end. */}
      <div className="relative overflow-hidden" style={{ height: total }}>
        {items.slice(start, end).map((it, i) => {
          const key = keys[start + i]!
          return (
            <div
              key={key}
              data-key={key}
              role="listitem"
              className="absolute inset-x-0"
              style={{ top: offsets[start + i] }}
            >
              {render(it)}
            </div>
          )
        })}
      </div>
    </div>
  )
}
