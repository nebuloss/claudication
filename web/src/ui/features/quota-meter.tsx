import type { Quota } from '../../api/client'

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
      <p className="m-0 text-xs text-on-surface-variant">
        {pending ? 'Reading the subscription usage…' : 'Subscription usage is not available yet.'}
      </p>
    )
  }

  // The server's own normalised rows when it sent them; otherwise the two
  // headline windows, which it always does.
  const rows =
    quota.limits !== undefined && quota.limits.length > 0
      ? quota.limits.map((l) => ({
          title: l.title,
          percent: l.percent,
          resets: l.resets_at,
          severity: l.severity,
          active: l.is_active,
        }))
      : [
          {
            title: 'Current session',
            percent: quota.five_hour_util,
            resets: quota.five_hour_reset,
            severity: quota.five_hour_status === 'allowed' ? 'normal' : quota.five_hour_status,
            active: false,
          },
          {
            title: 'Current week (all models)',
            percent: quota.seven_day_util,
            resets: quota.seven_day_reset,
            severity: quota.seven_day_status === 'allowed' ? 'normal' : quota.seven_day_status,
            active: false,
          },
        ]

  return (
    <div className="flex flex-col gap-3">
      {rows.map((r) => (
        <Window key={r.title} {...r} />
      ))}
      <p className="m-0 text-[11px] text-on-surface-variant">
        From the account's own usage endpoint, read {relative(quota.updated_at)} — the same figures
        as <span className="font-mono">/usage</span> in Claude Code, so they include whatever else
        uses this account.
      </p>
    </div>
  )
}

function Window({
  title,
  percent,
  resets,
  severity,
  active,
}: {
  title: string
  percent: number
  resets?: string
  severity?: string
  active: boolean
}) {
  // -1 is "never fetched", which is not the same as zero and must not draw an
  // empty bar as though the window were untouched.
  if (percent < 0) {
    return (
      <Row title={title} right="unknown" active={false}>
        <div className="h-2 rounded-full bg-surface-high" />
      </Row>
    )
  }

  const pct = Math.min(100, Math.round(percent))
  const bad = severity !== undefined && severity !== '' && severity !== 'normal'
  const colour = bad || pct >= 95 ? 'bg-error' : pct >= 75 ? 'bg-warning' : 'bg-primary'

  return (
    <Row
      title={title}
      right={`${pct}%`}
      hint={bad ? severity : resets !== undefined && resets !== '' ? `resets ${relative(resets)}` : undefined}
      tone={bad || pct >= 95 ? 'error' : undefined}
      active={active}
    >
      <div className="h-2 overflow-hidden rounded-full bg-surface-high">
        <div className={`h-full rounded-full ${colour}`} style={{ width: `${Math.max(pct, 1)}%` }} />
      </div>
    </Row>
  )
}

function Row({
  title,
  right,
  hint,
  tone,
  active,
  children,
}: {
  title: string
  right: string
  hint?: string
  tone?: 'error'
  active: boolean
  children: React.ReactNode
}) {
  return (
    <div>
      <div className="mb-1 flex items-baseline justify-between gap-3 text-xs">
        <span className={active ? 'font-medium text-on-surface' : 'text-on-surface-variant'}>
          {title}
          {/* The server marks the window currently doing the limiting. */}
          {active && <span className="ml-1.5 text-on-surface-variant">· binding</span>}
        </span>
        <span
          className={`tabular-nums ${tone === 'error' ? 'font-medium text-error' : 'text-on-surface'}`}
        >
          {right}
          {hint !== undefined && (
            <span className="ml-2 font-normal text-on-surface-variant">{hint}</span>
          )}
        </span>
      </div>
      {children}
    </div>
  )
}

/** "in 3h", "12m ago" — enough to know whether it is worth waiting. */
function relative(iso: string | undefined): string {
  if (iso === undefined || iso === '') return 'unknown'
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'unknown'
  const delta = then - Date.now()
  const mins = Math.round(Math.abs(delta) / 60000)
  const text =
    mins < 1
      ? 'now'
      : mins < 60
        ? `${mins}m`
        : mins < 1440
          ? `${Math.round(mins / 60)}h`
          : `${Math.round(mins / 1440)}d`
  if (text === 'now') return 'just now'
  return delta < 0 ? `${text} ago` : `in ${text}`
}
