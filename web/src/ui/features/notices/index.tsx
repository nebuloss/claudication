import { api, type Notice } from '../../../api/client'
import { READS } from '../../../api/live'
import { ago, relative } from '../../format'
import { useLoader } from '../../hooks'
import { Card, ICON, Muted, Popover, Tip, follow, useAnchoredPanel } from '../../primitives'
import { providerLabel } from '../../providers'
import { toneFill, type Tone } from '../../styles/tone'

/**
 * What needs the operator now: the gateway's current problems, worked out
 * from its state on every read (GET /admin/notices) — an account to sign in
 * again, a model failing everywhere, a client whose requests keep failing.
 * Nothing to dismiss: a notice goes once its problem does. Each is a link to
 * the page where it is dealt with.
 */
function useNotices() {
  return useLoader((signal) => api.gateway.notices(signal), { on: READS.notices })
}

/** A notice's severity in the meaning colours every chip and banner uses. */
const TONE: Record<Notice['severity'], Tone> = { error: 'error', warn: 'warn', info: 'neutral' }

/** When a notice began or ends, as few words as say it. */
function when(n: Notice): string {
  if (n.until !== undefined) return relative(n.until, 'now')
  if (n.since !== undefined) return ago(n.since)
  return ''
}

function NoticeRow({ n, onOpen }: { n: Notice; onOpen?: () => void }) {
  const at = when(n)
  const body = (
    <>
      <span
        className={`mt-1.5 size-2 shrink-0 rounded-full ${n.severity === 'info' ? 'bg-on-surface-variant/50' : toneFill[TONE[n.severity]]}`}
        aria-hidden
      />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex flex-wrap items-baseline gap-x-2 text-sm text-on-surface">
          {n.provider !== undefined && (
            <span className="text-xs font-medium text-on-surface-variant">{providerLabel(n.provider)}</span>
          )}
          <span className={n.link !== undefined ? 'underline-offset-2 group-hover:underline' : ''}>{n.title}</span>
          {at !== '' && <span className="text-xs text-on-surface-variant tabular-nums">{at}</span>}
        </span>
        {n.detail !== undefined && (
          <span className="line-clamp-2 text-xs text-on-surface-variant">{n.detail}</span>
        )}
      </span>
    </>
  )
  const row = 'group flex gap-3 rounded-md3-m px-2 py-2'
  if (n.link === undefined) return <li className={row}>{body}</li>
  const go = follow(n.link)
  return (
    <li>
      <a
        href={n.link}
        onClick={(e) => {
          go(e)
          onOpen?.()
        }}
        className={`${row} state-layer`}
      >
        {body}
      </a>
    </li>
  )
}

function NoticeList({ notices, onOpen }: { notices: Notice[]; onOpen?: () => void }) {
  return (
    <ul className="-mx-2 flex flex-col">
      {notices.map((n) => (
        <NoticeRow key={n.id} n={n} onOpen={onOpen} />
      ))}
    </ul>
  )
}

/**
 * The Overview's card: every notice, in a box that scrolls past five or so,
 * so Status stays on the first screen. Nothing at all when there is none —
 * the bell in the header still says so — so a healthy gateway's Overview
 * opens on its status.
 */
export function NoticesCard() {
  const { data } = useNotices()
  if (data === null || data.length === 0) return null
  const errors = data.filter((n) => n.severity === 'error').length
  return (
    <Card
      title="Notices"
      aside={
        <Muted as="span" size="xs">
          {errors > 0 ? `${errors} to fix · ${data.length} in all` : `${data.length} to look at`}
        </Muted>
      }
    >
      <div className="max-h-64 overflow-y-auto overscroll-contain pr-2">
        <NoticeList notices={data} />
      </div>
    </Card>
  )
}

/**
 * The header's bell: how many notices there are, in the colour of the worst,
 * and the list on a click, from any page.
 */
export function NoticeBell() {
  const { data } = useNotices()
  const p = useAnchoredPanel({ width: 420, maxHeight: 480 })
  const notices = data ?? []
  const worst = notices.some((n) => n.severity === 'error')
    ? 'bg-error text-on-error'
    : notices.some((n) => n.severity === 'warn')
      ? 'bg-warning text-on-warning'
      : 'bg-surface-highest text-on-surface'
  const label = notices.length === 0 ? 'Notices: nothing needs you' : `Notices: ${notices.length}`
  const bell = (
    <button
          ref={p.anchor}
          type="button"
          aria-label={label}
          aria-haspopup="dialog"
          aria-expanded={p.open}
          onClick={p.toggle}
          className="state-layer relative grid size-10 shrink-0 place-items-center rounded-md3-full border border-outline text-on-surface-variant"
        >
          <svg viewBox="0 0 24 24" className="size-5 fill-current" aria-hidden>
            <path d={ICON.bell} />
          </svg>
          {notices.length > 0 && (
            <span
              className={`absolute -top-1 -right-1 grid h-5 min-w-5 place-items-center rounded-full px-1 text-[0.6875rem] font-medium tabular-nums ${worst}`}
            >
              {notices.length}
            </span>
          )}
    </button>
  )
  return (
    <>
      {/* No tip while the panel is open: it would sit over the panel it
          names. */}
      {p.open ? bell : <Tip tip={label}>{bell}</Tip>}
      {p.at !== null && (
        <Popover panelRef={p.panel} at={p.at} role="dialog" label="Notices" className="p-3">
          <h2 className="mb-1 px-0 text-sm font-medium text-on-surface">Notices</h2>
          {notices.length === 0 ? (
            <Muted size="xs">No account, model or client needs attention now.</Muted>
          ) : (
            <NoticeList notices={notices} onOpen={p.close} />
          )}
        </Popover>
      )}
    </>
  )
}
