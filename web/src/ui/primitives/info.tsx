import { useId, type ReactNode } from 'react'
import { Popover, useAnchoredPanel } from './popover'

/**
 * The bubble: white with the body colour, where it was the card's grey with
 * the muted one — the help was the lowest-contrast text on the page. Each
 * paragraph is a point with a blue bullet, so a tip reads as a short list of
 * things to know rather than a block to wade through.
 */
const BULLETS = [
  '[&_p]:relative [&_p]:my-0 [&_p]:pl-4 [&_p+p]:mt-2',
  "[&_p]:before:absolute [&_p]:before:top-[0.6em] [&_p]:before:left-0 [&_p]:before:size-1.5 [&_p]:before:rounded-full [&_p]:before:bg-info [&_p]:before:content-['']",
].join(' ')

const TIP = `bg-surface-lowest! px-4 py-3 text-sm leading-relaxed text-on-surface ${BULLETS}`

/**
 * Help shown where it applies, not behind a mark to hover: each paragraph a
 * point with a blue bullet, in the body colour. A tip that has to be found
 * and held open was worse than the paragraph it replaced, on pages with room
 * to spare. On a card it sits in a white panel; inside a white tile, plain.
 */
export function Help({
  children,
  plain = false,
  className = '',
}: {
  children: ReactNode
  plain?: boolean
  className?: string
}) {
  return (
    <div
      className={`text-sm leading-relaxed text-on-surface ${BULLETS} ${
        plain ? '' : 'rounded-md3-l bg-surface-lowest px-4 py-3'
      } ${className}`}
    >
      {children}
    </div>
  )
}

/**
 * A small blue ⓘ holding the explanation of what is beside it.
 *
 * For dense lists only, where the explanation of every row shown at once
 * would bury the rows — the configuration's thirty settings. Anywhere with
 * room, help is shown where it applies (Help): a mark to find and hover is a
 * worse way to read it. Opened by hovering, by focus, or by a click, which is
 * what a touch screen has, and placed and dismissed like every other popup.
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
          className={TIP}
        >
          {children}
        </Popover>
      )}
    </>
  )
}

/**
 * The same bubble as InfoTip's, held by something already on the page rather
 * than by a mark: for a figure that stands for several, whose rest belong on
 * hover rather than in the row — a Google account's gauge, the fullest of its
 * allowances, with every allowance in the bubble. Each paragraph of `tip` is
 * a bulleted point. Opened by hovering, by focus, or by a press, which is
 * what a touch screen has; a press does not reach the row it sits in.
 */
export function HoverTip({ children, tip, label }: { children: ReactNode; tip: ReactNode; label: string }) {
  const p = useAnchoredPanel<HTMLSpanElement>({ width: 320, maxHeight: 360 })
  const id = useId()
  return (
    <>
      <span
        ref={p.anchor}
        tabIndex={0}
        aria-label={label}
        aria-describedby={p.open ? id : undefined}
        onClick={(e) => {
          e.stopPropagation()
          e.preventDefault()
          p.toggle()
        }}
        onMouseEnter={p.show}
        onMouseLeave={p.close}
        onFocus={p.show}
        onBlur={p.close}
        className="inline-flex cursor-default rounded-md3-s outline-offset-2 focus-visible:outline-2 focus-visible:outline-primary"
      >
        {children}
      </span>
      {p.at !== null && (
        <Popover panelRef={p.panel} at={p.at} role="tooltip" label={label} id={id} className={TIP}>
          {tip}
        </Popover>
      )}
    </>
  )
}

const NOTE_TONES = {
  info: {
    box: 'bg-info-container text-on-info-container',
    icon: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z',
  },
  warn: {
    box: 'bg-warning-container text-on-warning-container',
    icon: 'M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z',
  },
  error: {
    box: 'bg-error-container text-on-error-container',
    icon: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z',
  },
}

/**
 * A line of help that has to stay on the page, as a tinted box with an icon
 * rather than small grey text: a cost, a consequence, where something is. In
 * the info, warning or error container colour, so it is read rather than
 * skimmed past.
 */
export function Note({
  tone = 'info',
  children,
  className = '',
}: {
  tone?: keyof typeof NOTE_TONES
  children: ReactNode
  className?: string
}) {
  const t = NOTE_TONES[tone]
  return (
    <div
      className={`flex items-start gap-2.5 rounded-md3-m px-3 py-2.5 text-sm leading-relaxed ${t.box} ${className}`}
    >
      <svg viewBox="0 0 24 24" aria-hidden className="mt-0.5 size-[18px] shrink-0 fill-current">
        <path d={t.icon} />
      </svg>
      <div className="min-w-0 [&_p]:m-0 [&_p+p]:mt-1.5">{children}</div>
    </div>
  )
}
