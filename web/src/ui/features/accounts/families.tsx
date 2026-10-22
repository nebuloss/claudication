import { api, type Account } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, InlineLink, Muted } from '../../primitives'

/**
 * Which version of each family this account would serve: what a client
 * naming a family gets when its request lands here (internal/service/models,
 * family.go) — the newest version of it this account lists and has on, so a
 * plan without the latest serves the one before, or the one version the
 * family is set to on Settings › Models, if it has it. Read from the same report
 * as Settings › Models, so the two cannot disagree; the switches below are
 * what changes it.
 */
export default function AccountFamilies({ account, version }: { account: Account; version: number }) {
  const { data } = useLoader((signal) => api.models.families(signal), { deps: [account.id, version] })
  const report = data?.find((p) => p.provider === account.provider)
  const row = report?.accounts.find((a) => a.id === account.id)
  if (report === undefined || row === undefined || report.families.length === 0) return null
  return (
    <Card title="Serves for each family">
      <ul className="grid grid-cols-1 gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[12rem_1fr]">
        {report.families.map((f) => {
          const v = row.serves[f.follows] ?? ''
          return (
            <li key={f.follows} className="contents">
              <span className="text-on-surface">{f.title}</span>
              <span>
                {v !== '' ? (
                  <>
                    <code className="text-on-surface">{v}</code>
                    {v !== f.serving && f.serving !== '' && (
                      <Muted as="span" size="xs">
                        {' '}
                        — not the newest, {f.serving}, which this account does not list or has off
                      </Muted>
                    )}
                  </>
                ) : (
                  <Muted as="span" size="xs">
                    {(f.pinned ?? '') !== ''
                      ? `none: the family is set to ${f.pinned ?? ''}, which this account does not list or has off`
                      : 'none: this account lists no version of it, or has every one off'}
                  </Muted>
                )}
              </span>
            </li>
          )
        })}
      </ul>
      <div className="mt-3">
        <Muted size="xs">
        A request for a family landing here is served this version.{' '}
        <InlineLink href="/settings/models">Settings › Models</InlineLink> shows every account&rsquo;s.
        </Muted>
      </div>
    </Card>
  )
}
