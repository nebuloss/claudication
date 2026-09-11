import { type ReactNode } from 'react'
import { defsFor } from './paint'
import { BandScale, LinearScale } from './scale'
import { type Stack } from './stack'

/**
 * A stacked column chart.
 *
 * Presentation only: it takes a Stack and a hovered index and draws them. The
 * readout that goes with a hover belongs to whatever card is using the chart,
 * because a tooltip floating over the plot either clips at the right-hand edge
 * or covers the columns it is describing, and a fixed place above has neither
 * problem.
 *
 * The internal coordinate system is fixed and the SVG scales to whatever width
 * it is given, so the same chart works in a wide card and on a phone without a
 * measurement pass or a resize observer.
 */
const W = 720
const PAD = { top: 10, right: 6, bottom: 22, left: 46 }

export function ColumnChart<T>({
  stack,
  label,
  format,
  height = 210,
  hovered = null,
  onHover,
  describe,
}: {
  stack: Stack<T>
  /** The x-axis label for a datum. */
  label: (datum: T) => string
  /** How to write a value on the axis. */
  format: (value: number) => string
  height?: number
  hovered?: number | null
  onHover?: (i: number | null) => void
  /** The title text a pointer gets on a column, for the native tooltip. */
  describe?: (datum: T) => string
}) {
  const plotH = height - PAD.top - PAD.bottom
  const y = new LinearScale(stack.peak, PAD.top + plotH, PAD.top)
  const x = new BandScale(stack.length, PAD.left, W - PAD.right, { maxWidth: 34 })
  const labelled = x.labelled()

  return (
    <svg
      viewBox={`0 0 ${W} ${height}`}
      className="h-auto w-full"
      role="img"
      aria-label={`${format(stack.sum)} over ${stack.length} columns`}
      onMouseLeave={() => onHover?.(null)}
    >
      <defs>{defsFor(stack.paints())}</defs>

      {y.ticks.map((v) => (
        <g key={v}>
          <line
            x1={PAD.left}
            x2={W - PAD.right}
            y1={y.y(v)}
            y2={y.y(v)}
            stroke="currentColor"
            strokeWidth={1}
            className="text-outline-variant"
          />
          <text
            x={PAD.left - 8}
            y={y.y(v) + 4}
            textAnchor="end"
            fontSize={11}
            fill="currentColor"
            className="tabular-nums text-on-surface-variant"
          >
            {format(v)}
          </text>
        </g>
      ))}

      {stack.data.map((datum, i) => {
        const bar = x.bar(i)
        const slot = x.slot(i)
        let cursor = PAD.top + plotH
        return (
          <g key={label(datum) + i} opacity={hovered === null || hovered === i ? 1 : 0.45}>
            {stack.segments(i).map(({ series, value }) => {
              // A column with traffic never renders as nothing: one pixel of
              // colour is the difference between "quiet" and "down".
              const h = Math.max(1.5, y.height(value))
              cursor -= h
              return (
                <rect
                  key={series.key}
                  x={bar.x}
                  y={cursor}
                  width={bar.width}
                  height={h}
                  {...series.paint.shape()}
                />
              )
            })}

            {/* A full-height target, so a quiet day's thin column is still
                easy to point at. */}
            <rect
              x={slot.x}
              y={PAD.top}
              width={slot.width}
              height={plotH}
              fill="transparent"
              onMouseEnter={() => onHover?.(i)}
            >
              {describe !== undefined && <title>{describe(datum)}</title>}
            </rect>

            {labelled(i) && (
              <text
                x={slot.x + slot.width / 2}
                y={height - 6}
                textAnchor="middle"
                fontSize={11}
                fill="currentColor"
                className="text-on-surface-variant"
              >
                {label(datum)}
              </text>
            )}
          </g>
        )
      })}
    </svg>
  )
}

/**
 * The key to a chart's series, with each swatch painted by the same Paint the
 * marks use — so it cannot describe something the chart is not drawing.
 *
 * `values` puts a figure beside each label, which is what makes a hover
 * readout out of a legend rather than needing a second component for it.
 */
export function Legend<T>({
  stack,
  values,
  format,
}: {
  stack: Stack<T>
  /** A value per series, or undefined for a plain key. */
  values?: (series: (typeof stack.series)[number]) => number | undefined
  format: (value: number) => string
}): ReactNode {
  return (
    <span className="flex flex-wrap gap-3">
      {stack.series.map((series) => {
        const value = values?.(series)
        return (
          <span key={series.key} className="flex items-center gap-1.5 text-xs text-on-surface-variant">
            <span
              className="inline-block size-2.5 rounded-[2px]"
              style={series.paint.swatch()}
            />
            {series.label}
            {value !== undefined && (
              <span className="tabular-nums text-on-surface">{format(value)}</span>
            )}
          </span>
        )
      })}
    </span>
  )
}
