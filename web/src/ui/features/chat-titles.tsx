import { api } from '../../api/client'
import { useAction, useOptimistic } from '../hooks'
import { Card, ErrorBanner, Note, SettingSwitch, Stack } from '../primitives'

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
  const { busy, error, run } = useAction<'enabled' | 'capture'>()
  const [shownEnabled, applyEnabled] = useOptimistic(enabled)
  const [shownCapture, applyCapture] = useOptimistic(capture)

  const write = (which: 'enabled' | 'capture', next: boolean) => () =>
    run(which, async () => {
      await api.setChatTitles({ [which]: next })
      onChanged()
    })

  return (
    <Card
      info={
        <p>
          Conversations are grouped by the session id their client sends, which is always just an
          identifier. These decide whether they also get a readable name.
        </p>
      }
    >
      <ErrorBanner error={error} className="mb-4" />

      <Stack gap={3}>
        <SettingSwitch
          checked={shownCapture}
          disabled={busy !== ''}
          onChange={(v) => void applyCapture(v, write('capture', v))}
          label="Read names clients generate"
          info={
            <p>
              opencode and crush name their own conversations by asking a model, and that request
              comes through here carrying the same session id. Its answer is the name they
              display, so this reads it rather than guessing one. Costs nothing — the request was
              theirs and the answer was going to them anyway.
            </p>
          }
        />

        <SettingSwitch
          checked={shownEnabled}
          disabled={busy !== ''}
          onChange={(v) => void applyEnabled(v, write('enabled', v))}
          label="Ask for a name when the client does not"
          info={
            <>
              <p>
                Claude Code never asks a model to name its conversations, so there is nothing to
                read. With this on the gateway asks for one itself, once per conversation.{' '}
                <strong className="font-medium text-on-surface">This spends your subscription</strong>{' '}
                rather than relaying someone else&rsquo;s request — one short call, made on that
                chat&rsquo;s own model and account so it reads the prompt cache that is already
                warm instead of re-sending the conversation.
              </p>
              <p>
                Billed to an API key the gateway issues itself, called{' '}
                <code>gateway (internal)</code>, so the cost shows up in Usage like anything else
                alongside the chat it named. That key is the gateway&rsquo;s, not yours to edit or
                withdraw — this switch is the control, and turning it off is what stops it.
              </p>
            </>
          }
        >
          <Note tone="warn">Spends your subscription: one short call per conversation.</Note>
        </SettingSwitch>
      </Stack>
    </Card>
  )
}
