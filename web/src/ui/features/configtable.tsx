import { api, type GatewayConfig, type Setting } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, Chip, Empty, Spinner, Table } from '../primitives'

/** How a value got here, as something you can scan a column for. */
function OriginChip({ origin }: { origin: Setting['origin'] }) {
  if (origin === 'env') return <Chip tone="warn">environment</Chip>
  if (origin === 'file') return <Chip tone="ok">config file</Chip>
  return <Chip>default</Chip>
}

/**
 * Every configuration value, and which of the three sources supplied it.
 *
 * The provenance column is the point. The three sources are not equal and the
 * losing one is silent — the file is read first and the environment overrides
 * it — so an operator can edit config.yaml, restart, and watch nothing change
 * with no error to explain why. Showing "environment" against a row is the
 * whole answer to that, and it is not visible anywhere else.
 *
 * Read-only, deliberately. claudication never writes its config file back:
 * re-serialising a YAML document destroys every comment in it, and the
 * comments are where an operator wrote down why a value is what it is. So this
 * screen answers "what is set, and where do I change it" rather than offering
 * an edit box that would quietly eat the file.
 */
export default function ConfigTable() {
  const { data, error, loading } = useLoader<GatewayConfig>(() => api.config())

  const fromEnv = (data?.settings ?? []).filter((s) => s.origin === 'env')

  return (
    <Card>
      <CardTitle>Configuration</CardTitle>

      {loading && data === null ? (
        <p className="flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : error !== '' || data === null ? (
        <Empty>Could not read the configuration.</Empty>
      ) : (
        <>
          <p className="mt-0 mb-4 text-sm text-on-surface-variant">
            {data.path === '' ? (
              <>
                No config file is in use — every value below is a default or came from the
                environment. Point the service at one with <code>-config</code>.
              </>
            ) : (
              <>
                Read from <code className="text-on-surface">{data.path}</code>, and never written
                back: your comments in it are safe. Edit the file and restart to change anything
                here.
              </>
            )}
          </p>

          {fromEnv.length > 0 && (
            <Banner tone="warn" className="mb-4">
              {fromEnv.length === 1
                ? 'One setting is coming from the environment'
                : `${fromEnv.length} settings are coming from the environment`}
              , which overrides the file silently — editing the config file will not change{' '}
              {fromEnv.map((s) => s.key).join(', ')}.
            </Banner>
          )}

          <Table head={['Setting', 'Value', 'From']}>
            {data.settings.map((s) => (
              <tr key={s.key} className="border-b border-outline-variant last:border-0">
                <td className="px-2 py-2 align-top">
                  <div className="font-mono text-xs whitespace-nowrap text-on-surface">{s.key}</div>
                  <div className="mt-0.5 max-w-md text-xs text-on-surface-variant">{s.doc}</div>
                  {s.env !== undefined && s.env !== '' && (
                    <div className="mt-0.5 font-mono text-[11px] text-on-surface-variant">
                      {s.env}
                    </div>
                  )}
                </td>
                <td className="px-2 py-2 align-top">
                  {s.value === '' ? (
                    <span className="text-xs text-on-surface-variant">unset</span>
                  ) : (
                    <span className="font-mono text-xs break-all text-on-surface">{s.value}</span>
                  )}
                </td>
                <td className="px-2 py-2 align-top">
                  <OriginChip origin={s.origin} />
                </td>
              </tr>
            ))}
          </Table>
        </>
      )}
    </Card>
  )
}
