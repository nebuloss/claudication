import { useState } from 'react'
import { Solid, type Paint } from '../../charts/paint'
import { StateStrip } from '../../charts/strip'
import { Card, Chip, Muted, Stat, StatGrid, type Tone } from '../../primitives'
import { lasted, percent, useStatus, type Bucket, type Incident, type StatusReport, type UptimeState } from './report'

/**
 * The gateway's availability over time, drawn the same on the public welcome
 * page and on the admin Overview: the uptime figures, a strip of 90 days and
 * one of 24 hours, and the recent incidents.
 *
 * Every word here is about time and state, because the public page renders it
 * and the public page names nothing else. That is also why it imports only the
 * charts' paint and strip and the primitives, never the admin API client.
 */

/**
 * What each state looks like and is called. The tones are the MD3 roles the
 * rest of the UI already gives these meanings — success, warning, error — and
 * paused is the outline, because a deliberate stop is a fact rather than a
 * fault. None is the faintest surface: a bar that is there, saying nothing.
 */
const STATES: Record<UptimeState, { label: string; paint: Paint; tone: Tone }> = {
  up: { label: 'Up', paint: new Solid('var(--color-success)'), tone: 'ok' },
  degraded: { label: 'Degraded', paint: new Solid('var(--color-warning)'), tone: 'warn' },
  down: { label: 'Down', paint: new Solid('var(--color-error)'), tone: 'error' },
  paused: { label: 'Paused', paint: new Solid('var(--color-outline)'), tone: 'neutral' },
  none: { label: 'No data', paint: new Solid('var(--color-surface-highest)'), tone: 'neutral' },
}

const LEGEND: UptimeState[] = ['up', 'degraded', 'down', 'paused', 'none']

// UTC throughout, because the buckets are UTC hours and days: a local label on
// a UTC bar would put an outage on the wrong day for half the world.
const dayFormat = new Intl.DateTimeFormat(undefined, { timeZone: 'UTC', weekday: 'short', day: 'numeric', month: 'short' })
const hourFormat = new Intl.DateTimeFormat(undefined, { timeZone: 'UTC', hour: '2-digit', minute: '2-digit' })
const whenFormat = new Intl.DateTimeFormat(undefined, {
  timeZone: 'UTC',
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
})

function bucketWords(b: Bucket, period: string): string {
  const s = STATES[b.state]
  if (b.state === 'none') return `${period}: no data`
  if (b.up === null) return `${period}: ${s.label.toLowerCase()}`
  const verdict = b.state === 'up' ? '' : `, ${s.label.toLowerCase()} at times`
  return `${period}: ${percent(b.up)} up${verdict}`
}

function dayWords(b: Bucket): string {
  return bucketWords(b, `${dayFormat.format(b.start)} (UTC)`)
}

function hourWords(b: Bucket): string {
  return bucketWords(b, `${hourFormat.format(b.start)}–${hourFormat.format(b.start + 3_600_000)} UTC`)
}

/** How many of each state a strip holds, for the one sentence a screen reader gets. */
function summary(buckets: Bucket[], unit: string): string {
  const n: Partial<Record<UptimeState, number>> = {}
  for (const b of buckets) n[b.state] = (n[b.state] ?? 0) + 1
  const parts = LEGEND.filter((s) => (n[s] ?? 0) > 0).map((s) => `${n[s]} ${STATES[s].label.toLowerCase()}`)
  return `The last ${buckets.length} ${unit}: ${parts.join(', ')}.`
}

/** The three headline figures. */
export function UptimeFigures({ report }: { report: StatusReport }) {
  return (
    <StatGrid columns={3}>
      <Stat label="Last 24 hours" value={percent(report.uptime['24h'])} hint="uptime" />
      <Stat label="Last 7 days" value={percent(report.uptime['7d'])} hint="uptime" />
      <Stat label="Last 90 days" value={percent(report.uptime['90d'])} hint="uptime" />
    </StatGrid>
  )
}

/** One strip with its ends labelled and a readout above it for the hovered bar. */
function Strip({
  buckets,
  title,
  from,
  to,
  words,
  unit,
}: {
  buckets: Bucket[]
  title: string
  from: string
  to: string
  words: (b: Bucket) => string
  unit: string
}) {
  const [hovered, setHovered] = useState<number | null>(null)
  const shown = hovered !== null ? buckets[hovered] : undefined
  return (
    <div>
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h3 className="text-sm font-medium text-on-surface">{title}</h3>
        {/* A fixed place for the readout rather than a floating tooltip, as
            the other charts do: one over a 400px strip covers the bars it
            describes. Kept one line tall when empty, so nothing jumps. */}
        <span className="min-h-5 text-xs text-on-surface-variant tabular-nums" aria-hidden>
          {shown !== undefined ? words(shown) : ''}
        </span>
      </div>
      <StateStrip
        data={buckets}
        paint={(b) => STATES[b.state].paint}
        label={summary(buckets, unit)}
        describe={words}
        hovered={hovered}
        onHover={setHovered}
      />
      <div className="mt-1.5 flex justify-between text-xs text-on-surface-variant">
        <span>{from}</span>
        <span>{to}</span>
      </div>
    </div>
  )
}

/** The 90-day strip, the 24-hour strip, and the key to their colours. */
export function UptimeStrips({ report }: { report: StatusReport }) {
  return (
    <div className="flex flex-col gap-6">
      <Strip buckets={report.days} title="Last 90 days" from="90 days ago" to="Today" words={dayWords} unit="days" />
      <Strip buckets={report.hours} title="Last 24 hours" from="24 hours ago" to="Now" words={hourWords} unit="hours" />
      <ul className="flex flex-wrap gap-x-4 gap-y-2" aria-label="Key">
        {LEGEND.map((s) => (
          <li key={s} className="flex items-center gap-1.5 text-xs text-on-surface-variant">
            <span style={STATES[s].paint.swatch()} aria-hidden />
            {STATES[s].label}
          </li>
        ))}
      </ul>
    </div>
  )
}

function IncidentRow({ incident, now }: { incident: Incident; now: number }) {
  const s = STATES[incident.state]
  const end = incident.end ?? now
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-3 first:pt-0 last:pb-0">
      <Chip tone={s.tone}>{s.label}</Chip>
      <span className="text-sm text-on-surface tabular-nums">{whenFormat.format(incident.start)} UTC</span>
      <span className="text-sm text-on-surface-variant">
        {incident.end === null ? `ongoing, ${lasted(end - incident.start)} so far` : `for ${lasted(end - incident.start)}`}
      </span>
    </li>
  )
}

/** The recent stretches that were degraded or down, newest first. */
export function IncidentList({ report, limit = 8 }: { report: StatusReport; limit?: number }) {
  if (report.incidents.length === 0) {
    return <Muted>No incidents in the last 90 days.</Muted>
  }
  return (
    <ul className="divide-y divide-outline-variant/40">
      {report.incidents.slice(0, limit).map((i) => (
        <IncidentRow key={i.start} incident={i} now={report.generated} />
      ))}
    </ul>
  )
}

/**
 * The same history on the admin Overview, read from the same public endpoint
 * the welcome page reads, so the operator sees exactly what the world does.
 */
export function AvailabilityCard() {
  const { result, last } = useStatus()
  return (
    <Card title="Availability">
      {last === null ? (
        <Muted>{result.kind === 'unreachable' ? 'Could not read the availability history.' : 'Loading…'}</Muted>
      ) : (
        <div className="flex flex-col gap-6">
          <UptimeFigures report={last} />
          <UptimeStrips report={last} />
          <div>
            <h3 className="mb-2 text-sm font-medium text-on-surface">Recent incidents</h3>
            <IncidentList report={last} />
          </div>
        </div>
      )}
    </Card>
  )
}
