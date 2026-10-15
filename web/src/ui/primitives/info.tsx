import { useId, type ReactNode } from 'react'
import { Popover, useAnchoredPanel } from './popover'

/**
 * A small blue ⓘ holding the explanation of what is beside it.
 *
 * The help used to be paragraphs above every control, read once and then
 * scrolled past on every visit after. It is here instead, a pointer or a tap
 * away: opened by hovering, by focusing it from the keyboard, or by a click,
 * which is what a touch screen has. It is the shared popover, so it is placed,
 * followed and dismissed like every other popup.
 */
export function InfoTip({ children, label = 'More about this' }: { children: ReactNode; label?: string }) {
  const p = useAnchoredPanel({ width: 340, maxHeight: 420 })
  const id = useId()
  return (
    <>
      <button
        ref={p.anchor}
        type="button"
        aria-label={label}
        aria-expanded={p.open}
        aria-describedby={p.open ? id : undefined}
        onClick={p.toggle}
        onMouseEnter={p.show}
        onMouseLeave={p.close}
        onFocus={p.show}
        onBlur={p.close}
        className="state-layer inline-grid size-8 shrink-0 place-items-center rounded-full align-middle text-info"
      >
        {/* Filled, not outlined: a solid blue disc is a point of contrast on
            a page of neutral fills, which is half of what it is for. */}
        <svg viewBox="0 0 24 24" aria-hidden className="size-[22px] fill-current">
          <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z" />
        </svg>
      </button>
      {p.at !== null && (
        <Popover
          panelRef={p.panel}
          at={p.at}
          role="tooltip"
          label={label}
          id={id}
          className="px-4 py-3 leading-relaxed text-on-surface-variant [&_p]:my-0 [&_p+p]:mt-2"
        >
          {children}
        </Popover>
      )}
    </>
  )
}
