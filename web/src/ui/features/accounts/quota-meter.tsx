import type { Quota } from '../../../api/client'
import { QuotaWindow } from '../../domain/quota'
import { MeterRow, Muted, Stack, UsageBar } from '../../primitives'
import { relative } from '../../format'

/**
 * The two headline windows, short enough for a row: the account list, and the
 * header of an account's page.
 *
 * Drawn from the same QuotaWindow as the full meter below, so a row and the
 * page it opens cannot colour one account differently — they used to, with
 * thresholds of their own, and the row ignored the server's severity.
 */
export function QuotaGauges({ quota }: { quota: Quota }) {
  return (
    <>
      <Gauge label="5h" window={new QuotaWindow(quota.five_hour_util, quota.five_hour_status)} />
      <Gauge label="7d" window={new QuotaWindow(quota.seven_day_util, quota.seven_day_status)} />
    </>
  )
}

function Gauge({ label, window: w }: { label: string; window: QuotaWindow }) {
  return (
    <span
      className="flex items-center gap-1.5"
      title={w.percent === null ? `${label}: not read yet` : `${label}: ${w.label} used`}
    >
      <span className="w-5 text-on-surface-variant">{label}</span>
      <UsageBar size="sm" percent={w.percent} tone={w.tone} />
      <span className={`w-8 text-right ${w.level === 'critical' ? 'text-error' : ''}`}>
        {w.percent === null ? '–' : w.label}
      </span>
    </span>
  )
}

/**
 * What the subscription has left — the same rows `/usage` prints in the client,
 * because they come from the same place: /api/oauth/usage.
 *
 * These are the subscription's own figures, so they count every client on the
 * account rather than only traffic through this gateway. Percentages arrive
 * 0-100, as the endpoint sends them.
 */
export default function QuotaMeter({
  quota,
  pending,
}: {
  quota: Quota | undefined
  pending: boolean
}) {
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
          title: l.title,
          window: new QuotaWindow(l.percent, l.severity),
          resets: l.resets_at,
          active: l.is_active,
        }))
      : [
          {
            title: 'Current session',
            window: new QuotaWindow(quota.five_hour_util, quota.five_hour_status),
            resets: quota.five_hour_reset,
            active: false,
          },
          {
            title: 'Current week (all models)',
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
      <p className="text-[11px] text-on-surface-variant">
        From the account's own usage endpoint, read {relative(quota.updated_at)} — the same figures
        as <span className="font-mono">/usage</span> in Claude Code, so they include whatever else
        uses this account.
      </p>
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
      hint={
        w.flag !== ''
          ? w.flag
          : known && resets !== undefined && resets !== ''
            ? `resets ${relative(resets)}`
            : undefined
      }
      alert={w.level === 'critical'}
      active={known && active}
    >
      <UsageBar percent={w.percent} tone={w.tone} />
    </MeterRow>
  )
}
