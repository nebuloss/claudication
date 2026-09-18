import { useCallback, useEffect, useMemo, useState } from 'react'
import { ApiError, api, messageOf, type RequestRow } from '../../api/client'
import {
  Banner,
  ErrorModal,
  Chip,
  Segmented,
  Empty,
  KeyValue,
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

/**
 * The activity list, paged.
 *
 * Not useLoader: this is the one panel that accumulates rather than replaces,
 * so it holds its own rows and the cursor for the next page. Fifty rows is
 * about seven minutes of history on a busy gateway, which made "recent" the
 * only thing this could ever answer.
 */
/** The filters this screen reads off the URL, and writes back to it. */
export type RequestFilter = {
  chat: string
  key: string
  model: string
  ip: string
  status: string
  /** '', 'relayed' or 'rejected' — whether it reached the upstream at all. */
  kind: string
}

/** The query string as a filter. Unknown parameters are ignored. */
export function filterFromSearch(search: string): RequestFilter {
  const q = new URLSearchParams(search)
  return {
    chat: q.get('chat') ?? '',
    key: q.get('key') ?? '',
    model: q.get('model') ?? '',
    ip: q.get('ip') ?? '',
    // Only one value means anything today; anything else reads as unfiltered
    // rather than as an error, because a URL is something people edit.
    status: q.get('status') === 'failed' ? 'failed' : '',
    kind: ['relayed', 'rejected'].includes(q.get('kind') ?? '') ? (q.get('kind') as string) : '',
  }
}

/** A filter as a path, for a link that carries it. */
export function searchFromFilter(f: RequestFilter): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(f)) if (v !== '') q.set(k, v)
  const s = q.toString()
  return s === '' ? '/requests' : `/requests?${s}`
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

  // Clicking the column already in effect turns it around; clicking a new one
  // starts at the end people mean first — newest, biggest, worst — except for
  // the two text columns, where A-Z is what "sorted" means.
  const sortBy = (key: SortKey) =>
    setSort((prev) =>
      prev.key === key
        ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
        : { key, dir: key === 'model' || key === 'key_name' ? 'asc' : 'desc' },
    )

  const column = (label: string, key: SortKey): Column => ({
    label,
    onSort: () => sortBy(key),
    sorted: sort.key === key ? sort.dir : undefined,
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

  // Set by clicking a cell or arriving from a link, so nothing else on screen
  // says they are on. Each one is its own chip and removes itself.
  const pinned = Object.entries(filter).filter(
    ([k, v]) => v !== '' && (k === 'chat' || k === 'key' || k === 'ip' || k === 'model'),
  )
  const active = Object.entries(filter).filter(([, v]) => v !== '')

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

        {/* Only offered once the log holds both kinds, so a gateway that has
            never refused anything is not asked to choose between them. */}
        {(filter.kind !== '' || rows.some((r) => r.rejected)) && (
          <Segmented
            label="Kind"
            value={filter.kind === '' ? 'any' : filter.kind}
            onChange={(v) => onNarrow({ ...filter, kind: v === 'any' ? '' : v })}
            options={[
              { id: 'any', label: 'Relayed and refused', content: 'Any' },
              { id: 'relayed', label: 'Reached the upstream', content: 'Relayed' },
              { id: 'rejected', label: 'Refused by this gateway', content: 'Refused' },
            ]}
          />
        )}

        <span className="flex-grow" />
        <TextButton onClick={() => void load('')}>Refresh</TextButton>
      </div>

      {/* Only the filters with no control of their own. Show and Kind already
          say what they are set to, so a chip repeating them was two places to
          read one fact; a chat, a key, an address or a model is set by a click
          or a link and has nothing else on screen to say so. */}
      {pinned.length > 0 && (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-on-surface-variant">Narrowed to</span>
          {pinned.map(([k, v]) => (
            <button
              key={k}
              type="button"
              onClick={() => onNarrow({ ...filter, [k]: '' })}
              title={`Stop filtering by ${k}`}
              className="state-layer inline-flex h-7 items-center gap-1.5 rounded-[var(--radius-md3-s)] border border-outline px-2.5 text-xs font-medium text-on-surface-variant"
            >
              {k}: {v.length > 26 ? `${v.slice(0, 23)}…` : v}
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
          {active.length > 0
            ? 'No requests match this filter.'
            : 'No requests recorded yet.'}
        </Empty>
      ) : (
        <Table
          cap
          head={[
            // Not Date and not Time: the cell is a time on today's rows, a
            // date and a time on older ones, and an epoch once copied.
            column('Timestamp', 'at'),
            column('Model', 'model'),
            column('Key', 'key_name'),
            'IP',
            column('Status', 'status'),
            column('Tokens', 'tokens'),
            column('Duration', 'duration'),
            'Message',
          ]}
        >
          {data.map((r, i) => (
            <tr key={`${r.at}-${i}`} className="border-b border-outline-variant last:border-0">
              <td
                title={r.at}
                className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant"
              >
                {stamp(r.at)}
              </td>
              {/* Clickable for the same reason the address is: the model is
                  right there in the row you are reading, and hunting for it
                  again in a list of every model the gateway has ever served is
                  work the row can do for you. */}
              <td className="px-2 py-2 whitespace-nowrap">
                {r.model === undefined || r.model === '' ? (
                  '—'
                ) : (
                  <button
                    type="button"
                    onClick={() => onNarrow({ ...filter, model: r.model ?? '' })}
                    title={`Only requests for ${r.model}`}
                    className="state-layer rounded-[var(--radius-md3-xs)] px-1 underline decoration-dotted underline-offset-2 hover:text-primary"
                  >
                    {r.model}
                  </button>
                )}
              </td>
              <td className="px-2 py-2 whitespace-nowrap text-on-surface-variant">
                {dash(r.key_name)}
              </td>
              {/* Clickable, because "everything from that machine" is the next
                  question the moment one address looks wrong. */}
              <td className="px-2 py-2 font-mono text-xs whitespace-nowrap text-on-surface-variant">
                {r.ip === undefined || r.ip === '' ? (
                  '—'
                ) : (
                  <button
                    type="button"
                    onClick={() => onNarrow({ ...filter, ip: r.ip ?? '' })}
                    title={`Only requests from ${r.ip}`}
                    className="state-layer rounded-[var(--radius-md3-xs)] px-1 underline decoration-dotted underline-offset-2 hover:text-primary"
                  >
                    {r.ip}
                  </button>
                )}
              </td>
              <td className="px-2 py-2">
                <Chip tone={statusTone(r)}>
                  {r.status === 0 ? 'failed' : r.status}
                  {r.streaming && r.status === 200 ? ' ·' : ''}
                </Chip>
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">
                {r.input_tokens + r.output_tokens > 0
                  ? `${compact(r.input_tokens)} / ${compact(r.output_tokens)}`
                  : '—'}
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">{r.duration_ms}ms</td>
              {/* Named, not blank with a button in it. A column whose heading
                  is empty and whose cell says "Log" tells you there is more to
                  read without saying what about — the reason is the thing you
                  came for, so the short form is here and the button opens the
                  upstream's own words. */}
              <td className="max-w-[24rem] px-2 py-2">
                {r.error !== undefined && r.error !== '' ? (
                  <div className="flex items-center gap-2">
                    {/* min-w-0 is what lets it truncate: a flex item defaults
                        to min-content, so without it the text refuses to
                        shrink and pushes the button out of the column. */}
                    <span
                      className={`min-w-0 flex-1 truncate text-xs ${
                        r.error_kind === 'content_check' ? 'text-warning' : 'text-on-surface-variant'
                      }`}
                      title={r.error}
                    >
                      {shortMessage(r)}
                    </span>
                    <TextButton size="sm" onClick={() => setShown(r)}>
                      Log
                    </TextButton>
                  </div>
                ) : (
                  <span className="text-on-surface-variant">—</span>
                )}
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

      {shown !== null && shown.error !== undefined && shown.error !== '' && (
        <RequestLog row={shown} onClose={() => setShown(null)} />
      )}
    </>
  )
}

/**
 * One failed request, in full.
 *
 * A dialog rather than a panel under the table: the row it belongs to is
 * usually scrolled well out of view by the time the text appears below fifty
 * others, and reading an upstream error means reading all of it — which is the
 * one thing a strip at the bottom of a long list makes hard.
 *
 * The identifying columns come along because the operator opened this from a
 * row they can no longer see, and "which request was that" is the first thing
 * they lose.
 */
function RequestLog({ row, onClose }: { row: RequestRow; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)

  const tokens =
    row.input_tokens + row.output_tokens > 0
      ? `${compact(row.input_tokens)} in / ${compact(row.output_tokens)} out`
      : '—'

  const copy = async () => {
    setCopyFailed(false)
    if (await copyText(asText(row))) {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
      return
    }
    // Not a modal on top of this one: the text is already on screen and
    // selectable, so saying which keys to press is the whole remedy.
    setCopyFailed(true)
  }

  return (
    <ErrorModal
      title="Request log"
      size="lg"
      onClose={onClose}
      message={
        <>
          The upstream's own words, unmodified — it is the only thing that separates an expired
          token from a plan restriction.
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {row.error_kind === 'content_check' && (
          <Banner tone="warn">
            <strong>This is a content check, not a quota limit.</strong> The message below talks
            about billing, but adding usage credit will not fix it — the upstream classified this
            request's <em>content</em> as coming from a third-party app rather than from Claude
            Code. The same key usually succeeds on the next request with slightly different
            content, which is what makes it look like a flaky quota problem.
            <br />
            <br />
            Two triggers are known and already rewritten on the way out: a tool named{' '}
            <code>mcp_x</code> instead of <code>mcp__x</code>, and Claude Code's own{' '}
            <code>Is directory a git repo:</code> line inside a foreign system prompt. Seeing this
            anyway means a third trigger — bisect the request body to find it.
          </Banner>
        )}

        <KeyValue
          items={[
            // The exact instant, not "3m ago": this is the value that gets
            // matched against an upstream request_id or somebody else's log.
            ['Timestamp', <span className="tabular-nums">{row.at}</span>],
            [
              'Status',
              <Chip tone={statusTone(row)}>{row.status === 0 ? 'failed' : row.status}</Chip>,
            ],
            ['Model', dash(row.model)],
            ['Key', dash(row.key_name)],
            ['Account', dash(row.account_email)],
            ['Path', row.path],
            ['Streaming', row.streaming ? 'yes' : 'no'],
            ['Tokens', tokens],
            ['Duration', `${row.duration_ms}ms`],
          ]}
        />

        <div>
          <div className="mb-2 flex items-center justify-between gap-4">
            <span className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
              Error
            </span>
            {/* The whole log, not just the error text: what gets pasted into an
                issue is useless without the model and the status beside it. */}
            <TonalButton onClick={() => void copy()}>
              {copied ? 'Copied' : 'Copy log'}
            </TonalButton>
          </div>
          <Verbatim>{row.error ?? ''}</Verbatim>
          {copyFailed && (
            <p className="mt-2 mb-0 text-xs text-error">
              The browser only gives a page the clipboard over HTTPS, and this gateway is being
              served over plain HTTP. Select the text above and press{' '}
              <span className="font-mono">⌘C</span> or <span className="font-mono">Ctrl-C</span>.
            </p>
          )}
        </div>
      </div>
    </ErrorModal>
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
  return [
    `timestamp: ${r.at}`,
    `epoch:     ${Number.isNaN(epoch) ? '—' : epoch}`,
    `status:    ${r.status === 0 ? 'failed (no response)' : r.status}`,
    `model:     ${dash(r.model)}`,
    `key:       ${dash(r.key_name)}`,
    `account:   ${dash(r.account_email)}`,
    `path:      ${r.path}`,
    `streaming: ${r.streaming ? 'yes' : 'no'}`,
    `tokens:    ${r.input_tokens} in / ${r.output_tokens} out / ${r.cache_tokens} cache`,
    `duration:  ${r.duration_ms}ms`,
    '',
    r.error ?? '',
  ].join('\n')
}

type SortKey = 'at' | 'model' | 'key_name' | 'status' | 'tokens' | 'duration'
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
    case 'key_name':
      return r.key_name ?? ''
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

/**
 * The short message a request came back with.
 *
 * A content refusal is named as one, because the message it arrives with is
 * about billing and means nothing of the sort — the whole point of classifying
 * it is that reading the raw text sends people to the wrong problem. Everything
 * else falls back to the upstream's own error type, which is usually one word
 * and is the word an operator would search for.
 */
function shortMessage(r: RequestRow): string {
  if (r.error_kind === 'content_check') return 'refused on content'
  const text = r.error ?? ''
  // The envelope is JSON often enough to be worth reading, and the type inside
  // it is the useful half.
  const match = /"type"\s*:\s*"([a-z_]+)"/.exec(text)
  if (match !== null && match[1] !== 'error') return match[1].replace(/_/g, ' ')
  if (r.status === 0) return 'no answer'
  return text.length > 40 ? `${text.slice(0, 37)}…` : text
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
