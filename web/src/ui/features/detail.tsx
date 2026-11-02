import { useEffect, type ReactNode } from 'react'
import { nameItem, type ItemKind } from '../names'
import { EditableTitle, Freshness } from '../primitives'
import { ICON, type IconName } from '../primitives/icons'

/**
 * The top of an account's or a client's pages: what it is, and the state it is
 * in at a glance. Where it sits and its pages are the app's navigation — the
 * trail above and the rail beside — so they are not repeated here; this only
 * tells that navigation the item's name, so the trail says it rather than its
 * id.
 *
 * With `rename`, the title is renamed where it stands (EditableTitle): the
 * header is where the name is read, so it is where it is changed.
 */
export function DetailHeader({
  kind,
  id,
  title,
  rename,
  subtitle,
  mark,
  aside,
  freshness,
  section,
}: {
  kind: ItemKind
  id: string
  title: string
  rename?: Omit<Parameters<typeof EditableTitle>[0], 'shown'>
  subtitle?: ReactNode
  /** Whose it is at a glance: its mark on its accent, before the title — the picker that recolours it. */
  mark?: ReactNode
  /** Status at a glance: chips, meters. */
  aside?: ReactNode
  freshness?: Parameters<typeof Freshness>[0]
  /**
   * The page of the item this is, on any page but its overview: the header
   * is then one row — whose page it is, small, and the page's own title —
   * and the status, the subtitle and the controls stay on the overview,
   * where the item is read and changed. A row of chips repeated above every
   * page was the height of a card spent on what the overview already says.
   */
  section?: { label: string; icon?: IconName }
}) {
  useEffect(() => nameItem(kind, id, title), [kind, id, title])
  if (section !== undefined) {
    return (
      <div className="flex min-w-0 items-center justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          {mark !== undefined && <span className="shrink-0">{mark}</span>}
          <span className="truncate text-base text-on-surface-variant">{title}</span>
        </div>
        <h2 className="m-0 flex shrink-0 items-center gap-2 text-2xl leading-8 font-normal text-on-surface">
          {section.icon !== undefined && (
            <svg viewBox="0 0 24 24" aria-hidden className="size-6 shrink-0 fill-current text-on-surface-variant">
              <path d={ICON[section.icon]} />
            </svg>
          )}
          {section.label}
        </h2>
      </div>
    )
  }
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      {/* A title being edited takes the whole row, the status wrapping under
          it: beside the chips and meters it was left a few words wide, too
          narrow to see what is being typed. A title read is only as wide as
          itself, with its pencil beside it. */}
      <div className="min-w-0 grow has-[form]:basis-full">
        <div className="flex min-w-0 items-center gap-3">
          {mark !== undefined && <span className="shrink-0">{mark}</span>}
          <div className="min-w-0 grow">
            {rename !== undefined ? (
              <EditableTitle {...rename} shown={title} />
            ) : (
              <h2 className="m-0 text-2xl leading-8 font-normal break-all text-on-surface">{title}</h2>
            )}
          </div>
        </div>
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
