import type { ReactNode } from 'react'
import { Button } from './button'
import { follow, openRow } from './link'

/**
 * One item of a list that opens a page per item: an account, an API key.
 *
 * One component for both, so the two lists read as one family — a rounded
 * row, its identity on the left, its state in the middle, and a chevron that
 * says the whole row opens it. The whole row is the target; links and buttons
 * inside it keep their own clicks.
 */
export function ListRow({
  href,
  leading,
  title,
  subtitle,
  chips,
  meters,
  actions,
  highlight = false,
}: {
  href: string
  /** A rank, an icon: what this item is at a glance. */
  leading?: ReactNode
  title: string
  subtitle?: ReactNode
  /** Its state, as chips. */
  chips?: ReactNode
  /** Its figures: windows, limits, budgets. */
  meters?: ReactNode
  /** Controls beside the chevron, like moving it in an order. */
  actions?: ReactNode
  /** The item in use right now, tinted. */
  highlight?: boolean
}) {
  return (
    <div
      onClick={openRow(href)}
      className={`state-layer flex cursor-pointer flex-wrap items-center gap-x-4 gap-y-2 rounded-md3-l px-4 py-3 ${
        highlight ? 'bg-secondary-container' : 'bg-surface-lowest'
      }`}
    >
      {/* basis-48: the name's share before the row wraps. With none, the
          figures beside it fitted on a phone by squeezing the name to
          nothing, so a list of keys showed no key names. */}
      <div className="flex min-w-0 flex-1 basis-48 items-center gap-3">
        {leading}
        <div className="min-w-0">
          {/* A link for the keyboard and for opening in a new tab, drawn as a
              title: the whole row already says it opens. */}
          <a href={href} onClick={follow(href)} className="block truncate font-medium text-on-surface">
            {title}
          </a>
          {subtitle !== undefined && (
            <span className="block truncate text-xs text-on-surface-variant tabular-nums">{subtitle}</span>
          )}
        </div>
      </div>
      {chips !== undefined && <div className="flex flex-wrap items-center gap-1">{chips}</div>}
      {meters !== undefined && (
        <div className="flex flex-wrap items-center gap-3 text-xs tabular-nums text-on-surface">{meters}</div>
      )}
      <div className="flex items-center gap-1">
        {actions}
        <span aria-hidden className="grid size-9 place-items-center text-on-surface-variant">
          <svg viewBox="0 0 24 24" className="size-5 fill-current">
            <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
          </svg>
        </span>
      </div>
    </div>
  )
}

/**
 * The row above a list: its add action first, where the eye starts, and the
 * list's own figures and freshness at the far end. Above the list's card
 * rather than inside it, so the card starts with the list.
 */
export function ListToolbar({ action, children }: { action?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      {action}
      <div className="ml-auto flex items-center gap-3">{children}</div>
    </div>
  )
}

/**
 * A figure about the whole list, at the toolbar's far end: on a wide screen
 * only, since on a phone the list is right under it saying the same.
 */
export function ListSummary({ children }: { children: ReactNode }) {
  return (
    <span className="hidden text-sm whitespace-nowrap text-on-surface-variant sm:inline">{children}</span>
  )
}

/** A list's add action: filled, with a plus, so it reads as the way to grow the list. */
export function AddButton({
  onClick,
  disabled = false,
  children,
}: {
  onClick: () => void
  disabled?: boolean
  children: ReactNode
}) {
  return (
    <Button onClick={onClick} disabled={disabled} icon="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z">
      {children}
    </Button>
  )
}

/** The round badge a row leads with: a rank, or an icon. */
export function RowBadge({ children }: { children: ReactNode }) {
  return (
    <span className="grid size-7 shrink-0 place-items-center rounded-full bg-surface-high text-xs font-medium text-on-surface-variant">
      {children}
    </span>
  )
}
