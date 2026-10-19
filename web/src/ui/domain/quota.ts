import type { Tone } from '../styles/tone'

/**
 * How close a subscription window is to its limit.
 *
 * `unknown` is its own level because the gateway reports a window it has
 * never read as -1, and that is not the same as an untouched window: the
 * account list used to clamp it to 0% and draw an empty, healthy-looking bar
 * over an account nobody had measured yet.
 */
export type QuotaLevel = 'unknown' | 'normal' | 'high' | 'critical'

/** At or above this a window is worth watching. */
const HIGH_PCT = 75
/** At or above this it is about to refuse requests. */
const CRITICAL_PCT = 95

/** Severities that mean nothing is wrong: the server's word, the upstream's, and silence. */
const CALM = new Set(['', 'normal', 'allowed'])

/**
 * The level of a window used `pct` percent (0-100, or -1 for never read),
 * given the severity the server attached to it.
 *
 * A severity other than normal wins outright, whatever the figure: it is
 * Anthropic saying the window is warning or refusing, and a figure read a
 * minute ago can lag it.
 */
export function quotaLevel(pct: number, severity = ''): QuotaLevel {
  if (!CALM.has(severity)) return 'critical'
  if (pct < 0) return 'unknown'
  if (pct >= CRITICAL_PCT) return 'critical'
  if (pct >= HIGH_PCT) return 'high'
  return 'normal'
}

const LEVEL_TONE: Record<QuotaLevel, Tone> = {
  unknown: 'neutral',
  normal: 'ok',
  high: 'warn',
  critical: 'error',
}

/**
 * One window of a subscription — the five hours, the week, a per-model week —
 * as both meters draw it: the figure, how it reads, and what colour it is.
 */
export class QuotaWindow {
  /** 0-100, rounded; null when the window has never been read. */
  readonly percent: number | null
  /** The server's severity when it is anything but calm, otherwise ''. */
  readonly flag: string
  readonly level: QuotaLevel

  constructor(util: number, severity = '') {
    this.percent = util < 0 ? null : Math.min(100, Math.round(util))
    this.flag = CALM.has(severity) ? '' : severity
    this.level = quotaLevel(util, severity)
  }

  get tone(): Tone {
    return LEVEL_TONE[this.level]
  }

  /** "42%", or "unknown" for a window never read. */
  get label(): string {
    return this.percent === null ? 'unknown' : `${this.percent}%`
  }
}
