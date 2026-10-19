/** Counts, sizes and shares, as the screens print them. */

/** Thousands separators and a k/M suffix, so a token count stays readable. */
export function compact(n: number): string {
  // A billion tokens is an ordinary week here — cache reads are counted, and
  // they dwarf everything else. Without this tier the biggest number on the
  // screen read "1936M", which is four digits and a unit that has run out: the
  // eye has to divide by a thousand to learn it is about two billion.
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(n >= 10_000_000_000 ? 0 : 1)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`
  // One decimal below ten of a unit and none above it, everywhere: 1.9B, 19B,
  // 1.9M, 19M, 1.9k, 19k. Three significant figures is more than a chart label
  // can be read to anyway, and the exact value is a hover away.
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`
  return n.toLocaleString()
}

/** Bytes as MiB, the unit memory limits are set in. */
export function mib(bytes: number): string {
  const v = bytes / (1 << 20)
  return v >= 100 ? `${Math.round(v)} MiB` : `${v.toFixed(1)} MiB`
}

/**
 * A percentage that is often tiny: a key's part of the subscription. Under a
 * tenth it says so, rather than rounding a real share down to 0%.
 */
export function smallPercent(v: number): string {
  if (v > 0 && v < 0.1) return '<0.1%'
  return `${v < 10 ? v.toFixed(1) : Math.round(v)}%`
}
