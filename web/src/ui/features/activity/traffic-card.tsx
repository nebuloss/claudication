import { useState } from 'react'
import { api, type Usage } from '../../../api/client'
import { Traffic } from '../usage/traffic'
import { useLoader } from '../../hooks'
import { Card, Loading, Muted } from '../../primitives'

/** How many days of history the chart can show. */
const WINDOWS = [7, 14, 30] as const

/** The chart aggregates whole days, so it barely moves minute to minute. */
const TRAFFIC_REFRESH_MS = 60_000

/**
 * What the gateway has been doing, as one chart over time.
 *
 * Only the chart, on Activity's overview under the last hour: Activity is
 * where the gateway's traffic is read, and the Overview keeps to whether it
 * can serve. The tables by model, client and account are Activity's other
 * pages.
 *
 * Its own loader, so changing the window does not re-fetch the status, and a
 * usage table that is off or empty cannot take the screen down with it.
 */
export default function TrafficCard() {
  const [days, setDays] = useState(14)
  const { data: usage } = useLoader<Usage>(
    (signal) => api.traffic.usage(days, {}, signal),
    {
      deps: [days],
      refreshMs: TRAFFIC_REFRESH_MS,
    },
  )
  return (
    <Card title="Traffic">
      {usage === null ? (
        <Loading />
      ) : usage.enabled === false ? (
        <Muted>
          Per-request history is off — <code>usage.retention-days</code> is 0, so the gateway
          records nothing to chart.
        </Muted>
      ) : (
        <Traffic cross={usage.report?.cross} days={days} onDays={setDays} windows={WINDOWS} />
      )}
    </Card>
  )
}
