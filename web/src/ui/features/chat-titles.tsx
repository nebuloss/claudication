import { useEffect, useState } from 'react'
import { api } from '../../api/client'
import { Banner, Card, CardTitle, Switch } from '../primitives'

/**
 * The switches for naming conversations.
 *
 * Two of them, because they are two different bargains and collapsing them
 * into one would hide the only part worth deciding about.
 *
 * Reading a name costs nothing: opencode and crush already ask a model to name
 * their own conversations, and that answer passes through this gateway on its
 * way back to them. Asking for a name spends the operator's subscription — the
 * only thing the gateway does that originates upstream traffic rather than
 * relaying someone else's — so it is off until switched on and says plainly
 * what it costs.
 */
export default function ChatTitles({
  enabled,
  capture,
  onChanged,
}: {
  enabled: boolean
  capture: boolean
  onChanged: () => void
}) {
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  // Shown state, which leads the server rather than following it.
  //
  // These switches were driven straight off the reloaded config, so flipping
  // one did nothing until the write and the refetch had both come back — a
  // visible pause in which the control sits where you did not put it, which
  // reads as the click having missed. It moves now and is corrected if the
  // write fails.
  const [shown, setShown] = useState({ enabled, capture })

  // The server is still the authority: when the reload lands, or something
  // else changes the setting, that is what the switch shows.
  useEffect(() => {
    setShown({ enabled, capture })
  }, [enabled, capture])

  async function toggle(which: 'enabled' | 'capture', next: boolean) {
    const before = shown
    setShown({ ...shown, [which]: next })
    setBusy(which)
    setError('')
    try {
      await api.setChatTitles({ [which]: next })
      onChanged()
    } catch (e) {
      setShown(before)
      setError(e instanceof Error ? e.message : 'Could not change the setting.')
    } finally {
      setBusy('')
    }
  }

  return (
    <Card>
      <CardTitle>Chat names</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        Conversations are grouped by the session id their client sends, which is always just an
        identifier. These decide whether they also get a readable name.
      </p>

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      <div className="flex flex-col gap-3">
        <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
          <Switch
            checked={shown.capture}
            disabled={busy !== ''}
            onChange={(v) => void toggle('capture', v)}
            label="Read names clients generate"
          />
          <p className="mt-2 mb-0 text-xs text-on-surface-variant sm:ml-16">
            opencode and crush name their own conversations by asking a model, and that request
            comes through here carrying the same session id. Its answer is the name they display,
            so this reads it rather than guessing one. Costs nothing — the request was theirs and
            the answer was going to them anyway.
          </p>
        </div>

        <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
          <Switch
            checked={shown.enabled}
            disabled={busy !== ''}
            onChange={(v) => void toggle('enabled', v)}
            label="Ask for a name when the client does not"
          />
          <div className="mt-2 sm:ml-16">
            <p className="mt-0 mb-2 text-xs text-on-surface-variant">
              Claude Code never asks a model to name its conversations, so there is nothing to
              read. With this on the gateway asks for one itself, once per conversation.{' '}
              <strong className="font-medium text-on-surface">
                This spends your subscription
              </strong>{' '}
              rather than relaying someone else&rsquo;s request — one short call, made on that
              chat&rsquo;s own model and account so it reads the prompt cache that is already warm
              instead of re-sending the conversation.
            </p>
            <p className="m-0 text-xs text-on-surface-variant">
              Billed to an API key the gateway issues itself, called{' '}
              <code className="font-mono">gateway (internal)</code>, so the cost shows up in Usage
              like anything else. Give that key a token budget to cap it. This switch is what stops
              it — deleting the key never did, because the next chat needing a name just gets
              another one issued.
            </p>
          </div>
        </div>
      </div>
    </Card>
  )
}
