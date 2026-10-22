import { useCallback, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { Checkbox } from './field'
import { tile } from './surface'
import { defaultOpen, leafDepth, place, type Placed, type TreeNode } from './check-tree-state'

/**
 * A checklist folded into groups, drawn from data: the caller says what the
 * nodes are, and the tree works out everything that follows from their
 * shape — depth, indentation, each group's box and count, which groups are
 * open, what a search leaves (check-tree-state, where it is tested).
 *
 * Checkboxes, not switches. A list of twenty switches read as twenty
 * settings, two columns of them — groups' on the right, items' on the left
 * — and the mixed group was a switch stuck halfway that looked broken. A
 * checklist is what the list is: which of these are in.
 *
 * Every group has its tri-state box, open or folded: the hierarchy reads as
 * boxes, each level's standing for everything under it. Hiding an open
 * group's box (tried, on the reasoning that its children's boxes were right
 * there) made a group's state something to work out rather than see.
 *
 * The geometry is one rule at every depth: a row is a chevron's slot, the
 * box, then the name, and each level starts one step in — the step being
 * the box and its gap, so a node's box lands under its parent's name. A
 * model is visibly inside its family the way a family is inside its
 * provider, and a fourth level would sit inside a third the same way.
 */
export interface CheckNode extends TreeNode {
  /** Shown for the label, when it is more than the words. */
  name?: ReactNode
  /** A leaf's facts, one per column where the list is wide. */
  facts?: ReactNode[]
  /** Whatever acts on a leaf, at the end of its row. */
  end?: ReactNode
  children?: readonly CheckNode[]
}

/** rem: the box (2rem) and the gap after it — where a row's name starts past its box. */
const STEP = 2.25
/** rem: the chevron's slot, before every row's box, so a leaf's box lines up with a group's. */
const SLOT = 1.5

/** The words a group's heading uses for itself: the top level bolder than the rest. */
function heading(depth: number): string {
  return depth === 0 ? 'text-sm font-semibold text-on-surface' : 'text-sm font-medium text-on-surface'
}

/**
 * Which groups are open, and the way to fold one: the defaults until the
 * operator folds something, then what they left, remembered per list
 * (`storageKey`) in this browser. Only what was changed is kept, so a group
 * that appears tomorrow takes its default rather than inheriting a fold
 * nobody made. A storage that throws — a private window, blocked site data
 * — leaves the folds working for the visit and forgotten after.
 */
function useFolds(storageKey: string | undefined, defaults: ReadonlySet<string>) {
  const key = storageKey === undefined ? undefined : `claudication:folds:${storageKey}`
  const [changed, setChanged] = useState<Record<string, boolean>>(() => {
    if (key === undefined) return {}
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
        if (key !== undefined) {
          try {
            window.localStorage.setItem(key, JSON.stringify(next))
          } catch {
            // Remembered for this visit only.
          }
        }
        return next
      }),
    [defaults, key],
  )
  return { isOpen, toggle }
}

/**
 * The tree's box: a tile that scrolls on its own, because an account lists a
 * model per release, and a page that grows with them puts the toolbar above
 * the list a long scroll from the row being looked for. A size container, so
 * a leaf lays its facts out in columns when the list is wide, whatever the
 * window around it.
 *
 * `filter` is the search: the leaves it matches, every group above one open,
 * and a group's box and count over what it shows. The search field stays
 * with the caller, beside whatever else acts on what it shows.
 */
export function CheckTree({
  nodes,
  onChange,
  label,
  columns = '',
  storageKey,
  filter = '',
  disabled = false,
}: {
  nodes: readonly CheckNode[]
  /** A box moved: the leaves it stands for — one for a leaf, every one a group's box moves. */
  onChange: (leafKeys: string[], checked: boolean) => void
  label: string
  /** The fixed widths of the leaves' facts columns, as a grid template. */
  columns?: string
  /** Which list this is, for remembering its folds; left out, they last the visit. */
  storageKey?: string
  filter?: string
  /** Every box held still: while a batch is being written. */
  disabled?: boolean
}) {
  const defaults = useMemo(() => defaultOpen(nodes), [nodes])
  const fold = useFolds(storageKey, defaults)
  const searching = filter.trim() !== ''
  const placed = place(nodes, fold.isOpen, filter)
  const slot = nodes.some((n) => n.children !== undefined) ? SLOT : 0
  // Each leaf is its own grid, so its columns line up with the others' only
  // when every leaf has the same template. The name's column is capped, so
  // the facts stay beside the name they describe rather than across a wide
  // list from it, and widened by the deepest leaf's indent, so a shallower
  // leaf's name gets the room rather than its facts starting further left.
  // The room left over goes to `end`, set against the right edge.
  const deepest = leafDepth(nodes) * STEP + slot
  const cols = `minmax(${deepest + 8}rem,${deepest + 14}rem) ${columns} minmax(0,1fr)`

  const row = (p: Placed<CheckNode>): ReactNode => {
    const n = p.node
    const indent = `${p.depth * STEP + slot}rem`
    const off = disabled || p.disabled
    if (!p.group) {
      const facts = n.facts ?? []
      return (
        <li
          key={n.key}
          className="flex list-none flex-col py-1 pl-[var(--indent)] @3xl:grid @3xl:[grid-template-columns:var(--cols)] @3xl:items-center @3xl:gap-x-3 @3xl:py-0 @3xl:pl-0"
          style={{ '--indent': indent, '--cols': cols } as CSSProperties}
        >
          <div className="flex min-w-0 @3xl:pl-[var(--indent)]">
            <Checkbox
              dense
              label={n.label}
              checked={n.checked === true}
              onChange={(v) => onChange(p.leaves, v)}
              disabled={off}
            >
              {/* A leaf that is out reads quieter, so what is in stands out
                  on a scan without reading every box. */}
              <span className={`min-w-0 truncate ${n.checked === true ? 'text-on-surface' : 'text-on-surface-variant'}`}>
                {n.name ?? n.label}
              </span>
            </Checkbox>
          </div>
          {facts.length > 0 && (
            <div className="flex flex-wrap gap-x-3 pl-9 text-xs text-on-surface-variant @3xl:contents">
              {/* Narrow, the first fact — the leaf's id — takes a line of its
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
          {n.end !== undefined && (
            <div className="flex min-w-0 flex-wrap items-center gap-2 pt-1 pl-9 @3xl:justify-end @3xl:pt-0 @3xl:pl-0">
              {n.end}
            </div>
          )}
        </li>
      )
    }
    const name = (
      <>
        {/* The box's 2rem and the gap after it, so the heading starts where
            its children's boxes do. */}
        <span className="w-9 shrink-0" aria-hidden />
        <span className={`min-w-0 truncate ${heading(p.depth)}`}>{n.name ?? n.label}</span>
        <span className="ml-2 shrink-0 text-xs text-on-surface-variant tabular-nums">{p.count}</span>
      </>
    )
    return (
      <li key={n.key} className={`list-none ${p.depth === 0 ? 'py-1' : ''}`}>
        {/* The heading is one button across the row, so the whole line folds;
            the box sits over it in its own cell, a sibling rather than inside
            a button, where an input may not be. While searching there is
            nothing to fold — every match is shown — so it is a label. */}
        <div className="grid grid-cols-[1.5rem_2rem_minmax(0,1fr)] items-center" style={{ paddingLeft: `${p.depth * STEP}rem` }}>
          {!searching ? (
            <button
              type="button"
              aria-expanded={p.open}
              aria-label={`${p.open ? 'Fold' : 'Unfold'} ${n.label}`}
              onClick={() => fold.toggle(n.key)}
              className="state-layer col-span-full row-start-1 flex h-9 min-w-0 items-center rounded-md3-s pr-2 text-left text-on-surface-variant"
            >
              <span className="grid w-6 shrink-0 place-items-center">
                <svg
                  viewBox="0 0 24 24"
                  aria-hidden
                  className={`size-5 fill-current transition-transform ${p.open ? 'rotate-90' : ''}`}
                >
                  <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
                </svg>
              </span>
              {name}
            </button>
          ) : (
            <div className="col-span-full row-start-1 flex h-9 min-w-0 items-center pl-6">{name}</div>
          )}
          <span className="relative z-10 col-start-2 row-start-1 grid place-items-center">
            <Checkbox
              dense
              quiet
              label={`All of ${n.label}`}
              checked={p.state === 'on'}
              indeterminate={p.state === 'mixed'}
              onChange={(v) => onChange(p.leaves, v)}
              disabled={off}
            />
          </span>
        </div>
        {p.open && <ul className="m-0 list-none p-0">{p.children.map(row)}</ul>}
      </li>
    )
  }

  return (
    <ul
      aria-label={label}
      className={`${tile} @container m-0 flex max-h-[min(36rem,70vh)] list-none flex-col divide-y divide-outline-variant overflow-y-auto overscroll-contain px-1 py-1 @3xl:px-2`}
    >
      {placed.length === 0 && (
        <li className="list-none px-3 py-6 text-sm text-on-surface-variant">
          {searching ? `Nothing matches “${filter.trim()}”.` : 'Nothing to list.'}
        </li>
      )}
      {placed.map(row)}
    </ul>
  )
}
