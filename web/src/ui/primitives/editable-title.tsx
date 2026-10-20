import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { useAction } from '../hooks'
import { IconButton } from './button'
import { outlinedField } from './field'
import { commitTitle } from './title-edit'

/** The heading's type, shared by both states so the name does not jump. */
const TITLE = 'text-2xl leading-8 font-normal text-on-surface'

const PENCIL = 'M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 0 0 0-1.41l-2.34-2.34a1 1 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z'
const CHECK = 'M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4z'
const CROSS = 'M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z'
// Backspace-with-a-cross: what takes the name away, as distinct from the
// cross that only stops editing.
const CLEAR =
  'M22 3H7c-.69 0-1.23.35-1.59.88L0 12l5.41 8.11c.36.53.9.89 1.59.89h15c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-3 12.59L17.59 17 14 13.41 10.41 17 9 15.59 12.59 12 9 8.41 10.41 7 14 10.59 17.59 7 19 8.41 15.41 12 19 15.59z'

/**
 * A page's name, renamed where it stands: the heading with a pencil at its
 * right, which turns it into a field with a check and a cross.
 *
 * In place rather than in a card further down: the name was a field in a card
 * of its own on the client's overview, under two others, so renaming meant
 * finding it, and the card spent a panel on one word that the header already
 * showed. The header is where the name is read, so it is where it is changed.
 *
 * Enter or the check saves, Escape or the cross puts it back. The field opens
 * with the name selected, so typing replaces it and an arrow key keeps it, and
 * focus goes back to the pencil afterwards, so the keyboard is where it was.
 * Nothing is saved on blur: a click elsewhere on the page is not a decision to
 * rename, and losing what was typed to one would be worse than leaving the
 * field open.
 *
 * `value` is the stored name and `shown` what the heading says, which differ
 * for an account known by its address: its name is '' and its heading is the
 * address. `clearLabel` offers giving the name up, for exactly that case.
 */
export function EditableTitle({
  value,
  shown = value,
  placeholder,
  label,
  clearLabel,
  onSave,
}: {
  value: string
  shown?: string
  /** What an empty field stands for: an account's address. */
  placeholder?: string
  /** What the field holds, for assistive technology: "Client name". */
  label: string
  /** Set, '' is a name worth saving, and a button in the field saves it. */
  clearLabel?: string
  onSave: (name: string) => Promise<void>
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value)
  // What was just saved, shown until the page's next read says the same: the
  // reload lands a moment after the write, and the old name flashing back in
  // between reads as the rename not having taken.
  const [saved, setSaved] = useState<string | null>(null)
  const { busy, error, setError, run } = useAction()
  const input = useRef<HTMLInputElement>(null)
  const pencil = useRef<HTMLSpanElement>(null)
  const errorId = useId()
  const returnFocus = useRef(false)
  const clearable = clearLabel !== undefined

  useEffect(() => setSaved(null), [value])

  useLayoutEffect(() => {
    if (editing) {
      input.current?.focus()
      input.current?.select()
    } else if (returnFocus.current) {
      returnFocus.current = false
      pencil.current?.querySelector('button')?.focus()
    }
  }, [editing])

  const open = () => {
    setDraft(value)
    setError('')
    setEditing(true)
  }
  const close = () => {
    returnFocus.current = true
    setEditing(false)
    setError('')
  }
  const save = async (text: string) => {
    if (busy !== '') return
    const c = commitTitle(text, value, { clearable })
    if (c.kind === 'unchanged') return close()
    if (c.kind === 'invalid') {
      setError(c.reason)
      input.current?.focus()
      return
    }
    const ok = await run('save', () => onSave(c.value))
    if (ok) {
      setSaved(c.value === '' ? (placeholder ?? shown) : c.value)
      close()
    } else {
      input.current?.focus()
    }
  }

  if (!editing) {
    return (
      <div className="flex min-w-0 items-center gap-2">
        <h2 className={`m-0 min-w-0 break-all ${TITLE}`}>{saved ?? shown}</h2>
        <span ref={pencil} className="contents">
          <IconButton label="Rename" onClick={open}>
            <path d={PENCIL} />
          </IconButton>
        </span>
      </div>
    )
  }

  return (
    <form
      className="flex min-w-0 max-w-2xl flex-col gap-1"
      onSubmit={(e) => {
        e.preventDefault()
        void save(draft)
      }}
    >
      <div className="flex min-w-0 items-center gap-2">
        <input
          ref={input}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            if (error !== '') setError('')
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              // The page's own Escape handlers (a dialog's) are not this one's.
              e.preventDefault()
              e.stopPropagation()
              if (busy === '') close()
            }
          }}
          aria-label={label}
          aria-invalid={error !== '' || undefined}
          aria-describedby={error !== '' ? errorId : undefined}
          placeholder={placeholder}
          readOnly={busy !== ''}
          spellCheck={false}
          autoComplete="off"
          className={`${outlinedField(true, 'none')} min-w-0 flex-1 basis-48 ${TITLE} placeholder:text-on-surface-variant ${
            error !== '' ? 'border-error!' : ''
          }`}
        />
        <IconButton type="submit" label="Save" tone="primary" busy={busy === 'save' && draft.trim() !== ''}>
          <path d={CHECK} />
        </IconButton>
        {clearable && value !== '' && (
          <IconButton label={clearLabel} busy={busy === 'save' && draft.trim() === ''} disabled={busy !== ''}
            onClick={() => {
              setDraft('')
              void save('')
            }}>
            <path d={CLEAR} />
          </IconButton>
        )}
        <IconButton label="Cancel" disabled={busy !== ''} onClick={close}>
          <path d={CROSS} />
        </IconButton>
      </div>
      {error !== '' && (
        <p id={errorId} role="alert" className="m-0 text-sm text-error">
          {error}
        </p>
      )}
    </form>
  )
}
