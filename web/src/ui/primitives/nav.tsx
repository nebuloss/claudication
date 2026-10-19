import type { ReactNode } from 'react'
import type { NavLevel, NavLink } from '../route'
import { follow } from './link'

/**
 * The app's navigation: one trail across the top, one rail down the side,
 * for every page. Both are read off the path (route.ts), and both are plain
 * links — the browser can open any of them in a new tab or copy it — with the
 * click intercepted only so the app is not rebuilt to change a path.
 */

const chevron = 'M9.3 6.7 10.7 5.3 17.4 12l-6.7 6.7-1.4-1.4 5.3-5.3z'

/**
 * The trail's first step, the gateway's overview, drawn as a house: it is the
 * root every path starts from, not one more page called Overview.
 */
function HomeMark() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden className="block size-5 fill-current">
      <path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z" />
    </svg>
  )
}

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
      className="min-w-0 overflow-x-auto rounded-md3-full bg-primary-container p-1 text-on-primary-container"
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
              {/* Every step a link, the current one too: drawn as the page
                  you are on, and clicked like the others — it opens that
                  page from its top, which is also how to get back to the top
                  of a long one. */}
              <a
                href={c.href}
                onClick={follow(c.href)}
                aria-current={last ? 'page' : undefined}
                aria-label={i === 0 ? c.label : undefined}
                title={i === 0 ? c.label : undefined}
                className={`state-layer block truncate rounded-md3-full py-1.5 font-medium ${
                  i === 0 ? 'px-2' : 'px-3'
                } ${last ? 'max-w-[28ch] bg-surface-lowest text-on-surface shadow-sm' : 'max-w-[24ch]'}`}
              >
                {i === 0 ? <HomeMark /> : c.label}
              </a>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

/**
 * One entry of a rail: the admin app's, and the docs page's contents, which
 * borrow it so the two pages navigate alike. `on` for the page being read.
 */
export function railItem(on: boolean): string {
  return `state-layer flex h-10 shrink-0 items-center rounded-md3-full px-4 text-sm font-medium whitespace-nowrap ${
    on ? 'bg-primary-container text-on-primary-container' : 'text-on-surface-variant hover:text-on-surface'
  }`
}

/**
 * The choices of the level this page is on, and the way up.
 *
 * Only that level: at the top the screens, under an account its pages, under
 * its settings their sections — so the rail is always a handful of short
 * entries. Vertical beside the page on a wide screen; a row that scrolls
 * sideways above it on a narrow one.
 *
 * A filled panel with the page open in the primary container colour, the
 * path bar's: the two navigations read as one, and the rail stands off the
 * page. Back is a raised chip, the way out rather than one more entry.
 */
export function NavRail({ level, row = false }: { level: NavLevel; row?: boolean }) {
  return (
    <nav
      aria-label="Navigation"
      className={`bg-surface-container ${
        row
          ? 'overflow-x-auto rounded-md3-full p-1'
          : 'flex flex-col gap-1 rounded-md3-xl p-2'
      }`}
    >
      <ul className={`m-0 flex list-none gap-1 p-0 ${row ? 'min-w-max items-center' : 'flex-col'}`}>
        {level.back !== undefined && (
          <li className={row ? '' : 'mb-1 border-b border-outline-variant/60 pb-2'}>
            <a
              href={level.back.href}
              onClick={follow(level.back.href)}
              title={`Back to ${level.back.label}`}
              className="state-layer flex h-10 min-w-0 items-center gap-2 rounded-md3-full bg-surface-lowest pr-4 pl-2 text-sm font-medium text-on-surface"
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
                className={railItem(on)}
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

/**
 * The favicon's stenosis, at header size, on its tonal tile: the one mark
 * both headers carry, the admin app's and the docs page's, so they read as
 * one product whatever each page's own colours are.
 */
export function BrandMark() {
  return (
    <span className="grid size-10 shrink-0 place-items-center rounded-md3-m bg-primary-container text-on-primary-container">
      <svg viewBox="0 0 32 32" className="size-5" aria-hidden>
        <g fill="none" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round">
          <path d="M3 8c6.5 0 6.5 6 10 6s3.5-6 10-6" />
          <path d="M3 24c6.5 0 6.5-6 10-6s3.5 6 10 6" />
        </g>
        <circle cx="27.5" cy="16" r="2.75" fill="currentColor" />
      </svg>
    </span>
  )
}

/**
 * The product's name beside its mark, with the running build after it: the
 * left of both the admin header and the sign-in screen's, so signing in and
 * being signed in read as one product.
 */
export function AppTitle({ version }: { version?: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-3">
      <BrandMark />
      <h1 className="flex min-w-0 items-baseline gap-2 text-lg leading-10 font-normal whitespace-nowrap text-on-surface">
        claudication gateway
        {version}
      </h1>
    </div>
  )
}

/** A build string, set small after the product's name. */
export function VersionTag({ children }: { children: string }) {
  return <span className="truncate font-mono text-xs text-on-surface-variant">{children}</span>
}
