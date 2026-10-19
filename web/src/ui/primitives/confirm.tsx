import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useAction } from '../hooks'
import { Banner, FilledButton, Modal, Spinner, TextButton } from './index'

/**
 * A question to put to the operator before doing something that cannot be
 * taken back.
 *
 * `action`, when given, is run from the dialog itself: the confirm button
 * shows it in flight, a failure is reported in the dialog where the question
 * was asked, and the dialog closes only once it has succeeded. Without one the
 * dialog only answers yes or no and the caller does the rest.
 */
export interface ConfirmRequest {
  title: string
  body?: ReactNode
  /** Says what happens — "Delete key", not "OK". */
  confirmLabel: string
  /** 'danger' for a destructive action: the confirm button is drawn in the error colour. */
  tone?: 'danger'
  action?: () => Promise<unknown>
}

/**
 * The confirmation behind every destructive button.
 *
 * It replaced window.confirm, which cannot be styled, is headed with the
 * page's address rather than the action, blocks the whole tab while it is up,
 * and can be switched off by the browser once the operator ticks "don't allow
 * this page to show dialogs" — after which it answers no at once and every
 * Delete in the UI silently did nothing.
 *
 * Cancel takes focus, so a click or Space pressed by reflex as the dialog opens
 * is not a yes. Escape and the backdrop cancel. Enter presses whichever button
 * has focus, so the reflex Enter lands on Cancel too: a destructive question
 * answers yes only to a deliberate choice. Enter anywhere else in the dialog
 * confirms, as a form's submit would. Nothing
 * cancels while the action is in flight: closing would hide a write that is
 * still happening.
 */
export function ConfirmDialog({
  title,
  body,
  confirmLabel,
  tone,
  action,
  onSettle,
}: ConfirmRequest & { onSettle: (confirmed: boolean) => void }) {
  const { pending, error, run } = useAction()

  const cancel = () => {
    if (!pending) onSettle(false)
  }
  const accept = async () => {
    if (pending) return
    if (action === undefined || (await run('confirm', action))) onSettle(true)
  }
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== 'Enter' || e.defaultPrevented) return
    if (e.target instanceof HTMLButtonElement) return
    e.preventDefault()
    void accept()
  }

  return (
    <Modal title={title} onClose={cancel}>
      <div onKeyDown={onKeyDown}>
        {body !== undefined && <div className="text-sm text-on-surface-variant">{body}</div>}
        {error !== '' && (
          <Banner tone="error" className="mt-4">
            {error}
          </Banner>
        )}
        <div className="mt-6 flex justify-end gap-2">
          <TextButton onClick={cancel} disabled={pending} autoFocus>
            Cancel
          </TextButton>
          <FilledButton
            type="button"
            tone={tone === 'danger' ? 'error' : undefined}
            onClick={() => void accept()}
            disabled={pending}
          >
            {pending && <Spinner />}
            {confirmLabel}
          </FilledButton>
        </div>
      </div>
    </Modal>
  )
}

type Confirm = (request: ConfirmRequest) => Promise<boolean>

const ConfirmContext = createContext<Confirm>(() =>
  Promise.reject(new Error('useConfirm needs a ConfirmProvider above it')),
)

/**
 * Where confirmations are drawn: one dialog for the whole app, so a call site
 * is one line and needs no state of its own to show it.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<{ request: ConfirmRequest; id: number } | null>(null)
  // Held outside state, so answering never depends on a render having run.
  const pending = useRef<((confirmed: boolean) => void) | null>(null)
  const next = useRef(0)

  const confirm = useCallback<Confirm>(
    (request) =>
      new Promise<boolean>((resolve) => {
        // A second question replaces the first, which is answered no rather
        // than left waiting for ever.
        pending.current?.(false)
        pending.current = resolve
        setOpen({ request, id: ++next.current })
      }),
    [],
  )

  const settle = useCallback((confirmed: boolean) => {
    pending.current?.(confirmed)
    pending.current = null
    setOpen(null)
  }, [])

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {open !== null && <ConfirmDialog key={open.id} {...open.request} onSettle={settle} />}
    </ConfirmContext.Provider>
  )
}

/** `await confirm({...})` is true once the operator said yes (and `action`, if any, succeeded). */
export function useConfirm(): Confirm {
  return useContext(ConfirmContext)
}
