import { useState, type ReactNode } from 'react'
import { api, type SlotUse, type SubscriptionShare } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Empty, ErrorState, Freshness, Loading, Stack, WindowPicker } from '../../primitives'
import { lastDays } from '../../format'

/** The windows Activity's tables are read over: a day, a week, a month. */
const WINDOWS = [1, 7, 30] as const

/** A table over days barely moves second to second; the request log is the live view. */
const REFRESH_MS = 30_000

export type GatewaySlots = { rows: SlotUse[]; shares: Record<string, SubscriptionShare> }

/**
 * One of Activity's tables in its card: the window picker and the freshness
 * in the header, the gateway-wide rows loaded for that window, and the
 * loading, failed and empty states said the same way on all three pages. The
 * page draws only its table from the rows.
 *
 * `always` is for a page that has rows to show with no traffic at all — the
 * accounts, each with its windows — so an idle week is not an empty page.
 */
export function GatewayUsage({
  title,
  always = false,
  children,
}: {
  title: string
  always?: boolean
  children: (data: GatewaySlots, days: number) => ReactNode
}) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.traffic.slots(days, signal),
    { deps: [days], refreshMs: REFRESH_MS },
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
            <Freshness updatedAt={updatedAt} refreshing={refreshing} live={live} onRefresh={() => void reload()} />
          </div>
        }
      >
        {data === null ? (
          <Loading />
        ) : data.rows.length === 0 && !always ? (
          <Empty>Nothing proxied in {lastDays(days)}.</Empty>
        ) : (
          children(data, days)
        )}
      </Card>
    </Stack>
  )
}
