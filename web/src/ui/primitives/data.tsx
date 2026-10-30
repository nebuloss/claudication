import type { ReactNode } from 'react'
import { toneContainer, toneInk, type Tone } from '../styles/tone'
import { Tip } from './info'
import { tile } from './surface'

/** Figures and states, as the screens print them: chips, stats, label/value pairs. */

/** A state at a glance: serving, cooling down, 3 models off. */
export function Chip({ children, tone = 'neutral' }: { children: ReactNode; tone?: Tone }) {
  return (
    <span
      className={`inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md3-s px-2.5 text-xs font-medium ${toneContainer[tone]}`}
    >
      {children}
    </span>
  )
}

/**
 * A chip that is a choice already made, and the way to unmake it: a filter
 * in force, pressed to take it off. Outlined rather than filled, so a row of
 * them reads as controls and not as states.
 */
export function InputChip({
  children,
  onRemove,
  title,
}: {
  children: ReactNode
  onRemove: () => void
  title?: string
}) {
  return (
    <Tip tip={title}>
      <button
        type="button"
        onClick={onRemove}
        className="state-layer inline-flex h-7 items-center gap-1.5 rounded-md3-s border border-outline px-2.5 text-xs font-medium text-on-surface-variant"
      >
        {children}
        <span aria-hidden className="text-base leading-none">
          &times;
        </span>
      </button>
    </Tip>
  )
}

/**
 * The small capitals a label is set in: above a stat, beside a value, over a
 * field that is not an input.
 */
export function Overline({ children, as: Tag = 'div' }: { children: ReactNode; as?: 'div' | 'span' | 'dt' | 'h3' }) {
  return (
    <Tag className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">{children}</Tag>
  )
}

/** A headline number with its label. The unit of the Overview tab. */
export function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string
  value: ReactNode
  hint?: ReactNode
  tone?: Tone
}) {
  return (
    <div className={`${tile} px-4 py-3`}>
      <Overline>{label}</Overline>
      <div className={`mt-1 text-2xl leading-8 font-normal tabular-nums ${toneInk[tone ?? 'neutral']}`}>
        {value}
      </div>
      {hint !== undefined && <div className="mt-0.5 text-xs text-on-surface-variant">{hint}</div>}
    </div>
  )
}

/**
 * Stats side by side, two to a row on a phone. `columns` is how many fit on a
 * wide screen; each count has one layout, so every row of four in the app
 * breaks at the same width.
 */
const COLUMNS = {
  // Two at every width: a grid that shares its card with something beside
  // it, where four across would be four slivers.
  2: 'grid-cols-2',
  3: 'grid-cols-2 lg:grid-cols-3',
  4: 'grid-cols-2 sm:grid-cols-4',
  5: 'grid-cols-2 sm:grid-cols-3 lg:grid-cols-5',
} as const

export function StatGrid({ columns = 4, children }: { columns?: keyof typeof COLUMNS; children: ReactNode }) {
  return <div className={`grid gap-3 ${COLUMNS[columns]}`}>{children}</div>
}

/** Label/value pairs, dense enough to scan. */
export function KeyValue({ items }: { items: [string, ReactNode][] }) {
  return (
    // items-baseline, because the label is 12px and the value 14px: left to
    // stretch, each starts at the top of its own cell and the two sit visibly
    // out of step. Baselines are what the eye reads a label/value pair by.
    <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-4 gap-y-1.5 text-sm">
      {items.map(([label, value]) => (
        <div key={label} className="contents">
          <Overline as="dt">{label}</Overline>
          <dd className="m-0 min-w-0 break-words text-on-surface">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

/** A figure with its label in front, inline: "Rate 200 / minute". */
export function Figure({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span>
      <span className="text-on-surface-variant">{label}</span> {children}
    </span>
  )
}

/**
 * A row of figures beside an item's name: its chips and its Figures, in the
 * small type of a row. The account and key lists and their pages' headers
 * use it, so a key reads the same in the list and on its page.
 */
export function Figures({ children }: { children: ReactNode }) {
  return (
    <span className="flex flex-wrap items-center gap-3 text-xs tabular-nums text-on-surface">{children}</span>
  )
}
