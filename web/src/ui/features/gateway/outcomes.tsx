import { useState } from 'react'
import { api } from '../../../api/client'
import { READS } from '../../../api/live'
import { useLoader } from '../../hooks'
import { Card, Loading } from '../../primitives'
import { DEFAULT_DAYS, TrafficOf, WINDOWS } from './slots'

/**
 * The requests by outcome, a line per status, over days: above the request
 * log, which is where a line that went red is followed to its requests.
 */
export function Outcomes() {
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
