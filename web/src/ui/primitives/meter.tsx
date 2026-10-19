import type { ReactNode } from 'react'
import { toneFill, type Tone } from '../styles/tone'

/**
 * A horizontal fill for how much of something is used: a quota window, a
 * budget.
 *
 * `sm` is the short bar that sits inline beside a figure in a list row; `md`
 * spans its container, under a heading. `percent` null draws the track alone,
 * for a figure that has never been read — distinct from 0, which still shows
 * a sliver so a reading of nothing looks read.
 */
export function UsageBar({
  percent,
  tone,
  size = 'md',
}: {
  percent: number | null
  tone: Tone
  size?: 'sm' | 'md'
}) {
  const track = size === 'sm' ? 'h-1.5 w-12' : 'h-2 w-full'
  return (
    <span className={`block shrink-0 overflow-hidden rounded-full bg-surface-high ${track}`}>
      {percent !== null && (
        <span
          className={`block h-full rounded-full ${toneFill[tone]}`}
          style={{ width: `${Math.max(1, Math.min(100, percent))}%` }}
        />
      )}
    </span>
  )
}

/**
 * A meter under its heading: what is measured on the left, the figure on the
 * right with a word on how to read it, and the bar beneath. The quota windows
 * and the gateway's memory are both this, so both were drawn twice.
 *
 * `alert` sets the figure in the error colour, for a window about to refuse
 * or memory about to run out; `active` raises the title, for the one window
 * doing the limiting.
 */
export function MeterRow({
  title,
  figure,
  hint,
  alert = false,
  active = false,
  children,
}: {
  title: ReactNode
  figure: ReactNode
  hint?: ReactNode
  alert?: boolean
  active?: boolean
  /** The bar, and anything that belongs under it. */
  children: ReactNode
}) {
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3 text-xs">
        <span className={active ? 'font-medium text-on-surface' : 'text-on-surface-variant'}>{title}</span>
        <span className={`tabular-nums ${alert ? 'font-medium text-error' : 'text-on-surface'}`}>
          {figure}
          {hint !== undefined && <span className="ml-2 font-normal text-on-surface-variant">{hint}</span>}
        </span>
      </div>
      {children}
    </div>
  )
}
