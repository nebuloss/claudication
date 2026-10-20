import { type Paint } from './paint'
import { BandScale } from './scale'

/**
 * A row of equal bars, one per period, each painted by what the period was:
 * the status page's strip of days or hours.
 *
 * A state per bar rather than a value, so no y scale: a bar's height saying
 * "how up" would make a 99.9% day and a 100% day look the same and a 50% day
 * half as tall, which reads as quieter rather than broken. The colour carries
 * the verdict and the readout the figure.
 *
 * Presentation only, like ColumnChart: the hovered bar and what to say about
 * it belong to the card. The internal width is fixed and the SVG scales, so 90
 * bars fit a phone as thin lines and a desktop as a comb, with no measuring.
 */
const W = 720

export function StateStrip<T>({
  data,
  paint,
  label,
  describe,
  height = 40,
  hovered = null,
  onHover,
}: {
  data: T[]
  /** What fills a bar. */
  paint: (datum: T) => Paint
  /** The whole strip, for assistive technology. */
  label: string
  /** The native tooltip on a bar: its period and what it was. */
  describe: (datum: T) => string
  height?: number
  hovered?: number | null
  onHover?: (i: number | null) => void
}) {
  const x = new BandScale(data.length, 0, W, { gap: data.length > 60 ? 2 : 3 })
  return (
    <svg
      viewBox={`0 0 ${W} ${height}`}
      preserveAspectRatio="none"
      className="block w-full"
      style={{ height }}
      role="img"
      aria-label={label}
      onMouseLeave={() => onHover?.(null)}
    >
      {data.map((datum, i) => {
        const bar = x.bar(i)
        const slot = x.slot(i)
        return (
          <g key={i} opacity={hovered === null || hovered === i ? 1 : 0.55}>
            <rect x={bar.x} y={0} width={bar.width} height={height} rx={2} {...paint(datum).shape()} />
            {/* The whole slot answers the pointer, gap included, so a thin
                bar on a phone is not a target to aim at. */}
            <rect
              x={slot.x}
              y={0}
              width={slot.width}
              height={height}
              fill="transparent"
              onMouseEnter={() => onHover?.(i)}
              onTouchStart={() => onHover?.(i)}
            >
              <title>{describe(datum)}</title>
            </rect>
          </g>
        )
      })}
    </svg>
  )
}
