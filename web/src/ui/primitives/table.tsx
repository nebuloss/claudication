import { useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
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
        className={`state-layer group flex w-full items-center gap-1.5 px-2 py-2 text-left ${
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
 * A scrollable table. Wide content must never make the page scroll.
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
}) {
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
    <div className={`isolate -mx-2 px-2 ${cap ? 'max-h-[30rem] overflow-auto' : 'overflow-x-auto'}`}>
      <table
        className={`text-sm [&_td]:align-middle ${
          resizable === undefined
            ? 'w-full min-w-max'
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
                  className={`relative border-b border-outline text-left text-xs font-semibold tracking-wide text-on-surface-variant uppercase ${
                    col.onSort === undefined && col.menu === undefined ? 'px-2 py-2' : 'p-0'
                  } ${cap ? 'sticky top-0 z-10 bg-surface-container' : ''}`}
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
                      className={`state-layer group flex w-full items-center gap-1.5 px-2 py-2 text-left ${
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
                      onPointerDown={onDown(labels[i])}
                      onPointerMove={onMove}
                      onPointerUp={onUp}
                      onPointerCancel={onUp}
                      onDoubleClick={() => {
                        // Back to content width: the escape hatch for a column
                        // dragged to something unreadable.
                        setWidths((w) => {
                          const { [labels[i]]: _drop, ...rest } = w
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
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}


/**
 * A table row, ruled under like every other.
 *
 * `onClick` makes the whole row the target, with a hover fill to say so;
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
  const rule = total ? 'border-t' : 'border-b last:border-0'
  return (
    <tr
      onClick={onClick}
      className={`${rule} border-outline-variant ${
        onClick !== undefined ? 'state-layer cursor-pointer hover:bg-surface-container' : ''
      } ${selected || total ? 'bg-surface-container/40' : ''}`}
    >
      {children}
    </tr>
  )
}

/** Column caps for a cell that truncates rather than taking the width it wants. */
const MAX = { 9: 'max-w-[9rem]', 11: 'max-w-[11rem]', 18: 'max-w-[18rem]' } as const

/**
 * A table cell. The flags are the only ways cells here differ: `muted` for a
 * secondary column, `nums` for figures that should line up, `nowrap` for a
 * value that must stay on one line, `mono` for an identifier, and `max` (in
 * rem) for a column that truncates.
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
}: {
  children?: ReactNode
  muted?: boolean
  nums?: boolean
  nowrap?: boolean
  mono?: boolean
  max?: keyof typeof MAX
  title?: string
  colSpan?: number
}) {
  return (
    <td
      title={title}
      colSpan={colSpan}
      className={`px-2 py-2 ${muted ? 'text-on-surface-variant' : ''} ${nums ? 'tabular-nums' : ''} ${
        nowrap ? 'whitespace-nowrap' : ''
      } ${max !== undefined ? MAX[max] : ''}`}
    >
      {max !== undefined || mono ? (
        <div className={`${max !== undefined ? 'truncate' : ''} ${mono ? 'font-mono text-xs' : ''}`}>
          {children}
        </div>
      ) : (
        children
      )}
    </td>
  )
}
