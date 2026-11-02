import { useState } from 'react'
import { api, messageOf, type Notice } from '../../../api/client'
import { READS } from '../../../api/live'
import { ProviderMark } from '../../brands'
import { ago, relative } from '../../format'
import { useLoader } from '../../hooks'
import { Button, ICON, IconButton, Modal, Muted, Tip, follow, type IconName } from '../../primitives'
import { useSession } from '../../session'
import { toneContainer, type Tone } from '../../styles/tone'

/**
 * What needs the operator now: the gateway's current problems, worked out
 * from its state on every read (GET /admin/notices) — an account to sign in
 * again, a model failing everywhere, a client whose requests keep failing.
 * A notice goes once its problem does, and each is a link to the page where
 * it is dealt with. One that will not go — a model retired upstream — can be
 * marked read: it stays listed, faded and last, out of the bell's count, until
 * its problem is gone; the gateway forgets the mark then, so the same problem
 * starting again is news.
 *
 * Read through the bell in the header, from any page: its count in the
 * colour of the worst, and the notification centre — a sheet along the
 * window's edge — on a click.
 */
function useNotices() {
  return useLoader((signal) => api.gateway.notices(signal), { on: READS.notices })
}

type Severity = Notice['severity']

/** How each severity is drawn: its glyph on its meaning's colour, and what the filter calls it. */
const SEVERITY: Record<Severity, { icon: IconName; tone: Tone; label: string }> = {
  error: { icon: 'error', tone: 'error', label: 'To fix' },
  warn: { icon: 'warning', tone: 'warn', label: 'Warnings' },
  info: { icon: 'info', tone: 'neutral', label: 'Info' },
}

/** The groups the centre lists notices under, in its order, each with its section's glyph. */
const GROUPS: { kind: Notice['kind']; label: string; icon: IconName }[] = [
  { kind: 'gateway', label: 'Gateway', icon: 'gateway' },
  { kind: 'account', label: 'Accounts', icon: 'accounts' },
  { kind: 'model', label: 'Models', icon: 'models' },
  { kind: 'client', label: 'Clients', icon: 'clients' },
]

/** When a notice began or ends, as few words as say it. */
function when(n: Notice): string {
  if (n.until !== undefined) return relative(n.until, 'now')
  if (n.since !== undefined) return ago(n.since)
  return ''
}

function Glyph({ icon, className = 'size-4' }: { icon: IconName; className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden className={`${className} shrink-0 fill-current`}>
      <path d={ICON[icon]} />
    </svg>
  )
}

/**
 * One notice: its severity's glyph on its colour, the provider it is about,
 * the sentence and what it rests on, when — and the whole row the way to
 * where it is dealt with.
 */
function NoticeRow({
  n,
  onOpen,
  onMark,
}: {
  n: Notice
  onOpen: () => void
  onMark: (ids: string[], read: boolean) => Promise<void>
}) {
  const s = SEVERITY[n.severity]
  const at = when(n)
  const read = n.read !== undefined
  const [busy, setBusy] = useState(false)
  const body = (
    <>
      <span className={`grid size-9 shrink-0 place-items-center rounded-full ${toneContainer[s.tone]}`}>
        <Glyph icon={s.icon} className="size-5" />
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="text-sm font-medium text-on-surface">{n.title}</span>
        {n.detail !== undefined && (
          <span className="line-clamp-3 text-xs text-on-surface-variant">{n.detail}</span>
        )}
        {(n.provider !== undefined || at !== '') && (
          <span className="flex items-center gap-2 text-xs text-on-surface-variant">
            {n.provider !== undefined && <ProviderMark provider={n.provider} size="sm" />}
            {at !== '' && <span className="tabular-nums">{at}</span>}
          </span>
        )}
      </span>
      {n.link !== undefined && <Glyph icon="next" className="mt-2 size-5 text-on-surface-variant" />}
    </>
  )
  const row = 'flex min-w-0 flex-1 gap-3 p-3'
  const go = n.link === undefined ? undefined : follow(n.link)
  return (
    // The row is the way to the notice's page; the read switch sits beside
    // it, not in it, since a button cannot live inside a link.
    <li className={`flex items-start rounded-md3-l bg-surface-lowest ${read ? 'opacity-60' : ''}`}>
      {n.link === undefined || go === undefined ? (
        <div className={row}>{body}</div>
      ) : (
        <a
          href={n.link}
          onClick={(e) => {
            go(e)
            onOpen()
          }}
          className={`${row} state-layer rounded-md3-l`}
        >
          {body}
        </a>
      )}
      <span className="p-2">
        <IconButton
          label={read ? 'Mark as unread' : 'Mark as read'}
          busy={busy}
          onClick={() => {
            setBusy(true)
            void onMark([n.id], !read).finally(() => setBusy(false))
          }}
        >
          <path d={ICON[read ? 'unread' : 'done']} />
        </IconButton>
      </span>
    </li>
  )
}

/**
 * The notification centre: every notice, filtered by how bad it is and
 * grouped by what it is about, each a link that closes the centre on the way
 * to its page.
 */
function NoticeCenter({ notices, onClose }: { notices: Notice[]; onClose: () => void }) {
  const [only, setOnly] = useState<Severity | 'all'>('all')
  const [error, setError] = useState('')
  const [marking, setMarking] = useState(false)
  const session = useSession()
  const count = (s: Severity) => notices.filter((n) => n.severity === s).length
  // Unread first, the server's order kept within each: what has been seen
  // goes to the bottom rather than out of sight.
  const filtered = only === 'all' ? notices : notices.filter((n) => n.severity === only)
  const shown = [...filtered.filter((n) => n.read === undefined), ...filtered.filter((n) => n.read !== undefined)]
  const unread = shown.filter((n) => n.read === undefined)
  // The list follows from the live stream: the mark's write tells every tab.
  const mark = async (ids: string[], read: boolean) => {
    setError('')
    try {
      await api.gateway.markNotices(ids, read)
    } catch (err) {
      if (!session.report(err)) setError(messageOf(err))
    }
  }
  const filters: { id: Severity | 'all'; label: string; n: number }[] = [
    { id: 'all', label: 'All', n: notices.length },
    ...(['error', 'warn', 'info'] as const).map((s) => ({ id: s, label: SEVERITY[s].label, n: count(s) })),
  ]
  return (
    <Modal title="Notifications" size="sheet" onClose={onClose}>
      {notices.length === 0 ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-3 py-16 text-center">
          <span className="grid size-14 place-items-center rounded-full bg-success-container text-on-success-container">
            <Glyph icon="done" className="size-7" />
          </span>
          <span className="text-base text-on-surface">Nothing needs you</span>
          <Muted size="xs">No account, model or client has a problem now. This updates as things change.</Muted>
        </div>
      ) : (
        <div className="flex flex-col gap-5">
          {/* By how bad: each with its count, those with none left out but All;
              and every unread one shown marked read at once. */}
          <div className="flex flex-wrap items-center gap-2">
            <div className="flex flex-1 flex-wrap gap-2" role="radiogroup" aria-label="Show">
              {filters
                .filter((f) => f.id === 'all' || f.n > 0)
                .map((f) => {
                  const on = f.id === only
                  return (
                    <button
                      key={f.id}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => setOnly(f.id)}
                      className={`state-layer flex h-8 items-center gap-1.5 rounded-md3-s px-3 text-xs font-medium ${
                        on
                          ? 'bg-secondary-container text-on-secondary-container'
                          : 'text-on-surface-variant ring-1 ring-outline-variant ring-inset'
                      }`}
                    >
                      {f.id !== 'all' && <Glyph icon={SEVERITY[f.id].icon} />}
                      {f.label}
                      <span className="tabular-nums opacity-70">{f.n}</span>
                    </button>
                  )
                })}
            </div>
            {unread.length > 0 && (
              <Button
                variant="text"
                size="sm"
                icon="doneAll"
                busy={marking}
                onClick={() => {
                  setMarking(true)
                  void mark(
                    unread.map((n) => n.id),
                    true,
                  ).finally(() => setMarking(false))
                }}
              >
                Mark all as read
              </Button>
            )}
          </div>
          {error !== '' && <p className="text-sm text-error">{error}</p>}
          {GROUPS.map((g) => {
            const items = shown.filter((n) => n.kind === g.kind)
            if (items.length === 0) return null
            return (
              <section key={g.kind} aria-label={g.label}>
                <h3 className="mb-2 flex items-center gap-2 text-xs font-semibold tracking-wide text-on-surface-variant uppercase">
                  <Glyph icon={g.icon} />
                  {g.label}
                  <span className="tabular-nums">{items.length}</span>
                </h3>
                <ul className="m-0 flex list-none flex-col gap-2 p-0">
                  {items.map((n) => (
                    <NoticeRow key={n.id} n={n} onOpen={onClose} onMark={mark} />
                  ))}
                </ul>
              </section>
            )
          })}
        </div>
      )}
    </Modal>
  )
}

/**
 * The header's bell: how many notices there are, in the colour of the worst,
 * and the notification centre on a click, from any page.
 */
export function NoticeBell() {
  const { data } = useNotices()
  const [open, setOpen] = useState(false)
  const notices = data ?? []
  // The count is what has not been read; the centre still lists the rest.
  const unread = notices.filter((n) => n.read === undefined)
  const worst = unread.some((n) => n.severity === 'error')
    ? 'bg-error text-on-error'
    : unread.some((n) => n.severity === 'warn')
      ? 'bg-warning text-on-warning'
      : 'bg-surface-highest text-on-surface'
  const label =
    unread.length > 0
      ? `Notifications: ${unread.length} unread`
      : notices.length > 0
        ? `Notifications: ${notices.length} read`
        : 'Notifications: nothing needs you'
  return (
    <>
      <Tip tip={label}>
        <button
          type="button"
          aria-label={label}
          aria-haspopup="dialog"
          aria-expanded={open}
          onClick={() => setOpen(true)}
          className="state-layer relative grid size-10 shrink-0 place-items-center rounded-md3-full border border-outline text-on-surface-variant"
        >
          <Glyph icon="bell" className="size-5" />
          {unread.length > 0 && (
            <span
              className={`absolute -top-1 -right-1 grid h-5 min-w-5 place-items-center rounded-full px-1 text-[0.6875rem] font-medium tabular-nums ${worst}`}
            >
              {unread.length}
            </span>
          )}
        </button>
      </Tip>
      {open && <NoticeCenter notices={notices} onClose={() => setOpen(false)} />}
    </>
  )
}
