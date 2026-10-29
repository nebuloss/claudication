import { useRef } from 'react'
import { api, type RunningRequest } from '../../../api/client'
import { countdown, sameDayStamp } from '../../format'
import { useLoader, useNow } from '../../hooks'
import { Chip, Muted, Table, Td, Tr } from '../../primitives'

/** How often the running requests are read again: a row comes and goes in seconds. */
const RUNNING_MS = 2_000

/**
 * The requests the gateway is serving now, above the ones it has recorded:
 * a request is written to the log only when it ends, so one still waiting on
 * a model's first token — the one an operator comes looking for — was in it
 * nowhere until it failed. Each says how long it has been running, counted
 * on the gateway's clock and moving every second between reads.
 *
 * `keyId` keeps one client's. Nothing at all while nothing is running.
 */
export function RunningRequests({ keyId = '' }: { keyId?: string }) {
  const read = useRef(0)
  const { data } = useLoader(
    async (signal) => {
      const r = await api.traffic.running(keyId, signal)
      read.current = Date.now()
      return r
    },
    { deps: [keyId], refreshMs: RUNNING_MS },
  )
  const now = useNow(1000)
  const running = data?.running ?? []
  if (running.length === 0) return null
  // The gateway's now when it answered, moved on by what this clock has run
  // since: a row's age is the gateway's, whatever this machine's clock says.
  const gatewayNow = Date.parse(data?.now ?? '') + (now - read.current)

  return (
    <div className="mb-6">
      <Muted size="xs">
        Running now · {running.length} — recorded below once {running.length === 1 ? 'it ends' : 'each ends'}
      </Muted>
      <div className="mt-2">
        <Table head={['Started', 'Model', 'Client', 'Agent', 'IP', 'Running for']}>
          {running.map((q, i) => (
            <RunningRow key={`${q.started_at}-${i}`} q={q} now={gatewayNow} />
          ))}
        </Table>
      </div>
    </div>
  )
}

function RunningRow({ q, now }: { q: RunningRequest; now: number }) {
  const seconds = Math.max(0, Math.floor((now - Date.parse(q.started_at)) / 1000))
  return (
    <Tr>
      <Td>
        <span className="tabular-nums">{sameDayStamp(q.started_at, 'second')}</span>
      </Td>
      <Td>
        <code title={q.model}>{q.virtual_model || q.model}</code>
        {q.virtual_model !== undefined && q.virtual_model !== '' && q.virtual_model !== q.model && (
          <Muted as="span" size="xs">
            {' '}
            → {q.model}
          </Muted>
        )}
      </Td>
      <Td>{q.key_name ?? '—'}</Td>
      <Td>{q.client ?? '—'}</Td>
      <Td>
        <span className="tabular-nums">{q.ip ?? '—'}</span>
      </Td>
      <Td>
        <Chip tone={seconds >= 60 ? 'warn' : 'neutral'}>
          {q.streaming ? 'streaming' : 'waiting'} · {countdown(seconds)}
        </Chip>
      </Td>
    </Tr>
  )
}
