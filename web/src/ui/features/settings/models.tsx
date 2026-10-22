import { api, type FamilyReport, type FamilyRow } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { accountHref } from '../../route'
import {
  Card,
  Checkbox,
  Chip,
  ErrorBanner,
  ErrorState,
  InlineLink,
  Loading,
  Muted,
  Note,
  Segmented,
  Select,
  Stack,
  Tile,
} from '../../primitives'

type Mode = 'family' | 'every'

/**
 * The global Models page: per provider, how its models are offered — by
 * family, the default, or every model — and per family which version it
 * serves: the latest (each account its newest) or one version for the whole
 * gateway. That choice is here alone, the owner's decision (2026-10-22): a
 * client's name is bound to a family or a model and picks no version.
 *
 * Only what connected accounts list (the owner's rule): never the public
 * catalogue, never a model no switched-on account lists. A provider with no
 * account is a line saying so, its mode still settable for when one is
 * connected. A version every account has switched off stays, greyed, so it
 * can be switched back on from its account's page, which each account named
 * links to.
 *
 * Managing a model — its switch, its test — is each account's Models page
 * alone, the owner's line, and a client's Allowed models is that client's
 * policy; this page is the binding layer over them, and shows the accounts'
 * versions read-only.
 *
 * What a family is served with is per account (internal/service/models,
 * family.go): each version lists the accounts that would serve the family
 * with it, so a plan without the newest Opus is seen serving the one before.
 */
export default function ModelSettings() {
  const { data, error, loading, reload } = useLoader((signal) => api.models.families(signal))
  if (loading && data === null) return <Loading>Reading what the accounts serve…</Loading>
  if (error !== '' || data === null) {
    return <ErrorState message={error || 'Could not read the model families.'} onRetry={() => void reload()} busy={loading} />
  }
  return (
    <>
      {data.map((p) => (
        <ProviderCard key={p.provider} report={p} onChanged={() => void reload()} />
      ))}
    </>
  )
}

function ProviderCard({ report: p, onChanged }: { report: FamilyReport; onChanged: () => void }) {
  const { busy, error, run } = useAction()
  const mode: Mode = p.by_family ? 'family' : 'every'
  const setMode = (m: Mode) =>
    run('mode', async () => {
      await api.models.setByFamily(p.provider, m === 'family')
      onChanged()
    })
  const names = new Map(p.accounts.map((a) => [a.id, a.name]))

  return (
    <Card
      title={p.title}
      aside={
        <Segmented<Mode>
          label={`How ${p.title} models are offered`}
          value={mode}
          onChange={(m) => {
            if (m !== mode && busy === '') void setMode(m)
          }}
          options={[
            { id: 'family', label: 'One choice per family, served by its newest version', content: 'Families' },
            { id: 'every', label: 'Every version, each a choice of its own', content: 'Every model' },
          ]}
        />
      }
    >
      <Stack gap={4}>
        <p className="text-sm text-on-surface">
          {p.by_family ? (
            <>
              Clients are offered each family once — a model name&rsquo;s menu, a client&rsquo;s Allowed models, a
              direct client&rsquo;s list and the MCP server&rsquo;s — and the version each family serves is set
              here, for every client: the latest, each account serving the newest it has, so a plan without the
              latest still serves the family; or one version, served by the accounts that have it.
            </>
          ) : (
            <>
              Clients are offered every version, each a choice of its own. A name bound to a family before keeps
              serving it as set below.
            </>
          )}
        </p>
        <ErrorBanner error={error} />
        {!p.connected ? (
          <Note>
            No {p.title} account is connected, so there is nothing to list.{' '}
            <InlineLink href="/accounts" primary>
              Connect a {p.title} account
            </InlineLink>
            ; the mode above applies once one is.
          </Note>
        ) : (
          <>
            {p.families.length === 0 && <Muted>The connected accounts list no model of a family.</Muted>}
            {p.families.map((f) => (
              <FamilyTile key={f.follows} family={f} names={names} onChanged={onChanged}
                connected={p.accounts.filter((a) => !a.disabled).map((a) => a.id)} />
            ))}
            {(p.other ?? []).length > 0 && (
              <Muted size="xs">
                Offered as themselves, belonging to no family: {(p.other ?? []).map((id, i) => (
                  <span key={id}>
                    {i > 0 && ', '}
                    <code>{id}</code>
                  </span>
                ))}
                .
              </Muted>
            )}
            <Muted size="xs">
              A version is switched on or off, and tested, on its account&rsquo;s Models page — each account above
              links there.
            </Muted>
          </>
        )}
      </Stack>
    </Card>
  )
}

/**
 * One family: which version it serves — the latest, each account its own
 * newest, or one version for the whole gateway — and each version with the
 * accounts that would serve it.
 *
 * The version is chosen here and nowhere else, the owner's decision
 * (2026-10-22): a client's name is bound to the family, and every client
 * naming it is served the same way. Set to one version, an account without
 * it does not serve the family, which the tile says, naming who can.
 */
function FamilyTile({
  family: f,
  names,
  connected,
  onChanged,
}: {
  family: FamilyRow
  names: Map<string, string>
  /** The switched-on accounts, which could serve a version set. */
  connected: string[]
  onChanged: () => void
}) {
  const { busy, error, run } = useAction()
  const pinned = f.pinned ?? ''
  const latest = pinned === ''
  const set = (model: string) =>
    run('version', async () => {
      await api.models.setVersion(f.follows, model)
      onChanged()
    })
  // Following the latest, the menu shows what that is now, and is not a choice.
  const servedBy = (m: string) => f.versions.find((v) => v.model === m)?.served_by ?? []
  const latestHint = (() => {
    const others = f.versions.filter((v) => v.model !== f.serving && v.served_by.length > 0)
    return others.length === 0
      ? 'each account its newest'
      : `${others.map((v) => `${v.model} on ${v.served_by.map((id) => names.get(id) ?? id).join(', ')}`).join('; ')}`
  })()
  const options = latest
    ? [{ value: '', label: f.serving !== '' ? `Latest: ${f.serving}` : 'Latest', hint: latestHint }]
    : f.versions.map((v) => ({ value: v.model, label: v.title || v.model, hint: v.model, disabled: v.on.length === 0 }))
  const pinnedVersion = f.versions.find((v) => v.model === pinned)
  const lacking = pinned !== '' ? connected.filter((id) => !(pinnedVersion?.on ?? []).includes(id)) : []

  return (
    <Tile>
      <div className="flex flex-col gap-3 px-4 py-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium text-on-surface">
            {f.title} <code className="ml-1 text-xs text-on-surface-variant">{f.id}</code>
          </span>
          {f.serving === '' && <Chip tone="warn">{f.why ?? 'no account serves it'}</Chip>}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Checkbox
            checked={latest}
            label="Follow the latest"
            disabled={busy !== ''}
            title="Each account serves the newest version of the family it has, so a plan without the latest still serves it"
            onChange={(on) => void set(on ? '' : (f.serving || f.versions[0]?.model) ?? '')}
          />
          <Select
            label="Version"
            value={latest ? '' : pinned}
            options={options}
            disabled={latest || busy !== ''}
            onChange={(v) => void set(v)}
            width="fixed"
          />
        </div>
        {!latest && (
          <Note tone={lacking.length > 0 ? 'warn' : 'info'}>
            Every request for {f.title} is sent {pinned}
            {servedBy(pinned).length > 0 && (
              <>
                , by <Accounts ids={servedBy(pinned)} names={names} />
              </>
            )}
            .{' '}
            {lacking.length > 0 && (
              <>
                <Accounts ids={lacking} names={names} /> {lacking.length === 1 ? 'does' : 'do'} not have it, and{' '}
                {lacking.length === 1 ? 'serves' : 'serve'} no {f.title}.
              </>
            )}
          </Note>
        )}
        <ErrorBanner error={error} />
        <ul className="flex flex-col gap-1.5">
          {f.versions.map((v) => {
            const idle = v.listed.filter((id) => !v.served_by.includes(id))
            return (
              <li
                key={v.model}
                className={`grid grid-cols-1 items-center gap-x-4 gap-y-1 text-sm sm:grid-cols-[14rem_1fr] ${v.off ? 'opacity-60' : ''}`}
              >
                <span className="min-w-0 truncate">
                  <code className="text-on-surface" title={v.title}>
                    {v.model}
                  </code>
                </span>
                <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                  {v.off && <Chip>off on every account</Chip>}
                  {v.served_by.length > 0 && (
                    <span className="text-xs text-on-surface-variant">
                      served by <Accounts ids={v.served_by} names={names} />
                    </span>
                  )}
                  {idle.length > 0 && (
                    <Muted as="span" size="xs">
                      {v.off ? 'listed by ' : v.served_by.length > 0 ? 'also listed by ' : 'listed by '}
                      <Accounts ids={idle} names={names} />
                    </Muted>
                  )}
                </span>
              </li>
            )
          })}
        </ul>
      </div>
    </Tile>
  )
}

/**
 * Accounts by name, each a link to its own Models page: where a version is
 * switched on or off and tested, which this page only reads.
 */
function Accounts({ ids, names }: { ids: string[]; names: Map<string, string> }) {
  return (
    <>
      {ids.map((id, i) => (
        <span key={id}>
          {i > 0 && ', '}
          <InlineLink href={`${accountHref(id)}/models`}>{names.get(id) ?? id}</InlineLink>
        </span>
      ))}
    </>
  )
}
