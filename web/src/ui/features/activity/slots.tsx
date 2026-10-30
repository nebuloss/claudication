import { useState, type ReactNode } from 'react'
import { api, type SlotUse, type SubscriptionShare, type Usage } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Empty, ErrorState, Freshness, Loading, Muted, Stack, WindowPicker } from '../../primitives'
import { lastDays } from '../../format'
import { Traffic, type Dimension } from '../usage/traffic'
import { READS } from '../../../api/live'

/**
 * The windows Activity is read over: a week, a fortnight, a month — one
 * picker per page, driving its chart and its table together, so the lines
 * and the rows under them are the same days.
 *
 * Not the 24 hours the tables once offered: the chart counts whole days, and
 * a day is one point, not a line. The live view of the last hour is the
 * Requests page's. A fortnight stays because it is the chart's best reading
 * of a trend — two of each weekday — and a week because it is the table's.
 */
export const WINDOWS = [7, 14, 30] as const

/** Open on a week: what a table of where the traffic went is usually asked over. */
export const DEFAULT_DAYS = 7

export type GatewaySlots = { rows: SlotUse[]; shares: Record<string, SubscriptionShare> }

/**
 * One of Activity's breakdowns in its card: the window picker and the
 * freshness in the header, then the traffic over those days by the same
 * names as the table, then the table, drawn by the page from the gateway-wide
 * rows. The loading, failed and empty states are said the same way on all
 * three pages.
 *
 * One read for both — the rows and the report the chart is drawn from — so
 * the two share one freshness and one refresh, and cannot be from different
 * moments.
 *
 * `always` is for a page that has rows to show with no traffic at all — the
 * accounts, each with its windows — so an idle week is not an empty page.
 */
export function GatewayUsage({
  title,
  by,
  always = false,
  children,
}: {
  title: string
  /** The breakdown the chart draws: the names the table lists. */
  by: Exclude<Dimension, 'status'>
  always?: boolean
  children: (data: GatewaySlots, days: number) => ReactNode
}) {
  const [days, setDays] = useState<number>(DEFAULT_DAYS)
  const { data, error, loading, reload, refreshing, updatedAt } = useLoader(
    async (signal) => {
      const [slots, usage] = await Promise.all([
        api.traffic.slots(days, signal),
        api.traffic.usage(days, {}, signal),
      ])
      return { slots, usage }
    },
    { deps: [days], on: READS.slots },
  )
  if (error !== '' && data === null) {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  return (
    <Stack>
      <Card
        title={title}
        aside={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <WindowPicker value={days} windows={WINDOWS} onChange={setDays} />
            <Freshness updatedAt={updatedAt} refreshing={refreshing} />
          </div>
        }
      >
        {data === null ? (
          <Loading />
        ) : data.slots.rows.length === 0 && !always ? (
          <Empty>Nothing proxied in {lastDays(days)}.</Empty>
        ) : (
          <>
            <TrafficOf usage={data.usage} by={by} days={days} />
            <div className="mt-6">{children(data.slots, days)}</div>
          </>
        )}
      </Card>
    </Stack>
  )
}

/**
 * The gateway-wide chart, or why there is none: with per-request history off
 * there is nothing recorded to draw, which is not the same as a quiet week.
 */
export function TrafficOf({
  usage,
  ...chart
}: {
  usage: Usage
} & Omit<Parameters<typeof Traffic>[0], 'cross'>) {
  if (usage.enabled === false) {
    return (
      <Muted>
        Per-request history is off — <code>usage.retention-days</code> is 0, so the gateway records
        nothing to chart.
      </Muted>
    )
  }
  return <Traffic cross={usage.report?.cross} {...chart} />
}
