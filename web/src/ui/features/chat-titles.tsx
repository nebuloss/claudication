import { useState } from 'react'
import { api } from '../../api/client'
import { Banner, Card, CardTitle, Switch } from '../primitives'

/**
 * The switch for naming conversations.
 *
 * Its own card rather than a row among the API surfaces, because it is a
 * different kind of decision. Those switches say what the gateway will answer;
 * this one says whether it may spend the operator's subscription on its own
 * behalf — the only thing in the gateway that originates upstream traffic
 * rather than relaying someone else's.
 *
 * Off until switched on, and the copy says plainly what turning it on costs.
 * A gateway whose whole job is managing a metered resource should not quietly
 * spend it, and an operator who finds out afterwards is right to be annoyed.
 */
export default function ChatTitles({
  enabled,
  onChanged,
}: {
  enabled: boolean
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function toggle(next: boolean) {
    setBusy(true)
    setError('')
    try {
      await api.setChatTitles(next)
      onChanged()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not change the setting.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardTitle>Chat names</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        Claude Code never asks a model to name its own conversations, so unlike opencode and crush
        there is no name on the wire to read. With this on, the gateway asks for one itself — once
        per conversation, the first time it sees one.
      </p>

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
        <Switch
          checked={enabled}
          disabled={busy}
          onChange={(v) => void toggle(v)}
          label="Name conversations"
        />
        <div className="mt-2 sm:ml-16">
          <p className="mt-0 mb-2 text-xs text-on-surface-variant">
            This is the one thing the gateway does that spends your subscription rather than
            relaying someone else&rsquo;s request. It is one short request per conversation, made on
            that chat&rsquo;s own model and account so it reads the prompt cache that is already
            warm instead of re-sending the conversation — measured at 11,406 cached tokens read and
            none written.
          </p>
          <p className="m-0 text-xs text-on-surface-variant">
            It is billed to an API key the gateway issues itself, called{' '}
            <code className="font-mono">gateway (internal)</code>, so what it costs shows up in
            Usage like anything else. Give that key a token budget to cap it, or delete it to stop
            it.
          </p>
        </div>
      </div>
    </Card>
  )
}
