import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  ApiError,
  api,
  messageOf,
  type FacetValue,
  type RequestFacets,
  type RequestRow,
} from '../../api/client'
import {
  Banner,
  Modal,
  Chip,
  Segmented,
  Empty,
  KeyValue,
  MenuRow,
  Spinner,
  Table,
  type Column,
  TonalButton,
  TextButton,
  Verbatim,
  compact,
  copyText,
} from '../primitives'

/**
 * Every request that reached the gateway, newest first.
 *
 * Its own screen rather than a panel of Usage, because they answer different
 * questions over different rows. Usage is what was *spent* — it groups by key,
 * by model, by account, and every one of those is a thing a request had. The
 * log is what *arrived*, including requests that spent nothing because they
 * were refused, and those have no key to group under.
 *
 * Filters come off the URL so a link can carry them: /requests?chat=…&status=failed
 * is what the chat table links to, which is why that table needs no request
 * list of its own. A URL is also something an operator can edit, share and
 * bookmark, which a filter held only in component state never is.
 */

const PAGE = 50

/** Columns whose first click should sort A-Z, because that is what sorted means for a word. */
const TEXT_COLUMNS: SortKey[] = ['model', 'client', 'ip', 'chat', 'kind', 'message']

/**
 * The activity list, paged.
 *
 * Not useLoader: this is the one panel that accumulates rather than replaces,
 * so it holds its own rows and the cursor for the next page. Fifty rows is
 * about seven minutes of history on a busy gateway, which made "recent" the
 * only thing this could ever answer.
 */
/**
 * The columns that hold a set of values rather than one.
 *
 * A menu is a column of checkboxes, so "sonnet or haiku" has to be sayable;
 * one tick is a one-element set and needs no special case. An empty string is
 * a real member — a refused request has no model, the gateway's own titling
 * rows carry no client — so `?model=` means the rows with nothing there.
 */
export const SET_FIELDS = ['chat', 'key', 'model', 'client', 'ip', 'code', 'message', 'kind'] as const
export type SetField = (typeof SET_FIELDS)[number]

/** The filters this screen reads off the URL, and writes back to it. */
export type RequestFilter = Record<SetField, string[]> & {
  /**
   * A predicate over statuses rather than one of them, which is why it is not
   * in `code`: 429 and 500 are two values, "anything that went wrong" is a
   * question about codes plus stream errors. The two compose.
   */
  status: string
}

/** Nothing narrowed. The one place the field list is spelled out. */
export function emptyFilter(): RequestFilter {
  return { chat: [], key: [], model: [], client: [], ip: [], code: [], message: [], kind: [], status: '' }
}

/** The query string as a filter. Unknown parameters are ignored. */
export function filterFromSearch(search: string): RequestFilter {
  const q = new URLSearchParams(search)
  const out = emptyFilter()
  // getAll, because a set travels as a repeated parameter: a model name is not
  // ours to reserve a comma in.
  for (const field of SET_FIELDS) out[field] = q.getAll(field)
  // Only one value means anything today; anything else reads as unfiltered
  // rather than as an error, because a URL is something people edit.
  out.status = q.get('status') === 'failed' ? 'failed' : ''
  return out
}

/** A filter as a query string, without the path in front of it. */
export function queryFromFilter(f: RequestFilter): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(f)) {
    if (Array.isArray(v)) {
      for (const one of v) q.append(k, one)
    } else if (v !== '') {
      q.set(k, v)
    }
  }
  return q.toString()
}

/** A filter as a path, for a link that carries it. */
export function searchFromFilter(f: RequestFilter): string {
  const s = queryFromFilter(f)
  return s === '' ? '/requests' : `/requests?${s}`
}

/**
 * A link to this screen, narrowed to whatever the caller names.
 *
 * Callers pass only what they mean. Spelling out every empty field at each
 * call site is how a new field silently fails to compile somewhere else — which
 * is exactly what adding `client` did.
 */
export function requestsHref(narrow: Partial<RequestFilter>): string {
  return searchFromFilter({ ...emptyFilter(), ...narrow })
}

/** With a value added, or taken out if it was already there. */
function toggled(current: string[], value: string): string[] {
  return current.includes(value) ? current.filter((v) => v !== value) : [...current, value]
}

export default function RequestsPanel({ onExpired }: { onExpired: () => void }) {
  // Read once per mount and on Back, so a link lands on its filter and the
  // browser's history still works.
  const [filter, setFilter] = useState<RequestFilter>(() =>
    filterFromSearch(window.location.search),
  )
  useEffect(() => {
    const onPop = () => setFilter(filterFromSearch(window.location.search))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const narrow = (next: RequestFilter) => {
    setFilter(next)
    window.history.pushState(null, '', searchFromFilter(next))
  }

  return <RecentRequests filter={filter} onNarrow={narrow} onExpired={onExpired} />
}

function RecentRequests({
  filter,
  onNarrow,
  onExpired,
}: {
  filter: RequestFilter
  onNarrow: (f: RequestFilter) => void
  onExpired: () => void
}) {
  const [rows, setRows] = useState<RequestRow[]>([])
  const [cursor, setCursor] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [shown, setShown] = useState<RequestRow | null>(null)
  const [sort, setSort] = useState<Sort>({ key: 'at', dir: 'desc' })
  const [facets, setFacets] = useState<RequestFacets | null>(null)

  // What the column menus can offer, counted over the whole log rather than
  // the page on screen — a menu built from fifty rows can only offer what you
  // are already looking at, which is what made the old model picker pointless.
  // Its own request because it is not paged and the list is: recomputing five
  // GROUP BYs per scroll would pay for all of history on every page.
  const loadFacets = useCallback(() => {
    api
      .requestFacets()
      .then((res) => setFacets(res.facets ?? null))
      // No banner: the table is fine without menus, and the values already
      // filtered still show so they can be cleared.
      .catch(() => undefined)
  }, [])

  useEffect(loadFacets, [loadFacets])

  // Clicking the column already in effect turns it around; clicking a new one
  // starts at the end people mean first — newest, biggest, worst — except for
  // the two text columns, where A-Z is what "sorted" means.
  const sortBy = (key: SortKey, dir?: 'asc' | 'desc') =>
    setSort((prev) => {
      if (dir !== undefined) return { key, dir }
      if (prev.key === key) return { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
      return { key, dir: TEXT_COLUMNS.includes(key) ? 'asc' : 'desc' }
    })

  const column = (label: string, key: SortKey): Column => ({
    label,
    onSort: (dir) => sortBy(key, dir),
    sorted: sort.key === key ? sort.dir : undefined,
  })

  // Which facet belongs to which filter field. One place, because the menus
  // and the chips both need it and they were answering it differently: a chat
  // read as its name in the menu and as sixteen hex characters in the chip
  // that the menu had just set.
  const facetFor = (field: SetField): FacetValue[] => {
    switch (field) {
      case 'model':
        return facets?.models ?? []
      case 'client':
        return facets?.clients ?? []
      case 'ip':
        return facets?.ips ?? []
      case 'code':
        return facets?.statuses ?? []
      case 'chat':
        return facets?.chats ?? []
      case 'message':
        return facets?.messages ?? []
      case 'kind':
        return facets?.kinds ?? []
      default:
        return []
    }
  }

  // What to call a filtered value. The facet's label where there is one — a
  // chat's name, "no answer" for a status of 0 — and the value itself
  // otherwise, which is the common case and already readable.
  const labelOf = (field: SetField, value: string): string => {
    const hit = facetFor(field).find((v) => v.value === value)
    return hit?.label !== undefined && hit.label !== '' ? hit.label : value
  }

  // The same heading, plus the values that column actually holds. Clicking the
  // title opens them; clicking a cell still narrows to that one row's value,
  // which is the faster move when what you want is already in front of you.
  const faceted = (label: string, key: SortKey, field: SetField): Column => ({
    ...column(label, key),
    filtered: filter[field].length > 0,
    menu: (
      <FacetList
        values={facetFor(field)}
        chosen={filter[field]}
        loaded={facets !== null}
        onToggle={(v) => onNarrow({ ...filter, [field]: toggled(filter[field], v) })}
        onClear={() => onNarrow({ ...filter, [field]: [] })}
      />
    ),
  })

  const load = useCallback(
    async (after: string) => {
      const first = after === ''
      first ? setLoading(true) : setLoadingMore(true)
      setError('')
      try {
        const page = await api.recentRequests(PAGE, after, filter)
        // Append when paging, replace when refreshing, so Refresh cannot leave
        // a stale tail below freshly loaded rows.
        setRows((prev) => (first ? page.rows : [...prev, ...page.rows]))
        setCursor(page.nextCursor)
      } catch (err) {
        if (err instanceof ApiError && err.isUnauthenticated) {
          onExpired()
          return
        }
        setError(messageOf(err))
      } finally {
        first ? setLoading(false) : setLoadingMore(false)
      }
    },
    // filter is part of the query, so a change to it makes a different list.
    [onExpired, filter],
  )

  useEffect(() => {
    // From the first page: the cursor belongs to the previous filter, and
    // paging on with it would append rows from a list nobody asked for.
    setRows([])
    setCursor('')
    void load('')
  }, [load])

  // Sorted here rather than by the server: the cursor is a keyset on (at, id),
  // so a different order would need a different cursor. This orders the rows
  // that have been loaded — press Load more to sort over more of them.
  const data = useMemo(() => sortRows(rows, sort), [rows, sort])

  // One chip per value, not per column: a column filtered to three models is
  // three things you might want to stop doing, and a single chip that dropped
  // all of them would make the third undoable only by re-ticking two.
  const pinned = SET_FIELDS.flatMap((field) =>
    filter[field].map((value) => ({ field, value })),
  )
  const narrowed = pinned.length > 0 || filter.status !== ''

  const query = queryFromFilter(filter)
  const downloadQuery = query === '' ? '' : `?${query}`

  // No card and no title of its own: it is the body of a tab now, and the tab
  // is already called Requests.
  return (
    <>
      {/* The controls, and then what the URL is asking for. Both: the pickers
          say what can be narrowed, the summary says what is narrowed — and a
          filtered list that looks like the whole list is how someone concludes
          the gateway served four requests all week. */}
      <div className="mb-4 flex flex-wrap items-end gap-3">
        <Segmented
          label="Show"
          value={filter.status === 'failed' ? 'failed' : 'all'}
          onChange={(v) => onNarrow({ ...filter, status: v === 'failed' ? 'failed' : '' })}
          options={[
            { id: 'all', label: 'All requests', content: 'All' },
            { id: 'failed', label: 'Failures only', content: 'Failures' },
          ]}
        />

kind control        <span className="flex-grow" />
        {/* An anchor rather than a fetch-and-blob: the browser already knows
            how to save a response it is given, it carries the session cookie
            on its own, and a download that streams from the server never has
            the whole file in the page.

            The same filter as the table, so what lands in the file is what was
            on the screen — including the rows below the ones loaded, which is
            the reason to download rather than select and copy. */}
        <a
          href={`/admin/requests/export${downloadQuery}`}
          className="state-layer inline-flex h-10 items-center rounded-[var(--radius-md3-full)] border border-outline px-3 text-sm font-medium text-primary"
        >
          Download CSV
        </a>
        <TextButton onClick={() => void load('')}>Refresh</TextButton>
      </div>

      {/* Only the filters with no control of their own. Show and Kind already
          say what they are set to, so a chip repeating them was two places to
          read one fact. These arrive from a click, a menu or a link, and the
          heading's funnel says which column without saying which value. */}
      {pinned.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-on-surface-variant">Narrowed to</span>
          {pinned.map(({ field, value }) => (
            <button
              key={`${field}:${value}`}
              type="button"
              onClick={() =>
                onNarrow({ ...filter, [field]: filter[field].filter((v) => v !== value) })
              }
              title={`Stop filtering by this ${fieldWord(field)}`}
              className="state-layer inline-flex h-7 items-center gap-1.5 rounded-[var(--radius-md3-s)] border border-outline px-2.5 text-xs font-medium text-on-surface-variant"
            >
              {fieldWord(field)}: {chipValue(labelOf(field, value))}
              <span aria-hidden className="text-base leading-none">
                &times;
              </span>
            </button>
          ))}
        </div>
      )}

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      {loading ? (
        <p className="flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : data === null || data.length === 0 ? (
        <Empty>
          {narrowed ? 'No requests match this filter.' : 'No requests recorded yet.'}
        </Empty>
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
            faceted('Model', 'model', 'model'),
            faceted('Chat', 'chat', 'chat'),
            faceted('Client', 'client', 'client'),
            faceted('IP', 'ip', 'ip'),
            faceted('Status', 'status', 'code'),
            faceted('Forwarded', 'kind', 'kind'),
            column('Tokens', 'tokens'),
            column('Duration', 'duration'),
            faceted('Message', 'message', 'message'),
          ]}
        >
          {data.map((r, i) => (
            // The whole row opens the log. The Log button it replaces was a
            // control in every row of a table where the row itself did
            // nothing, which is a lot of width spent saying "clickable" — and
            // once every request has a record worth reading, not only the
            // failed ones, a button per row is a button on every row.
            //
            // The cells that filter stop the click before it gets here, so a
            // narrow-by-this-value click does not also open a dialog.
            <tr
              key={`${r.at}-${i}`}
              onClick={() => setShown(r)}
              className="state-layer cursor-pointer border-b border-outline-variant last:border-0 hover:bg-surface-container"
            >
              <td
                title={r.at}
                className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant"
              >
                {stamp(r.at)}
              </td>
              {/* Clickable for the same reason the address is: the model is
                  right there in the row you are reading, and hunting for it
                  again in a list of every model the gateway has ever served is
                  work the row can do for you.

                  Truncated rather than given the width it wants:
                  claude-haiku-4-5-20251001 is 25 characters that differ from
                  its neighbour in the last eight, and at full width it pushed
                  everything after it off the screen. Drag the edge for more. */}
              <td className="max-w-[11rem] px-2 py-2">
                <div className="truncate">
                {r.model === undefined || r.model === '' ? (
                  '—'
                ) : (
                  <Filterable onClick={() => onNarrow({ ...filter, model: [r.model ?? ''] })} title={`Only requests for ${r.model}`}>
                      {r.model}
                    </Filterable>
                )}
                </div>
              </td>
              {/* The id, shortened. Nobody reads a session id, but they do
                  recognise the first characters of the one they are looking
                  at — and the click is what this column is for. */}
              <td className="max-w-[9rem] px-2 py-2 text-on-surface-variant">
                <div className="truncate font-mono text-xs">
                  {r.conversation_id === undefined || r.conversation_id === '' ? (
                    '—'
                  ) : (
                    <Filterable onClick={() => onNarrow({ ...filter, chat: [r.conversation_id ?? ''] })} title={`Only requests in ${r.conversation_id}`}>
                      {r.conversation_id.slice(0, 12)}
                    </Filterable>
                  )}
                </div>
              </td>
              {/* What was running, not who is paying. The key is still what
                  the chip and the links filter on; it just made a poor column,
                  because a key is named by whoever minted it and one called
                  "test" can serve every request on the gateway. */}
              <td className="px-2 py-2 whitespace-nowrap text-on-surface-variant">
                {r.client === undefined || r.client === '' ? (
                  '—'
                ) : (
                  <Filterable onClick={() => onNarrow({ ...filter, client: [r.client ?? ''] })} title={`Only requests from ${r.client}`}>
                      {r.client}
                    </Filterable>
                )}
              </td>
              {/* Clickable, because "everything from that machine" is the next
                  question the moment one address looks wrong. */}
              <td className="px-2 py-2 font-mono text-xs whitespace-nowrap text-on-surface-variant">
                {r.ip === undefined || r.ip === '' ? (
                  '—'
                ) : (
                  <Filterable onClick={() => onNarrow({ ...filter, ip: [r.ip ?? ''] })} title={`Only requests from ${r.ip}`}>
                      {r.ip}
                    </Filterable>
                )}
              </td>
              {/* Clickable like the model and the address. The chip is what
                  you already read to decide the row is interesting, so it is
                  also what you reach for to see the rest of its kind. Narrows
                  on the exact code, which is not the same question as the
                  Failures switch: that one spans every code plus the streams
                  that died after a 200. */}
              <td className="px-2 py-2">
                <Filterable
                  plain
                  onClick={() => onNarrow({ ...filter, code: [String(r.status)] })}
                  title={
                    r.status === 0
                      ? 'Only requests that got no answer'
                      : `Only requests that answered ${r.status}`
                  }
                >
                  <Chip tone={statusTone(r)}>
                    {r.status === 0 ? 'failed' : r.status}
                    {r.streaming && r.status === 200 ? ' ·' : ''}
                  </Chip>
                </Filterable>
              </td>
              {/* What the Kind control above the table used to say. A column,
                  because it is a property of the row like every other filter
                  here, and a control above the table for one of them and
                  menus in the headings for the rest was two idioms for one
                  job. */}
              <td className="px-2 py-2 whitespace-nowrap">
                <Filterable
                  onClick={() =>
                    onNarrow({ ...filter, kind: [r.rejected === true ? 'rejected' : 'relayed'] })
                  }
                  title={
                    r.rejected === true
                      ? 'Only requests this gateway refused'
                      : 'Only requests that reached the upstream'
                  }
                >
                  <span className={r.rejected === true ? 'text-warning' : 'text-on-surface-variant'}>
                    {r.rejected === true ? 'no' : 'yes'}
                  </span>
                </Filterable>
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">
                {r.input_tokens + r.output_tokens > 0
                  ? `${compact(r.input_tokens)} / ${compact(r.output_tokens)}`
                  : '—'}
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">{r.duration_ms}ms</td>
              {/* The stored class rather than the text: the text ends in a
                  request_id, so it is unique per row and filters to one. */}
              <td className="max-w-[18rem] px-2 py-2">
                <div className="truncate text-xs">
                  {r.error_code === undefined || r.error_code === '' ? (
                    <span className="text-on-surface-variant">—</span>
                  ) : (
                    <Filterable
                      onClick={() => onNarrow({ ...filter, message: [r.error_code ?? ''] })}
                      title={r.error}
                    >
                      <span
                        className={
                          r.error_kind === 'content_check'
                            ? 'text-warning'
                            : 'text-on-surface-variant'
                        }
                      >
                        {messageLabel(r.error_code)}
                      </span>
                    </Filterable>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </Table>
      )}

      {data.length > 0 && (
        <div className="mt-4 flex items-center gap-3 border-t border-outline-variant pt-4">
          {cursor !== '' ? (
            <TonalButton onClick={() => void load(cursor)} disabled={loadingMore}>
              {loadingMore && <Spinner />}
              {loadingMore ? 'Loading…' : 'Load more'}
            </TonalButton>
          ) : (
            <span className="text-xs text-on-surface-variant">
              That is everything kept — history goes back {' '}
              <code>usage.retention-days</code>.
            </span>
          )}
          <span className="text-xs text-on-surface-variant" aria-live="polite">
            {data.length} shown
          </span>
        </div>
      )}

      {shown !== null && <RequestLog row={shown} onClose={() => setShown(null)} />}
    </>
  )
}

/**
 * One request, in full.
 *
 * A dialog rather than a panel under the table: the row it belongs to is
 * usually scrolled well out of view by the time anything appears below fifty
 * others, and reading an upstream error means reading all of it — which is the
 * one thing a strip at the bottom of a long list makes hard.
 *
 * Every row opens this, not only the failed ones. A successful request has a
 * record worth reading too — which account served it, what the cache did, how
 * long it took — and the row is the obvious thing to click for it. That is
 * also why the copy here is the whole record rather than the error text: an
 * error pasted into an issue without the model and the account beside it is a
 * sentence with no subject, and a request that worked has no error to paste.
 */
function RequestLog({ row, onClose }: { row: RequestRow; onClose: () => void }) {
  const failed = row.error !== undefined && row.error !== ''

  const tokens =
    row.input_tokens + row.output_tokens > 0
      ? `${compact(row.input_tokens)} in / ${compact(row.output_tokens)} out` +
        (row.cache_tokens > 0 ? ` / ${compact(row.cache_tokens)} cached` : '')
      : '—'

  return (
    <Modal title="Request" onClose={onClose} size="lg">
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <Chip tone={statusTone(row)}>
            {row.status === 0 ? 'failed' : row.status}
            {row.streaming && row.status === 200 ? ' ·' : ''}
          </Chip>
          <span className="text-sm text-on-surface-variant">
            {row.rejected === true ? 'refused by this gateway' : 'forwarded to the upstream'}
          </span>
          <span className="flex-grow" />
          <CopyButton label="Copy record" value={asText(row)} />
        </div>

        {row.error_kind === 'content_check' && (
          <Banner tone="warn">
            <strong>This is a content check, not a quota limit.</strong> The message below talks
            about billing, but adding usage credit will not fix it — the upstream classified this
            request&rsquo;s <em>content</em> as coming from a third-party app rather than from
            Claude Code. The same key usually succeeds on the next request with slightly different
            content, which is what makes it look like a flaky quota problem.
            <br />
            <br />
            Two triggers are known and already rewritten on the way out: a tool named{' '}
            <code>mcp_x</code> instead of <code>mcp__x</code>, and Claude Code&rsquo;s own{' '}
            <code>Is directory a git repo:</code> line inside a foreign system prompt. Seeing this
            anyway means a third trigger — bisect the request body to find it.
          </Banner>
        )}

        <KeyValue
          items={[
            // The exact instant, not "3m ago": this is the value that gets
            // matched against an upstream request_id or somebody else's log.
            ['Timestamp', <span className="tabular-nums">{row.at}</span>],
            ['Outcome', messageLabel(row.error_code) === '—' ? 'no error' : messageLabel(row.error_code)],
            ['Model', dash(row.model)],
            ['Chat', <Mono>{dash(row.conversation_id)}</Mono>],
            ['Client', dash(row.client)],
            ['Key', dash(row.key_name)],
            ['Account', dash(row.account_email)],
            ['IP', <Mono>{dash(row.ip)}</Mono>],
            ['Path', <Mono>{row.path}</Mono>],
            ['Tokens', tokens],
            ['Duration', `${row.duration_ms}ms`],
          ]}
        />

        {failed && (
          <div>
            <div className="mb-2 flex items-center justify-between gap-4">
              <span className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
                What the upstream said
              </span>
              {/* Separate from the record above, because the two get pasted in
                  different places: the record into a message to somebody, this
                  into a search. */}
              <CopyButton label="Copy message" value={row.error ?? ''} />
            </div>
            <Verbatim>{row.error ?? ''}</Verbatim>
          </div>
        )}
      </div>
    </Modal>
  )
}

/** An identifier, in the face that makes one readable. */
function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-xs">{children}</span>
}

/**
 * Copy one thing, and say whether it worked.
 *
 * The failure is worth its own words rather than silence: the clipboard is
 * refused over plain HTTP, which is exactly how this gateway is served behind
 * a proxy that terminates TLS — so the button does nothing and there is no way
 * to tell that from a copy that succeeded.
 */
function CopyButton({ label, value }: { label: string; value: string }) {
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle')

  const copy = async () => {
    if (await copyText(value)) {
      setState('done')
      window.setTimeout(() => setState('idle'), 1500)
      return
    }
    setState('failed')
  }

  return (
    <span className="flex items-center gap-2">
      {state === 'failed' && (
        <span className="text-xs text-error">
          Needs HTTPS — select it and press <span className="font-mono">⌘C</span>
        </span>
      )}
      <TonalButton onClick={() => void copy()}>{state === 'done' ? 'Copied' : label}</TonalButton>
    </span>
  )
}

/**
 * An absolute local timestamp, seconds kept.
 *
 * "3m ago" reads well until the moment it matters, which is lining a request
 * up against something outside this page — an upstream request_id, a service
 * log, whoever reported the error. Then it is the one thing that cannot be
 * matched. Seconds stay because a retry storm puts a dozen rows in one minute.
 *
 * The date is dropped for today's rows, which is nearly all of them at a
 * fortnight's retention, and the full instant is on the row's title either way.
 */
function stamp(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return iso
  const time = at.toLocaleTimeString(undefined, { hour12: false })
  const now = new Date()
  const today =
    at.getFullYear() === now.getFullYear() &&
    at.getMonth() === now.getMonth() &&
    at.getDate() === now.getDate()
  if (today) return time
  return `${at.toLocaleDateString(undefined, { day: '2-digit', month: 'short' })} ${time}`
}

/** The row as something worth pasting somewhere else. */
function asText(r: RequestRow): string {
  // Both spellings of the instant: the ISO one to read, the epoch to grep a
  // log with.
  const epoch = Math.round(new Date(r.at).getTime() / 1000)
  const lines = [
    `timestamp: ${r.at}`,
    `epoch:     ${Number.isNaN(epoch) ? '—' : epoch}`,
    `status:    ${r.status === 0 ? 'failed (no response)' : r.status}`,
    `outcome:   ${messageLabel(r.error_code) === '—' ? 'no error' : messageLabel(r.error_code)}`,
    `forwarded: ${r.rejected === true ? 'no, refused by the gateway' : 'yes'}`,
    `model:     ${dash(r.model)}`,
    `chat:      ${dash(r.conversation_id)}`,
    `client:    ${dash(r.client)}`,
    `key:       ${dash(r.key_name)}`,
    `account:   ${dash(r.account_email)}`,
    `ip:        ${dash(r.ip)}`,
    `path:      ${r.path}`,
    `streaming: ${r.streaming ? 'yes' : 'no'}`,
    `tokens:    ${r.input_tokens} in / ${r.output_tokens} out / ${r.cache_tokens} cache`,
    `duration:  ${r.duration_ms}ms`,
  ]
  // Only when there is one: a blank heading followed by nothing reads as
  // something having gone missing.
  if (r.error !== undefined && r.error !== '') lines.push('', r.error)
  return lines.join('\n')
}

type SortKey =
  | 'at'
  | 'model'
  | 'client'
  | 'ip'
  | 'chat'
  | 'kind'
  | 'message'
  | 'status'
  | 'tokens'
  | 'duration'
type Sort = { key: SortKey; dir: 'asc' | 'desc' }

/**
 * What a row is worth for a given column.
 *
 * Status is the one that is not simply its own value. A request that never got
 * an answer is recorded as 0, which sorts below 200 and would put the worst
 * failures at the far end from the 4xx and 5xx ones — so sorting by status,
 * the thing you do to find what went wrong, would scatter the failures to both
 * ends of the table. Ranked above every real status instead, so one click puts
 * every failure together at the top.
 */
function sortValue(r: RequestRow, key: SortKey): string | number {
  switch (key) {
    case 'at':
      return new Date(r.at).getTime()
    case 'model':
      return r.model ?? ''
    case 'client':
      return r.client ?? ''
    case 'ip':
      return r.ip ?? ''
    case 'chat':
      return r.conversation_id ?? ''
    case 'kind':
      return r.rejected === true ? 'no' : 'yes'
    case 'message':
      return r.error_code ?? ''
    case 'status':
      return r.status === 0 ? 1000 : r.status
    case 'tokens':
      return r.input_tokens + r.output_tokens
    case 'duration':
      return r.duration_ms
  }
}

function sortRows(rows: RequestRow[], sort: Sort): RequestRow[] {
  return [...rows].sort((a, b) => {
    const av = sortValue(a, sort.key)
    const bv = sortValue(b, sort.key)
    const cmp =
      typeof av === 'number' && typeof bv === 'number'
        ? av - bv
        : String(av).localeCompare(String(bv))
    if (cmp !== 0) return sort.dir === 'asc' ? cmp : -cmp
    // Ties always fall back to newest first, whichever way the column runs.
    // Without it, rows sharing a status would shuffle on every re-render.
    return new Date(b.at).getTime() - new Date(a.at).getTime()
  })
}


function dash(v: string | undefined): string {
  return v === undefined || v === '' ? '—' : v
}

function statusTone(r: RequestRow): 'ok' | 'warn' | 'error' {
  if (r.error !== undefined && r.error !== '') return 'error'
  if (r.status === 0 || r.status >= 500) return 'error'
  if (r.status >= 400) return 'warn'
  return 'ok'
}

/** What a filter field is called where someone reads it. */
function fieldWord(field: SetField): string {
  // The query parameter and the column heading are not always the same word,
  // and the chip belongs to the heading: `code` is what the URL says and
  // Status is what the column says, `kind` is what the URL says and Forwarded
  // is what the column says.
  return FIELD_WORDS[field] ?? field
}

const FIELD_WORDS: Partial<Record<SetField, string>> = {
  code: 'status',
  kind: 'forwarded',
}

/** A filtered value in a chip: short enough to sit in one, and never blank. */
function chipValue(value: string): string {
  if (value === '') return 'none'
  return value.length > 26 ? `${value.slice(0, 23)}…` : value
}

/**
 * The checkboxes under a column heading.
 *
 * Anything already filtered stays in the list even when the server did not
 * offer it — past the cap, or arrived from a link — because a tick you cannot
 * see is a filter you cannot lift.
 */
function FacetList({
  values,
  chosen,
  loaded,
  onToggle,
  onClear,
}: {
  values: FacetValue[]
  chosen: string[]
  loaded: boolean
  onToggle: (value: string) => void
  onClear: () => void
}) {
  const offered = new Set(values.map((v) => v.value))
  const rows = [
    ...values,
    ...chosen.filter((v) => !offered.has(v)).map((v): FacetValue => ({ value: v, count: 0 })),
  ]

  if (rows.length === 0) {
    return (
      <p className="px-3 py-2 text-xs font-normal tracking-normal text-on-surface-variant">
        {loaded ? 'Nothing recorded in this column yet.' : 'Loading…'}
      </p>
    )
  }

  return (
    <>
      {chosen.length > 0 && (
        <>
          <MenuRow onClick={onClear}>Clear {chosen.length} selected</MenuRow>
          <div className="my-1 border-t border-outline-variant" />
        </>
      )}
      {rows.map((v) => (
        <label
          key={v.value}
          className="state-layer flex cursor-pointer items-center gap-2 px-3 py-1.5 text-sm font-normal tracking-normal text-on-surface"
        >
          <input
            type="checkbox"
            checked={chosen.includes(v.value)}
            onChange={() => onToggle(v.value)}
            className="size-4 shrink-0 accent-primary"
          />
          <span className="min-w-0 flex-1 truncate" title={v.label ?? v.value}>
            {v.label ?? (v.value === '' ? 'none' : v.value)}
          </span>
          {/* Totals over the whole log, not over what this filter leaves, so
              they do not move as you tick things and cannot talk you into a
              corner you then cannot see the way out of. */}
          {v.count > 0 && (
            <span className="shrink-0 text-xs tabular-nums text-on-surface-variant">
              {compact(v.count)}
            </span>
          )}
        </label>
      ))}
    </>
  )
}

/**
 * A value in a row that narrows the table to itself.
 *
 * One component rather than the same markup in every cell, and it is the one
 * place that knows the click must not reach the row. The row opens the log
 * now, so without this, narrowing to a model would also open a dialog about
 * the request that happened to be carrying it.
 *
 * `plain` for a cell that brings its own appearance — the status chip — where
 * the dotted underline would be drawn through it.
 */
function Filterable({
  onClick,
  title,
  plain = false,
  children,
}: {
  onClick: () => void
  title?: string
  plain?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      title={title}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
      className={
        plain
          ? 'state-layer rounded-[var(--radius-md3-s)]'
          : 'state-layer rounded-[var(--radius-md3-xs)] px-1 underline decoration-dotted underline-offset-2 hover:text-primary'
      }
    >
      {children}
    </button>
  )
}

/**
 * What to call a stored outcome class.
 *
 * The upstream's own words with the underscores taken out, which is what it
 * calls them in its own documentation, plus the two this gateway names itself:
 * a content check, and nothing having come back at all.
 */
const MESSAGE_LABELS: Record<string, string> = {
  content_check: 'refused on content',
  no_answer: 'no answer',
  other: 'failed',
}

function messageLabel(code: string | undefined): string {
  if (code === undefined || code === '') return '—'
  return MESSAGE_LABELS[code] ?? code.replace(/_/g, ' ')
}
