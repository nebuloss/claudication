import { type ReactNode } from 'react'
import { MAGNITUDE, Palette, type Paint } from './paint'

/** One row of a ranked list: a name, how much of it, and what to print. */
export type Rank = {
  label: string
  value: number
  /** What to show at the right-hand end. Defaults to the value. */
  right?: ReactNode
}

/**
 * A ranked list of horizontal bars — which model, which key, which account.
 *
 * Deliberately not a charting abstraction: one CSS width is the whole
 * requirement, and everything a library would add here is something this does
 * not need. What it does need, and what got repeated by hand before, is the
 * rule about colour: `identity` decides whether a bar's colour means *which
 * thing* or merely *how much*, and colouring a time series by position says
 * something untrue.
 */
export function RankedBars({
  rows,
  identity = true,
  format,
}: {
  rows: Rank[]
  /** False where a bar means magnitude over time rather than which thing. */
  identity?: boolean
  format: (value: number) => string
}) {
  const max = rows.reduce((m, r) => Math.max(m, r.value), 0)
  const palette = identity ? new Palette(rows.map((r) => r.label)) : null

  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((row) => (
        <RankedBar
          key={row.label}
          row={row}
          max={max}
          paint={palette?.paint(row.label) ?? MAGNITUDE}
          format={format}
        />
      ))}
    </div>
  )
}

function RankedBar({
  row,
  max,
  paint,
  format,
}: {
  row: Rank
  max: number
  paint: Paint
  format: (value: number) => string
}) {
  // Never zero-width where there is something to show: a bar you cannot see is
  // indistinguishable from a row that should not be there.
  const pct = max > 0 ? Math.max(row.value > 0 ? 2 : 0, Math.round((row.value / max) * 100)) : 0

  return (
    <div className="flex items-center gap-3 text-sm">
      {/* The label is not decoration: four of the light-mode fills sit under
          3:1 on the card surface, and a visible label is what makes that
          legible rather than colour-alone. */}
      <div className="w-40 shrink-0 truncate text-on-surface" title={row.label}>
        {row.label}
      </div>
      <div className="h-5 min-w-0 flex-1 rounded-[var(--radius-md3-s)] bg-surface-high">
        <div
          className="h-full rounded-[var(--radius-md3-s)]"
          style={{ width: `${pct}%`, ...paint.swatch() }}
        />
      </div>
      <div className="w-28 shrink-0 text-right tabular-nums text-on-surface">
        {row.right ?? format(row.value)}
      </div>
    </div>
  )
}

/**
 * One stacked bar: the shares of a whole, rather than a list of magnitudes.
 *
 * The question a composition answers is "what share", and a row of independent
 * bars answers a different one — which is why this exists alongside
 * RankedBars rather than as an option on it.
 */
export function CompositionBar({
  rows,
  limit = 6,
  format,
  more,
}: {
  rows: Rank[]
  /** Rows past this are grouped, so the key stays readable. */
  limit?: number
  format: (value: number) => string
  /** How to label the grouped remainder. */
  more?: (count: number) => string
}) {
  const ranked = rows.filter((r) => r.value > 0).sort((a, b) => b.value - a.value)
  const total = ranked.reduce((n, r) => n + r.value, 0)
  if (total === 0) return null

  const head = ranked.slice(0, limit)
  const rest = ranked.slice(limit)
  if (rest.length > 0) {
    head.push({
      label: (more ?? ((n) => `${n} more`))(rest.length),
      value: rest.reduce((n, r) => n + r.value, 0),
    })
  }

  // Colour by rank here rather than by name: the parts of one bar have to be
  // told apart from each other, and the legend sits immediately beneath.
  const palette = new Palette(head.map((r) => r.label))

  return (
    <div>
      <div className="flex h-7 w-full overflow-hidden rounded-[var(--radius-md3-s)]">
        {head.map((row) => (
          <div
            key={row.label}
            className="h-full"
            style={{ width: `${(row.value / total) * 100}%`, ...palette.paint(row.label).swatch() }}
            title={`${row.label}: ${format(row.value)}`}
          />
        ))}
      </div>
      <div className="mt-3 grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
        {head.map((row) => (
          <div key={row.label} className="flex items-center gap-2 text-xs">
            <span
              className="inline-block size-2.5 shrink-0 rounded-[2px]"
              style={palette.paint(row.label).swatch()}
            />
            <span className="min-w-0 flex-1 truncate font-mono text-on-surface" title={row.label}>
              {row.label}
            </span>
            <span className="shrink-0 tabular-nums text-on-surface-variant">
              {format(row.value)}
            </span>
            <span className="w-10 shrink-0 text-right tabular-nums text-on-surface-variant">
              {Math.round((row.value / total) * 100)}%
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
