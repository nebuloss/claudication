import type { ReactNode } from 'react'
import { ErrorState, Empty, Loading } from './feedback'
import { Card } from './surface'

/**
 * A table in its own card: what every list of rows in the admin UI is drawn
 * as, so a table looks and behaves the same on every page — its title and
 * the controls that shape it (a window, a freshness mark) in the header, the
 * table, and a note under it when the figures need one.
 *
 * No title when the first column already says what the rows are — a table of
 * clients headed "Clients" says it twice — and then what shapes the rows (a
 * window picker) is a toolbar above the table.
 *
 * The states every table has are said here once: the first read in flight, a
 * read that failed, and no rows. A chart over the same rows is a card of its
 * own above it, never inside it, so the table never shares its card.
 */
export function TableCard({
  title,
  aside,
  ready,
  error = '',
  onRetry,
  busy = false,
  empty,
  note,
  children,
}: {
  title?: ReactNode
  /** The window picker, the freshness mark: what shapes or describes the rows. */
  aside?: ReactNode
  /** Whether there is an answer to show: false while the first read is in flight. */
  ready: boolean
  /** A read that failed with nothing to show instead. */
  error?: string
  onRetry?: () => void
  busy?: boolean
  /** What to say instead of a table with no rows; undefined when there are rows. */
  empty?: ReactNode
  /** A caption under the table: how to read a column, where a figure comes from. */
  note?: ReactNode
  children: ReactNode
}) {
  let body: ReactNode
  if (!ready && error !== '') {
    body = <ErrorState message={error} onRetry={onRetry ?? (() => {})} busy={busy} />
  } else if (!ready) {
    body = <Loading />
  } else if (empty !== undefined) {
    body = <Empty>{empty}</Empty>
  } else {
    body = (
      <>
        {children}
        {note !== undefined && <div className="mt-4">{note}</div>}
      </>
    )
  }
  return (
    <Card title={title} aside={title !== undefined ? aside : undefined}>
      {title === undefined && aside !== undefined && (
        <div className="mb-4 flex flex-wrap items-center gap-2">{aside}</div>
      )}
      {body}
    </Card>
  )
}
