import { type ReactNode } from 'react'
import { Line, type Paint } from './paint'
import { LinearScale, LogScale } from './scale'

/** One line: a name, how to paint it, and its value on a given column. */
export type LineSeries<T> = {
  key: string
  label: string
  paint: Paint
  at: (datum: T) => number
  /** Set on a series that is a state rather than an identity — the failures
   *  line — so the tooltip can rule it off from the rest. */
  aside?: boolean
}

/**
 * A line per series over time.
 *
 * Lines rather than columns because these charts carry a dozen series at once,
 * and a bar per series per day is three pixels wide across a month. A line
 * does not care how many columns there are.
 *
 * The internal coordinate system is fixed and the SVG scales to whatever width
 * it is given, so one chart works in a wide card and on a phone without a
 * measurement pass or a resize observer.
 */
const W = 720
const PAD = { top: 14, right: 10, bottom: 26, left: 56 }

export function LineChart<T>({
  data,
  series,
  label,
  format,
  log = true,
  height = 240,
  hovered = null,
  onHover,
}: {
  data: readonly T[]
  series: readonly LineSeries<T>[]
  /** The x-axis label for a column. */
  label: (datum: T) => string
  format: (value: number) => string
  /** Logarithmic by default: see LogScale for why these series need it. */
  log?: boolean
  height?: number
  hovered?: number | null
  onHover?: (i: number | null) => void
}) {
  const plotH = height - PAD.top - PAD.bottom
  const plotW = W - PAD.left - PAD.right

  const values = series.flatMap((s) => data.map((d) => s.at(d)))
  const scale = log
    ? new LogScale(values, PAD.top + plotH, PAD.top)
    : new LinearScale(Math.max(...values, 0), PAD.top + plotH, PAD.top)

  // A point scale, not a band: a line's vertices sit ON the columns, and the
  // first and last touch the edges of the plot rather than float inside it.
  const step = data.length > 1 ? plotW / (data.length - 1) : 0
  const x = (i: number) => PAD.left + (data.length > 1 ? i * step : plotW / 2)
  // Vertices stop being helpful once they outnumber the pixels between them.
  const vertices = data.length <= 16
  const every = Math.max(1, Math.ceil(data.length / 6))

  const nearest = (clientX: number, box: DOMRect): number => {
    const inPlot = ((clientX - box.left) / box.width) * W
    let best = 0
    for (let i = 1; i < data.length; i++) {
      if (Math.abs(x(i) - inPlot) < Math.abs(x(best) - inPlot)) best = i
    }
    return best
  }

  return (
    <svg
      viewBox={`0 0 ${W} ${height}`}
      className="h-auto w-full"
      role="img"
      aria-label={`${series.length} series over ${data.length} columns`}
      onMouseLeave={() => onHover?.(null)}
    >
      {scale.ticks.map((v) => (
        <g key={v}>
          <line
            x1={PAD.left}
            x2={W - PAD.right}
            y1={scale.y(v)}
            y2={scale.y(v)}
            stroke="currentColor"
            strokeWidth={1}
            className="text-outline-variant"
          />
          <text
            x={PAD.left - 8}
            y={scale.y(v) + 4}
            textAnchor="end"
            fontSize={11}
            fill="currentColor"
            className="tabular-nums text-on-surface-variant"
          >
            {format(v)}
          </text>
        </g>
      ))}

      {series.map((s) => {
        const path = data
          .map((d, i) => `${i === 0 ? 'M' : 'L'}${x(i)},${scale.y(s.at(d))}`)
          .join(' ')
        const dashed = s.paint instanceof Line && s.paint.dashed
        return (
          <g key={s.key}>
            <path d={path} {...s.paint.shape()} />
            {vertices &&
              !dashed &&
              data.map((d, i) => (
                <circle
                  key={i}
                  cx={x(i)}
                  cy={scale.y(s.at(d))}
                  r={hovered === i ? 4 : 2.5}
                  fill={s.paint instanceof Line ? s.paint.colour : 'currentColor'}
                />
              ))}
          </g>
        )
      })}

      {data.map((d, i) =>
        i % every === 0 ? (
          <text
            key={label(d)}
            x={x(i)}
            y={height - 8}
            textAnchor="middle"
            fontSize={11}
            fill="currentColor"
            className="text-on-surface-variant"
          >
            {label(d)}
          </text>
        ) : null,
      )}

      {hovered !== null && (
        <line
          x1={x(hovered)}
          x2={x(hovered)}
          y1={PAD.top}
          y2={PAD.top + plotH}
          stroke="currentColor"
          strokeWidth={1}
          strokeDasharray="3 3"
          className="text-outline"
        />
      )}

      {/* One target over the whole plot: the pointer picks the nearest column
          rather than having to land on a line two pixels wide. */}
      <rect
        x={PAD.left}
        y={PAD.top}
        width={plotW}
        height={plotH}
        fill="transparent"
        onMouseMove={(e) =>
          onHover?.(nearest(e.clientX, e.currentTarget.ownerSVGElement!.getBoundingClientRect()))
        }
      />
    </svg>
  )
}

/**
 * Where the hover readout sits, as a fraction across the plot.
 *
 * Past the middle it flips to the other side of the guide, so a panel near the
 * right-hand edge never runs off the card — which is the failure an SVG
 * tooltip has and the reason this one is an overlaid element instead.
 */
export function tipPosition(i: number, count: number): { left?: string; right?: string } {
  const frac = count > 1 ? i / (count - 1) : 0.5
  return frac > 0.55
    ? { right: `${6 + (1 - frac) * 84}%` }
    : { left: `${6 + frac * 84}%` }
}

/**
 * The values at the hovered column.
 *
 * Sorted by size rather than by the order the series happen to be in: the
 * question at a glance is which line is on top, and a list in a fixed order
 * makes that a reading exercise. Anything at zero stays in the list but dimmed,
 * because "this model did nothing today" is an answer.
 */
export function HoverValues<T>({
  datum,
  title,
  series,
  format,
  position,
}: {
  datum: T
  title: string
  series: readonly LineSeries<T>[]
  format: (value: number) => string
  position: { left?: string; right?: string }
}): ReactNode {
  const rows = series.filter((s) => s.aside !== true).map((s) => ({ s, v: s.at(datum) }))
  rows.sort((a, b) => b.v - a.v)
  const aside = series.filter((s) => s.aside === true)

  return (
    <div
      className="pointer-events-none absolute top-2 z-10 min-w-44 max-w-[62%] rounded-[var(--radius-md3-s)] border border-outline-variant bg-surface-lowest px-3 py-2 text-xs shadow-lg"
      style={position}
    >
      <div className="mb-1.5 font-mono text-[11px] text-on-surface-variant">{title}</div>
      {rows.map(({ s, v }) => (
        <Row key={s.key} series={s} value={v} format={format} />
      ))}
      {aside.length > 0 && (
        <>
          <div className="my-1.5 h-px bg-outline-variant" />
          {aside.map((s) => (
            <Row key={s.key} series={s} value={s.at(datum)} format={format} />
          ))}
        </>
      )}
    </div>
  )
}

function Row<T>({
  series,
  value,
  format,
}: {
  series: LineSeries<T>
  value: number
  format: (value: number) => string
}) {
  return (
    <div className={`flex items-center gap-2 py-px ${value === 0 ? 'opacity-45' : ''}`}>
      <span className="shrink-0" style={series.paint.swatch()} />
      <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-on-surface">
        {series.label}
      </span>
      <span className="shrink-0 font-medium tabular-nums text-on-surface">{format(value)}</span>
    </div>
  )
}

/** The key to a line chart: the mark in the key is the mark on the chart. */
export function LineLegend<T>({ series }: { series: readonly LineSeries<T>[] }): ReactNode {
  return (
    <div className="mt-3.5 flex flex-wrap gap-x-4 gap-y-1.5">
      {series.map((s) => (
        <span key={s.key} className="flex items-center gap-2 text-xs text-on-surface-variant">
          <span className="shrink-0" style={s.paint.swatch()} />
          <code className="font-mono text-[11px]">{s.label}</code>
        </span>
      ))}
    </div>
  )
}
