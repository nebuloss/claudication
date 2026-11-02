/**
 * The arithmetic of a virtual list (virtual.tsx): where each row sits, and
 * which rows a scroll position shows. Pure, so `make test-web` checks it
 * without a DOM.
 */

/**
 * Where each row starts, from each row's size: n + 1 numbers, the last
 * being the whole list's height.
 */
export function offsetsOf(sizes: readonly number[]): number[] {
  const out = new Array<number>(sizes.length + 1)
  out[0] = 0
  for (let i = 0; i < sizes.length; i++) out[i + 1] = out[i]! + sizes[i]!
  return out
}

/**
 * The rows [start, end) any part of which is within `overscan` of the view
 * — `height` pixels from `top` — so a row is drawn just before it scrolls in.
 */
export function visibleRange(
  offsets: readonly number[],
  top: number,
  height: number,
  overscan: number,
): [start: number, end: number] {
  const n = offsets.length - 1
  if (n <= 0) return [0, 0]
  const from = top - overscan
  const to = top + height + overscan
  // The first row ending past `from`.
  let lo = 0
  let hi = n
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (offsets[mid + 1]! > from) hi = mid
    else lo = mid + 1
  }
  const start = lo
  // The first row starting at or past `to`.
  lo = start
  hi = n
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (offsets[mid]! >= to) hi = mid
    else lo = mid + 1
  }
  return [start, Math.max(start, lo)]
}

/** The row a scroll position falls in: the one under the view's top edge. */
export function rowAt(offsets: readonly number[], top: number): number {
  const n = offsets.length - 1
  let lo = 0
  let hi = Math.max(0, n - 1)
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (offsets[mid]! <= top) lo = mid
    else hi = mid - 1
  }
  return lo
}
