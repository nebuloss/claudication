import type { ReactNode } from 'react'
import { ITEM_PAGES, type Section } from '../route'
import { Breadcrumbs, Freshness, LinkTabs, SectionLayout, type Crumb } from '../primitives'

/**
 * The top of an account's or a key's pages: where it sits in the tree, what it
 * is, the state it is in at a glance, and its pages — Usage, Chats, Requests,
 * Settings — each a path under it.
 *
 * The name is said once, here. The breadcrumb above it is the way back, so
 * there is no "All accounts" link, and the pages below do not repeat it.
 */
export function DetailHeader({
  trail,
  title,
  subtitle,
  aside,
  base,
  page,
  section,
  freshness,
}: {
  /** The steps above this item: the list it is in. */
  trail: Crumb[]
  title: string
  subtitle?: ReactNode
  /** Status at a glance: chips, meters. */
  aside?: ReactNode
  /** The item's own path; each page is under it. */
  base: string
  page: string
  /** The settings section open, for the last step of the trail. */
  section?: string
  freshness?: Parameters<typeof Freshness>[0]
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <Breadcrumbs trail={[...trail, { label: title, href: `${base}/usage` }, ...pageCrumbs(base, page, section)]} />
        {freshness !== undefined && <Freshness {...freshness} />}
      </div>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <h2 className="m-0 text-2xl leading-8 font-normal break-all text-on-surface">{title}</h2>
          {subtitle !== undefined && (
            <p className="mt-1 mb-0 text-sm text-on-surface-variant">{subtitle}</p>
          )}
        </div>
        {aside !== undefined && <div className="flex flex-wrap items-center gap-3">{aside}</div>}
      </div>
      <LinkTabs label="Pages" base={base} pages={ITEM_PAGES} current={page} />
    </div>
  )
}

/**
 * The page's own steps in the trail: the page, and under Settings the section
 * open. The last is unlinked, being where you are.
 */
function pageCrumbs(base: string, page: string, section?: string): Crumb[] {
  const p = ITEM_PAGES.find((x) => x.id === page)
  if (p === undefined) return []
  if (section === undefined) return [{ label: p.label }]
  return [{ label: p.label, href: `${base}/${p.id}` }, { label: section }]
}

/**
 * An item's settings, one section per page beside the list of them. The
 * destructive section is drawn apart in the error colour.
 */
export function ItemSettings({
  base,
  sections,
  current,
  danger,
  children,
}: {
  base: string
  sections: readonly Section[]
  current: string
  danger: string
  children: ReactNode
}) {
  return (
    <SectionLayout
      label="Settings"
      base={`${base}/settings`}
      sections={sections}
      current={current}
      tone={{ [danger]: 'error' }}
    >
      {children}
    </SectionLayout>
  )
}
