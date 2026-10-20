/**
 * Instants and spans, as the screens print them.
 *
 * Each of these was written out inside the component that first needed it,
 * and the copies drifted: two `relative()`s disagreed about what to say for
 * the last minute, and a request's time and a chat's start were each their
 * own idea of "today's date is not worth printing". What differs between call
 * sites now is an argument, so the difference is visible where it is chosen.
 *
 * Every function here takes the server's ISO string and is total: a value
 * that does not parse comes back as a fallback, never as "Invalid Date".
 */

/** How finely a time of day is printed. */
export type Precision = 'minute' | 'second'

const TIME: Record<Precision, Intl.DateTimeFormatOptions> = {
  minute: { hour: '2-digit', minute: '2-digit' },
  // 24-hour with seconds: a retry storm puts a dozen rows in one minute.
  second: { hour12: false },
}

/**
 * The date beside a time that is not today's. Two-digit days at second
 * precision, because that is the request log's, a column read down the page
 * where every row should be the same width.
 */
const DAY: Record<Precision, Intl.DateTimeFormatOptions> = {
  minute: { month: 'short', day: 'numeric' },
  second: { month: 'short', day: '2-digit' },
}

function parse(iso: string | undefined): Date | null {
  if (iso === undefined || iso === '') return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

function isToday(d: Date): boolean {
  const now = new Date()
  return (
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  )
}

/** The full local date and time, or `never` when there is no instant. */
export function dateTime(iso: string | undefined): string {
  if (iso === undefined || iso === '') return 'never'
  const d = parse(iso)
  return d === null ? iso : d.toLocaleString()
}

/** The time of day alone, for a chart's axis inside one chat. */
export function timeOfDay(iso: string, precision: Precision = 'minute'): string {
  const d = parse(iso)
  return d === null ? iso : d.toLocaleTimeString(undefined, TIME[precision])
}

/**
 * The time, with the date in front only when it is not today.
 *
 * The date is dropped for today's rows, which is nearly all of them at a
 * fortnight's retention, and the full instant belongs on the row's title.
 */
export function sameDayStamp(iso: string, precision: Precision, invalid: string = iso): string {
  const d = parse(iso)
  if (d === null) return invalid
  const time = d.toLocaleTimeString(undefined, TIME[precision])
  if (isToday(d)) return time
  return `${d.toLocaleDateString(undefined, DAY[precision])} ${time}`
}

/** Month and year — when a model was released — or '' when unknown. */
export function monthYear(iso: string | undefined): string {
  const d = parse(iso)
  return d === null ? '' : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short' })
}

/**
 * A window picker's choice as a sentence ends it: "the last 24 hours", "the
 * last 7 days". An empty table says which window it is empty for, rather
 * than "in this window", which made the reader look up at the picker.
 */
export function lastDays(days: number): string {
  return days === 1 ? 'the last 24 hours' : `the last ${days} days`
}

/** "3 minutes ago", give or take, for something that happened. */
export function ago(iso: string | undefined): string {
  if (iso === undefined || iso === '') return 'never'
  const d = parse(iso)
  if (d === null) return ''
  const seconds = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

/**
 * "in 7h", "12m ago": either side of now, enough to judge a token or a reset
 * at a glance.
 *
 * `now` is what to say inside the minute either side. Without it that minute
 * reads "0m ago" or "in 0m", which is right for a countdown about to land and
 * odd for a reading that has just been taken.
 */
export function relative(iso: string | undefined, now?: string): string {
  const d = parse(iso)
  if (d === null) return 'unknown'
  const delta = d.getTime() - Date.now()
  const mins = Math.round(Math.abs(delta) / 60000)
  if (now !== undefined && mins < 1) return now
  const text =
    mins < 60 ? `${mins}m` : mins < 1440 ? `${Math.round(mins / 60)}h` : `${Math.round(mins / 1440)}d`
  return delta < 0 ? `${text} ago` : `in ${text}`
}

/**
 * Seconds left as a clock, "14:59", for a countdown read while it ticks.
 * duration's whole minutes sat on "14m" for a minute, flipped between 15m and
 * 14m as the second rounded, and read "0m" for all of the last minute.
 */
export function countdown(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

/** A duration in seconds as "3d 4h", for uptime. */
export function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** The span between two instants in the units someone reads off a clock. */
export function between(first: string, last: string): string {
  const ms = new Date(last).getTime() - new Date(first).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const mins = Math.round(ms / 60000)
  if (mins < 1) return '<1m'
  if (mins < 60) return `${mins}m`
  const h = Math.floor(mins / 60)
  return `${h}h ${mins % 60}m`
}

/** A latency, in seconds once it is long enough to matter. */
export function millis(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}
