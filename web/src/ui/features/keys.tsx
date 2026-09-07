import { useState } from 'react'
import { api, messageOf, type ApiKey } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  CopyField,
  Empty,
  Field,
  FilledButton,
  OutlinedButton,
  Spinner,
  Table,
  TextButton,
  TonalButton,
  ago,
  compact,
} from '../primitives'

/**
 * Client API keys — the credential you hand to Claude Code.
 *
 * The store keeps sha256(key) and a lookup prefix, never the key, so the
 * plaintext is shown once at creation and is genuinely unrecoverable after
 * that. The UI has to be honest about it rather than implying it can be looked
 * up later.
 */
export default function Keys({ onExpired }: { onExpired: () => void }) {
  const { data, error, loading, reload } = useLoader(() => api.listKeys(), onExpired)
  const [minted, setMinted] = useState<{ key: ApiKey; plaintext: string } | null>(null)
  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState('')

  const act = async (id: string, fn: () => Promise<unknown>) => {
    setBusy(id)
    setActionError('')
    try {
      await fn()
      await reload()
    } catch (err) {
      setActionError(messageOf(err))
    }
    setBusy('')
  }

  return (
    <div className="flex flex-col gap-5">
      {minted !== null && (
        <Card>
          <CardTitle
            aside={<TextButton onClick={() => setMinted(null)}>Done</TextButton>}
          >
            {minted.key.name}
          </CardTitle>
          <Banner tone="warn" className="mb-4">
            This is the only time the key is shown. Nothing can retrieve it afterwards — only the
            hash is stored. Copy it now.
          </Banner>
          <CopyField label="API key" value={minted.plaintext} />
        </Card>
      )}

      <NewKey onCreated={(m) => { setMinted(m); void reload() }} />

      <Card>
        <CardTitle
          aside={
            data !== null && data.keys.length > 0 ? (
              <span className="text-sm text-on-surface-variant">
                traffic over {data.windowDays}d
              </span>
            ) : null
          }
        >
          Keys
        </CardTitle>

        {error !== '' && (
          <Banner tone="error" className="mb-4">
            {error}
          </Banner>
        )}
        {actionError !== '' && (
          <Banner tone="error" className="mb-4">
            {actionError}
          </Banner>
        )}

        {loading ? (
          <p className="flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : data === null || data.keys.length === 0 ? (
          <Empty>No API keys yet. Create one above and give it to a client.</Empty>
        ) : (
          <Table head={['Name', 'Key', 'Requests', 'Tokens', 'Last used', 'Limit', '']}>
            {data.keys.map((k) => (
              <tr key={k.id} className="border-b border-outline-variant last:border-0">
                <td className="px-2 py-3">
                  <div className="text-on-surface">{k.name}</div>
                  <div className="text-xs text-on-surface-variant">
                    created {ago(k.created_at)}
                  </div>
                </td>
                <td className="px-2 py-3 font-mono text-xs text-on-surface-variant">{k.display}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.requests)}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.tokens)}</td>
                <td className="px-2 py-3 text-on-surface-variant">{ago(k.last_used_at)}</td>
                <td className="px-2 py-3 text-on-surface-variant">
                  {k.rpm_limit > 0 ? `${k.rpm_limit}/min` : 'default'}
                </td>
                <td className="px-2 py-3">
                  <div className="flex justify-end">
                    <TextButton
                      tone="error"
                      disabled={busy === k.id}
                      onClick={() => void act(k.id, () => api.deleteKey(k.id))}
                    >
                      Delete
                    </TextButton>
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        )}

        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Deleting stops a key working immediately and cannot be undone. The Usage tab keeps
          showing what it spent, under the name it was recorded with.
        </p>
      </Card>
    </div>
  )
}

function NewKey({ onCreated }: { onCreated: (m: { key: ApiKey; plaintext: string }) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [rpm, setRpm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!open) {
    return (
      <div>
        <TonalButton onClick={() => setOpen(true)}>New API key</TonalButton>
      </div>
    )
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (name.trim() === '') {
      setError('Give the key a name — it is how you will recognise it later.')
      return
    }
    const limit = rpm.trim() === '' ? 0 : Number(rpm)
    if (!Number.isFinite(limit) || limit < 0) {
      setError('The rate limit must be a number of requests per minute, or blank.')
      return
    }

    setBusy(true)
    setError('')
    try {
      onCreated(await api.createKey(name.trim(), Math.floor(limit)))
      setOpen(false)
      setName('')
      setRpm('')
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  return (
    <Card>
      <CardTitle>New API key</CardTitle>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Name" value={name} onChange={setName} autoFocus />
        <Field
          label="Requests per minute (blank = server default)"
          value={rpm}
          onChange={setRpm}
          type="number"
        />
        {error !== '' && <Banner tone="error">{error}</Banner>}
        <div className="flex items-center gap-2">
          <FilledButton disabled={busy}>
            {busy && <Spinner />}
            {busy ? 'Creating…' : 'Create key'}
          </FilledButton>
          <OutlinedButton onClick={() => setOpen(false)} disabled={busy}>
            Cancel
          </OutlinedButton>
        </div>
      </form>
    </Card>
  )
}
