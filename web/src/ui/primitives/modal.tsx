import { useEffect, useId, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useEscapeKey } from '../hooks'
import { Button, IconButton } from './button'

/** Everything inside a dialog that can hold focus. */
const FOCUSABLE =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * A modal dialog. `aria-modal` is a claim about behaviour, not an
 * implementation of it: without a name, a focus trap and focus restoration, a
 * screen reader announces an unnamed dialog and the keyboard walks straight out
 * of it into the page underneath, which is still being read out as though it
 * were reachable.
 *
 * `size` widens the panel for content that is genuinely wider than a
 * confirmation — an API key is 40-odd monospace characters and wrapping it
 * makes it harder to check what was copied. `sheet` is a panel along the
 * right edge, the window's height, for a list read beside the page it
 * leads to: the notification centre.
 */
export function Modal({
  title,
  children,
  onClose,
  size = 'md',
}: {
  title: string
  children: ReactNode
  onClose: () => void
  size?: 'md' | 'lg' | 'sheet'
}) {
  useEscapeKey(onClose)
  const sheet = size === 'sheet'
  const panel = useRef<HTMLDivElement>(null)
  const titleId = useId()

  useEffect(() => {
    const returnTo = document.activeElement as HTMLElement | null
    // Focus the panel rather than its first control: the operator should hear
    // the dialog's name before its buttons. A dialog that marks a control
    // data-autofocus gets that instead — a confirmation's Cancel, so a key
    // pressed by reflex does not destroy anything. The name is still read
    // out, because focus is entering the labelled dialog.
    const preferred = panel.current?.querySelector<HTMLElement>('[data-autofocus]')
    ;(preferred ?? panel.current)?.focus()
    return () => returnTo?.focus?.()
  }, [])

  const trap = (e: KeyboardEvent) => {
    if (e.key !== 'Tab' || panel.current === null) return
    const stops = Array.from(panel.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    const first = stops[0]
    const last = stops[stops.length - 1]
    if (first === undefined || last === undefined) return
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === panel.current)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
      e.preventDefault()
      first.focus()
    }
  }

  // Rendered into document.body rather than wherever it was written.
  //
  // `position: fixed` is only relative to the viewport while no ancestor
  // establishes a containing block, and a transform, a filter, or a
  // backdrop-filter anywhere above is enough to do that — at which point a
  // dialog written inside a panel is clipped by the panel instead of covering
  // the page. A portal makes that impossible to get wrong by accident: this is
  // on top of the UI because it is not inside it.
  return createPortal(
    <div
      className={`fixed inset-0 z-50 flex ${
        sheet ? 'justify-end' : 'items-center justify-center overflow-y-auto p-4'
      }`}
    >
      <div className="fixed inset-0 bg-black/50 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={trap}
        className={
          sheet
            ? 'animate-sheet-in relative flex h-full w-full max-w-md flex-col overflow-y-auto overscroll-contain rounded-l-md3-xl bg-surface-high p-6 shadow-xl outline-none'
            : `relative my-auto w-full ${
                size === 'lg' ? 'max-w-xl' : 'max-w-md'
              } rounded-md3-xl bg-surface-high p-6 shadow-xl outline-none`
        }
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <h2 id={titleId} className="text-xl leading-7 font-normal text-on-surface">
            {title}
          </h2>
          <IconButton label="Close" onClick={onClose}>
            <path d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" />
          </IconButton>
        </div>
        {children}
      </div>
    </div>,
    document.body,
  )
}

/**
 * A failure worth stopping for, rather than a line of text under a field.
 *
 * `size` is passed through for the same reason Modal has it: an upstream error
 * is wrapped JSON, and narrowing it to a confirmation's width turns the one
 * thing worth reading into a column of fragments.
 */
export function ErrorModal({
  title,
  message,
  children,
  onClose,
  size = 'md',
}: {
  title: string
  message: ReactNode
  children?: ReactNode
  onClose: () => void
  size?: 'md' | 'lg'
}) {
  return (
    <Modal title={title} onClose={onClose} size={size}>
      <div className="flex gap-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-full bg-error-container text-on-error-container">
          <svg viewBox="0 0 24 24" className="size-6 fill-current" aria-hidden>
            <path d="M11 15h2v2h-2v-2zm0-8h2v6h-2V7zm1-5C6.47 2 2 6.5 2 12a10 10 0 1 0 10-10zm0 18a8 8 0 1 1 0-16 8 8 0 0 1 0 16z" />
          </svg>
        </span>
        <div className="min-w-0 flex-1 text-sm text-on-surface-variant">{message}</div>
      </div>
      {children !== undefined && <div className="mt-4">{children}</div>}
      <div className="mt-6 flex justify-end">
        <Button onClick={onClose}>Close</Button>
      </div>
    </Modal>
  )
}
