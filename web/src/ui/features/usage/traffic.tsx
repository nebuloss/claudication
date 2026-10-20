import { useMemo, useState } from 'react'
import { type UsageCell } from '../../../api/client'
import {
  FAILURE_LINE,
  HoverValues,
  LineChart,
  LineLegend,
  Palette,
  tipPosition,
  type LineSeries,
} from '../../charts'
import { Muted, Segmented, WindowPicker } from '../../primitives'
import { compact } from '../../format'

/**
 * The traffic chart: a line per name of one breakdown, over days.
 *
 * One chart, given the breakdown to draw, rather than one per question.
 * Overview used to draw its own columns and Usage its own bars, which meant
 * two places deciding what a colour meant and two places to fix when the
 * answer changed. Which breakdown is the page's to say, not a tab bar's
 * inside the chart: Activity draws each on the page whose table holds the
 * same names — models over the models, outcomes over the request log.
 */

export type Dimension = 'model' | 'key' | 'account' | 'status'

type Spec = {
  label: string
  unit: string
  /** False where the breakdown has never carried token counts. */
  tokens?: boolean
  /** False where the failures are the lines, so a total would double-count. */
  failureLine?: boolean
}

const DIMENSIONS: Record<Dimension, Spec> = {
  model: { label: 'Models', unit: 'model' },
  key: { label: 'Clients', unit: 'client' },
  account: { label: 'Accounts', unit: 'account' },
  status: { label: 'Status', unit: 'code', tokens: false, failureLine: false },
}

/** Past this many lines the key stops being readable; the tail becomes one. */
const LIMIT = 6

type Row = { day: string; value: (name: string) => number; errors: number }

/**
 * Pivot the long-form cross-tab into a column per day and a line per name.
 *
 * The gateway sends one row per (day, name) because that is what its grouped
 * query already produces; turning it into a grid is this screen's job and not
 * the database's.
 */
function pivot(cells: readonly UsageCell[], metric: 'requests' | 'tokens') {
  const value = (c: UsageCell) =>
    metric === 'requests' ? c.requests : c.input_tokens + c.output_tokens + c.cache_tokens

  const days: string[] = []
  const grid = new Map<string, Map<string, UsageCell>>()
  const totals = new Map<string, number>()
  for (const c of cells) {
    if (!grid.has(c.day)) {
      grid.set(c.day, new Map())
      days.push(c.day)
    }
    grid.get(c.day)!.set(c.name, c)
    totals.set(c.name, (totals.get(c.name) ?? 0) + c.requests)
  }

  const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([n]) => n)
  const head = ranked.slice(0, LIMIT)
  const tail = ranked.slice(LIMIT)
  const grouped = tail.length > 0 ? `${tail.length} more` : null
  const names = grouped === null ? head : [...head, grouped]
  const members = (n: string) => (n === grouped ? tail : [n])

  const rows: Row[] = days.map((day) => {
    const byName = grid.get(day)!
    return {
      day,
      value: (n) => members(n).reduce((s, m) => s + (byName.has(m) ? value(byName.get(m)!) : 0), 0),
      errors: [...byName.values()].reduce((s, c) => s + c.errors, 0),
    }
  })

  return { rows, names, total: rows.reduce((s, r) => s + names.reduce((n, x) => n + r.value(x), 0), 0) }
}

export function Traffic({
  cross,
  by,
  days,
  onDays,
  windows,
}: {
  /** The cross-tabs, keyed by dimension, as /admin/usage returns them. */
  cross: Record<string, UsageCell[]> | undefined
  /** Which names the lines are. */
  by: Dimension
  days: number
  /** A window picker of the chart's own, where nothing else on the page picks one. */
  onDays?: (d: number) => void
  windows?: readonly number[]
}) {
  const [metric, setMetric] = useState<'requests' | 'tokens'>('requests')
  const [log, setLog] = useState(true)
  const [hovered, setHovered] = useState<number | null>(null)

  const current = DIMENSIONS[by]
  const cells = cross?.[by] ?? []
  // Tokens is not merely empty by status, it has never been recorded; offering
  // the control there would be a button that does nothing.
  const effective = current.tokens === false ? 'requests' : metric

  const { rows, names, total } = useMemo(() => pivot(cells, effective), [cells, effective])

  const series: LineSeries<Row>[] = useMemo(() => {
    const palette = new Palette(names)
    const out: LineSeries<Row>[] = names.map((name) => ({
      key: name,
      label: name,
      // A stroke of the palette's colour for this name: hue is identity, and
      // the name's slot does not move when the ranking does.
      paint: palette.stroke(name),
      at: (r) => r.value(name),
    }))
    if (current.failureLine !== false && effective === 'requests') {
      out.push({
        key: '__failed',
        label: 'failed',
        paint: FAILURE_LINE,
        at: (r) => r.errors,
        aside: true,
      })
    }
    return out
  }, [names, current.failureLine, effective])

  const unit = effective === 'requests' ? 'requests' : 'tokens'
  const shown = hovered !== null ? rows[hovered] : undefined

  return (
    <>
      {/* A row that wraps, not the tab bar's aside it once rode in: that bar
          scrolled sideways, so on a phone the controls sat off the screen. */}
      <div className="flex flex-wrap items-center gap-2">
        {current.tokens !== false && (
          <Segmented
            label="Metric"
            value={effective}
            onChange={(v) => setMetric(v)}
            options={[
              { id: 'requests' as const, label: 'Requests', content: 'Requests' },
              { id: 'tokens' as const, label: 'Tokens', content: 'Tokens' },
            ]}
          />
        )}
        <Segmented
          label="Scale"
          value={log ? 'log' : 'linear'}
          onChange={(v) => setLog(v === 'log')}
          options={[
            { id: 'log' as const, label: 'Logarithmic', content: 'Log' },
            { id: 'linear' as const, label: 'Linear', content: 'Linear' },
          ]}
        />
        {onDays !== undefined && windows !== undefined && (
          <WindowPicker label="History window" value={days} windows={windows} onChange={onDays} />
        )}
      </div>

      {rows.length === 0 ? (
        <div className="mt-4">
          <Muted>Nothing in the last {days} days.</Muted>
        </div>
      ) : (
        <>
          <div className="mt-4 mb-1 flex flex-wrap items-baseline gap-x-4 gap-y-1">
            <span className="text-2xl font-medium tabular-nums text-on-surface">
              {compact(
                shown === undefined ? total : names.reduce((n, x) => n + shown.value(x), 0),
              )}
            </span>
            <span className="text-xs text-on-surface-variant">
              {shown === undefined
                ? `${unit} over ${rows.length} days, by ${current.unit}`
                : `${unit} on ${shown.day}`}
            </span>
          </div>

          <div className="relative">
            {shown !== undefined && (
              <HoverValues
                datum={shown}
                title={shown.day}
                series={series}
                format={compact}
                position={tipPosition(hovered ?? 0, rows.length)}
              />
            )}
            <LineChart
              data={rows}
              series={series}
              label={(r) => r.day.slice(5)}
              format={compact}
              log={log}
              hovered={hovered}
              onHover={setHovered}
            />
          </div>

          <LineLegend series={series} />
        </>
      )}
    </>
  )
}
