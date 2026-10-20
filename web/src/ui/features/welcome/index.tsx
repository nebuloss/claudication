import { api, type Health } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Chip, Muted, type Tone } from '../../primitives'

/**
 * The words on the page, apart from how it is drawn.
 *
 * Nothing about setting a client up. That lives on each client's page in the
 * admin UI, where the key and the address are known; written here it would be
 * instructions with a hole where the key goes, for a reader who cannot fill it.
 */
const COPY = {
  title: 'An API for coding tools',
  what: (
    <>
      This address relays Claude and GPT models to tools such as Claude Code, Codex, opencode and
      crush, over the subscriptions its operator has connected. It speaks the Anthropic Messages
      API and the OpenAI Responses API.
    </>
  ),
  access:
    'There is nothing to sign in to here. A tool needs a key from whoever runs this gateway, and they set it up from their admin console.',
}

/**
 * How often the status is asked again. A status that is only as fresh as the
 * page load says "online" about a gateway that stopped an hour ago; /health
 * costs nothing to answer.
 */
const REFRESH_MS = 30_000

/** What the status chip says, by what /health answered. */
function status(health: Health | null, error: string): { tone: Tone; label: string } {
  if (error !== '') return { tone: 'error', label: 'Not answering' }
  if (health === null) return { tone: 'neutral', label: 'Checking…' }
  return health.status === 'ok'
    ? { tone: 'ok', label: 'Online' }
    : { tone: 'warn', label: health.status }
}

/**
 * The root of a relay that has no admin UI beside it: what the address is, and
 * whether it is up.
 *
 * Reads /health and nothing else — the one endpoint every listener already
 * answers to anyone — so the page cannot tell a passer-by more than a monitor
 * learns. No link to the admin UI: this page is only served where the admin is
 * on another address, and naming that address is what splitting the listeners
 * keeps from the public side.
 */
export default function Welcome() {
  const { data, error } = useLoader<Health>((signal) => api.gateway.health(signal), {
    refreshMs: REFRESH_MS,
  })
  const s = status(data, error)

  return (
    <Card title={COPY.title}>
      <p className="mb-4 text-sm leading-relaxed text-on-surface">{COPY.what}</p>
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Chip tone={s.tone}>{s.label}</Chip>
        {data !== null && data.version !== '' && (
          <Chip>
            <span className="font-mono">{data.version}</span>
          </Chip>
        )}
      </div>
      <Muted>{COPY.access}</Muted>
    </Card>
  )
}
