import type { MouseEvent, ReactNode } from 'react'
import { navigate, type Section } from '../route'

/**
 * Navigation, as links. Every place in the UI is a path (see route.ts), so
 * everything that moves between places is an <a href> the browser can open in
 * a new tab, copy, or show in its status bar — the click is intercepted only
 * so the app is not rebuilt to change a path.
 */

/** Follow an in-app link without a reload; leave modified clicks to the browser. */
export function follow(href: string) {
  return (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
    e.preventDefault()
    navigate(href)
    window.scrollTo(0, 0)
  }
}

/** One step of a breadcrumb trail; the last has no href, being where you are. */
export type Crumb = { label: string; href?: string }

/** Where this page sits in the tree, each step above it a link. */
export function Breadcrumbs({ trail }: { trail: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="min-w-0">
      <ol className="m-0 flex list-none flex-wrap items-center gap-1 p-0 text-sm">
        {trail.map((c, i) => (
          <li key={i} className="flex min-w-0 items-center gap-1">
            {i > 0 && (
              <svg viewBox="0 0 24 24" aria-hidden className="size-4 shrink-0 fill-on-surface-variant/60">
                <path d="M9.3 6.7 10.7 5.3 17.4 12l-6.7 6.7-1.4-1.4 5.3-5.3z" />
              </svg>
            )}
            {c.href !== undefined ? (
              <a
                href={c.href}
                onClick={follow(c.href)}
                className="state-layer truncate rounded-[var(--radius-md3-full)] px-2 py-1 text-on-surface-variant hover:text-on-surface"
              >
                {c.label}
              </a>
            ) : (
              <span aria-current="page" className="truncate px-2 py-1 text-on-surface">
                {c.label}
              </span>
            )}
          </li>
        ))}
      </ol>
    </nav>
  )
}

/**
 * The pages of one place, as MD3 secondary tabs: Usage, Chats, Requests,
 * Settings under an account. Each is a link to its path.
 */
export function LinkTabs({
  label,
  base,
  pages,
  current,
  aside,
}: {
  label: string
  /** The place's path; each page is base/<id>. */
  base: string
  pages: readonly Section[]
  current: string
  aside?: ReactNode
}) {
  return (
    <div className="-mx-1 overflow-x-auto px-1">
      <nav
        aria-label={label}
        className="flex w-full min-w-max items-center gap-1 border-b border-outline-variant"
      >
        {pages.map((p) => {
          const href = `${base}/${p.id}`
          const on = p.id === current
          return (
            <a
              key={p.id}
              href={href}
              onClick={follow(href)}
              aria-current={on ? 'page' : undefined}
              className={`state-layer relative rounded-t-[var(--radius-md3-m)] px-4 py-3 text-sm font-medium whitespace-nowrap ${
                on ? 'text-primary' : 'text-on-surface-variant hover:text-on-surface'
              }`}
            >
              {p.label}
              {on && <span className="absolute inset-x-2 bottom-0 h-[3px] rounded-t-full bg-primary" />}
            </a>
          )
        })}
        {aside !== undefined && <div className="ml-auto pb-1 pl-4">{aside}</div>}
      </nav>
    </div>
  )
}

/**
 * A list of sections beside the one open, as an MD3 navigation drawer: one
 * page per section, each a path, so a section is a link and a bookmark rather
 * than a card somewhere down a long page.
 *
 * Beside the content on a wide screen; above it, as a row that scrolls
 * sideways, on a narrow one.
 */
export function SectionLayout({
  label,
  base,
  sections,
  current,
  tone,
  children,
}: {
  label: string
  /** The place's path; each section is base/<id>. */
  base: string
  sections: readonly Section[]
  current: string
  /** Sections drawn in the error colour: the destructive ones. */
  tone?: Record<string, 'error'>
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-5 md:flex-row md:items-start md:gap-8">
      <nav aria-label={label} className="-mx-1 overflow-x-auto px-1 md:sticky md:top-32 md:mx-0 md:w-60 md:shrink-0 md:overflow-visible md:px-0">
        <ul className="m-0 flex w-max list-none gap-1 p-0 md:w-auto md:flex-col">
          {sections.map((s) => {
            const href = `${base}/${s.id}`
            const on = s.id === current
            const danger = tone?.[s.id] === 'error'
            return (
              <li key={s.id}>
                <a
                  href={href}
                  onClick={follow(href)}
                  aria-current={on ? 'page' : undefined}
                  className={`state-layer flex h-10 items-center rounded-[var(--radius-md3-full)] px-4 text-sm font-medium whitespace-nowrap md:h-12 md:px-5 ${
                    on
                      ? 'bg-secondary-container text-on-secondary-container'
                      : danger
                        ? 'text-error'
                        : 'text-on-surface-variant hover:text-on-surface'
                  }`}
                >
                  {s.label}
                </a>
              </li>
            )
          })}
        </ul>
      </nav>
      <div className="flex min-w-0 flex-1 flex-col gap-5">{children}</div>
    </div>
  )
}
