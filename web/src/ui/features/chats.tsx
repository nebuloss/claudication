import { Fragment, useCallback, useMemo, useState, type ReactNode } from 'react'
import { ApiError, api, messageOf, type Chat, type Chats } from '../../api/client'
import { LogScale, Palette } from '../charts'
import { useLoader } from '../hooks'
import ChatContextDetail from './chat-context'
import { requestsHref, type RequestFilter } from './requests'
import { navigate } from '../route'
import {
  ContextMenu,
  Empty,
  ErrorState,
  MenuRow,
  Spinner,
  Table,
  compact,
  type Column,
} from '../primitives'

/**
 * Usage, per conversation.
 *
 * A per-day rollup cannot tell one runaway chat from a busy week, and a chat
 * is the unit a person actually thinks in. The grouping comes from the
 * identifier the client already sends — Claude Code's session uuid, opencode's
 * and crush's `ses_…`, Codex's session id — all of them in headers, so nothing
 * on this screen is derived from message content.
 *
 * A title, when there is one, is either the name the client asked a model for,
 * one the gateway generated (and renames as the chat grows), or one an
 * operator typed — right-click a chat to rename it or hand it back to
 * automatic naming. The id stays beneath it either way: it is the key back to
 * the client's own session list.
 */

function without(p: Record<string, string>, id: string): Record<string, string> {
  const next = { ...p }
  delete next[id]
  return next
}

/** A message about one row, shown under the table until the next action. */
type Notice = { id: string; text: string; tone: 'error' | 'info' }

/**
 * A title edited in place.
 *
 * Enter saves and Escape cancels; leaving the field cancels too, because a
 * rename saved by clicking somewhere else is a rename nobody meant to make.
 */
function TitleEditor({
  initial,
  onSave,
  onCancel,
}: {
  initial: string
  onSave: (title: string) => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState(initial)
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (draft.trim() !== '') onSave(draft)
      }}
      className="flex min-w-0 flex-col gap-0.5"
    >
      <input
        autoFocus
        aria-label="Chat title"
        value={draft}
        maxLength={120}
        onChange={(e) => setDraft(e.target.value)}
        onFocus={(e) => e.target.select()}
        onBlur={onCancel}
        onKeyDown={(e) => {
          if (e.key === 'Escape') onCancel()
        }}
        className="w-full rounded-[var(--radius-md3-xs)] border border-primary bg-surface-lowest px-1 py-0.5 font-medium text-on-surface outline-none"
      />
      <span className="text-xs text-on-surface-variant">Enter to save · Esc to cancel</span>
    </form>
  )
}

type SortKey = 'tokens' | 'context' | 'requests' | 'last'

/** Model colours, so a chat's models read the same here as in the charts. */
function useModelPalette(chats: Chat[]): Palette {
  return useMemo(() => {
    const names: string[] = []
    for (const c of chats) {
      for (const m of c.models) if (!names.includes(m)) names.push(m)
    }
    return new Palette(names)
  }, [chats])
}

/** A span in the units someone reads off a clock, not milliseconds. */
function span(first: string, last: string): string {
  const ms = new Date(last).getTime() - new Date(first).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const mins = Math.round(ms / 60000)
  if (mins < 1) return '<1m'
  if (mins < 60) return `${mins}m`
  const h = Math.floor(mins / 60)
  return `${h}h ${mins % 60}m`
}

function started(at: string): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return '—'
  const today = new Date()
  const sameDay =
    d.getFullYear() === today.getFullYear() &&
    d.getMonth() === today.getMonth() &&
    d.getDate() === today.getDate()
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  return sameDay ? time : `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}`
}

/**
 * The id, shortened to the part that identifies it.
 *
 * A uuid's first two groups are already unique across anything one gateway
 * sees, and the full value stays in the title attribute for copying. Shortened
 * rather than hidden: it is the join key back to the client's own session
 * list, which is the whole point of printing it.
 */
function shortID(id: string): string {
  if (id.length <= 22) return id
  return `${id.slice(0, 19)}…`
}

/**
 * A number that opens the request log filtered to what it counted.
 *
 * An <a> with a real href, so middle-click and "open in new tab" work and the
 * status bar says where it goes; the click is intercepted only to avoid
 * rebuilding the whole app to change a query parameter.
 */
function RequestLink({
  filter,
  tone,
  children,
}: {
  filter: Partial<RequestFilter>
  tone?: 'error'
  children: ReactNode
}) {
  const href = requestsHref(filter)
  return (
    <a
      href={href}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
        e.preventDefault()
        navigate(href)
      }}
      className={`state-layer rounded-[var(--radius-md3-xs)] px-1 underline decoration-dotted underline-offset-2 ${
        tone === 'error' ? 'text-error' : 'hover:text-primary'
      }`}
    >
      {children}
    </a>
  )
}

/** Slow enough not to repaint a table of bars constantly, fast enough to follow along. */
const CHATS_REFRESH_MS = 20_000

function tokens(c: Chat): number {
  return c.input_tokens + c.output_tokens + c.cache_tokens
}

export default function ChatsPanel({ days, onExpired }: { days: number; onExpired: () => void }) {
  const [sort, setSort] = useState<SortKey>('tokens')
  // The chat whose context curve is open, if any. One at a time: each is a
  // chart that follows its chat while open.
  const [open, setOpen] = useState<string | null>(null)
  const { data, error, loading, reload } = useLoader<Chats>(
    () => api.chats(days),
    onExpired,
    [days],
    // A chat gains requests as it is being held, and this panel is the one an
    // operator watches while using the gateway from another window.
    CHATS_REFRESH_MS,
  )

  // The right-click menu, the title being edited, and the chats waiting on a
  // refresh — keyed to the title they had when it was asked for, so the note
  // goes away by itself once the list shows a different one.
  const [menu, setMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [pending, setPending] = useState<Record<string, string>>({})
  const [notice, setNotice] = useState<Notice | null>(null)
  // The chat whose refresh is on its way back.
  const [renaming, setRenaming] = useState<string | null>(null)
  const closeMenu = useCallback(() => setMenu(null), [])

  const failed = (id: string, err: unknown) => {
    if (err instanceof ApiError && err.isUnauthenticated) {
      onExpired()
      return
    }
    setNotice({ id, text: messageOf(err), tone: 'error' })
  }

  const refreshTitle = (c: Chat) => {
    setMenu(null)
    setNotice(null)
    setRenaming(c.id)
    api
      .retitleChat(c.id)
      .then(
        (res) => {
          if (res.pending === true) {
            // Only after a restart, for a chat that has not spoken since.
            setPending((p) => ({ ...p, [c.id]: c.title }))
            setNotice({
              id: c.id,
              text: 'The gateway restarted since this chat’s last request, so it has nothing to name it from yet. It will be renamed at that next request.',
              tone: 'info',
            })
            return
          }
          setPending((p) => without(p, c.id))
          void reload()
        },
        (err) => failed(c.id, err),
      )
      .finally(() => setRenaming(null))
  }

  /**
   * The menu's toggle. Manual on a chat with no title has nothing to keep, so
   * it goes straight to typing one — saving it is what makes it manual.
   */
  const setMode = (c: Chat, manual: boolean) => {
    setMenu(null)
    if (manual && c.title === '') {
      setEditing(c.id)
      return
    }
    api.setTitleMode(c.id, manual).then(
      () => {
        if (manual) setPending((p) => without(p, c.id))
        void reload()
      },
      (err) => failed(c.id, err),
    )
  }

  const saveTitle = (c: Chat, title: string) => {
    setEditing(null)
    api.setChatTitle(c.id, title).then(
      () => {
        setPending((p) => without(p, c.id))
        setNotice(null)
        void reload()
      },
      (err) => failed(c.id, err),
    )
  }

  const report = data?.report
  const chats = report?.chats ?? []
  const palette = useModelPalette(chats)

  const sorted = useMemo(() => {
    const rows = [...chats]
    rows.sort((a, b) => {
      if (sort === 'requests') return b.requests - a.requests
      if (sort === 'context') return (b.context?.now ?? 0) - (a.context?.now ?? 0)
      if (sort === 'last') return new Date(b.last).getTime() - new Date(a.last).getTime()
      return tokens(b) - tokens(a)
    })
    return rows
  }, [chats, sort])

  // Every bar is read against the busiest chat, so the column compares chats
  // with each other rather than against an axis nobody drew.
  //
  // On a decade scale, and for the reason the Usage bars are: chats differ by
  // orders of magnitude, so linear widths put the leader at 100% and pinned
  // every other row to the minimum — a column of identical stubs. Normalised so
  // the leader fills, because in a ranked list the longest bar is the unit of
  // comparison. The number is printed beside it, since a log bar ranks rather
  // than quantifies.
  const width = useMemo(() => {
    const values = sorted.map(tokens)
    const scale = new LogScale(values, 0, 100)
    const leader = scale.y(Math.max(1, ...values))
    return (v: number) => (leader > 0 ? Math.max(2, (scale.y(v) / leader) * 100) : 2)
  }, [sorted])

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }

  const unattributed = report?.unattributed
  const anonymous = unattributed !== undefined && unattributed.requests > 0

  if (sorted.length === 0 && !anonymous) {
    return <Empty>No conversations in this window.</Empty>
  }

  const column = (label: string, key: SortKey): Column => ({
    label,
    onSort: () => setSort(key),
    sorted: sort === key ? 'desc' : undefined,
  })

  return (
    <div className="flex flex-col gap-3">
      <Table
        cap
        resizable="chats"
        head={[
          'Chat',
          'Client',
          'Models',
          column('Tokens', 'tokens'),
          column('Context', 'context'),
          column('Requests', 'requests'),
          column('Started', 'last'),
          'Span',
        ]}
      >
        {sorted.map((c) => (
          <Fragment key={c.id}>
            <tr
              className={`border-b border-outline-variant last:border-0 ${
                open === c.id ? 'bg-surface-container/40' : ''
              }`}
            >
              {/* The name when there is one, the id when there is not. The id
                  stays on the line beneath either way: it is the join key back to
                  the client's own session list, which is where the conversation
                  itself can actually be read. */}
              <td className="px-2 py-2">
                {editing === c.id ? (
                  <TitleEditor
                    initial={c.title}
                    onSave={(title) => saveTitle(c, title)}
                    onCancel={() => setEditing(null)}
                  />
                ) : (
                  /* The row's own control: opening the context curve is what a
                     chat row is clicked for, and a button says so to a keyboard
                     and a screen reader as well as to a pointer. Right-click (or
                     the keyboard's menu key) is for its name. */
                  <button
                    type="button"
                    aria-expanded={open === c.id}
                    aria-haspopup="menu"
                    onClick={() => setOpen(open === c.id ? null : c.id)}
                    onContextMenu={(e) => {
                      e.preventDefault()
                      setNotice(null)
                      setMenu({ id: c.id, x: e.clientX, y: e.clientY })
                    }}
                    className="state-layer flex w-full min-w-0 flex-col rounded-[var(--radius-md3-xs)] text-left"
                  >
                    <span className="flex min-w-0 items-center gap-1.5 font-medium text-on-surface" title={c.title}>
                      <span className="truncate">
                        {open === c.id ? '▾ ' : '▸ '}
                        {c.title || 'Unnamed'}
                      </span>
                      {renaming === c.id && <Spinner className="shrink-0" />}
                    </span>
                    <span title={c.id} className="truncate font-mono text-xs text-on-surface-variant">
                      {shortID(c.id)}
                      {c.title_manual && ' · named by hand'}
                      {pending[c.id] !== undefined && pending[c.id] === c.title &&
                        ' · renaming at next request'}
                    </span>
                  </button>
                )}
                {menu?.id === c.id && (
                  <ContextMenu at={menu} label={`Title of ${c.title || c.id}`} onClose={closeMenu}>
                    {/* One switch, and the action that goes with where it is:
                        an automatic title can be asked for again, a manual one
                        is typed. */}
                    <MenuRow checked={!c.title_manual} onClick={() => setMode(c, !c.title_manual)}>
                      Automatic naming
                    </MenuRow>
                    <div className="my-1 border-t border-outline-variant" />
                    {c.title_manual ? (
                      <MenuRow
                        onClick={() => {
                          setMenu(null)
                          setEditing(c.id)
                        }}
                      >
                        Rename…
                      </MenuRow>
                    ) : (
                      <MenuRow onClick={() => refreshTitle(c)}>Refresh title</MenuRow>
                    )}
                  </ContextMenu>
                )}
              </td>
              <td className="px-2 py-2 whitespace-nowrap text-on-surface-variant">
                {c.client || '—'}
              </td>
              <td className="px-2 py-2 whitespace-nowrap">
                <div className="flex items-center gap-2">
                  {c.models.map((m) => (
                    <span key={m} className="flex items-center gap-1 text-xs">
                      <span
                        aria-hidden
                        className="size-2 shrink-0 rounded-[2px]"
                        style={{ background: palette.colour(m) }}
                      />
                      {m.replace(/^claude-/, '')}
                    </span>
                  ))}
                  {c.models.length === 0 && <span className="text-on-surface-variant">—</span>}
                </div>
              </td>
              <td className="px-2 py-2 whitespace-nowrap">
                <div className="flex items-center gap-2">
                  <span className="h-2 w-24 shrink-0 overflow-hidden rounded-sm bg-surface-container">
                    <span
                      className="block h-full"
                      style={{
                        width: `${width(tokens(c))}%`,
                        background: palette.colour(c.models[0] ?? ''),
                      }}
                    />
                  </span>
                  <span className="tabular-nums">{compact(tokens(c))}</span>
                </div>
              </td>
              {/* What the client resends each turn, and how often it has started
                  over. The peak is the question a compaction setting answers. */}
              <td className="px-2 py-2 whitespace-nowrap">
                {c.context !== undefined && c.context.model !== '' ? (
                  <div className="flex flex-col">
                    <span className="tabular-nums text-on-surface">{compact(c.context.now)}</span>
                    <span className="text-xs tabular-nums text-on-surface-variant">
                      peak {compact(c.context.peak)}
                      {c.context.compactions > 0 && ` · ${c.context.compactions}× compacted`}
                    </span>
                  </div>
                ) : (
                  <span className="text-on-surface-variant">—</span>
                )}
              </td>
              {/* Both numbers link into the request log, on different filters:
                  the total asks "what happened in this chat", the failures ask
                  "which ones broke". They are real links — the URL carries the
                  filter, so it can be shared, bookmarked and walked back to —
                  rather than a list this table grows for itself. */}
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">
                <RequestLink filter={{ chat: [c.id] }}>
                  {c.requests}
                </RequestLink>
                {c.errors > 0 && (
                  <RequestLink
                    tone="error"
                    filter={{ chat: [c.id], status: 'failed' }}
                  >
                    · {c.errors} failed
                  </RequestLink>
                )}
              </td>
              <td
                title={c.first}
                className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant"
              >
                {started(c.first)}
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant">
                {span(c.first, c.last)}
              </td>
            </tr>
            {open === c.id && (
              <tr className="border-b border-outline-variant bg-surface-container/40">
                <td colSpan={8} className="px-3 pt-1 pb-4">
                  <ChatContextDetail
                    id={c.id}
                    colour={palette.colour(c.context?.model || c.models[0] || '')}
                    onExpired={onExpired}
                  />
                </td>
              </tr>
            )}
          </Fragment>
        ))}

        {/* Shown rather than dropped: without it the totals here would not add
            up to the ones above, and this row is how an operator finds out a
            client is talking to the gateway unlabelled. Never merged into a
            chat — a fabricated grouping is indistinguishable from a true one
            once it is in the table. */}
        {anonymous && (
          <tr className="border-t border-outline-variant bg-surface-container/40">
            <td className="px-2 py-2">
              <div className="flex flex-col">
                <span className="font-medium text-on-surface-variant">Unattributed</span>
                <span className="text-xs text-on-surface-variant italic">no session id sent</span>
              </div>
            </td>
            <td className="px-2 py-2 text-on-surface-variant">—</td>
            <td className="px-2 py-2 text-on-surface-variant">—</td>
            <td className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant">
              {compact(tokens(unattributed))}
            </td>
            <td className="px-2 py-2 text-on-surface-variant">—</td>
            <td className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant">
              {unattributed.requests}
              {unattributed.errors > 0 && <span> · {unattributed.errors} failed</span>}
            </td>
            <td className="px-2 py-2 text-on-surface-variant">—</td>
            <td className="px-2 py-2 text-on-surface-variant">—</td>
          </tr>
        )}
      </Table>

      {notice !== null && (
        <p
          role={notice.tone === 'error' ? 'alert' : 'status'}
          className={`m-0 text-sm ${notice.tone === 'error' ? 'text-error' : 'text-on-surface-variant'}`}
        >
          {notice.text}
        </p>
      )}

      <p className="m-0 text-xs text-on-surface-variant">
        A chat is grouped by the session id its client sends, never by anything read from the
        messages. Context is what its main model is resent each turn; open a chat to see it grow
        and where its client compacted. Right-click a chat’s name to rename it. {report !== undefined && report.total > sorted.length
          ? `Showing the ${sorted.length} busiest of ${report.total}.`
          : ''}
      </p>
    </div>
  )
}
