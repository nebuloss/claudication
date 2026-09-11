import { type Paint } from './paint'

/** One quantity read off a datum, and how to paint it. */
export type Series<T> = {
  key: string
  label: string
  paint: Paint
  value: (datum: T) => number
}

/**
 * Data and series, with the sums a stacked chart needs computed once.
 *
 * Stacked rather than grouped is the shape these charts want, in every metric,
 * because every series is a part of its column's whole: failures are a share
 * of the requests made, and input, output and cache are a share of the tokens
 * spent. Side by side would show a hundred requests beside five failures as a
 * tall bar and a short one, when the fact worth seeing is that five of the
 * hundred failed.
 *
 * A class because the totals get asked for repeatedly — the peak sets the
 * axis, the sum sets the headline, each column needs its own — and computing
 * them where they are used means walking the data four times and, eventually,
 * one of those walks disagreeing with the others.
 */
export class Stack<T> {
  /** One total per datum, in order. */
  readonly totals: readonly number[]
  /** The largest column, which is what the value axis has to cover. */
  readonly peak: number
  /** Every datum, every series, added up. */
  readonly sum: number

  constructor(
    readonly data: readonly T[],
    readonly series: readonly Series<T>[],
  ) {
    this.totals = data.map((d) => series.reduce((n, s) => n + Math.max(0, s.value(d)), 0))
    this.peak = this.totals.reduce((n, v) => Math.max(n, v), 0)
    this.sum = this.totals.reduce((n, v) => n + v, 0)
  }

  get length(): number {
    return this.data.length
  }

  /** The total of one datum. */
  total(i: number): number {
    return this.totals[i] ?? 0
  }

  /**
   * The parts of one column, bottom-up, skipping the empty ones.
   *
   * Skipping matters: a zero-height rect is invisible but still a node, and a
   * thirty-day chart of three series is ninety of them for nothing.
   */
  segments(i: number): { series: Series<T>; value: number }[] {
    const datum = this.data[i]
    if (datum === undefined) return []
    const out: { series: Series<T>; value: number }[] = []
    for (const series of this.series) {
      const value = Math.max(0, series.value(datum))
      if (value > 0) out.push({ series, value })
    }
    return out
  }

  /** One series added up across every datum, for a legend or a caption. */
  seriesTotal(series: Series<T>): number {
    return this.data.reduce((n, d) => n + Math.max(0, series.value(d)), 0)
  }

  /** Every paint in play, for the `<defs>` the chart has to emit. */
  paints(): Paint[] {
    return this.series.map((s) => s.paint)
  }
}
