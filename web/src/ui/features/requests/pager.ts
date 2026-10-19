/** Rows per page. Fifty is about seven minutes of history on a busy gateway. */
export const PAGE = 50

/**
 * A page as the API answers it. Generic over the row, which need only say when
 * it happened: the pager is pure, and checked by `make test-web` without the
 * API client.
 */
type Stamped = { at: string }
type Page<R extends Stamped> = { rows: R[]; nextCursor: string }

/**
 * The rows of the log held so far, and the cursor to the next page: the one
 * panel that accumulates rather than replaces, which is why it is not
 * useLoader's. Immutable, so each way the list can change is a method that
 * says what it does to what is already on screen.
 */
export class RequestLogPager<R extends Stamped> {
  constructor(
    readonly rows: readonly R[] = [],
    /** Where the next page starts; '' once everything kept has been read. */
    readonly cursor: string = '',
  ) {}

  /** A first page: a new filter, or Refresh. Nothing held survives it. */
  static first<R extends Stamped>(page: Page<R>): RequestLogPager<R> {
    return new RequestLogPager(page.rows, page.nextCursor)
  }

  /** Load more: the next page under what is held. */
  more(page: Page<R>): RequestLogPager<R> {
    return new RequestLogPager([...this.rows, ...page.rows], page.nextCursor)
  }

  /**
   * A poll's first page, merged without undoing paging. Re-reading the first
   * page and replacing the list would throw away every "Load more" the
   * operator has pressed, so this takes only the rows newer than the newest
   * held and puts them above.
   *
   * Unless a whole page of them is new, which means the gateway has served
   * more than a page since the last tick and there is a hole between what
   * arrived and what is held. Starting over is the honest answer to that — a
   * list with a silent gap in the middle is worse than a short one — and so
   * is the page's own cursor, which the hole would otherwise sit behind.
   */
  merged(page: Page<R>): RequestLogPager<R> {
    if (this.rows.length === 0) return RequestLogPager.first(page)
    const newest = this.rows.reduce((m, r) => Math.max(m, new Date(r.at).getTime()), 0)
    const fresh = page.rows.filter((r) => new Date(r.at).getTime() > newest)
    if (fresh.length === 0) return this
    if (fresh.length >= PAGE) return RequestLogPager.first(page)
    return new RequestLogPager([...fresh, ...this.rows], this.cursor)
  }
}
