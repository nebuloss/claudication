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
