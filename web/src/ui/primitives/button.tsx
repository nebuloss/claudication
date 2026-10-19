import type { ReactNode } from 'react'
import { Spinner } from './spinner'

/**
 * Every button in the UI, as one component with its look chosen by props.
 *
 * There were seven: Filled, Tonal, Outlined, Text, Small, Icon and the add
 * button, each with its own props — one took a className and one did not, one
 * could be a submit and one could not, and FilledButton alone defaulted to
 * type="submit", so a filled button dropped into a form that was not its own
 * submitted it. The variants are the MD3 emphasis ladder, highest first:
 *
 *  - `filled`, the one action a panel exists for;
 *  - `tonal`, a frequent action beside it — Copy, Load more;
 *  - `outlined`, a secondary one, and at `sm` the pill inside a card header;
 *  - `text`, the lightest button that is still visibly one (see below).
 *
 * `type` defaults to "button" for all of them. A form says which button
 * submits it, by passing type="submit"; nothing else does by accident.
 */
export type ButtonVariant = 'filled' | 'tonal' | 'outlined' | 'text'
export type ButtonSize = 'md' | 'sm'
/**
 * 'error' for the button that destroys something, so it does not read as the
 * safe default; 'muted' for a text button whose state is off, such as Live.
 */
export type ButtonTone = 'error' | 'muted'

/**
 * Padding and ink per variant and size.
 *
 * Text buttons keep their outline: MD3's carry no boundary at all, which
 * reads fine on a marketing page and badly in a table of actions, where a bare
 * "Delete" read as a label until you hovered it. It is `outline` rather than
 * `outline-variant` because a control boundary is exactly the case that wants
 * 3:1; what separates a text button from an outlined one is the ink.
 *
 * Small is for a control inside a table row or a card header. The 40px target
 * is right for a button you go to and wrong for one in a row of 13px text: it
 * made those rows taller than their neighbours, so a column of timestamps
 * stepped up and down the page depending on which requests had failed. The
 * small outlined pill is on-surface on a surface fill rather than the muted
 * ink: at 12px the muted ink neither reads as a button nor holds up against a
 * tinted panel behind it.
 */
const SHAPE: Record<ButtonVariant, Record<ButtonSize, string>> = {
  filled: { md: 'h-10 gap-2 px-6 text-sm', sm: 'h-7 shrink-0 gap-1.5 px-3 text-xs' },
  tonal: { md: 'h-10 gap-2 px-5 text-sm', sm: 'h-7 shrink-0 gap-1.5 px-3 text-xs' },
  outlined: {
    md: 'h-10 gap-2 border border-outline px-5 text-sm',
    sm: 'h-7 shrink-0 gap-1.5 border border-outline bg-surface px-3 text-xs',
  },
  text: {
    md: 'h-10 gap-2 border border-outline px-3 text-sm',
    sm: 'h-7 shrink-0 gap-1.5 border border-outline px-2.5 text-xs',
  },
}

function ink(variant: ButtonVariant, size: ButtonSize, tone: ButtonTone | undefined): string {
  switch (variant) {
    case 'filled':
      return tone === 'error' ? 'bg-error text-on-error' : 'bg-primary text-on-primary'
    case 'tonal':
      return 'bg-secondary-container text-on-secondary-container'
    case 'outlined':
      if (tone === 'error') return 'text-error'
      return size === 'sm' ? 'text-on-surface' : 'text-on-surface-variant'
    case 'text':
      return tone === 'error' ? 'text-error' : tone === 'muted' ? 'text-on-surface-variant' : 'text-primary'
  }
}

export type ButtonLook = { variant?: ButtonVariant; size?: ButtonSize; tone?: ButtonTone }

/**
 * A button's classes, for something that has to look like a button and cannot
 * be one: a download, a link to another page.
 */
export function buttonClass({ variant = 'filled', size = 'md', tone }: ButtonLook = {}): string {
  return `state-layer inline-flex items-center justify-center rounded-md3-full font-medium disabled:pointer-events-none disabled:opacity-40 ${SHAPE[variant][size]} ${ink(variant, size, tone)}`
}

export function Button({
  children,
  onClick,
  disabled = false,
  busy = false,
  type = 'button',
  variant = 'filled',
  size = 'md',
  tone,
  icon,
  title,
  pressed,
  autoFocus = false,
}: ButtonLook & {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  /** In flight: a spinner before the label, and not pressable again. */
  busy?: boolean
  type?: 'button' | 'submit'
  /** SVG path data for a leading icon, which takes the label's left padding. */
  icon?: string
  title?: string
  /** A toggle button's state, for assistive technology. */
  pressed?: boolean
  /** Where a Modal puts focus when it opens, instead of on the dialog itself. */
  autoFocus?: boolean
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled || busy}
      title={title}
      aria-pressed={pressed}
      data-autofocus={autoFocus ? '' : undefined}
      className={`${buttonClass({ variant, size, tone })} ${icon !== undefined ? 'pl-4' : ''}`}
    >
      {busy && <Spinner className={size === 'sm' ? 'size-3' : ''} />}
      {icon !== undefined && !busy && (
        <svg viewBox="0 0 24 24" aria-hidden className="size-5 fill-current">
          <path d={icon} />
        </svg>
      )}
      {children}
    </button>
  )
}

/**
 * A link drawn as a button: somewhere to go, or a file to save, with the
 * prominence of an action.
 *
 * An anchor rather than a button with an onClick, so the browser's own link
 * handling — a new tab, a download, the status bar — still applies. `external`
 * opens it in a new tab and marks it with the arrow that says it leaves the
 * admin UI.
 */
export function ButtonLink({
  href,
  children,
  external = false,
  variant = 'outlined',
  size,
  tone,
}: ButtonLook & { href: string; children: ReactNode; external?: boolean }) {
  return (
    <a
      href={href}
      // noreferrer as well as noopener: the opened page has no business
      // knowing which gateway sent it.
      {...(external ? { target: '_blank', rel: 'noopener noreferrer' } : {})}
      className={buttonClass({ variant, size, tone })}
    >
      {children}
      {external && (
        <svg viewBox="0 0 24 24" aria-hidden className="-ml-0.5 size-3.5 shrink-0 fill-current">
          <path d="M14 3h7v7h-2V6.41l-9.29 9.3-1.42-1.42 9.3-9.29H14V3zM5 5h5v2H7v10h10v-3h2v5H5V5z" />
        </svg>
      )}
    </a>
  )
}

/** A square icon-only button. `children` is the SVG path data. */
export function IconButton({
  label,
  onClick,
  disabled,
  tone,
  children,
}: {
  label: string
  onClick?: () => void
  disabled?: boolean
  tone?: 'error'
  children: ReactNode
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
      className={`state-layer grid size-9 shrink-0 place-items-center rounded-md3-full border border-outline disabled:pointer-events-none disabled:opacity-30 ${
        tone === 'error' ? 'text-error' : 'text-on-surface-variant'
      }`}
    >
      <svg viewBox="0 0 24 24" className="size-5 fill-current" aria-hidden>
        {children}
      </svg>
    </button>
  )
}

/**
 * IconButton's twin for somewhere to go rather than something to do.
 *
 * An anchor, not a button with an onClick: a link the keyboard and the middle
 * mouse button both understand, and one the browser will open in a new tab
 * because it says so rather than because script said so. The size of the app
 * bar's other controls, which is why it is a step larger than IconButton.
 */
export function IconLink({
  label,
  href,
  children,
}: {
  label: string
  href: string
  children: ReactNode
}) {
  return (
    <a
      href={href}
      title={label}
      aria-label={label}
      target="_blank"
      rel="noopener noreferrer"
      className="state-layer grid size-10 shrink-0 place-items-center rounded-md3-full border border-outline text-on-surface-variant"
    >
      <svg viewBox="0 0 24 24" className="size-5 fill-current" aria-hidden>
        {children}
      </svg>
    </a>
  )
}
