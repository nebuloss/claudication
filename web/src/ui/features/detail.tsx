import { useEffect, type ReactNode } from 'react'
import { nameItem } from '../names'
import { Freshness } from '../primitives'

/**
 * The top of an account's or a key's pages: what it is, and the state it is
 * in at a glance. Where it sits and its pages are the app's navigation — the
 * trail above and the rail beside — so they are not repeated here; this only
 * tells that navigation the item's name, so the trail says it rather than its
 * id.
 */
export function DetailHeader({
  kind,
  id,
  title,
  subtitle,
  aside,
  freshness,
}: {
  kind: 'accounts' | 'keys'
  id: string
  title: string
  subtitle?: ReactNode
  /** Status at a glance: chips, meters. */
  aside?: ReactNode
  freshness?: Parameters<typeof Freshness>[0]
}) {
  useEffect(() => nameItem(kind, id, title), [kind, id, title])
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      <div className="min-w-0">
        <h2 className="m-0 text-2xl leading-8 font-normal break-all text-on-surface">{title}</h2>
        {subtitle !== undefined && (
          <p className="mt-1 mb-0 text-sm text-on-surface-variant">{subtitle}</p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {aside}
        {freshness !== undefined && <Freshness {...freshness} />}
      </div>
    </div>
  )
}
