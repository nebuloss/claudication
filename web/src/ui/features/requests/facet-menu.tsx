import type { FacetValue, RequestFacets } from '../../../api/client'
import { compact } from '../../format'
import { MenuDivider, MenuRow, Muted, menuItem } from '../../primitives'
import type { SetField } from './filter'
import { messageLabel } from './record'

/**
 * Which facet belongs to which filter field. One place, because the menus and
 * the chips both need it and they were answering it differently: a chat read
 * as its name in the menu and as sixteen hex characters in the chip that the
 * menu had just set. A Record rather than a switch, so a new field that has
 * no facet is a compile error here rather than an empty menu.
 */
const FACET: Record<SetField, (f: RequestFacets) => FacetValue[]> = {
  model: (f) => f.models,
  client: (f) => f.clients,
  ip: (f) => f.ips,
  code: (f) => f.statuses,
  chat: (f) => f.chats,
  // Labelled here, once, so the menu and the chip read the way the column
  // does. The stored codes are the upstream's own words and the cell
  // translates them; without this the column said "failed" while the chip it
  // set said "other".
  message: (f) =>
    (f.messages ?? []).map((v) => ({ ...v, label: v.value === '' ? 'no error' : messageLabel(v.value) })),
  kind: (f) => f.kinds,
  key: (f) => f.keys,
  account: (f) => f.accounts,
}

/**
 * What the column menus can offer, counted over the whole log rather than the
 * page on screen — a menu built from fifty rows can only offer what you are
 * already looking at.
 */
export class Facets {
  constructor(private readonly facets: RequestFacets | null) {}

  get loaded(): boolean {
    return this.facets !== null
  }

  of(field: SetField): FacetValue[] {
    // ?? []: the server sends null for a column with nothing in it.
    return this.facets === null ? [] : (FACET[field](this.facets) ?? [])
  }

  /**
   * What to call a filtered value. The facet's label where there is one — a
   * chat's name, "no answer" for a status of 0 — and the value itself
   * otherwise, which is the common case and already readable.
   */
  label(field: SetField, value: string): string {
    const hit = this.of(field).find((v) => v.value === value)
    return hit?.label !== undefined && hit.label !== '' ? hit.label : value
  }
}

/**
 * The checkboxes under a column heading.
 *
 * Anything already filtered stays in the list even when the server did not
 * offer it — past the cap, or arrived from a link — because a tick you cannot
 * see is a filter you cannot lift.
 */
export function FacetList({
  values,
  chosen,
  loaded,
  onToggle,
  onClear,
}: {
  values: FacetValue[]
  chosen: readonly string[]
  loaded: boolean
  onToggle: (value: string) => void
  onClear: () => void
}) {
  const offered = new Set(values.map((v) => v.value))
  const rows = [
    ...values,
    ...chosen.filter((v) => !offered.has(v)).map((v): FacetValue => ({ value: v, count: 0 })),
  ]

  if (rows.length === 0) {
    return (
      <div className="px-3 py-2">
        <Muted size="xs">{loaded ? 'Nothing recorded in this column yet.' : 'Loading…'}</Muted>
      </div>
    )
  }

  return (
    <>
      {chosen.length > 0 && (
        <>
          <MenuRow onClick={onClear}>Clear {chosen.length} selected</MenuRow>
          <MenuDivider />
        </>
      )}
      {rows.map((v) => (
        <label key={v.value} className={menuItem()}>
          <input
            type="checkbox"
            checked={chosen.includes(v.value)}
            onChange={() => onToggle(v.value)}
            className="size-4 shrink-0 accent-primary"
          />
          <span className="min-w-0 flex-1 truncate" title={v.label ?? v.value}>
            {v.label ?? (v.value === '' ? 'none' : v.value)}
          </span>
          {/* Totals over the whole log, not over what this filter leaves, so
              they do not move as you tick things and cannot talk you into a
              corner you then cannot see the way out of. */}
          {v.count > 0 && (
            <span className="shrink-0 text-xs tabular-nums text-on-surface-variant">
              {compact(v.count)}
            </span>
          )}
        </label>
      ))}
    </>
  )
}
