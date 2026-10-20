import type { ReactNode } from 'react'

/** Picking one of a few: a segmented control, a tab bar, a reporting window. */

/**
 * A segmented control: one bordered group, divided into options.
 *
 * The group carries the boundary rather than each option, which is what keeps a
 * row of three from looking like three separate buttons — and means the
 * unselected options are still visibly part of a control at rest.
 */
export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
  size = 'sm',
}: {
  options: { id: T; label: string; content: ReactNode }[]
  value: T
  onChange: (id: T) => void
  label: string
  /** md is the app bar's 40px, beside its other controls. */
  size?: 'sm' | 'md'
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex overflow-hidden rounded-md3-full border border-outline"
    >
      {options.map((o, i) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          title={o.label}
          onClick={() => onChange(o.id)}
          className={`state-layer grid ${size === 'md' ? 'h-10 min-w-10' : 'h-8 min-w-8'} place-items-center px-3 text-xs font-medium ${
            i > 0 ? 'border-l border-outline' : ''
          } ${
            value === o.id
              ? 'bg-secondary-container text-on-secondary-container'
              : 'text-on-surface-variant'
          }`}
        >
          {o.content}
        </button>
      ))}
    </div>
  )
}

/**
 * How many days a report looks back over, as a Segmented of "1d 7d 30d".
 *
 * Three screens drew this by hand, each with its own list of windows; the
 * list is still theirs to choose — the overview's chart wants a fortnight —
 * but the control and its labels are one.
 */
export function WindowPicker({
  value,
  windows,
  onChange,
  label = 'Reporting window',
}: {
  value: number
  windows: readonly number[]
  onChange: (days: number) => void
  label?: string
}) {
  return (
    <Segmented
      label={label}
      value={String(value)}
      onChange={(v) => onChange(Number(v))}
      // One day is the last 24 hours, not the calendar day "1d" reads as:
      // every window here is rolling back from now.
      options={windows.map((d) =>
        d === 1
          ? { id: '1', label: 'the last 24 hours', content: '24h' }
          : { id: String(d), label: `${d} days`, content: `${d}d` },
      )}
    />
  )
}

/**
 * A secondary tab bar, for choosing which view of one screen to look at.
 *
 * A smaller sibling of the app's own nav rather than a second copy of it: the
 * same underline, but two thirds the type, half the padding and a thinner
 * marker, so it reads as subordinate to the tab that got you here instead of
 * competing with it.
 *
 * Buttons rather than links, because this picks a view inside a screen and the
 * hash already belongs to the screen.
 *
 * `aside` rides at the right of the same rule — an action that belongs to the
 * selected view, with the border still running the full width behind it.
 */
export function SubNav<T extends string>({
  options,
  value,
  onChange,
  label,
  aside,
}: {
  options: { id: T; label: string }[]
  value: T
  onChange: (id: T) => void
  label: string
  aside?: ReactNode
}) {
  return (
    <div className="-mx-1 overflow-x-auto px-1">
      <div
        role="tablist"
        aria-label={label}
        className="flex w-full min-w-max items-center gap-1 border-b border-outline-variant"
      >
        {options.map((o) => (
          <button
            key={o.id}
            type="button"
            role="tab"
            aria-selected={value === o.id}
            onClick={() => onChange(o.id)}
            // 14px, not 12. These are the primary navigation of the screen
            // they sit on — the thing you read to decide where to go — and at
            // text-xs they were set smaller than the table rows underneath
            // them, which inverts the hierarchy: the labels mattered more and
            // looked like a footnote.
            className={`state-layer relative rounded-t-md3-s px-3.5 py-2.5 text-sm font-medium whitespace-nowrap ${
              value === o.id ? 'text-primary' : 'text-on-surface-variant'
            }`}
          >
            {o.label}
            {value === o.id && (
              <span className="absolute inset-x-1 bottom-0 h-[2px] rounded-t-full bg-primary" />
            )}
          </button>
        ))}
        {aside !== undefined && <div className="ml-auto pb-1 pl-4">{aside}</div>}
      </div>
    </div>
  )
}
