import { useMemo, useState } from 'react'
import { api, type ChatContext, type ChatDetail, type RequestRow } from '../../api/client'
import { HoverValues, LineChart, tipPosition, type LineSeries, Line } from '../charts'
import { useLoader } from '../hooks'
import { ErrorState, Spinner, compact } from '../primitives'

/** The request the gateway makes on its own behalf; see store.InternalPath. */
const INTERNAL_PATH = '/internal/title'

/** Follows the chat while it is open, at the rate the list beside it does. */
const DETAIL_REFRESH_MS = 20_000

type Point = { at: string; prompt: number }

/** A request's context, by the rule the server counts with: everything in the prompt. */
function prompt(r: RequestRow): number {
  return r.input_tokens + r.cache_tokens
}

function clock(at: string): string {
  const d = new Date(at)
  return Number.isNaN(d.getTime())
    ? at
    : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

/**
 * How one chat's context grew, turn by turn, and where it compacted.
 *
 * The curve is the reason this exists. A client decides when to compact from a
 * context window in its own configuration, and the cost of deciding late is
 * paid on every turn — the whole context is read again each time — so the
 * thing worth seeing is how long a chat sat near the top before it fell.
 *
 * Only the counted model's requests are drawn, the same ones the server counts:
 * a titling call on a small model in the middle of the line would read as the
 * context collapsing and recovering.
 */
export default function ChatContextDetail({
  id,
  colour,
  onExpired,
}: {
  id: string
  colour: string
  onExpired: () => void
}) {
  const { data, error, loading, reload } = useLoader<ChatDetail>(
    () => api.chat(id),
    onExpired,
    [id],
    DETAIL_REFRESH_MS,
  )
  const [hovered, setHovered] = useState<number | null>(null)

  const { points, marks } = useMemo(() => curve(data), [data])

  if (loading && data === null) {
    return (
      <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  if (data === null || points.length === 0) {
    return (
      <p className="m-0 text-sm text-on-surface-variant">
        No successful request in this chat yet, so there is no context to draw.
      </p>
    )
  }

  const ctx = data.context
  const series: LineSeries<Point>[] = [
    { key: 'context', label: 'Context', paint: new Line(colour), at: (p) => p.prompt },
  ]
  const shown = hovered !== null ? points[hovered] : undefined

  return (
    <div className="flex flex-col gap-2">
      <Summary ctx={ctx} requests={data.requests.length} />
      <div className="relative">
        {shown !== undefined && (
          <HoverValues
            datum={shown}
            title={new Date(shown.at).toLocaleString()}
            series={series}
            format={compact}
            position={tipPosition(hovered ?? 0, points.length)}
          />
        )}
        <LineChart
          data={points}
          series={series}
          label={(p) => clock(p.at)}
          format={compact}
          log={false}
          height={200}
          hovered={hovered}
          onHover={setHovered}
          marks={marks}
        />
      </div>
    </div>
  )
}

/** The counted model's requests as points, and the ones it compacted at. */
function curve(data: ChatDetail | null): { points: Point[]; marks: number[] } {
  if (data === null || data.context.model === '') return { points: [], marks: [] }
  const rows = data.requests.filter(
    (r) => r.status === 200 && r.path !== INTERNAL_PATH && r.model === data.context.model,
  )
  const points = rows.map((r) => ({ at: r.at, prompt: prompt(r) }))

  // The server says when, to the nanosecond; a request row says so to the
  // second. The first counted request in that second is the one that fell.
  const seconds = (at: string) => Math.floor(new Date(at).getTime() / 1000)
  const marks: number[] = []
  for (const at of data.context.compacted_at ?? []) {
    const s = seconds(at)
    const i = points.findIndex((p, j) => seconds(p.at) === s && !marks.includes(j))
    if (i >= 0) marks.push(i)
  }
  return { points, marks }
}

function Summary({ ctx, requests }: { ctx: ChatContext; requests: number }) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-xs text-on-surface-variant">
      <span>
        <span className="font-medium text-on-surface tabular-nums">{compact(ctx.now)}</span> now
      </span>
      <span>
        <span className="tabular-nums">{compact(ctx.peak)}</span> peak
      </span>
      <span>
        {ctx.compactions === 0
          ? 'never compacted'
          : `compacted ${ctx.compactions}×`}
        {ctx.compactions > 0 && (
          <span className="ml-1 inline-block h-0 w-4 border-t-[1.5px] border-dashed border-warning align-middle" />
        )}
      </span>
      <span>
        on {ctx.model.replace(/^claude-/, '')} · last {requests} requests
      </span>
    </div>
  )
}
