import ChatTitles from './chat-titles'
import ConfigTable from './configtable'
import ImageFit from './image-fit'
import ModelsTab from './model-roles'
import PublicDocs from './public-docs'
import Security from './security'
import Surfaces from './surfaces'
import { api, type GatewayConfig, type Session } from '../../api/client'
import { useLoader } from '../hooks'
import { Card, Chip, Empty, ListRow, Spinner, type ChipTone } from '../primitives'
import { GATEWAY_SETTINGS, pathOf } from '../route'

/**
 * Settings: a page per section (/settings/<section>) — the model aliases
 * every client config asks for, which APIs are served, the switches, the
 * public docs page, the admin account, and the configuration as loaded.
 *
 * The configuration is loaded here rather than inside the table, because the
 * same request carries the API switches and the settings rows. One fetch, one
 * error state, and a switch that reloads both — so the switch card and the
 * provenance column can never disagree about what is in effect.
 *
 * There used to be an "Instance" card above the configuration table with the
 * listen address, the admin split and the public URL in it. Every one of those
 * is a configuration value, so once the table started reporting all of them —
 * with where each came from, which the card could not say — the card was four
 * rows of the same facts and a note that had moved too.
 *
 * The shell commands that used to sit at the bottom have gone to Setup, which
 * is where someone looks when they are trying to make something work rather
 * than trying to change how it is configured.
 */
export default function Settings({
  section,
  session,
  onSessionChanged,
  config,
  onConfigChanged,
}: {
  /** An id from GATEWAY_SETTINGS. */
  section: string
  session: Session
  onSessionChanged: () => void
  /**
   * Loaded by the shell rather than here.
   *
   * The same argument this screen already made for loading the switches and
   * the settings rows together, one level up: the header carries a link that
   * one of these switches turns on, and with two copies of the configuration
   * the card knew it had changed and the header did not — so the link only
   * appeared after a refresh.
   */
  config: { data: GatewayConfig | null; error: string; loading: boolean }
  onConfigChanged: () => void
}) {
  const { data, error, loading } = config
  const reload = onConfigChanged

  const page = (() => {
    if (section === '') {
      return <SettingsOverview session={session} config={config} />
    }
    if (section === 'aliases') return <ModelsTab />
    if (section === 'advanced') {
      return <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />
    }
    if (loading && data === null) {
      return (
        <Card>
          <p className="flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        </Card>
      )
    }
    if (error !== '' || data === null) {
      return (
        <Card>
          <Empty>Could not read the configuration.</Empty>
        </Card>
      )
    }
    switch (section) {
      case 'apis':
        return <Surfaces surfaces={data.surfaces} onChanged={() => void reload()} />
      case 'titles':
        return (
          <ChatTitles
            enabled={data.chat_titles}
            capture={data.chat_titles_capture}
            onChanged={() => void reload()}
          />
        )
      case 'images':
        return <ImageFit enabled={data.fit_images} onChanged={() => void reload()} />
      case 'documentation':
        return (
          <PublicDocs
            enabled={data.docs_enabled}
            url={data.docs_url}
            onChanged={() => void reload()}
          />
        )
      default:
        return <ConfigTable config={data} />
    }
  })()

  return <div className="flex flex-col gap-5">{page}</div>
}

/**
 * Settings at a glance: each section in a row with the state it is in, so
 * whether titles cost anything, which APIs answer and what claude-large is
 * right now can be read without opening seven pages. A row opens its section.
 */
function SettingsOverview({
  session,
  config,
}: {
  session: Session
  config: { data: GatewayConfig | null; error: string; loading: boolean }
}) {
  const roles = useLoader(() => api.modelRoles(''), undefined, [], 0)
  const c = config.data
  const on = (v: boolean | undefined): [string, ChipTone] =>
    v === undefined ? ['…', 'neutral'] : v ? ['on', 'ok'] : ['off', 'neutral']

  const large = roles.data?.roles.find((r) => r.role === 'claude-large')
  const small = roles.data?.roles.find((r) => r.role === 'claude-small')
  const problems = roles.data?.roles.filter((r) => r.problem !== undefined && r.problem !== '').length ?? 0
  const apisOn = c?.surfaces.filter((s) => s.enabled) ?? []
  const fromEnv = c?.settings.filter((s) => s.origin === 'env').length ?? 0

  const summary: Record<string, { text: string; chip?: [string, ChipTone] }> = {
    aliases: {
      text:
        large !== undefined
          ? `claude-large is ${large.serving || 'unserved'} · claude-small is ${small?.serving || 'unserved'}`
          : 'What claude-large, claude-small and the family names serve',
      chip: problems > 0 ? [`${problems} cannot serve`, 'error'] : undefined,
    },
    apis: {
      text: c === null ? '…' : apisOn.length === 0 ? 'Every API is off' : apisOn.map((s) => s.title).join(' · '),
      chip: c === null ? undefined : [`${apisOn.length} of ${c.surfaces.length} on`, apisOn.length === 0 ? 'error' : 'ok'],
    },
    titles: {
      text:
        c === null
          ? '…'
          : c.chat_titles
            ? 'Names conversations itself, on your subscription'
            : c.chat_titles_capture
              ? 'Reads the names clients generate'
              : 'Conversations keep their session ids',
      chip: on(c?.chat_titles || c?.chat_titles_capture),
    },
    images: {
      text: 'Shrinks images a many-image request would be refused for',
      chip: on(c?.fit_images),
    },
    documentation: {
      text: c?.docs_enabled ? c.docs_url || 'Served at the relay’s root' : 'The public page for configuring a client',
      chip: on(c?.docs_enabled),
    },
    advanced: {
      text:
        session.password_updated_at !== undefined
          ? `Admin password set ${new Date(session.password_updated_at).toLocaleDateString()}`
          : 'The admin password',
    },
    configuration: {
      text: c === null ? '…' : c.path === '' ? 'No config file: defaults and the environment' : c.path,
      chip: fromEnv > 0 ? [`${fromEnv} from the environment`, 'warn'] : undefined,
    },
  }

  return (
    <Card>
      <ul className="m-0 flex list-none flex-col gap-2 p-0">
        {GATEWAY_SETTINGS.map((s) => {
          const sum = summary[s.id]
          return (
            <li key={s.id}>
              <ListRow
                href={pathOf('settings', s.id)}
                title={s.label}
                subtitle={sum?.text}
                chips={
                  sum?.chip !== undefined ? <Chip tone={sum.chip[1]}>{sum.chip[0]}</Chip> : undefined
                }
              />
            </li>
          )
        })}
      </ul>
    </Card>
  )
}
