import type { ReactNode } from 'react'
import { navigate } from '../route'
import { AppLink, Freshness, SubNav } from '../primitives'

export type DetailSection = 'usage' | 'settings'

/** The section a path names: its own page is usage, `/settings` is settings. */
export function detailSection(section: string): DetailSection {
  return section === 'settings' ? 'settings' : 'usage'
}

/**
 * The top of an account's or a key's page: the way back, what it is, the
 * state it is in at a glance, and its two pages.
 *
 * Two pages because they are two errands. Usage is reading — what it did and
 * where it went — and the page an operator lands on. Settings is changing it:
 * limits, credentials, removing it. Keeping the controls off the reading page
 * is what lets that page be long without a destructive button in the middle.
 */
export function DetailHeader({
  back,
  backLabel,
  title,
  subtitle,
  aside,
  base,
  section,
  freshness,
}: {
  back: string
  backLabel: string
  title: string
  subtitle?: ReactNode
  /** Status at a glance: chips, meters. */
  aside?: ReactNode
  /** The item's own path; settings is under it. */
  base: string
  section: DetailSection
  freshness?: Parameters<typeof Freshness>[0]
}) {
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <AppLink href={back} className="text-sm text-on-surface-variant">
          ‹ {backLabel}
        </AppLink>
        {freshness !== undefined && <Freshness {...freshness} />}
      </div>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <h2 className="m-0 text-xl leading-7 font-normal break-all text-on-surface">{title}</h2>
          {subtitle !== undefined && (
            <p className="mt-1 mb-0 text-sm text-on-surface-variant">{subtitle}</p>
          )}
        </div>
        {aside !== undefined && <div className="flex flex-wrap items-center gap-3">{aside}</div>}
      </div>
      <SubNav
        label="Page"
        value={section}
        onChange={(s) => {
          navigate(s === 'settings' ? `${base}/settings` : base)
          window.scrollTo(0, 0)
        }}
        options={[
          { id: 'usage', label: 'Usage' },
          { id: 'settings', label: 'Settings' },
        ]}
      />
    </div>
  )
}
