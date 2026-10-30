import { api } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Loading, Muted, Stack, Stat, StatGrid } from '../../primitives'
import { compact } from '../../format'
import { PerMinute } from '../usage/live'
import { READS } from '../../../api/live'

/**
 * Gateway's own page: its load this last hour, before the pages under it
 * break the traffic down; the requests by outcome are on Requests, above the
 * log they lead into, and whether it has been up is on the Overview.
 */
export default function GatewayOverview() {
  return (
    <Stack>
      <LastHour />
    </Stack>
  )
}

/**
 * The last hour, a column a minute, with what is open this second: the same
 * strip a client's Usage page draws for that client, for the whole gateway.
 */
function LastHour() {
  const { data: l } = useLoader((signal) => api.traffic.live(signal), { on: READS.gatewayLive })
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
            <Muted size="xs">Per-request history is off, so only what is open now is known.</Muted>
          )}
        </Stack>
      )}
    </Card>
  )
}
