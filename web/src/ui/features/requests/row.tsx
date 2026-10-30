import type { ComponentProps, ReactNode } from 'react'
import { millis, sameDayStamp } from '../../format'
import { Chip, Muted, Td, Tip, Tr, appLinkClass } from '../../primitives'
import type { SetField } from './filter'
import { messageLabel, type RequestRecord } from './record'

/** A request's status as its chip: the same in the row and in the dialog. */
export function StatusChip({ record }: { record: RequestRecord }) {
  return <Chip tone={record.tone}>{record.statusLabel}</Chip>
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
 * the dotted underline would be drawn through it. `tip` says what the click
 * will narrow to.
 */
function Filterable({
  onClick,
  tip,
  plain = false,
  children,
}: {
  onClick: () => void
  tip?: string
  plain?: boolean
  children: ReactNode
}) {
  return (
    <Tip tip={tip}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onClick()
        }}
        className={plain ? 'state-layer rounded-md3-s' : appLinkClass({ cell: true })}
      >
        {children}
      </button>
    </Tip>
  )
}

/**
 * A cell whose value narrows the log to itself, or a dash when the row has
 * none. Four columns were this, each written out with its own copy of the
 * empty case; what differs between them is the cell's look and its words.
 */
export function FilterableCell({
  value,
  shown = value,
  tip,
  onNarrow,
  ...cell
}: {
  value: string | undefined
  /** What the cell prints, when it is not the whole value. */
  shown?: string
  tip: string
  onNarrow: (value: string) => void
} & Omit<ComponentProps<typeof Td>, 'children' | 'title'>) {
  return (
    <Td {...cell}>
      {value === undefined || value === '' ? (
        '—'
      ) : (
        <Filterable onClick={() => onNarrow(value)} tip={tip}>
          {shown}
        </Filterable>
      )}
    </Td>
  )
}

/**
 * One request as a row of the log. The whole row opens its record; the cells
 * that filter stop the click before it gets there.
 */
export function RequestRowView({
  record,
  onOpen,
  onNarrow,
}: {
  record: RequestRecord
  onOpen: () => void
  /** Narrow the log to this value of this column. */
  onNarrow: (field: SetField, value: string) => void
}) {
  const r = record.row
  return (
    // The whole row opens the log. The Log button it replaces was a control
    // in every row of a table where the row itself did nothing, which is a
    // lot of width spent saying "clickable" — and once every request has a
    // record worth reading, not only the failed ones, a button per row is a
    // button on every row.
    <Tr onClick={onOpen}>
      <Td title={r.at} nums nowrap muted>
        {sameDayStamp(r.at, 'second')}
      </Td>
      {/* Clickable for the same reason the address is: the model is right
          there in the row you are reading, and hunting for it again in a list
          of every model the gateway has ever served is work the row can do
          for you.

          Truncated rather than given the width it wants:
          claude-haiku-4-5-20251001 is 25 characters that differ from its
          neighbour in the last eight, and at full width it pushed everything
          after it off the screen. Drag the edge for more. */}
      <FilterableCell
        max={11}
        value={r.model}
        // The client's own name first, when it asked by one: that is the
        // name its configuration shows, and the model is what it served.
        shown={record.modelLabel}
        tip={`Only requests for ${r.model}${r.virtual_model ? `, asked for as ${r.virtual_model}` : ''}`}
        onNarrow={(v) => onNarrow('model', v)}
      />
      {/* The id, shortened. Nobody reads a session id, but they do recognise
          the first characters of the one they are looking at — and the click
          is what this column is for. The hover names the chat by its title,
          where the gateway has one, so the id says which conversation it is
          without opening it. */}
      <FilterableCell
        max={9}
        mono
        muted
        value={r.conversation_id}
        shown={r.conversation_id?.slice(0, 12)}
        // The chat's name, which the id is not; the full id where it has none.
        tip={r.chat_title || r.conversation_id || ''}
        onNarrow={(v) => onNarrow('chat', v)}
      />
      {/* Which client sent it, by the name it has now; narrows on its id so
          a rename does not lose the filter. */}
      <FilterableCell
        nowrap
        value={r.key_id}
        shown={r.key_name || r.key_id}
        tip={`Only requests from ${r.key_name ?? 'this client'}`}
        onNarrow={(v) => onNarrow('key', v)}
      />
      {/* The program that was running, from its User-Agent: what tells a
          custom client's several programs apart, and what a request refused
          before it named a key still has. */}
      <FilterableCell
        nowrap
        muted
        value={r.client}
        tip={`Only requests from ${r.client}`}
        onNarrow={(v) => onNarrow('client', v)}
      />
      {/* Clickable, because "everything from that machine" is the next
          question the moment one address looks wrong. */}
      <FilterableCell
        nowrap
        mono
        muted
        value={r.ip}
        tip={`Only requests from ${r.ip}`}
        onNarrow={(v) => onNarrow('ip', v)}
      />
      {/* Clickable like the model and the address. The chip is what you
          already read to decide the row is interesting, so it is also what
          you reach for to see the rest of its kind. Narrows on the exact
          code, which is not the same question as Failed only: that one spans
          every code plus the streams that died after a 200. */}
      <Td>
        <Filterable
          plain
          onClick={() => onNarrow('code', String(r.status))}
          tip={
            r.status === 0 ? 'Only requests that got no answer' : `Only requests that answered ${r.status}`
          }
        >
          <StatusChip record={record} />
        </Filterable>
      </Td>
      {/* What the Kind control above the table used to say. A column, because
          it is a property of the row like every other filter here, and a
          control above the table for one of them and menus in the headings
          for the rest was two idioms for one job. */}
      <Td nowrap>
        <Filterable
          onClick={() => onNarrow('kind', record.change ? 'change' : record.refused ? 'rejected' : 'relayed')}
          tip={
            record.change
              ? 'Only changes clients’ agents made through the MCP server'
              : record.refused
                ? 'Only requests this gateway refused'
                : 'Only requests that reached the upstream'
          }
        >
          <span className={record.refused ? 'text-warning' : 'text-on-surface-variant'}>
            {record.change ? 'no, a change' : record.refused ? 'no' : 'yes'}
          </span>
        </Filterable>
      </Td>
      <Td nums nowrap>
        {record.tokensShort}
      </Td>
      <Td nums nowrap>
        {r.duration_ms}ms
        {/* Under the total, because the two together are what tell a long
            answer from one that sat silent before starting. */}
        {r.first_token_ms !== undefined && (
          <Tip tip="Time to first token" block>
            <div className="text-[11px] text-on-surface-variant">first {millis(r.first_token_ms)}</div>
          </Tip>
        )}
      </Td>
      {/* The stored class rather than the text: the text ends in a
          request_id, so it is unique per row and filters to one. */}
      <Td max={18}>
        <span className="text-xs">
          {r.error_code === undefined || r.error_code === '' ? (
            <Muted as="span" size="xs">
              —
            </Muted>
          ) : (
            <Filterable onClick={() => onNarrow('message', r.error_code ?? '')} tip={r.error}>
              <span
                className={r.error_kind === 'content_check' ? 'text-warning' : 'text-on-surface-variant'}
              >
                {messageLabel(r.error_code)}
              </span>
            </Filterable>
          )}
        </span>
      </Td>
    </Tr>
  )
}
