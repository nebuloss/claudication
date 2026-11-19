import { useState } from 'react'
import { api, type CaptureStatus } from '../../../api/client'
import { bytesLabel, compact, relative } from '../../format'
import { useLoader } from '../../hooks'
import { Button, Card, ErrorState, Loading, Muted, Stat, StatGrid, useConfirm } from '../../primitives'

/**
 * Recorded content, gateway-wide: who is being recorded, what memory holds,
 * and — when recordings are kept on disk — what the file takes against its
 * budget, how far back it reaches, and what could not be written. Each client
 * switches its own recording on (its overview); this is where an operator
 * sees what that costs, takes all of it away, or downloads it.
 */
export default function Recordings({ keptDays }: { keptDays: number }) {
  const confirm = useConfirm()
  const [busy, setBusy] = useState(false)
  const { data, error, loading, reload } = useLoader<{ status: CaptureStatus }>(
    (signal) => api.captures.list('', signal),
    { on: ['captures'] },
  )

  const info = (
    <>
      <li>Each client records its own, switched on from its overview, for an hour at a time.</li>
      <li>
        {keptDays > 0
          ? `Kept on disk for ${String(keptDays)} ${keptDays === 1 ? 'day' : 'days'}, sealed, and never in a backup (capture.retention-days, capture.disk-budget).`
          : 'Held in memory only, and lost when the gateway restarts (capture.retention-days keeps them on disk).'}
      </li>
    </>
  )

  if (loading && data === null) {
    return (
      <Card title="Recordings" info={info}>
        <Loading />
      </Card>
    )
  }
  if (data === null) {
    return (
      <Card title="Recordings" info={info}>
        <ErrorState message={error || 'Could not read the recordings.'} onRetry={() => void reload()} busy={loading} />
      </Card>
    )
  }

  const st = data.status
  const recording = Object.keys(st.recording).length
  const stored = st.stored
  const empty = st.held === 0 && (stored?.recordings ?? 0) === 0

  const clear = async () => {
    const ok = await confirm({
      title: 'Clear every recording?',
      body: 'What memory holds and what is kept on disk, of every client. Recording that is on stays on.',
      confirmLabel: 'Clear recordings',
      tone: 'danger',
    })
    if (!ok) return
    setBusy(true)
    try {
      await api.captures.clear('')
    } finally {
      setBusy(false)
      void reload()
    }
  }

  return (
    <Card
      title="Recordings"
      info={info}
      aside={
        <div className="flex flex-wrap items-center gap-2">
          {!empty && (
            // A plain link: the browser downloads it.
            <a href={api.captures.exportHref('')} download className="text-sm text-primary underline underline-offset-2">
              Download all as HAR
            </a>
          )}
          <Button variant="text" tone="error" size="sm" onClick={() => void clear()} busy={busy} disabled={empty}>
            Clear all
          </Button>
        </div>
      }
    >
      <StatGrid>
        <Stat
          label="Recording now"
          value={recording}
          hint={recording === 1 ? 'client' : 'clients'}
          tone={recording > 0 ? 'ok' : undefined}
        />
        <Stat
          label="In memory"
          value={compact(st.held)}
          hint={`${bytesLabel(st.used)} of ${bytesLabel(st.budget)}`}
        />
        {stored !== undefined && (
          <>
            <Stat
              label="On disk"
              value={compact(stored.recordings)}
              hint={
                stored.budget > 0
                  ? `${bytesLabel(stored.bytes)} of ${bytesLabel(stored.budget)}`
                  : bytesLabel(stored.bytes)
              }
              tone={stored.budget > 0 && stored.bytes > stored.budget * 0.9 ? 'warn' : undefined}
            />
            <Stat
              label="Oldest kept"
              value={stored.oldest !== undefined ? relative(stored.oldest) : '—'}
              hint={
                stored.dropped > 0
                  ? `${compact(stored.dropped)} not written since the gateway started`
                  : `${compact(stored.written)} written since the gateway started`
              }
              tone={stored.dropped > 0 ? 'warn' : undefined}
            />
          </>
        )}
      </StatGrid>
      {stored === undefined && st.held > 0 && (
        <Muted size="xs">These go when the gateway restarts or is updated.</Muted>
      )}
    </Card>
  )
}
