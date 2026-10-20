import type { ReactNode } from 'react'
import { toneFill, type Tone } from '../styles/tone'

/**
 * A horizontal fill for how much of something is used: a quota window, a
 * budget.
 *
 * `sm` is the short bar that sits inline beside a figure in a list row; `md`
 * spans its container, under a heading. A reading of 0, or none at all (null,
 * never read), draws the empty track: a sliver at 0 read as a little use,
 * which is the one thing 0 is not. The figure beside the bar already tells
 * the two apart. Anything above 0 gets at least a sliver, so a small use is
 * still visible.
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
      {percent !== null && percent > 0 && (
        <span
          className={`block h-full rounded-full ${toneFill[tone]}`}
          style={{ width: `${Math.max(1, Math.min(100, percent))}%` }}
        />
      )}
    </span>
  )
}

/**
 * A short bar and its figure on one line, for a row or a table cell: a
 * window's use in a header, a client's share of a window in a column.
 *
 * The figure has a fixed width in ch, so a column of them keeps its bars in
 * line whether a row reads 4%, 43% or <0.1% — the header gauges had their own
 * copy of this with a pixel width that suited only their font size. Inline,
 * so a cell's alignment places it.
 */
export function InlineMeter({
  label,
  percent,
  tone,
  figure,
  title,
  alert = false,
}: {
  /** A short name in front: 5h, 7d. */
  label?: string
  percent: number | null
  tone: Tone
  figure: ReactNode
  title?: string
  /** The figure in the error colour, for a window about to refuse. */
  alert?: boolean
}) {
  return (
    <span className="inline-flex items-center gap-1.5 tabular-nums" title={title}>
      {label !== undefined && <span className="w-5 text-on-surface-variant">{label}</span>}
      <UsageBar size="sm" percent={percent} tone={tone} />
      <span className={`w-[5.5ch] text-right ${alert ? 'text-error' : ''}`}>{figure}</span>
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
