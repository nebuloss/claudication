import {
  createContext,
  useContext,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { Tip } from './info'
import { rowClick } from './link'
import { MenuRow } from './menu'
import { MenuDivider, Popover, useAnchoredPanel } from './popover'

/**
 * A column that can be sorted by clicking its heading.
 *
 * `sorted` is set only on the one column currently in effect, so the table has
 * exactly one direction indicator and `aria-sort` says the same thing.
 */
export type Column = {
  label: string
  /**
   * Called with no direction to turn the column around, or with one to set it.
   * The heading button uses the first; the menu, where there is one, offers
   * both explicitly, because a menu that toggles is a menu whose two entries
   * do the same thing.
   */
  onSort?: (dir?: 'asc' | 'desc') => void
  sorted?: 'asc' | 'desc'
  /**
   * A panel to drop under the heading — a column of checkboxes, typically.
   * When present the heading opens this instead of sorting, and the sort moves
   * inside it, so one click on a column title reaches everything that column
   * can do.
   */
  menu?: ReactNode
  /** This column is narrowing the table, so the heading can say so. */
  filtered?: boolean
  /**
   * 'end' for a column of figures: the heading sits over the right edge its
   * Td align="end" numbers line up on, so units and digits stack.
   */
  align?: 'end'
}

/**
 * A column heading that opens a menu: its sort and whatever col.menu holds.
 * The popup's behaviour — portalled, following the heading, scrolling inside
 * — is popover.tsx's.
 */
function HeaderMenu({ col, children }: { col: Column; children: ReactNode }) {
  const p = useAnchoredPanel({ width: 264 })
  const done = () => p.close()

  return (
    <>
      <button
        ref={p.anchor}
        type="button"
        onClick={p.toggle}
        aria-haspopup="menu"
        aria-expanded={p.open}
        className={`state-layer group flex w-full items-center gap-1.5 px-3 py-2.5 uppercase ${
          col.align === 'end' ? 'justify-end text-right' : 'text-left'
        } ${
          col.sorted !== undefined || col.filtered === true
            ? 'text-on-surface'
            : 'hover:text-on-surface'
        }`}
      >
        {col.label}
        {/* Which way it is sorted, when it is. */}
        <svg
          viewBox="0 0 10 6"
          aria-hidden
          className={`h-2 w-3 shrink-0 fill-current ${col.sorted === 'asc' ? 'rotate-180' : ''} ${
            col.sorted !== undefined ? 'opacity-100' : 'opacity-0'
          }`}
        >
          <path d="M0 0h10L5 6z" />
        </svg>
        {/* A funnel when the column is narrowing, so a filtered table says
            which column did it without opening anything. Otherwise a chevron
            that appears on hover, which is what says the heading opens. */}
        {col.filtered === true ? (
          <svg viewBox="0 0 12 12" aria-hidden className="h-3 w-3 shrink-0 fill-primary">
            <path d="M1 2h10L7 6.5V11L5 9.5V6.5z" />
          </svg>
        ) : (
          <svg
            viewBox="0 0 10 6"
            aria-hidden
            className="h-2 w-3 shrink-0 fill-current opacity-0 group-hover:opacity-50"
          >
            <path d="M0 0h10L5 6z" />
          </svg>
        )}
      </button>
      {p.at !== null && (
        <Popover panelRef={p.panel} at={p.at} role="menu" label={col.label}>
          {col.onSort !== undefined && (
            <>
              <MenuRow
                onClick={() => {
                  col.onSort?.('asc')
                  done()
                }}
                active={col.sorted === 'asc'}
              >
                Sort ascending
              </MenuRow>
              <MenuRow
                onClick={() => {
                  col.onSort?.('desc')
                  done()
                }}
                active={col.sorted === 'desc'}
              >
                Sort descending
              </MenuRow>
              <MenuDivider />
            </>
          )}
          {children}
        </Popover>
      )}
    </>
  )
}

/**
 * Column widths an operator has dragged, remembered per table.
 *
 * In localStorage because it is a preference about this browser, not a fact
 * about the gateway: two people looking at the same instance want their own
 * widths, and a width does not belong in a database that gets backed up
 * alongside the sealing key. Every read and write is guarded — private
 * windows, cleared site data and blocked storage all throw rather than return
 * empty — and a failure means unresized columns, which is the state everyone
 * starts in anyway.
 */
function loadWidths(key: string): Record<string, number> {
  try {
    const raw = window.localStorage.getItem(`claudication.cols.${key}`)
    if (raw === null) return {}
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return {}
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'number' && Number.isFinite(v) && v >= MIN_COL) out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

function saveWidths(key: string, widths: Record<string, number>) {
  try {
    window.localStorage.setItem(`claudication.cols.${key}`, JSON.stringify(widths))
  } catch {
    // Not worth telling anyone about: the table still works, it just forgets.
  }
}

/** Narrow enough to be a deliberate choice, wide enough to still be grabbable. */
const MIN_COL = 56

/**
 * Whether a table sits inside another's unfolded row. The outer one is the
 * tile; one inside it is drawn flat — no second tile, a ruled heading rather
 * than a band — so an unfolded row reads as part of its table.
 */
const Nested = createContext(false)

/**
 * A scrollable table. Wide content must never make the page scroll.
 *
 * Drawn on its own tile — the lightest surface, rounded and ringed — with its
 * headings on a tinted band, so a table stands out from the card it sits on
 * and its heading from its rows, in either theme. A table inside another's
 * unfolded row is flat (Nested).
 *
 * `cap` bounds the height and pins the header inside it, for a list that grows
 * without limit: fifty rows is most of a screen, and a "load more" that
 * appends fifty at a time pushes everything after the table somewhere no one
 * will scroll to. Sticky sits on the cells rather than the row, because a
 * sticky `thead` is still not honoured everywhere.
 */
export function Table({
  head,
  children,
  cap = false,
  resizable,
  stack = false,
}: {
  /** A plain label, or a column that can be sorted. */
  head: (string | Column)[]
  children: ReactNode
  cap?: boolean
  /**
   * Enable column resizing, remembering widths under this key.
   *
   * Opt-in rather than always on: most tables here are four columns of numbers
   * that need no adjusting, and a drag handle on every heading is a thing to
   * hit by accident. It earns its place on the wide ones, where what you want
   * to read — a chat name, a model list — is exactly what gets truncated.
   */
  resizable?: string
  /**
   * On a phone, each row as a small card rather than a strip to scroll
   * sideways: the cells side by side two to a line, each under its column's
   * name (Td's label), and a cell with no label — the row's name, a detail
   * unfolded under it — across the whole card. For a table whose rows are
   * read one at a time; a log scanned down a column still wants columns.
   */
  stack?: boolean
}) {
  const nested = useContext(Nested)
  const [widths, setWidths] = useState<Record<string, number>>(() =>
    resizable === undefined ? {} : loadWidths(resizable),
  )
  // The drag in flight. In a ref because it changes on every pointer move and
  // none of those should re-render anything but the one column being dragged.
  const drag = useRef<{ label: string; startX: number; startW: number } | null>(null)

  const labels = head.map((entry, i) => {
    const label = typeof entry === 'string' ? entry : entry.label
    return label === '' ? `blank-${i}` : label
  })

  const onDown = (label: string) => (e: ReactPointerEvent<HTMLDivElement>) => {
    const th = (e.currentTarget.parentElement as HTMLElement | null) ?? null
    if (th === null) return
    e.preventDefault()
    e.stopPropagation()
    drag.current = { label, startX: e.clientX, startW: th.getBoundingClientRect().width }
    // Captured on the handle, so a fast drag that outruns the pointer keeps
    // resizing instead of stopping the moment the cursor leaves the 6px strip.
    e.currentTarget.setPointerCapture(e.pointerId)
  }

  const onMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (d === null) return
    const next = Math.max(MIN_COL, Math.round(d.startW + (e.clientX - d.startX)))
    setWidths((w) => ({ ...w, [d.label]: next }))
  }

  const onUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (drag.current === null) return
    drag.current = null
    e.currentTarget.releasePointerCapture(e.pointerId)
    if (resizable !== undefined) saveWidths(resizable, widths)
  }

  return (
    // isolate for the same reason the code viewer does: a capped table's
    // sticky heading needs to sit above its own rows, not above the page.
    <div
      className={`isolate ${cap ? 'max-h-[30rem] overflow-auto' : 'overflow-x-auto'} ${
        nested ? '' : 'rounded-md3-l bg-surface-lowest ring-1 ring-outline-variant/60'
      }`}
    >
      <table
        className={`text-sm [&_td]:align-middle ${stack ? 'table-stack' : ''} ${
          resizable === undefined
            ? // A stacked table is as wide as the card on a phone, which is
              // the point of stacking it.
              stack
              ? 'w-full sm:min-w-max'
              : 'w-full min-w-max'
            : // Column rules, on a resizable table only.
              //
              // On the cells, heading included, so each edge is one unbroken
              // line from the top of the table to the bottom. It was two
              // before — a rule the resize handle drew inside the heading, and
              // a border on the body cells — which met at the heading's own
              // bottom border and left a visible step where they did not quite
              // line up.
              //
              // Lighter than the row rules: these run the height of the table
              // and there are ten of them, so at full strength they read as a
              // spreadsheet rather than a list. Never on the last column,
              // where the rule would be the table's own right edge drawn
              // twice.
              'min-w-full [&_td]:border-r [&_th]:border-r [&_td]:border-outline-variant/50 [&_th]:border-outline-variant/50 [&_td:last-child]:border-r-0 [&_th:last-child]:border-r-0'
        }`}
        // Fixed only once something has been dragged: until then the browser
        // sizes the columns to their content, which is the better default and
        // the one every other table here relies on.
        style={
          resizable !== undefined && Object.keys(widths).length > 0
            ? { tableLayout: 'fixed' }
            : undefined
        }
      >
        {resizable !== undefined && Object.keys(widths).length > 0 && (
          <colgroup>
            {labels.map((label) => (
              <col key={label} style={widths[label] ? { width: widths[label] } : undefined} />
            ))}
          </colgroup>
        )}
        <thead>
          <tr>
            {/* The rule sits on the cells, not the row: a sticky cell carries
                its own border along, a row's border stays where it started. */}
            {head.map((entry, i) => {
              const col: Column = typeof entry === 'string' ? { label: entry } : entry
              const key = labels[i] ?? col.label
              // A sortable heading has no padding of its own: the button
              // carries it, so the whole cell is the target rather than just
              // the few words in the middle of it.
              return (
                <th
                  key={col.label === '' ? `blank-${i}` : col.label}
                  aria-sort={
                    col.sorted === undefined
                      ? undefined
                      : col.sorted === 'asc'
                        ? 'ascending'
                        : 'descending'
                  }
                  className={`relative text-[11px] font-semibold tracking-wider text-on-surface-variant uppercase ${
                    col.align === 'end' ? 'text-right' : 'text-left'
                  } ${
                    // The table's edge inset is the cells': 20px. A heading
                    // whose button carries 12px of it takes the other 8.
                    col.onSort === undefined && col.menu === undefined
                      ? 'px-3 py-2.5 first:pl-5 last:pr-5'
                      : 'p-0 first:pl-2 last:pr-2'
                  } ${
                    nested
                      ? `border-b border-outline-variant ${cap ? 'sticky top-0 z-10 bg-surface-lowest' : ''}`
                      : `bg-surface-container ${cap ? 'sticky top-0 z-10' : ''}`
                  }`}
                >
                  {col.menu !== undefined ? (
                    <HeaderMenu col={col}>{col.menu}</HeaderMenu>
                  ) : col.onSort === undefined ? (
                    col.label
                  ) : (
                    <button
                      type="button"
                      // Wrapped, not passed: onSort takes an optional
                      // direction, and handing it the click event straight
                      // would make every heading "sort by MouseEvent".
                      onClick={() => col.onSort?.()}
                      className={`state-layer group flex w-full items-center gap-1.5 px-3 py-2.5 uppercase ${
                        col.align === 'end' ? 'justify-end text-right' : 'text-left'
                      } ${
                        col.sorted ? 'text-on-surface' : 'hover:text-on-surface'
                      }`}
                    >
                      {col.label}
                      {/* Its own tight viewBox rather than a glyph inside a
                          24-unit one, where the caret is a third of the box and
                          renders at about a third of its nominal size. Always
                          rendered, so the heading does not jump wider the
                          moment it becomes the sorted column. */}
                      <svg
                        viewBox="0 0 10 6"
                        aria-hidden
                        className={`h-2 w-3 shrink-0 fill-current ${
                          col.sorted === 'asc' ? 'rotate-180' : ''
                        } ${col.sorted ? 'opacity-100' : 'opacity-0 group-hover:opacity-50'}`}
                      >
                        <path d="M0 0h10L5 6z" />
                      </svg>
                    </button>
                  )}
                  {/* The grab strip. Last column excluded: dragging it widens
                      the table into space that is not there, which reads as
                      the whole layout jumping. */}
                  {resizable !== undefined && i < head.length - 1 && (
                    <div
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={`Resize ${col.label}`}
                      onPointerDown={onDown(key)}
                      onPointerMove={onMove}
                      onPointerUp={onUp}
                      onPointerCancel={onUp}
                      onDoubleClick={() => {
                        // Back to content width: the escape hatch for a column
                        // dragged to something unreadable.
                        setWidths((w) => {
                          const { [key]: _drop, ...rest } = w
                          if (resizable !== undefined) saveWidths(resizable, rest)
                          return rest
                        })
                      }}
                      className="group/resize absolute inset-y-0 -right-[5px] z-20 flex w-[10px] cursor-col-resize touch-none select-none justify-center"
                    >
                      {/* Nothing until it is pointed at: the edge is already
                          drawn, by the cells, all the way down. This only says
                          which one you are about to take hold of, and it sits
                          over the top of that line rather than beside it.

                          The strip around it stays wide. A 1px target is
                          nearly impossible to hit with a mouse, and the strip
                          is what you actually grab. */}
                      <span
                        aria-hidden
                        className="h-full w-0 bg-primary transition-all group-hover/resize:w-[3px]"
                      />
                    </div>
                  )}
                </th>
              )
            })}
          </tr>
        </thead>
        <tbody>
          <Nested.Provider value>{children}</Nested.Provider>
        </tbody>
      </table>
    </div>
  )
}


/**
 * A table row, ruled under like every other.
 *
 * `onClick` makes the whole row the target, with a hover fill to say so —
 * except over a link or a button inside it, which keeps its own click rather
 * than also firing the row's (the rule openRow follows for a list row);
 * `selected` tints the row whose detail is open under it, and a `total` row
 * — a remainder, not one more item — is tinted and ruled above instead.
 */
export function Tr({
  children,
  onClick,
  selected = false,
  total = false,
}: {
  children: ReactNode
  onClick?: () => void
  selected?: boolean
  total?: boolean
}) {
  const rule = total ? 'border-t border-outline-variant' : 'border-b border-outline-variant/50 last:border-0'
  return (
    <tr
      onClick={onClick !== undefined ? rowClick(onClick) : undefined}
      className={`${rule} transition-colors hover:bg-surface-low ${
        onClick !== undefined ? 'state-layer cursor-pointer' : ''
      } ${selected ? 'bg-primary-container/25 hover:bg-primary-container/35' : ''} ${
        total ? 'bg-surface-low font-medium' : ''
      }`}
    >
      {children}
    </tr>
  )
}

/**
 * What a selected row unfolds: one cell across the table, tinted like the
 * row above it so the two read as one item, and ruled under where the item
 * ends.
 */
export function TrDetail({ colSpan, children }: { colSpan: number; children: ReactNode }) {
  return (
    <tr className="border-b border-outline-variant/50 bg-primary-container/25 last:border-0">
      <td colSpan={colSpan} className="px-4 pt-1 pb-4">
        {children}
      </td>
    </tr>
  )
}

/**
 * The button that unfolds a row's TrDetail: a chevron that turns as it opens.
 * A real button, so the keyboard and a screen reader reach the detail as a
 * pointer does by clicking anywhere on the row.
 */
export function RowToggle({ open, onToggle, label }: { open: boolean; onToggle: () => void; label: string }) {
  return (
    <Tip tip={label}>
      <button
        type="button"
        aria-expanded={open}
        aria-label={label}
        onClick={onToggle}
        className="state-layer grid size-7 shrink-0 place-items-center rounded-full text-on-surface-variant"
      >
        <svg
          viewBox="0 0 24 24"
          aria-hidden
          className={`size-5 fill-current transition-transform ${open ? 'rotate-90' : ''}`}
        >
          <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
        </svg>
      </button>
    </Tip>
  )
}

/** Column caps for a cell that truncates rather than taking the width it wants. */
const MAX = { 9: 'max-w-[9rem]', 11: 'max-w-[11rem]', 18: 'max-w-[18rem]' } as const

/**
 * A table cell. The flags are the only ways cells here differ: `muted` for a
 * secondary column, `nums` for figures that should line up, `nowrap` for a
 * value that must stay on one line, `mono` for an identifier, and `max` (in
 * rem) for a column that truncates. `align="end"` sets a figure against the
 * right edge, under a heading aligned the same way, so a column of 12k, 1.2M
 * and 980 reads by its magnitude. `label` is the column's name again, which a
 * stacked table prints over the cell on a phone, where the heading is gone.
 */
export function Td({
  children,
  muted = false,
  nums = false,
  nowrap = false,
  mono = false,
  max,
  title,
  colSpan,
  align,
  label,
}: {
  children?: ReactNode
  muted?: boolean
  nums?: boolean
  nowrap?: boolean
  mono?: boolean
  max?: keyof typeof MAX
  title?: string
  colSpan?: number
  align?: 'end'
  label?: string
}) {
  return (
    <td
      colSpan={colSpan}
      data-label={label}
      className={`px-3 py-2.5 first:pl-5 last:pr-5 ${muted ? 'text-on-surface-variant' : ''} ${nums ? 'tabular-nums' : ''} ${
        nowrap ? 'whitespace-nowrap' : ''
      } ${max !== undefined ? MAX[max] : ''} ${align === 'end' ? 'text-right' : ''}`}
    >
      {/* The tip holds the whole content, so a truncated cell's full text is
          on the cell's own hover rather than only over its visible part. */}
      <Tip tip={title} block>
        {max !== undefined || mono ? (
          <div className={`${max !== undefined ? 'truncate' : ''} ${mono ? 'font-mono text-xs' : ''}`}>
            {children}
          </div>
        ) : (
          children
        )}
      </Tip>
    </td>
  )
}
