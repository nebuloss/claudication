import type { ReactNode } from 'react'
import { ErrorBanner } from './feedback'
import { Tile } from './surface'

/**
 * One name bound to what it serves, in its tile: what it is and a hint on
 * the right, its controls in a row (a menu, a field), and under them what it
 * serves now. A client's model name and a Settings › Models family are the
 * same kind of thing — a name and a menu of what answers to it — so they are
 * drawn by this one row, and look alike because they are alike.
 */
export function BindingRow({
  title,
  hint,
  status,
  error = '',
  children,
}: {
  title: ReactNode
  hint?: ReactNode
  /** Under the controls: what it serves now, and why not when it cannot. */
  status?: ReactNode
  error?: string
  /** The controls, laid out in one wrapping row. */
  children: ReactNode
}) {
  return (
    <Tile>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium text-on-surface">{title}</span>
          {hint !== undefined && <span className="text-xs text-on-surface-variant">{hint}</span>}
        </div>
        <div className="flex flex-wrap items-center gap-3">{children}</div>
        {status !== undefined && (
          <div className="flex flex-wrap items-center gap-2 text-xs text-on-surface-variant empty:hidden">{status}</div>
        )}
        <ErrorBanner error={error} />
      </div>
    </Tile>
  )
}
