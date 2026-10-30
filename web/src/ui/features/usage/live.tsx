import { ColumnChart, MAGNITUDE, Stack as ChartStack } from '../../charts'
import { compact } from '../../format'

/**
 * Requests per minute over the last hour, a column a minute, the newest on
 * the right: a client's live view on its Usage page and the gateway's on
 * Activity, which the server counts the same way (refusals included: this is
 * the load arriving). One drawing for both, so the two strips read alike and
 * a client's can be held against the gateway's.
 */
export function PerMinute({ minutes, height = 160 }: { minutes: readonly number[]; height?: number }) {
  const chart = new ChartStack(
    minutes.map((n, i) => ({ i, n })),
    [{ key: 'requests', label: 'Requests', paint: MAGNITUDE, value: (d: { n: number }) => d.n }],
  )
  // Labelled every ten minutes back from now, the last column being this one.
  const back = minutes.length
  return (
    <ColumnChart
      stack={chart}
      label={(d) => (d.i % 10 === 0 ? `−${back - d.i}m` : '')}
      format={compact}
      height={height}
      describe={(d) => `${d.n} requests`}
    />
  )
}
