import type { ReactNode } from 'react'
import { Help, Note } from './info'

/**
 * The containers a screen is built from, from the outside in: a page is a
 * column of cards, a card holds sections, a section holds tiles.
 *
 * Every container in the UI is told apart by its fill, never by a line round
 * it — surface-container for a card, surface-lowest for a tile inside one.
 * Outlines are for controls, which need an edge to be found.
 */

/** The vertical rhythm between a column's children, as named steps rather than spacing classes. */
const GAP = { 2: 'gap-2', 3: 'gap-3', 4: 'gap-4', 5: 'gap-5', 6: 'gap-6' } as const
export type Gap = keyof typeof GAP

/** Children one above the other. */
export function Stack({
  gap = 4,
  children,
  as: Tag = 'div',
}: {
  gap?: Gap
  children: ReactNode
  as?: 'div' | 'ul' | 'section'
}) {
  const list = Tag === 'ul' ? 'm-0 list-none p-0' : ''
  return <Tag className={`flex flex-col ${GAP[gap]} ${list}`}>{children}</Tag>
}

/** A screen's body: its cards and banners, a step apart. */
export function Page({ children }: { children: ReactNode }) {
  return <Stack gap={5}>{children}</Stack>
}

/**
 * A card, with its prose held to a readable line.
 *
 * The max-w on paragraphs is the whole reason every screen can share one page
 * width. A card spans the window because a table inside it needs to; a
 * sentence inside it set to the same 1800px is unreadable, and the eye loses
 * the start of the next line. Capping the text rather than the card is what
 * lets both be true, and it is one rule here instead of a class on every
 * paragraph in the app.
 *
 * `title` heads it (with `aside` at the right of the title, and `info` as
 * bullets under it); without a title, `info` is the card's first thing.
 */
export function Card({
  children,
  title,
  aside,
  info,
}: {
  children: ReactNode
  title?: ReactNode
  aside?: ReactNode
  /** What the card is for, as bullets. */
  info?: ReactNode
}) {
  return (
    <section className="rounded-md3-xl bg-surface-container p-6 [&_p]:max-w-[80ch]">
      {title !== undefined ? (
        <CardTitle aside={aside} info={info}>
          {title}
        </CardTitle>
      ) : (
        info !== undefined && <Help className="mb-4">{info}</Help>
      )}
      {children}
    </section>
  )
}

/** A card's heading, for the rare card whose title is not its first thing. */
export function CardTitle({
  children,
  aside,
  info,
}: {
  children: ReactNode
  aside?: ReactNode
  /** Help for the card, as bullets under its title. */
  info?: ReactNode
}) {
  return (
    <div className="mb-4 flex flex-col gap-3">
      {/* Wraps rather than squeezing: on a phone a toolbar of an aside goes
          under the title instead of beside it. */}
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
        <h2 className="text-xl leading-7 font-normal text-on-surface">{children}</h2>
        {aside}
      </div>
      {info !== undefined && <Help>{info}</Help>}
    </div>
  )
}

/**
 * A box inside a card: white on the card's tone, rounded, no outline. The
 * class, for the few boxes that lay their content out themselves.
 */
export const tile = 'rounded-md3-l bg-surface-lowest'

/** A box inside a card, padded for a row of content: a setting, an alias, a fact. */
export function Tile({ children, as: Tag = 'div' }: { children: ReactNode; as?: 'div' | 'li' }) {
  return <Tag className={`${tile} px-4 py-3`}>{children}</Tag>
}

/**
 * A titled group inside a card: the settings of one prefix, the aliases of one
 * kind. `title` may be left out where a group needs no heading on its own —
 * one provider's accounts, when there is one provider — and `label` still
 * names it to assistive technology.
 */
export function Section({
  title,
  label,
  children,
}: {
  title?: ReactNode
  label?: string
  children: ReactNode
}) {
  return (
    <section aria-label={label}>
      {title !== undefined && <h3 className="mb-2 text-sm font-medium text-on-surface">{title}</h3>}
      {children}
    </section>
  )
}

/**
 * The card that takes something out of use for good: what it is, what it
 * costs in the error colour, and the one button that does it.
 *
 * On a page of its own wherever it appears, so it cannot be hit on the way to
 * anything else; one component so every such card says it the same way.
 */
export function DangerZone({
  title,
  children,
  action,
}: {
  title: string
  /** What is lost, said plainly. */
  children: ReactNode
  action: ReactNode
}) {
  return (
    <Card title={title}>
      <Note tone="error" className="mb-4">
        {children}
      </Note>
      {action}
    </Card>
  )
}

/** A row of buttons. `end` for a dialog's or a form's closing actions. */
export function Actions({ children, end = false }: { children: ReactNode; end?: boolean }) {
  return (
    <div className={`flex flex-wrap items-center gap-2 ${end ? 'justify-end' : ''}`}>{children}</div>
  )
}
