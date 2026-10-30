import { useRef } from 'react'
import { api, type RunningRequest } from '../../../api/client'
import { sameDayStamp } from '../../format'
import { useLoader, useNow } from '../../hooks'
import { Chip, Muted, Td, Tip, Tr } from '../../primitives'

/** How often the running requests are read again: a row comes and goes in seconds. */
const RUNNING_MS = 2_000

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
    { deps: [keyId], refreshMs: keyId === null ? 0 : RUNNING_MS },
  )
  const clock = useNow(1000)
  return {
    rows: data?.running ?? [],
    now: Date.parse(data?.now ?? '') + (clock - read.current),
  }
}

/**
 * A running request as a row of the log, in the log's own columns: what it
 * is known to be, a running chip where its status will be, and its duration
 * counting up. Its tokens and message are its end's, so a dash until then.
 */
export function RunningRowView({ q, now }: { q: RunningRequest; now: number }) {
  const ms = Math.max(0, now - Date.parse(q.started_at))
  const slot = q.virtual_model !== undefined && q.virtual_model !== '' && q.virtual_model !== q.model
  return (
    <Tr>
      <Td title={q.started_at} nums nowrap muted>
        {sameDayStamp(q.started_at, 'second')}
      </Td>
      <Td max={11} title={slot ? `${q.virtual_model}, sent as ${q.model}` : q.model}>
        {slot ? `${q.virtual_model} → ${q.model}` : q.model}
      </Td>
      <Td max={9} mono muted>
        {q.conversation_id?.slice(0, 12) || '—'}
      </Td>
      <Td nowrap>{q.key_name || '—'}</Td>
      <Td nowrap muted>
        {q.client || '—'}
      </Td>
      <Td nowrap mono muted>
        {q.ip || '—'}
      </Td>
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
