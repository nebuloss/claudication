import { api, type FamilyReport, type FamilyRow } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { accountHref } from '../../route'
import {
  Card,
  Chip,
  ErrorBanner,
  ErrorState,
  InlineLink,
  Loading,
  Muted,
  Note,
  Select,
  SettingSwitch,
  Stack,
  Tile,
} from '../../primitives'

/**
 * The global Models page: per provider, a switch for how its models are
 * offered — by family, the default, or every model — and per family one menu
 * of which version it serves: the latest (each account its newest) or one version for the whole
 * gateway. That choice is here alone, the owner's decision (2026-10-22): a
 * client's name is bound to a family or a model and picks no version.
 *
 * Only what connected accounts list (the owner's rule): never the public
 * catalogue, never a model no switched-on account lists. A provider with no
 * account is a line saying so, its switch still settable for when one is
 * connected. The versions are in each family's menu, not listed under it:
 * which account serves which, and switching one, is each account's page.
 *
 * Managing a model — its switch, its test — is each account's Models page
 * alone, the owner's line, and a client's Allowed models is that client's
 * policy; this page is the binding layer over them.
 *
 * What Latest serves is per account (internal/service/models, family.go): a
 * plan without the newest Opus serves the one before.
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
  const setByFamily = (on: boolean) =>
    run('mode', async () => {
      await api.models.setByFamily(p.provider, on)
      onChanged()
    })
  const names = new Map(p.accounts.map((a) => [a.id, a.name]))

  return (
    <Card title={p.title}>
      <Stack gap={4}>
        <SettingSwitch
          label="Use model families"
          checked={p.by_family}
          disabled={busy !== ''}
          onChange={(on) => void setByFamily(on)}
          info={
            p.by_family
              ? 'Clients are offered each family once, served by the version set below.'
              : 'Clients are offered every version, each a choice of its own.'
          }
        />
        <ErrorBanner error={error} />
        {!p.connected ? (
          <Note>
            No {p.title} account is connected.{' '}
            <InlineLink href="/accounts" primary>
              Connect a {p.title} account
            </InlineLink>
            ; this applies once one is.
          </Note>
        ) : p.by_family ? (
          <>
            {p.families.length === 0 && <Muted>The connected accounts list no model of a family.</Muted>}
            {p.families.map((f) => (
              <FamilyBinding key={f.follows} family={f} names={names} onChanged={onChanged}
                connected={p.accounts.filter((a) => !a.disabled).map((a) => a.id)} />
            ))}
          </>
        ) : null /* every model: each is a choice of its own, so there is nothing to bind */}
      </Stack>
    </Card>
  )
}

/**
 * One family, drawn as a client's model name is: what it is, and one menu of
 * what it serves — Latest, each account its own newest version, so a plan
 * without the newest still serves the family; or one version for the whole
 * gateway. One menu, as a client's name has, rather than a checkbox that
 * greyed a menu out and a list of every version under it: the versions are in
 * the menu, and an account's own Models page is where they are switched.
 */
function FamilyBinding({
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
  const set = (model: string) =>
    run('version', async () => {
      await api.models.setVersion(f.follows, model)
      onChanged()
    })
  const options = [
    { value: '', label: 'Latest', hint: f.serving !== '' ? `now ${f.serving}` : 'each account its newest' },
    ...f.versions.map((v) => ({
      value: v.model,
      label: v.title || v.model,
      hint: v.model,
      disabled: v.on.length === 0,
    })),
  ]
  const pinnedVersion = f.versions.find((v) => v.model === pinned)
  const lacking = pinned !== '' ? connected.filter((id) => !(pinnedVersion?.on ?? []).includes(id)) : []

  return (
    <Tile>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-medium text-on-surface">{f.title}</span>
          <code className="text-xs text-on-surface-variant">{f.id}</code>
        </div>
        <Select label="Serves" value={pinned} options={options} disabled={busy !== ''} onChange={(v) => void set(v)} />
        {f.serving === '' && <Chip tone="warn">{f.why ?? 'no account serves it'}</Chip>}
        {lacking.length > 0 && (
          <Note tone="warn">
            <Accounts ids={lacking} names={names} /> {lacking.length === 1 ? 'does' : 'do'} not have {pinned}, and{' '}
            {lacking.length === 1 ? 'serves' : 'serve'} no {f.title}.
          </Note>
        )}
        <ErrorBanner error={error} />
      </div>
    </Tile>
  )
}

/** Accounts by name, each a link to its own Models page, where a version is switched. */
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
