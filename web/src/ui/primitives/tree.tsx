import { useCallback, useState, type ReactNode } from 'react'
import { Switch } from './field'
import { RowToggle } from './table'

/**
 * A list folded into groups: a heading row per group, with a chevron that
 * folds it, a count, and one switch over everything in it — on, off, or mixed
 * when only some of it is on. Groups nest: the models lists put a provider's
 * families inside the provider.
 *
 * The heading is a row of the list it heads rather than a card of its own,
 * so a folded tree still reads as one list, and a nested group is told from
 * its parent by its indent and its weight alone.
 */
export function TreeGroup({
  title,
  count,
  open,
  onToggle,
  state,
  onSwitch,
  disabled,
  level = 0,
  children,
}: {
  title: string
  /** What is on of what is in it: "3 of 4 on". */
  count: string
  open: boolean
  onToggle: () => void
  state: 'on' | 'off' | 'mixed'
  /** The group's switch: every item in it on, or every one off. */
  onSwitch: (on: boolean) => void
  disabled?: boolean
  level?: 0 | 1
  children: ReactNode
}) {
  return (
    <li className="list-none">
      <div className={`flex items-center gap-2 py-2 ${level === 1 ? 'pl-6' : ''}`}>
        <RowToggle open={open} onToggle={onToggle} label={`${open ? 'Fold' : 'Unfold'} ${title}`} />
        <button
          type="button"
          onClick={onToggle}
          className={`min-w-0 flex-1 truncate text-left ${
            level === 0 ? 'text-sm font-medium text-on-surface' : 'text-sm text-on-surface'
          }`}
        >
          {title}
          <span className="ml-2 text-xs font-normal text-on-surface-variant tabular-nums">{count}</span>
        </button>
        <Switch
          quiet
          label={`All of ${title}`}
          checked={state === 'on'}
          mixed={state === 'mixed'}
          onChange={onSwitch}
          disabled={disabled}
        />
      </div>
      {open && <ul className={`m-0 list-none p-0 ${level === 0 ? '' : 'pl-12'}`}>{children}</ul>}
    </li>
  )
}

/**
 * Which groups of one list are open, and the way to fold one: the defaults
 * until the operator folds something, then what they left, remembered per
 * list (`storageKey`) in this browser. Only what was changed is kept, so a
 * family that appears tomorrow takes the default rather than inheriting a
 * fold nobody made. A storage that throws — a private window, blocked site
 * data — leaves the folds working for the visit and forgotten after.
 */
export function useFolds(storageKey: string, defaults: ReadonlySet<string>) {
  const key = `claudication:folds:${storageKey}`
  const [changed, setChanged] = useState<Record<string, boolean>>(() => {
    try {
      const raw = window.localStorage.getItem(key)
      const parsed: unknown = raw === null ? {} : JSON.parse(raw)
      return typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, boolean>) : {}
    } catch {
      return {}
    }
  })
  const isOpen = useCallback((group: string) => changed[group] ?? defaults.has(group), [changed, defaults])
  const toggle = useCallback(
    (group: string) =>
      setChanged((c) => {
        const next = { ...c, [group]: !(c[group] ?? defaults.has(group)) }
        try {
          window.localStorage.setItem(key, JSON.stringify(next))
        } catch {
          // Remembered for this visit only.
        }
        return next
      }),
    [defaults, key],
  )
  return { isOpen, toggle }
}
