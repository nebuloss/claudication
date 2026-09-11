import { useState } from 'react'
import { type UsageBucket } from '../../api/client'
import { compact } from '../primitives'

/**
 * The charts on the Overview screen.
 *
 * Hand-rolled SVG rather than a charting library, for the same reason the
 * horizontal `Bar` primitive is: the binary is twelve megabytes and ships the
 * UI inside it, and a library would be a large fraction of that for two
 * pictures. What a library would buy — axes, stacking, a hover readout — is
 * two hundred lines here and is spelled out below.
 *
 * The colours come from CSS variables rather than Tailwind utilities because
 * SVG needs `fill`, and the variables are already redefined per theme, so a
 * chart follows light and dark without knowing either exists.
 */

/** The plot's own coordinate system; the SVG scales to whatever it is given. */
const W = 720
const H = 210
const PAD = { top: 10, right: 6, bottom: 22, left: 46 }
const PLOT_W = W - PAD.left - PAD.right
const PLOT_H = H - PAD.top - PAD.bottom

type Series = {
  key: string
  label: string
  colour: string
  /** Drawn as a shaded, striped tone of `colour` — a state, not an identity. */
  hatched?: boolean
  of: (b: UsageBucket) => number
}

/**
 * How a failure is drawn: the same colour, shaded and hatched.
 *
 * Not a red. Hue in this card means identity — which model, which kind of
 * token — and the mix underneath spends the whole categorical palette on
 * exactly that. Giving a *state* a hue of its own takes one of those away and
 * puts the same red in two legends meaning two different things. State is
 * better carried by value and texture, which are free.
 *
 * It also generalises the right way: split traffic by model tomorrow and each
 * model's failures are that model's own colour, shaded and striped, with no
 * palette to extend.
 *
 * Measured as CIE76 dE, for the two colours this chart uses and in both
 * themes — base against the shaded band, band against its stripes, and band
 * against the card it sits on:
 *
 *                      base/band  band/stripe  band/card
 *   light  primary          12.4         14.2       70.4
 *   dark   primary          18.3         22.0       57.0
 *   light  series-1         15.5         19.0       73.2
 *   dark   series-1         16.5         19.5       56.0
 *
 * The last column is the one that stops the shading going too far: darkening
 * toward black is what "shaded" means in a light theme, and in a dark one it
 * is the direction the background lies in, so it has to stay well clear.
 */
const DIM = 0.58

/** A darker tone of the same colour. Falls back to the colour itself. */
function dim(colour: string): string {
  return `color-mix(in oklab, ${colour} ${Math.round(DIM * 100)}%, black)`
}

/** One pattern per hatched series, so the id follows the series it paints. */
function hatchID(key: string): string {
  return `traffic-hatch-${key}`
}

/**
 * The same stripes in CSS, so the legend swatch is not a lie about the bar.
 *
 * The stripe is the card's own colour rather than a lighter tone of the fill:
 * it reads as the bar being cut through, and it needs no second definition to
 * follow the theme.
 */
function hatchCSS(colour: string): string {
  return (
    `repeating-linear-gradient(45deg, ${dim(colour)} 0 2.5px, ` +
    `var(--color-surface-container) 2.5px 4px)`
  )
}

const REQUEST_SERIES: Series[] = [
  {
    key: 'ok',
    label: 'Succeeded',
    // Primary rather than a series colour: with one metric there is one
    // identity here, and the Bar primitive's rule is that a series colour
    // identifies while plain primary means magnitude. It also leaves all
    // eight categorical hues to the model mix below, which needs them.
    colour: 'var(--color-primary)',
    of: (b) => Math.max(0, b.requests - b.errors),
  },
  {
    key: 'failed',
    label: 'Failed',
    colour: 'var(--color-primary)',
    hatched: true,
    of: (b) => b.errors,
  },
]

const TOKEN_SERIES: Series[] = [
  { key: 'in', label: 'Input', colour: 'var(--color-series-1)', of: (b) => b.input_tokens },
  { key: 'out', label: 'Output', colour: 'var(--color-series-3)', of: (b) => b.output_tokens },
  { key: 'cache', label: 'Cache', colour: 'var(--color-series-4)', of: (b) => b.cache_tokens },
]

/**
 * Gridlines on round numbers.
 *
 * A chart whose top line reads 1,183,402 is a chart nobody reads a value off.
 * This walks 1/2/5 × 10^n until four or fewer lines cover the data, which is
 * the smallest set of steps that always lands on a number worth printing.
 */
function ticks(max: number): number[] {
  if (max <= 0) return [0]
  const target = 4
  const raw = max / target
  const magnitude = Math.pow(10, Math.floor(Math.log10(raw)))
  const step = [1, 2, 5, 10].map((m) => m * magnitude).find((s) => max / s <= target) ?? magnitude * 10
  const out: number[] = []
  for (let v = 0; v <= max + step / 2; v += step) out.push(v)
  return out
}

/**
 * Requests or tokens per day, stacked.
 *
 * Stacked rather than grouped, in both metrics, because every series here is a
 * part of its column's whole: failures are a share of the requests made, and
 * input, output and cache are a share of the tokens spent. Side by side would
 * show a hundred requests beside five failures as a tall bar and a short one,
 * when the fact worth seeing is that five of the hundred failed.
 *
 * Hovering swaps the figures above the chart for that day's rather than
 * floating a tooltip over it: a tooltip near the right-hand edge either clips
 * or covers the columns it is describing, and the readout has a fixed place to
 * be where nothing has to move out of its way.
 */
export function TrafficChart({
  buckets,
  metric,
}: {
  buckets: UsageBucket[]
  metric: 'requests' | 'tokens'
}) {
  const [hover, setHover] = useState<number | null>(null)
  const series = metric === 'requests' ? REQUEST_SERIES : TOKEN_SERIES

  const totals = buckets.map((b) => series.reduce((n, s) => n + s.of(b), 0))
  const peak = totals.reduce((n, v) => Math.max(n, v), 0)
  const lines = ticks(peak)
  const top = lines[lines.length - 1] || 1

  const slot = PLOT_W / Math.max(1, buckets.length)
  const barW = Math.min(slot - 3, 34)
  const y = (v: number) => PAD.top + PLOT_H - (v / top) * PLOT_H

  // Enough labels to place the eye, few enough not to collide at 400px wide.
  const every = Math.max(1, Math.ceil(buckets.length / 6))

  const shown = hover !== null ? buckets[hover] : undefined
  const shownTotal = hover !== null ? totals[hover] : totals.reduce((n, v) => n + v, 0)

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className="text-2xl font-medium tabular-nums text-on-surface">
          {compact(shownTotal)}
        </span>
        <span className="text-xs text-on-surface-variant">
          {shown === undefined
            ? `${metric === 'requests' ? 'requests' : 'tokens'} over ${buckets.length} days`
            : `${metric === 'requests' ? 'requests' : 'tokens'} on ${shown.label}`}
        </span>
        <span className="ml-auto flex flex-wrap gap-3">
          {series.map((s) => (
            <span key={s.key} className="flex items-center gap-1.5 text-xs text-on-surface-variant">
              <span
                className="inline-block size-2.5 rounded-[2px]"
                style={
                  s.hatched === true
                    ? { background: s.colour, backgroundImage: hatchCSS(s.colour) }
                    : { background: s.colour }
                }
              />
              {s.label}
              {shown !== undefined && (
                <span className="tabular-nums text-on-surface">{compact(s.of(shown))}</span>
              )}
            </span>
          ))}
        </span>
      </div>

      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="h-auto w-full"
        role="img"
        aria-label={`${compact(shownTotal)} ${metric} over ${buckets.length} days`}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          {/* Vertical lines rotated 45°, which is cheaper than drawing
              diagonals and tiles without seams. The shaded fill is set through
              `style` over a plain `fill`, so a browser without color-mix drops
              the declaration and keeps the series colour rather than painting
              the band with nothing. */}
          {series
            .filter((s) => s.hatched === true)
            .map((s) => (
              <pattern
                key={s.key}
                id={hatchID(s.key)}
                width="6"
                height="6"
                patternUnits="userSpaceOnUse"
                patternTransform="rotate(45)"
              >
                <rect width="6" height="6" fill={s.colour} style={{ fill: dim(s.colour) }} />
                <line
                  x1="0"
                  y1="0"
                  x2="0"
                  y2="6"
                  stroke="var(--color-surface-container)"
                  strokeWidth="2.5"
                  opacity="0.55"
                />
              </pattern>
            ))}
        </defs>

        {lines.map((v) => (
          <g key={v}>
            <line
              x1={PAD.left}
              x2={W - PAD.right}
              y1={y(v)}
              y2={y(v)}
              stroke="currentColor"
              strokeWidth={1}
              className="text-outline-variant"
            />
            <text
              x={PAD.left - 8}
              y={y(v) + 4}
              textAnchor="end"
              fontSize={11}
              fill="currentColor"
              className="text-on-surface-variant tabular-nums"
            >
              {compact(v)}
            </text>
          </g>
        ))}

        {buckets.map((b, i) => {
          const x = PAD.left + i * slot + (slot - barW) / 2
          let cursor = PAD.top + PLOT_H
          return (
            <g key={b.label} opacity={hover === null || hover === i ? 1 : 0.45}>
              {series.map((s) => {
                const value = s.of(b)
                if (value <= 0) return null
                // A day with traffic never renders as nothing: one pixel of
                // colour is the difference between "quiet" and "down".
                const h = Math.max(1.5, (value / top) * PLOT_H)
                cursor -= h
                return (
                  <rect
                    key={s.key}
                    x={x}
                    y={cursor}
                    width={barW}
                    height={h}
                    fill={s.hatched === true ? `url(#${hatchID(s.key)})` : s.colour}
                  />
                )
              })}
              {/* A full-height target, so the thin columns of a quiet day are
                  still easy to point at. */}
              <rect
                x={PAD.left + i * slot}
                y={PAD.top}
                width={slot}
                height={PLOT_H}
                fill="transparent"
                onMouseEnter={() => setHover(i)}
              >
                <title>
                  {b.label}: {b.requests} requests, {b.errors} failed,{' '}
                  {compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tokens
                </title>
              </rect>
              {i % every === 0 && (
                <text
                  x={PAD.left + i * slot + slot / 2}
                  y={H - 6}
                  textAnchor="middle"
                  fontSize={11}
                  fill="currentColor"
                  className="text-on-surface-variant"
                >
                  {b.label}
                </text>
              )}
            </g>
          )
        })}
      </svg>
    </div>
  )
}

/** Anything past this many models is grouped, so the legend stays readable. */
const MIX_LIMIT = 6

/**
 * Where the tokens went, as one bar.
 *
 * A subscription is spent in tokens rather than requests, and the split across
 * models is the part that explains a week. One stacked bar rather than the
 * Usage tab's list of bars because this is a composition — the question is what
 * share, and a row of independent bars answers a different one.
 */
export function ModelMix({ buckets }: { buckets: UsageBucket[] }) {
  const rows = buckets
    .map((b) => ({
      label: b.label === '' ? 'unknown' : b.label,
      tokens: b.input_tokens + b.output_tokens + b.cache_tokens,
      requests: b.requests,
    }))
    .filter((r) => r.tokens > 0)
    .sort((a, b) => b.tokens - a.tokens)

  const total = rows.reduce((n, r) => n + r.tokens, 0)
  if (total === 0) return null

  const head = rows.slice(0, MIX_LIMIT)
  const rest = rows.slice(MIX_LIMIT)
  if (rest.length > 0) {
    head.push({
      label: `${rest.length} more`,
      tokens: rest.reduce((n, r) => n + r.tokens, 0),
      requests: rest.reduce((n, r) => n + r.requests, 0),
    })
  }

  return (
    <div>
      <div className="flex h-7 w-full overflow-hidden rounded-[var(--radius-md3-s)]">
        {head.map((r, i) => (
          <div
            key={r.label}
            className="h-full"
            style={{
              width: `${(r.tokens / total) * 100}%`,
              background: `var(--color-series-${(i % 8) + 1})`,
            }}
            title={`${r.label}: ${compact(r.tokens)} tokens over ${r.requests} requests`}
          />
        ))}
      </div>
      <div className="mt-3 grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
        {head.map((r, i) => (
          <div key={r.label} className="flex items-center gap-2 text-xs">
            <span
              className="inline-block size-2.5 shrink-0 rounded-[2px]"
              style={{ background: `var(--color-series-${(i % 8) + 1})` }}
            />
            <span className="min-w-0 flex-1 truncate font-mono text-on-surface" title={r.label}>
              {r.label}
            </span>
            <span className="shrink-0 tabular-nums text-on-surface-variant">
              {compact(r.tokens)}
            </span>
            <span className="w-10 shrink-0 text-right tabular-nums text-on-surface-variant">
              {Math.round((r.tokens / total) * 100)}%
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
