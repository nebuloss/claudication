import { api, type LiveStats } from '../../../api/client'
import { TOPICS } from '../../../api/live'
import { compact } from '../../format'
import { useLive, useLoader } from '../../hooks'
import { Card, Chip, ErrorState, Loading, Muted, Stack, Table, Td, Tr } from '../../primitives'

/**
 * How changes reach the screens (docs/data-architecture.md), as the gateway
 * counts them since it started: what to look at when a screen is late, or the
 * gateway busy.
 *
 * Each topic: how many times it changed, how many notices that was — the
 * interval caps them, so a busy topic has far fewer notices than changes — and
 * how many open tabs listen to it. Each cached read: how often it was answered
 * from what was kept, and what computing it cost the last time.
 *
 * Read again on every topic, so the counts move while it is open; it is the
 * one page that listens to all of them.
 */
export default function Diagnostics() {
  const live = useLive()
  const { data, error, loading, reload } = useLoader<LiveStats>((signal) => api.gateway.live(signal), {
    on: TOPICS,
  })
  if (loading && data === null) return <Loading />
  if (data === null) {
    return <ErrorState message={error || 'Could not read the diagnostics.'} onRetry={() => void reload()} busy={loading} />
  }
  const reads = [...data.reads].sort((a, b) => a.route.localeCompare(b.route))
  return (
    <Stack gap={4}>
      <Card
        title="Topics"
        aside={
          <Chip tone={live ? 'ok' : 'error'}>
            {live ? `this tab connected · ${data.streams} open` : 'this tab not connected'}
          </Chip>
        }
      >
        <Table head={['Topic', 'Changes', 'Notices', 'At most every', 'Tabs']}>
          {data.topics.map((t) => (
            <Tr key={t.topic}>
              <Td mono>{t.topic}</Td>
              <Td nums>{compact(t.version)}</Td>
              <Td nums>{compact(t.notices)}</Td>
              <Td nums muted>
                {t.interval_ms === 0 ? 'at once' : `${t.interval_ms / 1000} s`}
              </Td>
              <Td nums>{t.watchers}</Td>
            </Tr>
          ))}
        </Table>
      </Card>
      <Card title="Cached reads">
        {reads.length === 0 ? (
          <Muted size="xs">None read since the gateway started.</Muted>
        ) : (
          <Table head={['Read', 'Topics', 'Kept', 'Computed', 'Last cost']}>
            {reads.map((r) => (
              <Tr key={r.route}>
                <Td>{r.route}</Td>
                <Td mono muted>
                  {r.topics}
                </Td>
                <Td nums>{compact(r.hits)}</Td>
                <Td nums>{compact(r.misses)}</Td>
                <Td nums>{r.last_ms.toFixed(1)} ms</Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
    </Stack>
  )
}
