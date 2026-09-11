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
  /** Painted with diagonal stripes as well as its colour — see HATCH_ID. */
  hatched?: boolean
  of: (b: UsageBucket) => number
}

/**
 * The pattern that stripes the failure band.
 *
 * Colour alone is a single point of failure in a chart: it is the thing a
 * viewer may not see, a projector may wash out, and a two-pixel band gives too
 * little of it to judge by. Stripes are a second channel that survives all three,
 * and a failure is the one series here worth reading at a glance.
 */
const HATCH_ID = 'traffic-hatch'

/** The same stripes in CSS, so the legend swatch is not a lie about the bar. */
const HATCH_CSS =
  'repeating-linear-gradient(45deg, var(--color-series-8) 0 2.5px, ' +
  'var(--color-surface-container) 2.5px 4px)'

/**
 * Succeeded and failed, and why neither is a theme colour.
 *
 * They began as primary and error, which in the dark theme are #ffb59d and
 * #ffb4ab — one point of green and fourteen of blue apart, or one block of
 * peach once stacked. Measured as CIE76 dE, normal vision and simulated CVD:
 *
 *                            normal  protan  deutan
 *   light  primary/error       37.7     8.3    26.2
 *   dark   primary/error        7.7     7.5     4.2
 *   light  series-1/error     110.6    86.8    98.4
 *   dark   series-1/error      76.2    69.0    70.7
 *   light  series-1/series-8  102.3    77.6    90.8
 *   dark   series-1/series-8   89.1    69.0    78.4
 *
 * 4.2 under deuteranopia is no colour difference at all. The mistake was using
 * --color-error as a fill: in a dark theme MD3 sets it to the tone meant for
 * text on a dark surface, which is a pink, and a fill wants the saturated hue.
 * series-8 is that red in both themes, is already validated as part of the
 * categorical set, and in the dark theme beats --color-error outright.
 *
 * Series colours are also the more correct choice by the rule the Bar
 * primitive already states: series colours identify, plain primary means
 * magnitude. In a stacked column these two are identities, not sizes.
 */
const REQUEST_SERIES: Series[] = [
  {
    key: 'ok',
    label: 'Succeeded',
    colour: 'var(--color-series-1)',
    of: (b) => Math.max(0, b.requests - b.errors),
  },
  {
    key: 'failed',
    label: 'Failed',
    colour: 'var(--color-series-8)',
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
                style={s.hatched === true ? { background: HATCH_CSS } : { background: s.colour }}
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
              diagonals and tiles without seams. The stripe is the card's own
              colour rather than a lightened red, so it reads as the bar being
              cut through in either theme. */}
          <pattern
            id={HATCH_ID}
            width="6"
            height="6"
            patternUnits="userSpaceOnUse"
            patternTransform="rotate(45)"
          >
            <rect width="6" height="6" fill="var(--color-series-8)" />
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
                    fill={s.hatched === true ? `url(#${HATCH_ID})` : s.colour}
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
