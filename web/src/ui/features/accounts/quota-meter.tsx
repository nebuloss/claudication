import { useState } from 'react'
import { api, type Quota } from '../../../api/client'
import { FIVE_HOURS_S, QuotaWindow, SEVEN_DAYS_S, windowShort, windowTitle } from '../../domain/quota'
import { Button, HoverTip, InlineMeter, MeterRow, Muted, Stack, UsageBar } from '../../primitives'
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
 * Every account's gauges sit in one HoverTip, whose bubble is the account's
 * rows (usageRows) with their shares and resets: the same rows the account's
 * page draws, so the two cannot disagree, and the same widget for every
 * provider. No gauge carries a browser tooltip of its own.
 *
 * An account with neither window but rows of its own — Google's, an
 * allowance per line of models — has no one figure for the whole account:
 * its allowances are spent apart, and an average of them would describe
 * none. So its gauge is the fullest allowance, the one that refuses first,
 * as an account's binding window is, labelled by its length as every gauge
 * is ("7d"). One gauge, because the label's slot holds a length and not a
 * line's name; the bubble names them all.
 */
export function QuotaGauges({ quota, stacked = false }: { quota: Quota; stacked?: boolean }) {
  const windowed = quota.five_hour_util >= 0 || quota.seven_day_util >= 0
  const limits = quota.limits ?? []
  let gauges
  if (!windowed && limits.length > 0) {
    const fullest = limits.reduce((a, b) => (b.percent > a.percent ? b : a))
    gauges = (
      <Gauge label={windowShort(fullest.window_seconds ?? 0)} window={new QuotaWindow(fullest.percent, fullest.severity)} />
    )
  } else {
    gauges = (
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
  return (
    <HoverTip label="Subscription usage" tip={<UsageTip rows={usageRows(quota)} />}>
      {/* The row the list and the page header lay gauges in, or the column
          the activity table stacks them in. */}
      <span className={stacked ? 'inline-flex flex-col gap-1' : 'inline-flex items-center gap-3'}>{gauges}</span>
    </HoverTip>
  )
}

function Gauge({ label, window: w }: { label: string; window: QuotaWindow }) {
  return (
    <InlineMeter
      label={label}
      percent={w.percent}
      tone={w.tone}
      figure={w.percent === null ? '–' : w.label}
      alert={w.level === 'critical'}
    />
  )
}

/** One row of an account's usage, as the page and the bubble both say it. */
type UsageRow = {
  title: string
  window: QuotaWindow
  resets?: string
  active: boolean
}

/**
 * An account's usage as rows: the server's own normalised rows when it sent
 * them — Claude's /usage rows, ChatGPT's windows, Google's allowances —
 * otherwise the two headline windows, which it always sends. The one source
 * of what an account's usage says, for the full meter and the gauges' bubble.
 */
function usageRows(quota: Quota): UsageRow[] {
  if (quota.limits !== undefined && quota.limits.length > 0) {
    return quota.limits.map((l) => ({
      title: l.title !== '' ? l.title : windowTitle(l.window_seconds ?? 0),
      window: new QuotaWindow(l.percent, l.severity),
      resets: l.resets_at,
      active: l.is_active,
    }))
  }
  return [
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
}

/** What a row says after its title, the page's hint and the bubble alike. */
function rowHint(r: UsageRow): string {
  const known = r.window.percent !== null
  return [r.window.flag, known && r.resets !== undefined && r.resets !== '' ? `resets ${relative(r.resets)}` : '']
    .filter((s) => s !== '')
    .join(' · ')
}

/** The gauges' bubble: each row, its share and its reset, one point each. */
function UsageTip({ rows }: { rows: UsageRow[] }) {
  return (
    <>
      {rows.map((r) => {
        const hint = rowHint(r)
        return (
          <p key={r.title}>
            <span className="font-medium">{r.title}</span> · {r.window.percent === null ? 'not read yet' : `${r.window.label} used`}
            {hint !== '' && <span className="text-on-surface-variant"> · {hint}</span>}
          </p>
        )
      })}
    </>
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

  const rows = usageRows(quota)

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

function Window(r: UsageRow) {
  const { title, window: w, active } = r
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
      hint={rowHint(r) || undefined}
      alert={w.level === 'critical'}
      active={known && active}
    >
      <UsageBar percent={w.percent} tone={w.tone} />
    </MeterRow>
  )
}
