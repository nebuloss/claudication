import { type GatewayConfig, type Origin } from '../../api/client'
import { Banner, Card, CardTitle, Chip, Table } from '../primitives'

/** How a value got here, as something you can scan a column for. */
function OriginChip({ origin }: { origin: Origin }) {
  if (origin === 'database') return <Chip tone="ok">set here</Chip>
  if (origin === 'env') return <Chip tone="warn">environment</Chip>
  if (origin === 'file') return <Chip tone="ok">config file</Chip>
  return <Chip>default</Chip>
}

/**
 * Every configuration value, and which source supplied it.
 *
 * The provenance column is the point. The sources are not equal and the losing
 * one is silent — the file is read first and the environment overrides it — so
 * an operator can edit config.yaml, restart, and watch nothing change with no
 * error to explain why. Showing "environment" against a row is the whole
 * answer to that, and it is not visible anywhere else.
 *
 * Read-only, deliberately. claudication never writes its config file back:
 * re-serialising a YAML document destroys every comment in it, and the
 * comments are where an operator wrote down why a value is what it is. So this
 * table answers "what is set, and where do I change it" rather than offering
 * an edit box that would quietly eat the file. The one exception is the API
 * switches, which are runtime state and live in the database — they have their
 * own card above, so nothing here looks editable when it is not.
 */
export default function ConfigTable({ config }: { config: GatewayConfig }) {
  const fromEnv = config.settings.filter((s) => s.origin === 'env')

  return (
    <Card>
      <CardTitle>Configuration</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        {config.path === '' ? (
          <>
            No config file is in use — every value below is a default or came from the environment.
            Point the service at one with <code>-config</code>.
          </>
        ) : (
          <>
            Read from <code className="text-on-surface">{config.path}</code>, and never written
            back: your comments in it are safe. Edit the file and restart to change anything here.
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
        {config.settings.map((s) => (
          <tr key={s.key} className="border-b border-outline-variant last:border-0">
            <td className="px-2 py-2 align-top">
              <div className="font-mono text-xs whitespace-nowrap text-on-surface">{s.key}</div>
              <div className="mt-0.5 max-w-md text-xs text-on-surface-variant">{s.doc}</div>
              {s.env !== undefined && s.env !== '' && (
                <div className="mt-0.5 font-mono text-[11px] text-on-surface-variant">{s.env}</div>
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
    </Card>
  )
}
