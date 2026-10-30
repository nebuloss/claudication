import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, type RequestFacets } from '../../../api/client'
import {
  Button,
  ButtonLink,
  CardTitle,
  Empty,
  ErrorBanner,
  Freshness,
  InputChip,
  Loading,
  MenuDivider,
  MenuRow,
  Muted,
  Table,
  type Column,
} from '../../primitives'
import { FacetList, Facets } from './facet-menu'
import { RequestFilter, type RequestScope, type SetField } from './filter'
import { RequestRecord, type Sort, type SortKey } from './record'
import { RequestDetail } from './detail'
import { RequestRowView } from './row'
import { RunningRowView, runningMatches, useRunning } from './running'
import { useRequestLog } from './use-request-log'

export { RequestFilter, requestsHref, type FilterFields, type RequestScope } from './filter'

/**
 * Every request that reached the gateway, newest first.
 *
 * Its own screen rather than a panel of Usage, because they answer different
 * questions over different rows. Usage is what was *spent* — it groups by key,
 * by model, by account, and every one of those is a thing a request had. The
 * log is what *arrived*, including requests that spent nothing because they
 * were refused, and those have no key to group under.
 *
 * The folder is the log's parts: filter.ts the URL's filter, record.ts one
 * row's readings, use-request-log.ts the paging and polling, facet-menu.tsx
 * the column menus, row.tsx a row, detail.tsx the dialog, and this file the
 * panel putting them together.
 */

/** What a filter field is called where someone reads it. */
function fieldWord(field: SetField): string {
  // The query parameter and the column heading are not always the same word,
  // and the chip belongs to the heading: `code` is what the URL says and
  // Status is what the column says, `kind` is what the URL says and Forwarded
  // is what the column says.
  return FIELD_WORDS[field] ?? field
}

const FIELD_WORDS: Partial<Record<SetField, string>> = {
  key: 'client',
  client: 'agent',
  code: 'status',
  kind: 'forwarded',
}

/** A filtered value in a chip: short enough to sit in one, and never blank. */
function chipValue(value: string): string {
  if (value === '') return 'none'
  return value.length > 26 ? `${value.slice(0, 23)}…` : value
}

/**
 * The request log. Under an account or a key it is that one's requests: the
 * scope is in the path, so it is always applied and never written into the
 * query string, and the rest of the filter is.
 *
 * `title` is for a log that sits in a card among others, as on Activity's
 * Requests page: the heading takes the toolbar's row, beside its actions,
 * rather than a row of its own above them.
 */
export default function RequestsPanel({ scope = {}, title }: { scope?: RequestScope; title?: string }) {
  // Read once per mount and on Back, so a link lands on its filter and the
  // browser's history still works.
  const [filter, setFilter] = useState(() => RequestFilter.fromSearch(window.location.search))
  useEffect(() => {
    const onPop = () => setFilter(RequestFilter.fromSearch(window.location.search))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const narrow = (next: RequestFilter) => {
    const rest = next.outside(scope)
    setFilter(rest)
    window.history.pushState(null, '', rest.pathIn(scope))
  }

  // What is running now, at the top of what was recorded: a client's own, or
  // all — and none under an account, which a request is only leased to once
  // it is sent. The filter is applied to them as they are (runningMatches).
  const running = scope.account === undefined ? (scope.key ?? '') : null
  return (
    <RecentRequests
      filter={filter.scopedTo(scope)}
      scope={scope}
      onNarrow={narrow}
      title={title}
      running={running}
    />
  )
}

function RecentRequests({
  filter,
  scope,
  onNarrow,
  title,
  running,
}: {
  filter: RequestFilter
  /** The account or client whose page this is: what the page is, not a narrowing. */
  scope: RequestScope
  onNarrow: (f: RequestFilter) => void
  title?: string
  /** Whose running requests head the table: a client's id, '' for all, null for none. */
  running: string | null
}) {
  const [shown, setShown] = useState<RequestRecord | null>(null)
  const [sort, setSort] = useState<Sort>({ key: 'at', dir: 'desc' })
  const [facetData, setFacetData] = useState<RequestFacets | null>(null)
  const facets = useMemo(() => new Facets(facetData), [facetData])

  // Its own request because it is not paged and the list is: recomputing
  // five GROUP BYs per scroll would pay for all of history on every page. The
  // menus are counted over the whole log, so each poll re-reads them too: new
  // rows can add a value that was not offered a minute ago.
  const loadFacets = useCallback(() => {
    api.traffic
      .requestFacets()
      .then(setFacetData)
      // No banner: the table is fine without menus, and the values already
      // filtered still show so they can be cleared.
      .catch(() => undefined)
  }, [])
  useEffect(loadFacets, [loadFacets])

  const log = useRequestLog(filter, loadFacets)
  const live = useRunning(running)
  const runningRows = live.rows.filter((q) => runningMatches(q, filter))

  // Clicking the column already in effect turns it around; clicking a new one
  // starts at the end people mean first — newest, biggest, longest.
  const sortBy = (key: SortKey, dir?: 'asc' | 'desc') =>
    setSort((prev) => {
      if (dir !== undefined) return { key, dir }
      if (prev.key === key) return { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
      return { key, dir: 'desc' }
    })

  const column = (label: string, key: SortKey): Column => ({
    label,
    onSort: (dir) => sortBy(key, dir),
    sorted: sort.key === key ? sort.dir : undefined,
  })

  // The same heading, plus the values that column actually holds. Clicking the
  // title opens them; clicking a cell still narrows to that one row's value,
  // which is the faster move when what you want is already in front of you.
  const faceted = (label: string, field: SetField, lead?: ReactNode): Column => ({
    label,
    filtered: filter.narrows(field),
    menu: (
      <>
        {lead}
        <FacetList
          values={facets.of(field)}
          chosen={filter.values(field)}
          loaded={facets.loaded}
          onToggle={(v) => onNarrow(filter.toggled(field, v))}
          onClear={() => onNarrow(filter.cleared(field))}
        />
      </>
    ),
  })

  // Did it work, which is the question asked of this column far more often
  // than "which code". It lives here rather than above the table because this
  // is the column it is about, and a switch up there was the last filter on
  // the screen that was not where its column is.
  const outcomeRows = (
    <>
      <MenuRow onClick={() => onNarrow(filter.toggledStatus('ok'))} active={filter.status === 'ok'}>
        Successful only
      </MenuRow>
      <MenuRow
        onClick={() => onNarrow(filter.toggledStatus('failed'))}
        active={filter.status === 'failed'}
      >
        Failed only
      </MenuRow>
      <MenuDivider />
    </>
  )

  // This orders the rows that have been loaded — press Load more to sort over
  // more of them.
  const data = useMemo(() => RequestRecord.sorted(log.records, sort), [log.records, sort])

  // One chip per value, not per column: a column filtered to three models is
  // three things you might want to stop doing, and a single chip that dropped
  // all of them would make the third undoable only by re-ticking two.
  //
  // Every filter on this screen, as something you can see and take off. The
  // outcome predicate is reachable from a link — the chat table's failures —
  // and invisible on arrival is the one state it must not have, so it is a
  // chip too.
  //
  // Not the page's own scope: on a client's page every row is the client's,
  // the header says whose, and its chip could not be taken off anyway.
  const chips = filter.outside(scope).pinned().map(({ field, value }) => ({
    key: `${field}:${value}`,
    label: `${fieldWord(field)}: ${chipValue(facets.label(field, value))}`,
    title: `Stop filtering by this ${fieldWord(field)}`,
    clear: () => onNarrow(filter.without(field, value)),
  }))
  if (filter.status !== '') {
    chips.push({
      key: 'status',
      label: filter.status === 'failed' ? 'failures only' : 'successes only',
      title: 'Show every request again',
      clear: () => onNarrow(filter.withStatus('')),
    })
  }

  const query = filter.query()

  // No card, no title and no live mark of its own unless given a title: on
  // an account's or a client's page it is the body of a tab, the tab is
  // already called Requests, and the page's header already says it is live.
  // Nothing sits above the table there but what narrows it.
  return (
    <>
      {title !== undefined && (
        <CardTitle aside={<Freshness updatedAt={log.updatedAt} refreshing={log.refreshing} />}>
          {title}
        </CardTitle>
      )}

      {/* What is narrowed, all of it, in one place. A filtered list that looks
          like the whole list is how someone concludes the gateway served four
          requests all week, and the heading's funnel says which column without
          saying which value. */}
      {chips.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <Muted as="span">Narrowed to</Muted>
          {chips.map((c) => (
            <InputChip key={c.key} onRemove={c.clear} title={c.title}>
              {c.label}
            </InputChip>
          ))}
        </div>
      )}

      <ErrorBanner error={log.error} className="mb-4" />

      {log.loading ? (
        <Loading />
      ) : data.length === 0 && runningRows.length === 0 ? (
        <Empty>{chips.length > 0 ? 'No requests match this filter.' : 'No requests recorded yet.'}</Empty>
      ) : (
        <Table
          cap
          // Widths are remembered per browser: what you want to read here
          // depends on what you are looking for, and a model name, a chat id
          // and an error message cannot all have the room they want at once.
          resizable="requests"
          head={[
            // Not Date and not Time: the cell is a time on today's rows, a
            // date and a time on older ones, and an epoch once copied.
            column('Timestamp', 'at'),
            faceted('Model', 'model'),
            faceted('Chat', 'chat'),
            faceted('Client', 'key'),
            faceted('Agent', 'client'),
            faceted('IP', 'ip'),
            faceted('Status', 'code', outcomeRows),
            faceted('Forwarded', 'kind'),
            column('Tokens', 'tokens'),
            column('Duration', 'duration'),
            faceted('Message', 'message'),
          ]}
        >
          {/* Being served now, on top whatever the sort: they are the newest,
              and the ones an operator is watching. Each becomes a row below
              when it ends. */}
          {runningRows.map((q, i) => (
            <RunningRowView
              key={`running-${q.started_at}-${i}`}
              q={q}
              now={live.now}
              onNarrow={(field, value) => onNarrow(filter.only(field, value))}
            />
          ))}
          {data.map((r, i) => (
            <RequestRowView
              key={`${r.row.at}-${i}`}
              record={r}
              onOpen={() => setShown(r)}
              onNarrow={(field, value) => onNarrow(filter.only(field, value))}
            />
          ))}
        </Table>
      )}

      {data.length > 0 && (
        <div className="mt-4 flex items-center gap-3 border-t border-outline-variant pt-4">
          {log.cursor !== '' ? (
            <Button variant="tonal" onClick={log.more} busy={log.loadingMore}>
              {log.loadingMore ? 'Loading…' : 'Load more'}
            </Button>
          ) : (
            <Muted as="span" size="xs">
              That is everything kept — history goes back <code>usage.retention-days</code>.
            </Muted>
          )}
          <span className="text-xs text-on-surface-variant" aria-live="polite">
            {data.length} shown
          </span>
          <span className="flex-grow" />
          {/* Under the rows rather than above them: it is what you do with
              what you have read, and above the table it took a row of its
              own on every tab.

              An anchor rather than a fetch-and-blob: the browser already
              knows how to save a response it is given, it carries the session
              cookie on its own, and a download that streams from the server
              never has the whole file in the page.

              The same filter as the table, so what lands in the file is what
              was on the screen — including the rows below the ones loaded,
              which is the reason to download rather than select and copy. */}
          <ButtonLink href={`/admin/requests/export${query === '' ? '' : `?${query}`}`} variant="text">
            Download CSV
          </ButtonLink>
        </div>
      )}

      {shown !== null && <RequestDetail record={shown} onClose={() => setShown(null)} />}
    </>
  )
}
