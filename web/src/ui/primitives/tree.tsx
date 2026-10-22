import { useCallback, useState, type CSSProperties, type ReactNode } from 'react'
import { Checkbox } from './field'
import { tile } from './surface'

/**
 * A checklist folded into groups: the models lists put a provider's families
 * inside the provider, and a family's models inside it.
 *
 * Checkboxes, not switches. A list of twenty switches read as twenty
 * settings, two columns of them — groups' on the right, items' on the left
 * — and the mixed group was a switch stuck halfway that looked broken. A
 * checklist is what the list is: which of these are in.
 *
 * One box per visible thing. An open group shows its items, each with its
 * own box, and no box of its own: a box over boxes that are right there says
 * the same thing twice. A folded group shows one tri-state box in the column
 * its items' boxes would take, standing for them. To set a whole group from
 * the keyboard, fold it — Enter on its heading — and the box is the next Tab
 * stop; the list's own All and None do every group at once.
 *
 * The geometry is a step of 1.5rem per level: a group's chevron sits in the
 * step, its box (when folded) in the 2rem after it, its name after that. An
 * item one level down puts its box in that same 2rem, so a family's folded
 * box and its models' boxes share a column, and a model's name starts where
 * its family's does: the hierarchy is told by the outdented chevron and the
 * heading's weight, not by more controls.
 */

const STEP = 1.5 // rem per level

function indent(level: number): CSSProperties {
  return { paddingLeft: `${level * STEP}rem` }
}

/**
 * The tree's box: a tile that scrolls on its own, because an account lists a
 * model per release, and a page that grows with them puts the toolbar above
 * the list a long scroll from the row being looked for. A size container, so
 * a row lays its facts out in columns when the list is wide, whatever the
 * window around it.
 */
export function Tree({ children, label }: { children: ReactNode; label: string }) {
  return (
    <ul
      aria-label={label}
      className={`${tile} @container m-0 flex max-h-[min(36rem,70vh)] list-none flex-col divide-y divide-outline-variant overflow-y-auto overscroll-contain px-1 py-1 @3xl:px-2`}
    >
      {children}
    </ul>
  )
}

/** The words a group's heading uses for itself, by level: provider, family. */
const HEADING = ['text-sm font-semibold text-on-surface', 'text-sm font-medium text-on-surface'] as const

export function TreeGroup({
  title,
  count,
  open,
  onToggle,
  state,
  onSet,
  disabled,
  level = 0,
  children,
}: {
  title: string
  /** What is in of what there is: "3 of 4". */
  count: string
  open: boolean
  /**
   * Folds it. Left out while the list is filtered: every match is shown, so
   * the heading is a label over its matches and nothing to fold.
   */
  onToggle?: () => void
  state: 'on' | 'off' | 'mixed'
  /** The folded group's box: every item in it in, or every one out. */
  onSet: (on: boolean) => void
  disabled?: boolean
  level?: 0 | 1
  children: ReactNode
}) {
  const name = (
    <>
      {/* The box's 2rem and the label's gap after it, so the heading starts
          where its items' names do. */}
      <span className="w-9 shrink-0" aria-hidden />
      <span className={`min-w-0 truncate ${HEADING[level]}`}>{title}</span>
      <span className="ml-2 shrink-0 text-xs text-on-surface-variant tabular-nums">{count}</span>
    </>
  )
  return (
    <li className={`list-none ${level === 0 ? 'py-1' : ''}`}>
      {/* The heading is one button across the row, so the whole line folds;
          the box sits over it in its own cell, a sibling rather than inside
          a button, where an input may not be. */}
      <div className="grid grid-cols-[1.5rem_2rem_minmax(0,1fr)] items-center" style={indent(level)}>
        {onToggle !== undefined ? (
          <button
            type="button"
            aria-expanded={open}
            aria-label={`${open ? 'Fold' : 'Unfold'} ${title}`}
            onClick={onToggle}
            className="state-layer col-span-full row-start-1 flex h-9 min-w-0 items-center rounded-md3-s pr-2 text-left text-on-surface-variant"
          >
            <span className="grid w-6 shrink-0 place-items-center">
              <svg
                viewBox="0 0 24 24"
                aria-hidden
                className={`size-5 fill-current transition-transform ${open ? 'rotate-90' : ''}`}
              >
                <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
              </svg>
            </span>
            {name}
          </button>
        ) : (
          <div className="col-span-full row-start-1 flex h-9 min-w-0 items-center pl-6">{name}</div>
        )}
        {!open && (
          <span className="relative z-10 col-start-2 row-start-1 grid place-items-center">
            <Checkbox
              dense
              quiet
              label={`All of ${title}`}
              checked={state === 'on'}
              indeterminate={state === 'mixed'}
              onChange={onSet}
              disabled={disabled}
            />
          </span>
        )}
      </div>
      {open && <ul className="m-0 list-none p-0">{children}</ul>}
    </li>
  )
}

/**
 * One item of the checklist, on one line where the list is wide: its box and
 * name, then its facts in columns, then whatever acts on it (`end`). Each row
 * is its own grid, so the columns line up because their widths are fixed:
 * `columns` gives the facts', the name's is capped, and end takes the rest.
 * Narrow, the facts wrap under the name, in line with it.
 */
export function TreeItem({
  level,
  label,
  name,
  checked,
  onChange,
  disabled,
  facts = [],
  columns = '',
  end,
}: {
  level: number
  /** What the box is called: the model's name. */
  label: string
  /** Shown for the label, when it is more than the words. */
  name?: ReactNode
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  facts?: ReactNode[]
  /** The fixed widths of the facts' columns, as a grid template. */
  columns?: string
  end?: ReactNode
}) {
  return (
    <li
      className="flex list-none flex-col py-1 @3xl:grid @3xl:[grid-template-columns:var(--cols)] @3xl:items-center @3xl:gap-x-3 @3xl:py-0"
      // The name's column is capped, so the facts stay beside the name they
      // describe rather than across a wide list from it; the room left over
      // goes to `end`, set against the right edge.
      style={{ ...indent(level), '--cols': `minmax(8rem,14rem) ${columns} minmax(0,1fr)` } as CSSProperties}
    >
      <Checkbox dense label={label} checked={checked} onChange={onChange} disabled={disabled}>
        {/* An item that is out reads quieter, so what is in stands out on a
            scan without reading every box. */}
        <span className={`min-w-0 truncate ${checked ? 'text-on-surface' : 'text-on-surface-variant'}`}>
          {name ?? label}
        </span>
      </Checkbox>
      {facts.length > 0 && (
        <div className="flex flex-wrap gap-x-3 pl-9 text-xs text-on-surface-variant @3xl:contents">
          {/* Narrow, the first fact — the item's id — takes a line of its
              own and the rest share the next, so a row is always three
              lines rather than one fact orphaned on a fourth. */}
          {facts.map((f, i) => (
            <span
              key={i}
              className={`min-w-0 truncate empty:hidden @3xl:empty:block ${i === 0 ? 'basis-full @3xl:basis-auto' : ''}`}
            >
              {f}
            </span>
          ))}
        </div>
      )}
      {end !== undefined && (
        <div className="flex min-w-0 flex-wrap items-center gap-2 pt-1 pl-9 @3xl:justify-end @3xl:pt-0 @3xl:pl-0">
          {end}
        </div>
      )}
    </li>
  )
}

/**
 * Which groups of one list are open, and the way to fold one: the defaults
 * until the operator folds something, then what they left, remembered per
 * list (`storageKey`) in this browser. Only what was changed is kept, so a
 * family that appears tomorrow takes the default rather than inheriting a
 * fold nobody made. A storage that throws — a private window, blocked site
 * data — leaves the folds working for the visit and forgotten after.
 */
export function useFolds(storageKey: string, defaults: ReadonlySet<string>) {
  const key = `claudication:folds:${storageKey}`
  const [changed, setChanged] = useState<Record<string, boolean>>(() => {
    try {
      const raw = window.localStorage.getItem(key)
      const parsed: unknown = raw === null ? {} : JSON.parse(raw)
      return typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, boolean>) : {}
    } catch {
      return {}
    }
  })
  const isOpen = useCallback((group: string) => changed[group] ?? defaults.has(group), [changed, defaults])
  const toggle = useCallback(
    (group: string) =>
      setChanged((c) => {
        const next = { ...c, [group]: !(c[group] ?? defaults.has(group)) }
        try {
          window.localStorage.setItem(key, JSON.stringify(next))
        } catch {
          // Remembered for this visit only.
        }
        return next
      }),
    [defaults, key],
  )
  return { isOpen, toggle }
}
