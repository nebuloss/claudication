import { Fragment, useEffect, useId, useState, type ReactNode } from 'react'
import { Help } from './info'
import { Overline } from './data'
import { Popover, menuItem, useAnchoredPanel } from './popover'
import { tile } from './surface'

/** The controls that hold a value: text fields, selects, switches, sliders. */

/**
 * The outlined field's shape, shared by Field and Select so the two sit side
 * by side as one family: 56px, a medium rounded corner, the outline turning
 * primary and 2px when focused (or, for a select, open) — with the padding
 * a pixel less, so the text does not move when the border thickens.
 */
export function outlinedField(focused = false, top: 'label' | 'dots' = 'label'): string {
  const edge = focused
    ? 'border-2 border-primary px-[15px]'
    : 'border border-outline px-4 hover:border-on-surface focus:border-2 focus:border-primary focus:px-[15px] focus-visible:border-2 focus-visible:border-primary focus-visible:px-[15px]'
  // Room above the text for the label riding up. A password's dots sit on the
  // x-height, well below the middle of the line, and with the same room they
  // rode at the bottom of the field: they take less, which centres them.
  const pad = top === 'dots' ? 'pt-1' : 'pt-4'
  return `h-14 w-full rounded-md3-m bg-transparent ${pad} text-on-surface outline-none transition-colors ${edge}`
}

/** MD3 outlined text field, with the label riding up out of the way. */
export function Field({
  label,
  value,
  onChange,
  type = 'text',
  autoFocus,
  mono,
  autoComplete = 'off',
  grow = false,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  type?: string
  autoFocus?: boolean
  mono?: boolean
  /** Password fields name themselves so a manager can offer to save them. */
  autoComplete?: string
  /** Takes the room left in a row of controls, wrapping under them below 16rem. */
  grow?: boolean
}) {
  return (
    <label className={`block ${grow ? 'min-w-0 flex-1 basis-64' : ''}`}>
      <span className="relative block">
        <input
          type={type}
          value={value}
          autoFocus={autoFocus}
          placeholder=" "
          spellCheck={false}
          autoComplete={autoComplete}
          onChange={(e) => onChange(e.target.value)}
          className={`peer ${outlinedField(false, type === 'password' ? 'dots' : 'label')} text-base ${
            mono ? 'font-mono text-sm' : ''
          } ${type === 'password' ? 'tracking-[0.15em]' : ''}`}
        />
        <span className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-base text-on-surface-variant transition-all peer-focus:top-2.5 peer-focus:text-xs peer-focus:text-primary peer-[:not(:placeholder-shown)]:top-2.5 peer-[:not(:placeholder-shown)]:text-xs">
          {label}
        </span>
      </span>
    </label>
  )
}

/**
 * One choice in a Select. Options sharing a `group` are listed under its
 * heading, in the order they come; the heading is no choice of its own.
 */
export type SelectOption = { value: string; label: string; hint?: string; group?: string }

/**
 * A choice from a list, as an MD3 outlined select: the field the text fields
 * are, and a menu under it.
 *
 * Not a native <select>: its popup is the operating system's, which cannot be
 * styled and on Linux renders as a grey box in the middle of an MD3 page. The
 * keyboard is a select's — arrows to move, Enter to choose, Escape to close.
 */
export function Select({
  label,
  value,
  options,
  onChange,
  disabled = false,
  width = 'grow',
}: {
  label: string
  value: string
  options: SelectOption[]
  onChange: (value: string) => void
  disabled?: boolean
  /**
   * How much of a row it takes: what is left (`grow`), or a fixed 24rem
   * beside a field (`fixed`) — the whole row on a phone either way.
   */
  width?: 'grow' | 'fixed'
}) {
  const p = useAnchoredPanel({ width: 260, matchAnchor: true, maxHeight: 320 })
  const [cursor, setCursor] = useState(0)
  const id = useId()
  const current = options.find((o) => o.value === value) ?? options[0]

  // The cursor on the chosen option, and that option in view, as it opens.
  useEffect(() => {
    if (!p.open) return
    setCursor(Math.max(0, options.findIndex((o) => o.value === value)))
    // Only on opening: the list re-rendering while open must not move it.
  }, [p.open])
  useEffect(() => {
    if (!p.open) return
    p.panel.current
      ?.querySelector<HTMLElement>(`[data-index="${cursor}"]`)
      ?.scrollIntoView({ block: 'nearest' })
  }, [p.open, p.panel, cursor])

  const choose = (o: SelectOption) => {
    p.close()
    p.anchor.current?.focus()
    if (o.value !== value) onChange(o.value)
  }

  return (
    <div className={`relative ${width === 'fixed' ? 'w-full sm:w-96' : 'flex-1'}`}>
      <button
        ref={p.anchor}
        type="button"
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={p.open}
        aria-controls={p.open ? id : undefined}
        onClick={p.toggle}
        onKeyDown={(e) => {
          if (!p.open) {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault()
              p.show()
            }
            return
          }
          if (e.key === 'ArrowDown') {
            e.preventDefault()
            setCursor((c) => Math.min(options.length - 1, c + 1))
          } else if (e.key === 'ArrowUp') {
            e.preventDefault()
            setCursor((c) => Math.max(0, c - 1))
          } else if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            const o = options[cursor]
            if (o !== undefined) choose(o)
          } else if (e.key === 'Tab') {
            p.close()
          }
        }}
        className={`${outlinedField(p.open)} state-layer flex items-center gap-2 text-left disabled:opacity-60`}
      >
        <span className="min-w-0 flex-1 truncate text-sm">{current?.label ?? ''}</span>
        <svg
          viewBox="0 0 10 6"
          aria-hidden
          className={`h-2 w-3 shrink-0 fill-current text-on-surface-variant transition-transform ${p.open ? 'rotate-180' : ''}`}
        >
          <path d="M0 0h10L5 6z" />
        </svg>
      </button>
      <span
        className={`pointer-events-none absolute top-2.5 left-4 text-xs ${p.open ? 'text-primary' : 'text-on-surface-variant'}`}
      >
        {label}
      </span>
      {p.at !== null && (
        <Popover panelRef={p.panel} at={p.at} role="listbox" label={label} id={id}>
          {options.map((o, i) => (
            <Fragment key={o.value}>
            {o.group !== undefined && o.group !== options[i - 1]?.group && (
              <div role="presentation" className="px-3 pt-3 pb-1">
                <Overline as="span">{o.group}</Overline>
              </div>
            )}
            <div
              data-index={i}
              role="option"
              aria-selected={o.value === value}
              onMouseEnter={() => setCursor(i)}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => choose(o)}
              className={menuItem({ active: i === cursor, selected: o.value === value })}
            >
              <span className="min-w-0 flex-1">
                <span className="block truncate">{o.label}</span>
                {o.hint !== undefined && (
                  <span className="block truncate font-mono text-xs opacity-75">{o.hint}</span>
                )}
              </span>
              {o.value === value && (
                <svg viewBox="0 0 24 24" aria-hidden className="size-5 shrink-0 fill-current">
                  <path d="M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4z" />
                </svg>
              )}
            </div>
            </Fragment>
          ))}
        </Popover>
      )}
    </div>
  )
}

/**
 * MD3 switch: an on/off state that applies immediately, not a form value you
 * submit. Used where the choice is "is there a limit at all" — a checkbox reads
 * as an option, a switch reads as a state, and the second is what this is.
 */
export function Switch({
  checked,
  onChange,
  label,
  disabled,
  info,
  quiet = false,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
  disabled?: boolean
  /** What the switch does, as bullets under it, in line with its label. */
  info?: ReactNode
  /**
   * The label spoken but not shown: an item's on/off switch in a list row or
   * a page's header, where the row or the title already says what it is.
   */
  quiet?: boolean
}) {
  // Outside the label, so selecting its text never flips the switch.
  if (info !== undefined) {
    return (
      <div className="flex flex-col gap-2">
        <Switch checked={checked} onChange={onChange} label={label} disabled={disabled} quiet={quiet} />
        <Help plain className="sm:ml-16">
          {info}
        </Help>
      </div>
    )
  }
  return (
    <label className="flex cursor-pointer items-center gap-3 select-none">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={`relative inline-flex h-8 w-13 shrink-0 items-center rounded-md3-full border-2 transition-colors disabled:pointer-events-none disabled:opacity-40 ${
          checked ? 'border-primary bg-primary' : 'border-outline bg-surface-high'
        }`}
      >
        <span
          className={`absolute rounded-md3-full transition-all ${
            checked
              ? 'left-[calc(100%-1.75rem)] size-6 bg-on-primary'
              : 'left-1 size-4 bg-outline'
          }`}
        />
      </button>
      <span className={quiet ? 'sr-only' : 'text-sm text-on-surface'}>{label}</span>
    </label>
  )
}

/**
 * One setting that is a switch, in its own tile: the switch, what it does, and
 * whatever has to stay on the page under it — a cost, where it is published.
 *
 * Four settings cards drew this by hand; the switch moving at once and moving
 * back on a failure is useOptimistic's, so a card is its copy and its write.
 */
export function SettingSwitch({
  checked,
  onChange,
  label,
  info,
  disabled,
  children,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
  info?: ReactNode
  disabled?: boolean
  /** Under the switch: a Note, a list of the paths it governs. */
  children?: ReactNode
}) {
  return (
    <div className={`${tile} px-4 py-3`}>
      <Switch checked={checked} onChange={onChange} label={label} info={info} disabled={disabled} />
      {children !== undefined && <div className="mt-3 empty:hidden">{children}</div>}
    </div>
  )
}

/**
 * A slider over a fixed ladder of values rather than a linear range.
 *
 * Token budgets span orders of magnitude — ten thousand to tens of millions —
 * so a linear slider spends nine tenths of its travel on values nobody wants
 * and cannot land on a round number. The slider indexes into `steps` instead,
 * which makes every position a value worth choosing.
 */
export function StepSlider({
  steps,
  value,
  onChange,
  format,
  label,
}: {
  steps: number[]
  value: number
  onChange: (v: number) => void
  format: (v: number) => string
  label: string
}) {
  // A value that is not on the ladder — set through the API, or from an older
  // release — still has to be representable, so land on the nearest step.
  const index = steps.reduce(
    (best, step, i) => (Math.abs(step - value) < Math.abs((steps[best] ?? 0) - value) ? i : best),
    0,
  )
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between">
        <Overline as="span">{label}</Overline>
        <span className="font-mono text-sm tabular-nums text-on-surface">{format(value)}</span>
      </div>
      <input
        type="range"
        min={0}
        max={steps.length - 1}
        step={1}
        value={index}
        aria-label={label}
        aria-valuetext={format(value)}
        onChange={(e) => onChange(steps[Number(e.target.value)] ?? value)}
        className="h-2 w-full cursor-pointer appearance-none rounded-md3-full bg-surface-high accent-primary"
      />
    </div>
  )
}
