import type { MouseEvent } from 'react'
import { navigate, type NavLevel, type NavLink } from '../route'

/**
 * The app's navigation: one trail across the top, one rail down the side,
 * for every page. Both are read off the path (route.ts), and both are plain
 * links — the browser can open any of them in a new tab or copy it — with the
 * click intercepted only so the app is not rebuilt to change a path.
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

const chevron = 'M9.3 6.7 10.7 5.3 17.4 12l-6.7 6.7-1.4-1.4 5.3-5.3z'

/**
 * Where this page sits in the tree: every level above it a link, itself not.
 *
 * A filled, fully rounded bar in the primary container colour, with the page
 * itself as a filled chip — Material You's tonal emphasis. Drawn as plain text
 * it sat under the app's name in the same weight and colour and read as a
 * subtitle; filled, it reads as the control it is.
 */
export function Breadcrumbs({ trail }: { trail: NavLink[] }) {
  return (
    <nav
      aria-label="Breadcrumb"
      className="min-w-0 overflow-x-auto rounded-[var(--radius-md3-full)] bg-primary-container p-1 text-on-primary-container"
    >
      <ol className="m-0 flex min-w-max list-none items-center gap-0.5 p-0 text-sm">
        {trail.map((c, i) => {
          const last = i === trail.length - 1
          return (
            <li key={c.href + i} className="flex items-center gap-0.5">
              {i > 0 && (
                <svg viewBox="0 0 24 24" aria-hidden className="size-4 shrink-0 fill-current opacity-60">
                  <path d={chevron} />
                </svg>
              )}
              {last ? (
                <span
                  aria-current="page"
                  className="block max-w-[28ch] truncate rounded-[var(--radius-md3-full)] bg-surface-lowest px-3 py-1.5 font-medium text-on-surface shadow-sm"
                >
                  {c.label}
                </span>
              ) : (
                <a
                  href={c.href}
                  onClick={follow(c.href)}
                  className="state-layer block max-w-[24ch] truncate rounded-[var(--radius-md3-full)] px-3 py-1.5 font-medium"
                >
                  {c.label}
                </a>
              )}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

/**
 * The choices of the level this page is on, and the way up.
 *
 * Only that level: at the top the screens, under an account its pages, under
 * its settings their sections — so the rail is always a handful of short
 * entries. Vertical beside the page on a wide screen; a row that scrolls
 * sideways above it on a narrow one.
 */
export function NavRail({ level, row = false }: { level: NavLevel; row?: boolean }) {
  const item = (on: boolean) =>
    `state-layer flex h-10 shrink-0 items-center rounded-[var(--radius-md3-full)] px-4 text-sm font-medium whitespace-nowrap ${
      on
        ? 'bg-secondary-container text-on-secondary-container'
        : 'text-on-surface-variant hover:text-on-surface'
    }`
  return (
    <nav
      aria-label="Navigation"
      className={row ? '-mx-1 overflow-x-auto px-1' : 'flex flex-col gap-1'}
    >
      <ul className={`m-0 flex list-none gap-1 p-0 ${row ? 'min-w-max items-center' : 'flex-col'}`}>
        {level.back !== undefined && (
          <li className={row ? '' : 'mb-1 border-b border-outline-variant pb-2'}>
            <a
              href={level.back.href}
              onClick={follow(level.back.href)}
              title={`Back to ${level.back.label}`}
              className="state-layer flex h-10 min-w-0 items-center gap-2 rounded-[var(--radius-md3-full)] pr-4 pl-2 text-sm text-on-surface-variant hover:text-on-surface"
            >
              <svg viewBox="0 0 24 24" aria-hidden className="size-5 shrink-0 fill-current">
                <path d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z" />
              </svg>
              <span className={`truncate ${row ? 'max-w-[16ch]' : ''}`}>{level.back.label}</span>
            </a>
          </li>
        )}
        {level.items.map((l) => {
          const on = l.href === level.current
          return (
            <li key={l.href}>
              <a
                href={l.href}
                onClick={follow(l.href)}
                aria-current={on ? 'page' : undefined}
                className={item(on)}
              >
                {l.label}
              </a>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}
