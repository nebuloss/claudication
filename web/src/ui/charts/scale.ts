/**
 * Scales: the arithmetic between a value and a position.
 *
 * Classes rather than functions because a scale is a thing with a domain, a
 * range and several questions you ask of it — where does this value sit, how
 * tall is it, what are the gridlines — and threading those through as
 * arguments every time is how the arithmetic ends up duplicated and then
 * disagreeing with itself.
 *
 * The React components stay functions. Geometry has state worth holding;
 * rendering does not.
 */

/**
 * Gridlines on round numbers.
 *
 * A chart whose top line reads 1,183,402 is a chart nobody reads a value off.
 * This walks 1, 2, 5 × 10^n until `count` or fewer steps cover the data, which
 * is the smallest set that always lands on a number worth printing.
 */
export function niceTicks(max: number, count = 4): number[] {
  if (!Number.isFinite(max) || max <= 0) return [0]
  const rough = max / count
  const magnitude = Math.pow(10, Math.floor(Math.log10(rough)))
  const step =
    [1, 2, 5, 10].map((m) => m * magnitude).find((s) => max / s <= count) ?? magnitude * 10

  // Round the top UP to a whole step. Walking to `max` instead leaves an axis
  // whose last tick is below the data — 1,183,402 over a 500,000 step stopped
  // at 1,000,000 — and a column taller than the axis is drawn taller than the
  // plot, out through the top of the chart. The epsilon keeps a max that
  // already lands on a tick from gaining a spare one to floating-point drift.
  const top = Math.ceil(max / step - 1e-9) * step

  const out: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) out.push(v)
  return out
}

/**
 * A value axis, from zero to a rounded top.
 *
 * `y0` is the pixel of zero and `y1` the pixel of the top, which in SVG means
 * y0 > y1. Keeping both as pixels rather than a direction flag means the
 * caller never has to remember which way the axis points.
 */
export class LinearScale {
  /** The gridlines, in value space. The last is the top of the axis. */
  readonly ticks: readonly number[]
  /** The value at y1 — the data maximum, rounded up to a tick. */
  readonly max: number

  constructor(
    dataMax: number,
    private readonly y0: number,
    private readonly y1: number,
    tickCount = 4,
  ) {
    this.ticks = niceTicks(dataMax, tickCount)
    // Never zero: a chart of nothing still has to divide by something.
    this.max = this.ticks[this.ticks.length - 1] || 1
  }

  /** Where a value sits. */
  y(value: number): number {
    return this.y0 + (this.y1 - this.y0) * (value / this.max)
  }

  /** How tall a value is, which is not the same question as where it sits. */
  height(value: number): number {
    return Math.abs(this.y0 - this.y1) * (value / this.max)
  }
}

/**
 * A categorical axis: n equal slots across a span.
 *
 * Slots and bars are different things. The slot is the whole share of the axis
 * a datum owns and is what a pointer should hit — the thin column of a quiet
 * day is otherwise almost impossible to point at. The bar is what gets drawn,
 * narrower and centred inside it.
 */
export class BandScale {
  readonly step: number
  readonly width: number

  constructor(
    readonly count: number,
    private readonly from: number,
    to: number,
    { maxWidth = Infinity, gap = 3 }: { maxWidth?: number; gap?: number } = {},
  ) {
    this.step = (to - from) / Math.max(1, count)
    this.width = Math.max(1, Math.min(this.step - gap, maxWidth))
  }

  /** The whole slot, for hit testing. */
  slot(i: number): { x: number; width: number } {
    return { x: this.from + i * this.step, width: this.step }
  }

  /** The drawn bar, centred in its slot. */
  bar(i: number): { x: number; width: number } {
    return { x: this.from + i * this.step + (this.step - this.width) / 2, width: this.width }
  }

  /**
   * Which indices get a label, so they do not collide at a phone's width.
   * Always includes the first, so the axis has a left-hand anchor.
   */
  labelled(most = 6): (i: number) => boolean {
    const every = Math.max(1, Math.ceil(this.count / most))
    return (i) => i % every === 0
  }
}
