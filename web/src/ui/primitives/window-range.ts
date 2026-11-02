/**
 * The arithmetic of a window over a list (infinite.tsx, Windowed): which
 * rows a list opens on, and where the window goes when the list changes.
 * Pure, so `make test-web` checks it without a DOM.
 */

export type Range = readonly [start: number, end: number]

/** The window a list opens on: `page` rows around the focus, or its last page. */
export function openWindow(total: number, page: number, focus?: number): Range {
  if (focus !== undefined && focus >= 0 && focus < total) {
    const start = Math.max(0, Math.min(focus - Math.floor(page / 2), total - page))
    return [start, Math.min(total, start + page)]
  }
  return [Math.max(0, total - page), total]
}

/**
 * The window after the list went from `before` rows to `total`: a window
 * that reached the end follows it, one that did not stays where it was, and
 * either is kept inside the list. A list that was empty opens afresh.
 */
export function followWindow(range: Range, before: number, total: number, page: number, focus?: number): Range {
  if (before === 0) return openWindow(total, page, focus)
  const end = range[1] >= before ? total : Math.min(range[1], total)
  return [Math.min(range[0], Math.max(0, end - 1)), end]
}

/** The window widened by a page at its start, or at its end. */
export function widen(range: Range, total: number, page: number, edge: 'start' | 'end'): Range {
  return edge === 'start'
    ? [Math.max(0, range[0] - page), range[1]]
    : [range[0], Math.min(total, range[1] + page)]
}
