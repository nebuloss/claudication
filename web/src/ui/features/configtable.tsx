import { type GatewayConfig, type Origin } from '../../api/client'
import { Card, Chip, InfoTip, Note, tile } from '../primitives'

/** How a value got here, as something you can scan a column for. */
function OriginChip({ origin }: { origin: Origin }) {
  if (origin === 'database') return <Chip tone="ok">set here</Chip>
  if (origin === 'env') return <Chip tone="warn">environment</Chip>
  if (origin === 'file') return <Chip tone="ok">config file</Chip>
  return <Chip>default</Chip>
}

/**
 * The settings in groups, by the part of the key before its dot: log.level is
 * Logging. Keys with no dot are the gateway's own, and a prefix not listed
 * here is shown under its own name rather than dropped.
 */
const GROUPS: Record<string, string> = {
  '': 'Gateway',
  log: 'Logging',
  limits: 'Limits',
  passthrough: 'Relay',
  usage: 'Usage history',
  shutdown: 'Shutdown',
}

function groupOf(key: string): string {
  const dot = key.indexOf('.')
  return dot < 0 ? '' : key.slice(0, dot)
}

/**
 * Every configuration value, and which source supplied it.
 *
 * The provenance is the point. The sources are not equal and the losing one
 * is silent — the file is read first and the environment overrides it — so
 * an operator can edit config.yaml, restart, and watch nothing change with no
 * error to explain why. Showing "environment" against a row is the whole
 * answer to that, and it is not visible anywhere else.
 *
 * Read-only, deliberately. claudication never writes its config file back:
 * re-serialising a YAML document destroys every comment in it, and the
 * comments are where an operator wrote down why a value is what it is. So this
 * answers "what is set, and where do I change it" rather than offering an
 * edit box that would quietly eat the file.
 *
 * Each setting is a row like any list's: its name, its value, where it came
 * from. What it does, and the variable that overrides it, are behind its ⓘ —
 * on every row they made the table a wall of grey under thirty names.
 */
export default function ConfigTable({ config }: { config: GatewayConfig }) {
  const fromEnv = config.settings.filter((s) => s.origin === 'env')
  const groups: { id: string; settings: GatewayConfig['settings'] }[] = []
  for (const s of config.settings) {
    const id = groupOf(s.key)
    const g = groups.find((x) => x.id === id)
    if (g === undefined) groups.push({ id, settings: [s] })
    else g.settings.push(s)
  }

  return (
    <Card>
      <div className="mb-5 flex flex-col gap-3">
        <Note>
          {config.path === '' ? (
            <>
              No config file is in use — every value is a default or came from the environment.
              Point the service at one with <code className="font-mono">-config</code>.
            </>
          ) : (
            <>
              Read from <code className="font-mono font-medium">{config.path}</code>, and never
              written back: your comments in it are safe. Edit the file and restart to change
              anything here.
            </>
          )}
        </Note>
        {fromEnv.length > 0 && (
          <Note tone="warn">
            {fromEnv.length === 1
              ? 'One setting comes from the environment'
              : `${fromEnv.length} settings come from the environment`}
            , which overrides the file silently — editing the config file will not change{' '}
            {fromEnv.map((s, i) => (
              <span key={s.key}>
                {i > 0 && ', '}
                <code className="font-mono font-medium">{s.key}</code>
              </span>
            ))}
            .
          </Note>
        )}
      </div>

      <div className="flex flex-col gap-6">
        {groups.map((g) => (
          <section key={g.id}>
            <h3 className="mt-0 mb-2 text-sm font-medium text-on-surface">
              {GROUPS[g.id] ?? g.id}
            </h3>
            <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
              {g.settings.map((s) => (
                <li
                  key={s.key}
                  // A grid, not a wrapping row: every row's value starts at the
                  // same place, so the column can be read down.
                  className={`${tile} grid grid-cols-[minmax(0,1fr)_7rem] items-center gap-x-4 gap-y-1 px-4 py-2.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_7rem]`}
                >
                  <span className="flex min-w-0 items-center gap-1">
                    <code className="font-mono text-sm font-medium text-on-surface">{s.key}</code>
                    <InfoTip label={`About ${s.key}`}>
                      <p>{s.doc}</p>
                      {s.env !== undefined && s.env !== '' && (
                        <p>
                          Overridden by <code className="font-mono">{s.env}</code> in the
                          environment.
                        </p>
                      )}
                    </InfoTip>
                  </span>
                  <span className="col-span-2 row-start-2 min-w-0 text-sm sm:col-span-1 sm:row-start-auto">
                    {s.value === '' ? (
                      <span className="text-on-surface-variant">unset</span>
                    ) : (
                      <code className="font-mono break-all text-on-surface">{s.value}</code>
                    )}
                  </span>
                  <span className="justify-self-end">
                    <OriginChip origin={s.origin} />
                  </span>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </Card>
  )
}
