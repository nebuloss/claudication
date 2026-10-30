import { useEffect, type ReactNode } from 'react'
import { nameItem, type ItemKind } from '../names'
import { EditableTitle, Freshness } from '../primitives'

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
}: {
  kind: ItemKind
  id: string
  title: string
  rename?: Omit<Parameters<typeof EditableTitle>[0], 'shown'>
  subtitle?: ReactNode
  /** Whose it is at a glance: the provider's or the tool's mark, before the title. */
  mark?: ReactNode
  /** Status at a glance: chips, meters. */
  aside?: ReactNode
  freshness?: Parameters<typeof Freshness>[0]
}) {
  useEffect(() => nameItem(kind, id, title), [kind, id, title])
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      {/* A title being edited takes the whole row, the status wrapping under
          it: beside the chips and meters it was left a few words wide, too
          narrow to see what is being typed. A title read is only as wide as
          itself, with its pencil beside it. */}
      <div className="min-w-0 grow has-[form]:basis-full">
        <div className="flex min-w-0 items-center gap-3">
          {mark !== undefined && <span className="grid shrink-0 place-items-center [&>svg]:size-7">{mark}</span>}
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
