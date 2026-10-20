import { api } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Loading, Muted, Stack, Stat, StatGrid } from '../../primitives'
import { compact } from '../../format'
import { LIVE_MS, PerMinute } from '../usage/live'
import TrafficCard from './traffic-card'

/**
 * Activity's first page: the gateway's load, and nothing else — the last hour
 * as it happens, then the traffic over days. Where the traffic went is the
 * pages beside this one; whether the gateway can serve is the Overview's.
 */
export default function ActivityOverview() {
  return (
    <Stack>
      <LastHour />
      <TrafficCard />
    </Stack>
  )
}

/**
 * The last hour, a column a minute, with what is open this second: the same
 * strip a client's Usage page draws for that client, for the whole gateway.
 */
function LastHour() {
  const { data: l } = useLoader((signal) => api.traffic.live(signal), { refreshMs: LIVE_MS })
  const minutes = l?.per_minute ?? []
  const hour = minutes.reduce((n, m) => n + m, 0)
  const busiest = minutes.reduce((n, m) => Math.max(n, m), 0)
  return (
    <Card title="Last hour" aside={<Muted as="span" size="xs">requests per minute</Muted>}>
      {l === null ? (
        <Loading />
      ) : (
        <Stack gap={3}>
          <PerMinute minutes={minutes} height={120} />
          <StatGrid columns={3}>
            <Stat label="In flight" value={compact(l.in_flight)} hint="open now" />
            <Stat label="This hour" value={compact(hour)} hint="requests, refusals included" />
            <Stat label="Busiest minute" value={compact(busiest)} hint="requests" />
          </StatGrid>
          {!l.history_recorded && (
            <Muted size="xs">
              Per-request history is off, so only what is open now is known.
            </Muted>
          )}
        </Stack>
      )}
    </Card>
  )
}
