import { useRef } from 'react'
import { api, type RunningRequest } from '../../../api/client'
import { sameDayStamp } from '../../format'
import { useLoader, useNow } from '../../hooks'
import { Chip, Muted, Td, Tip, Tr } from '../../primitives'
import type { RequestFilter, SetField } from './filter'
import { FilterableCell } from './row'
import { READS } from '../../../api/live'

/**
 * The requests the gateway is serving now, for the top of the request log:
 * a request is written to the log only when it ends, so one still waiting on
 * a model's first token — the one an operator comes looking for — was in it
 * nowhere until it failed.
 *
 * `keyId` keeps one client's; null reads none. `now` is the gateway's clock —
 * its time when it answered, moved on by this one's since — so a row's time
 * running is the gateway's whatever this machine's clock says, and moves
 * every second between reads.
 */
export function useRunning(keyId: string | null): { rows: RunningRequest[]; now: number } {
  const read = useRef(0)
  const { data } = useLoader(
    async (signal) => {
      if (keyId === null) return null
      const r = await api.traffic.running(keyId, signal)
      read.current = Date.now()
      return r
    },
    { deps: [keyId], on: keyId === null ? [] : READS.running },
  )
  const clock = useNow(1000)
  return {
    rows: data?.running ?? [],
    now: Date.parse(data?.now ?? '') + (clock - read.current),
  }
}

/**
 * Whether a running request belongs in the log as it is narrowed: by the
 * columns it already has — model, chat, client, agent, address — matched as
 * a recorded row's are, and never under a narrowing it cannot answer yet
 * (its status, message, account, or whether it was forwarded), which only
 * its end says.
 */
export function runningMatches(q: RunningRequest, filter: RequestFilter): boolean {
  if (filter.status !== '') return false
  const has: Partial<Record<SetField, string>> = {
    model: q.model,
    chat: q.conversation_id ?? '',
    key: q.key_id ?? '',
    client: q.client ?? '',
    ip: q.ip ?? '',
  }
  return filter.pinned().every(({ field, value }) => has[field] === value)
}

/**
 * A running request as a row of the log, in the log's own columns: what it
 * is known to be — each narrowing the log on a click, as a recorded row's
 * does — a running chip where its status will be, and its duration counting
 * up. Its tokens and message are its end's, so a dash until then.
 */
export function RunningRowView({
  q,
  now,
  onNarrow,
}: {
  q: RunningRequest
  now: number
  onNarrow: (field: SetField, value: string) => void
}) {
  const ms = Math.max(0, now - Date.parse(q.started_at))
  const slot = q.virtual_model !== undefined && q.virtual_model !== '' && q.virtual_model !== q.model
  return (
    <Tr>
      <Td title={q.started_at} nums nowrap muted>
        {sameDayStamp(q.started_at, 'second')}
      </Td>
      <FilterableCell
        max={11}
        value={q.model}
        shown={slot ? `${q.virtual_model} → ${q.model}` : q.model}
        tip={`Only requests for ${q.model}${slot ? `, asked for as ${q.virtual_model}` : ''}`}
        onNarrow={(v) => onNarrow('model', v)}
      />
      <FilterableCell
        max={9}
        mono
        muted
        value={q.conversation_id}
        shown={q.conversation_id?.slice(0, 12)}
        tip={q.conversation_id ?? ''}
        onNarrow={(v) => onNarrow('chat', v)}
      />
      <FilterableCell
        nowrap
        value={q.key_id}
        shown={q.key_name || q.key_id}
        tip={`Only requests from ${q.key_name ?? 'this client'}`}
        onNarrow={(v) => onNarrow('key', v)}
      />
      <FilterableCell
        nowrap
        muted
        value={q.client}
        tip={`Only requests from ${q.client}`}
        onNarrow={(v) => onNarrow('client', v)}
      />
      <FilterableCell
        nowrap
        mono
        muted
        value={q.ip}
        tip={`Only requests from ${q.ip}`}
        onNarrow={(v) => onNarrow('ip', v)}
      />
      <Td>
        <Tip tip="Being served now: recorded here once it ends">
          <Chip tone={ms >= 60_000 ? 'warn' : 'neutral'}>{q.streaming ? 'streaming' : 'running'}</Chip>
        </Tip>
      </Td>
      <Td nowrap>
        <span className="text-on-surface-variant">yes</span>
      </Td>
      <Td nums nowrap>
        <Muted as="span" size="xs">
          —
        </Muted>
      </Td>
      <Td nums nowrap>
        {Math.floor(ms / 1000)}s
      </Td>
      <Td max={18}>
        <Muted as="span" size="xs">
          —
        </Muted>
      </Td>
    </Tr>
  )
}
