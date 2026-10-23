import { useState } from 'react'
import { api, type Quota } from '../../../api/client'
import { FIVE_HOURS_S, QuotaWindow, SEVEN_DAYS_S, windowShort, windowTitle } from '../../domain/quota'
import { Button, InlineMeter, MeterRow, Muted, Stack, UsageBar } from '../../primitives'
import { relative } from '../../format'
import { providerOf } from '../../providers'

/**
 * The two headline windows, short enough for a row: the account list, and the
 * header of an account's page.
 *
 * Drawn from the same QuotaWindow as the full meter below, so a row and the
 * page it opens cannot colour one account differently — they used to, with
 * thresholds of their own, and the row ignored the server's severity.
 *
 * Labelled from the windows' length, which is what put each provider's
 * figures in them: ChatGPT calls its windows primary and secondary, and
 * which is which is a matter of length, not of name.
 *
 * An account with neither window but rows of its own — Google's, an
 * allowance per line of models — shows the one that binds, or its first,
 * labelled by its window's length as every gauge is ("7d"), and named in
 * full on hover: two empty windows would say nothing was read when it was.
 * One, because the label's slot holds a length and not a line's name; the
 * account's page lists them all.
 */
export function QuotaGauges({ quota }: { quota: Quota }) {
  const windowed = quota.five_hour_util >= 0 || quota.seven_day_util >= 0
  const rows = quota.limits ?? []
  const row = rows.find((l) => l.is_active) ?? rows[0]
  if (!windowed && row !== undefined) {
    return (
      <Gauge
        label={windowShort(row.window_seconds ?? 0)}
        name={row.title}
        window={new QuotaWindow(row.percent, row.severity)}
      />
    )
  }
  return (
    <>
      <Gauge
        label={windowShort(FIVE_HOURS_S)}
        window={new QuotaWindow(quota.five_hour_util, quota.five_hour_status)}
      />
      <Gauge
        label={windowShort(SEVEN_DAYS_S)}
        window={new QuotaWindow(quota.seven_day_util, quota.seven_day_status)}
      />
    </>
  )
}

function Gauge({ label, name = label, window: w }: { label: string; name?: string; window: QuotaWindow }) {
  return (
    <InlineMeter
      label={label}
      percent={w.percent}
      tone={w.tone}
      figure={w.percent === null ? '–' : w.label}
      alert={w.level === 'critical'}
      title={w.percent === null ? `${name}: not read yet` : `${name}: ${w.label} used`}
    />
  )
}

/**
 * Asking the provider for an account's windows now rather than at the next
 * poll, and the button that does it: on the account's overview and on its
 * Usage page, which both show the windows. `reading` is for QuotaMeter's
 * pending line.
 */
export function useQuotaRead(accountId: string, onDone: () => void) {
  const [reading, setReading] = useState(false)
  const read = () => {
    setReading(true)
    void api.accounts
      .refreshUsage(accountId)
      .catch(() => {})
      .finally(() => {
        setReading(false)
        onDone()
      })
  }
  return { reading, read }
}

export function ReadAgain({ reading, onRead }: { reading: boolean; onRead: () => void }) {
  return (
    <Button variant="outlined" size="sm" busy={reading} onClick={onRead}>
      {reading ? 'Reading…' : 'Read again'}
    </Button>
  )
}

/**
 * What the subscription has left — for Claude the same rows `/usage` prints in
 * the client, because they come from the same place, /api/oauth/usage; for
 * ChatGPT one row per window, as the backend reports them, titled by length.
 *
 * These are the subscription's own figures, so they count every client on the
 * account rather than only traffic through this gateway. Percentages arrive
 * 0-100, as the endpoints send them.
 */
export default function QuotaMeter({
  quota,
  pending,
  provider,
}: {
  quota: Quota | undefined
  pending: boolean
  /** Whose figures, for saying where they come from. */
  provider: string
}) {
  const from = providerOf(provider)?.usage
  if (quota === undefined) {
    return (
      <Muted size="xs">
        {pending ? 'Reading the subscription usage…' : 'Subscription usage is not available yet.'}
      </Muted>
    )
  }

  // The server's own normalised rows when it sent them; otherwise the two
  // headline windows, which it always does.
  const rows =
    quota.limits !== undefined && quota.limits.length > 0
      ? quota.limits.map((l) => ({
          title: l.title !== '' ? l.title : windowTitle(l.window_seconds ?? 0),
          window: new QuotaWindow(l.percent, l.severity),
          resets: l.resets_at,
          active: l.is_active,
        }))
      : [
          {
            title: windowTitle(FIVE_HOURS_S),
            window: new QuotaWindow(quota.five_hour_util, quota.five_hour_status),
            resets: quota.five_hour_reset,
            active: false,
          },
          {
            title: windowTitle(SEVEN_DAYS_S),
            window: new QuotaWindow(quota.seven_day_util, quota.seven_day_status),
            resets: quota.seven_day_reset,
            active: false,
          },
        ]

  return (
    <Stack gap={3}>
      {rows.map((r) => (
        <Window key={r.title} {...r} />
      ))}
      <Muted size="xs">
        From the account&rsquo;s own usage endpoint
        {from !== undefined && <> at {from.source}</>}, read {relative(quota.updated_at)} — the same
        figures as {from !== undefined ? from.client : 'its own client shows'}, so they include
        whatever else uses this account.
      </Muted>
    </Stack>
  )
}

function Window({
  title,
  window: w,
  resets,
  active,
}: {
  title: string
  window: QuotaWindow
  resets?: string
  active: boolean
}) {
  // A window never read has no reset to speak of, and is not the one binding.
  const known = w.percent !== null
  return (
    <MeterRow
      title={
        <>
          {title}
          {/* The server marks the window currently doing the limiting. */}
          {known && active && <span className="ml-1.5 text-on-surface-variant">· binding</span>}
        </>
      }
      figure={w.label}
      // The flag and the reset together: a window flagged as close to its
      // limit is exactly the one whose reset you want to know, and the flag
      // used to take the reset's place.
      hint={
        [w.flag, known && resets !== undefined && resets !== '' ? `resets ${relative(resets)}` : '']
          .filter((s) => s !== '')
          .join(' · ') || undefined
      }
      alert={w.level === 'critical'}
      active={known && active}
    >
      <UsageBar percent={w.percent} tone={w.tone} />
    </MeterRow>
  )
}
