import { useEffect } from 'react'
import { api, type LiveStats } from '../../../api/client'
import { TOPICS } from '../../../api/live'
import { compact } from '../../format'
import { useLive, useLoader } from '../../hooks'
import { Card, ErrorState, Loading, Muted, Overline, Stat, StatGrid, Table, Td, Tr, UsageBar, tile } from '../../primitives'

/**
 * How changes reach the screens (docs/data-architecture.md), as the gateway
 * counts them since it started: what to look at when a screen is late, or the
 * gateway busy.
 *
 * First the whole in four figures — tabs listening, changes, notices sent and
 * how many changes the intervals folded away, how often a read was answered
 * from what was kept. Then a tile a topic: its changes and notices, the share
 * of changes that became a notice, and the tabs on it. Then each cached read:
 * how often it was kept, and what computing it cost the last time.
 *
 * Read again on every topic, so the counts move while it is open; it is the
 * one page that listens to all of them.
 */
export default function Diagnostics() {
  const live = useLive()
  const { data, error, loading, reload } = useLoader<LiveStats>((signal) => api.gateway.live(signal), {
    on: TOPICS,
  })
  // Read again when this tab's own stream opens or drops: a stream opening
  // changes no topic, so nothing else would say the count of tabs moved.
  useEffect(() => {
    void reload()
  }, [live, reload])
  if (loading && data === null) return <Loading />
  if (data === null) {
    return <ErrorState message={error || 'Could not read the diagnostics.'} onRetry={() => void reload()} busy={loading} />
  }
  const changes = data.topics.reduce((n, t) => n + t.version, 0)
  const notices = data.topics.reduce((n, t) => n + t.notices, 0)
  const hits = data.reads.reduce((n, r) => n + r.hits, 0)
  const asked = hits + data.reads.reduce((n, r) => n + r.misses, 0)
  const reads = [...data.reads].sort((a, b) => b.hits + b.misses - (a.hits + a.misses))

  return (
    <div className="flex flex-col gap-4">
      <StatGrid>
        <Stat
          label="Open tabs"
          value={data.streams}
          hint={live ? 'this one connected' : 'this one not connected'}
          tone={live ? 'ok' : 'error'}
        />
        <Stat label="Changes" value={compact(changes)} hint="since the gateway started" />
        <Stat
          label="Notices"
          value={compact(notices)}
          hint={changes > 0 ? `${percent(1 - notices / changes)} folded by the intervals` : 'none yet'}
        />
        <Stat
          label="Kept reads"
          value={asked > 0 ? percent(hits / asked) : '—'}
          hint={asked > 0 ? `${compact(hits)} of ${compact(asked)} answered without computing` : 'none read yet'}
        />
      </StatGrid>

      <Card title="Topics">
        <ul className="m-0 grid list-none gap-3 p-0 sm:grid-cols-2 lg:grid-cols-5">
          {data.topics.map((t) => {
            const share = t.version > 0 ? (t.notices / t.version) * 100 : 0
            return (
              <li key={t.topic} className={`${tile} flex flex-col gap-3 p-4`}>
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-sm font-medium text-on-surface">{t.topic}</span>
                  <span className="rounded-md3-s bg-surface-high px-1.5 py-0.5 text-[11px] text-on-surface-variant">
                    {t.interval_ms === 0 ? 'at once' : `${t.interval_ms / 1000} s`}
                  </span>
                </div>
                <dl className="m-0 grid grid-cols-2 gap-2">
                  <div>
                    <Overline as="dt">Changes</Overline>
                    <dd className="m-0 text-xl leading-7 text-on-surface tabular-nums">{compact(t.version)}</dd>
                  </div>
                  <div>
                    <Overline as="dt">Notices</Overline>
                    <dd className="m-0 text-xl leading-7 text-on-surface tabular-nums">{compact(t.notices)}</dd>
                  </div>
                </dl>
                <div>
                  <UsageBar percent={t.version > 0 ? share : null} tone="neutral" />
                  <div className="mt-1 text-[11px] text-on-surface-variant">
                    {t.version > 0 ? `${Math.round(share)}% of changes sent` : 'no change yet'}
                  </div>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-on-surface-variant">
                  <span
                    className={`size-2 rounded-full ${t.watchers > 0 ? 'bg-success' : 'bg-surface-highest'}`}
                    aria-hidden
                  />
                  {t.watchers === 0 ? 'no tab listening' : `${t.watchers} ${t.watchers === 1 ? 'tab' : 'tabs'} listening`}
                </div>
              </li>
            )
          })}
        </ul>
      </Card>

      <Card title="Cached reads">
        {reads.length === 0 ? (
          <Muted size="xs">None read since the gateway started.</Muted>
        ) : (
          <div className={`${tile} px-4 py-2`}>
            <Table head={['Read', 'Topics', 'Kept', 'Computed', 'Kept share', 'Last cost']}>
              {reads.map((r) => {
                const n = r.hits + r.misses
                return (
                  <Tr key={r.route}>
                    <Td nowrap>
                      <span className="font-medium text-on-surface">{r.route}</span>
                    </Td>
                    <Td>
                      <span className="flex flex-wrap gap-1">
                        {r.topics.split(',').map((t) => (
                          <span
                            key={t}
                            className="rounded-md3-s bg-surface-high px-1.5 py-0.5 font-mono text-[11px] text-on-surface-variant"
                          >
                            {t}
                          </span>
                        ))}
                      </span>
                    </Td>
                    <Td nums>{compact(r.hits)}</Td>
                    <Td nums>{compact(r.misses)}</Td>
                    <Td nowrap>
                      <span className="flex items-center gap-2 tabular-nums">
                        <span className="w-16">
                          <UsageBar percent={n > 0 ? (r.hits / n) * 100 : null} tone="ok" />
                        </span>
                        {n > 0 ? percent(r.hits / n) : '—'}
                      </span>
                    </Td>
                    <Td nums nowrap>
                      {r.last_ms.toFixed(1)} ms
                    </Td>
                  </Tr>
                )
              })}
            </Table>
          </div>
        )}
      </Card>
    </div>
  )
}

function percent(share: number): string {
  return `${Math.round(Math.max(0, Math.min(1, share)) * 100)}%`
}
