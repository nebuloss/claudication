/**
 * What a coloured thing means, separately from how a given primitive paints
 * it.
 *
 * Chip, Banner and Stat each grew their own union for this ('ok' | 'warn' |
 * 'error' | 'neutral', 'error' | 'warn', 'ok' | 'error'), and the two quota
 * meters spelled the colours out inline with different thresholds, so a
 * window at 92% read red on the account list and amber on the account's own
 * page. One type, and
 * one place mapping it onto the MD3 roles in index.css, is what keeps a
 * meaning the same colour everywhere.
 */
export type Tone = 'ok' | 'warn' | 'error' | 'neutral'

/** A filled container and the ink that sits on it: chips, banners. */
export const toneContainer: Record<Tone, string> = {
  ok: 'bg-success-container text-on-success-container',
  warn: 'bg-warning-container text-on-warning-container',
  error: 'bg-error-container text-on-error-container',
  neutral: 'bg-surface-high text-on-surface-variant',
}

/** Text in that meaning, on an ordinary surface: a headline figure. */
export const toneInk: Record<Tone, string> = {
  ok: 'text-success',
  warn: 'text-warning',
  error: 'text-error',
  neutral: 'text-on-surface',
}

/**
 * A solid fill, such as a bar's: the role colour itself. Neutral is the
 * accent, for a figure that is only a figure and passes no judgement.
 */
export const toneFill: Record<Tone, string> = {
  ok: 'bg-success',
  warn: 'bg-warning',
  error: 'bg-error',
  neutral: 'bg-primary',
}
