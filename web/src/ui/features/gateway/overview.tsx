import { useState } from 'react'
import { api } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Loading, Muted, Stack, Stat, StatGrid } from '../../primitives'
import { compact } from '../../format'
import { AvailabilityCard } from '../status'
import { PerMinute } from '../usage/live'
import { DEFAULT_DAYS, TrafficOf, WINDOWS } from './slots'
import { READS } from '../../../api/live'

/**
 * Gateway's own page: the gateway's load. The last hour live, then the
 * requests by outcome over days — is it failing now, has it been failing —
 * and whether it was up, before the pages under it break the traffic down.
 * Availability lives here rather than on the Overview, which says only what
 * needs the operator: an incident of the last day is one of its notices.
 */
export default function GatewayOverview() {
  return (
    <Stack>
      <LastHour />
      <Outcomes />
      <AvailabilityCard />
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

/** The requests by outcome, a line per status, over days. */
function Outcomes() {
  const [days, setDays] = useState<number>(DEFAULT_DAYS)
  const { data: usage } = useLoader((signal) => api.traffic.usage(days, {}, signal), {
    deps: [days],
    on: READS.usage,
  })
  return (
    <Card title="Outcomes">
      {usage === null ? (
        <Loading />
      ) : (
        <TrafficOf usage={usage} by="status" days={days} onDays={setDays} windows={WINDOWS} />
      )}
    </Card>
  )
}
