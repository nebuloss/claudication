import { Fragment, useCallback, useMemo, useState, type ReactNode } from 'react'
import {
  api,
  type Chat,
  type Chats,
  type UsageScope,
} from '../../../api/client'
import { LogScale, Palette } from '../../charts'
import { useAction, useLoader } from '../../hooks'
import ChatContextDetail from './context'
import { requestsHref, type FilterFields } from '../requests/filter'
import {
  AppLink,
  Card,
  Chip,
  ContextMenu,
  Empty,
  ErrorBanner,
  ErrorState,
  Loading,
  MenuRow,
  Muted,
  Stack,
  Table,
  Td,
  Tip,
  Tr,
  WindowPicker,
  type Column,
} from '../../primitives'
import { between, compact, sameDayStamp } from '../../format'
import { READS } from '../../../api/live'

/**
 * Usage, per conversation.
 *
 * A per-day rollup cannot tell one runaway chat from a busy week, and a chat
 * is the unit a person actually thinks in. The grouping comes from the
 * identifier the client already sends — Claude Code's session uuid, opencode's
 * and crush's `ses_…`, Codex's session id — all of them in headers, so nothing
 * on this screen is derived from message content.
 *
 * A title, when there is one, is the name the client asked a model for or one
 * the gateway generated — renamed once from the chat's opening exchange — or
 * one an operator typed by right-clicking the chat, which the gateway then
 * leaves alone. The id stays beneath it either way: it is the key back to the
 * client's own session list.
 */

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
        className="w-full rounded-md3-xs border border-primary bg-surface-lowest px-1 py-0.5 font-medium text-on-surface outline-none"
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

/**
 * The id, shortened to the part that identifies it.
 *
 * A uuid's first two groups are already unique across anything one gateway
 * sees, and the full value is on its hover. Shortened
 * rather than hidden: it is the join key back to the client's own session
 * list, which is the whole point of printing it.
 */
function shortID(id: string): string {
  if (id.length <= 22) return id
  return `${id.slice(0, 19)}…`
}

/**
 * What a chat is called on its row: its title, or, for one with none, how its
 * client was run. Claude Code never names a claude -p session, so a headless
 * chat is unnamed for good; "Headless (claude -p) — unnamed" says why, where
 * a bare "Unnamed" reads as a name still to come.
 */
function chatName(c: Chat): string {
  if (c.title !== '') return c.title
  return c.surface ? `${c.surface} — unnamed` : 'Unnamed'
}

/**
 * A number that opens the request log filtered to what it counted. A real
 * link, so the filter it carries can be shared, bookmarked and walked back to.
 */
function RequestLink({
  filter,
  tone,
  children,
}: {
  filter: Partial<FilterFields>
  tone?: 'error'
  children: ReactNode
}) {
  return (
    <AppLink href={requestsHref(filter)} cell tone={tone}>
      {children}
    </AppLink>
  )
}

function tokens(c: Chat): number {
  return c.input_tokens + c.output_tokens + c.cache_tokens
}

/** How far back the Chats tab looks. */
const CHAT_WINDOWS = [1, 7, 30] as const

/**
 * A chats page: every conversation the gateway has seen in the window, or one
 * account's or one key's. The tab above it says what it is, so it carries no
 * heading of its own — only the window to look over.
 */
export function ChatsPage({ scope = {} }: { scope?: UsageScope }) {
  const [days, setDays] = useState<number>(7)
  // The window above the card rather than in it, so the card starts with
  // the chats.
  return (
    <Stack gap={3}>
      <div className="flex justify-end">
        <WindowPicker value={days} windows={CHAT_WINDOWS} onChange={setDays} />
      </div>
      <Card>
        <ChatsPanel days={days} scope={scope} />
      </Card>
    </Stack>
  )
}

export default function ChatsPanel({
  days,
  scope = {},
}: {
  days: number
  /** One account's or one key's chats, or every chat when empty. */
  scope?: UsageScope
}) {
  const [sort, setSort] = useState<SortKey>('tokens')
  // The chat whose context curve is open, if any. One at a time: each is a
  // chart that follows its chat while open.
  const [open, setOpen] = useState<string | null>(null)
  const { data, error, loading, reload } = useLoader<Chats>(
    (signal) => api.traffic.chats(days, scope, signal),
    {
      deps: [days, scope.account, scope.key],
      // A chat gains requests as it is being held, and this panel is the one an
      // operator watches while using the gateway from another window.
      on: READS.chats,
    },
  )

  // The right-click menu, the title being edited, and what went wrong saving
  // one.
  const [menu, setMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const rename = useAction()
  const closeMenu = useCallback(() => setMenu(null), [])

  const saveTitle = (c: Chat, title: string) => {
    setEditing(null)
    void rename.run(c.id, async () => {
      await api.traffic.setChatTitle(c.id, title)
      void reload()
    })
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

  if (loading && data === null) return <Loading />
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
    <Stack gap={3}>
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
            <Tr selected={open === c.id}>
              {/* The name when there is one, the id when there is not. The id
                  stays on the line beneath either way: it is the join key back to
                  the client's own session list, which is where the conversation
                  itself can actually be read. */}
              <Td>
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
                  <div className="flex min-w-0 items-center gap-2">
                    <button
                      type="button"
                      aria-expanded={open === c.id}
                      aria-haspopup="menu"
                      onClick={() => setOpen(open === c.id ? null : c.id)}
                      onContextMenu={(e) => {
                        e.preventDefault()
                        rename.setError('')
                        setMenu({ id: c.id, x: e.clientX, y: e.clientY })
                      }}
                      className="state-layer flex min-w-0 flex-1 flex-col rounded-md3-xs text-left"
                    >
                      <Tip tip={c.title} block>
                        <span className="block truncate font-medium text-on-surface">
                          {open === c.id ? '▾ ' : '▸ '}
                          {chatName(c)}
                        </span>
                      </Tip>
                      <Tip tip={c.id} block>
                        <span className="block truncate font-mono text-xs text-on-surface-variant">
                          {shortID(c.id)}
                          {c.title_manual && ' · named by hand'}
                        </span>
                      </Tip>
                    </button>
                    {/* Only beside a name: an unnamed chat already says how
                        it was run in place of one. */}
                    {c.title !== '' && c.surface && <Chip>{c.surface}</Chip>}
                  </div>
                )}
                {menu?.id === c.id && (
                  <ContextMenu at={menu} label={`Title of ${c.title || c.id}`} onClose={closeMenu}>
                    <MenuRow
                      onClick={() => {
                        setMenu(null)
                        setEditing(c.id)
                      }}
                    >
                      Rename…
                    </MenuRow>
                  </ContextMenu>
                )}
              </Td>
              <Td muted nowrap>
                {c.client || '—'}
              </Td>
              <Td nowrap>
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
                  {c.models.length === 0 && <Muted as="span">—</Muted>}
                </div>
              </Td>
              <Td nowrap>
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
              </Td>
              {/* What the client resends each turn, and how often it has started
                  over. The peak is the question a compaction setting answers. */}
              <Td nowrap>
                {c.context !== undefined && c.context.model !== '' ? (
                  <div className="flex flex-col">
                    <span className="tabular-nums text-on-surface">{compact(c.context.now)}</span>
                    <span className="text-xs tabular-nums text-on-surface-variant">
                      peak {compact(c.context.peak)}
                      {c.context.compactions > 0 && ` · ${c.context.compactions}× compacted`}
                    </span>
                  </div>
                ) : (
                  <Muted as="span">—</Muted>
                )}
              </Td>
              {/* Both numbers link into the request log, on different filters:
                  the total asks "what happened in this chat", the failures ask
                  "which ones broke". They are real links — the URL carries the
                  filter, so it can be shared, bookmarked and walked back to —
                  rather than a list this table grows for itself. */}
              <Td nums nowrap>
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
              </Td>
              <Td title={c.first} nums nowrap muted>
                {sameDayStamp(c.first, 'minute', '—')}
              </Td>
              <Td nums nowrap muted>
                {between(c.first, c.last)}
              </Td>
            </Tr>
            {open === c.id && (
              <tr className="border-b border-outline-variant bg-surface-container/40">
                <td colSpan={8} className="px-3 pt-1 pb-4">
                  <ChatContextDetail
                    id={c.id}
                    colour={palette.colour(c.context?.model || c.models[0] || '')}
                   
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
          <Tr total>
            <Td muted>
              <div className="flex flex-col">
                <span className="font-medium">Unattributed</span>
                <span className="text-xs italic">no session id sent</span>
              </div>
            </Td>
            <Td muted>—</Td>
            <Td muted>—</Td>
            <Td muted nums nowrap>
              {compact(tokens(unattributed))}
            </Td>
            <Td muted>—</Td>
            <Td muted nums nowrap>
              {unattributed.requests}
              {unattributed.errors > 0 && <span> · {unattributed.errors} failed</span>}
            </Td>
            <Td muted>—</Td>
            <Td muted>—</Td>
          </Tr>
        )}
      </Table>

      <ErrorBanner error={rename.error} />

      <Muted size="xs">
        A chat is grouped by the session id its client sends, never by anything read from the
        messages. Context is what its main model is resent each turn; open a chat to see it grow
        and where its client compacted. Right-click a chat’s name to rename it. {report !== undefined && report.total > sorted.length
          ? `Showing the ${sorted.length} busiest of ${report.total}.`
          : ''}
      </Muted>
    </Stack>
  )
}
