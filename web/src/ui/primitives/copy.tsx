import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import { ICON } from './icons'
import { Button, IconButton } from './button'
import { Overline } from './data'
import { ErrorModal } from './modal'

/** Taking a value away: copying it, saving it as a file, and keeping a secret out of sight until then. */

/**
 * Copy text, honestly.
 *
 * navigator.clipboard exists only in a secure context, and this gateway is
 * built to be reached over plain HTTP on a LAN or through a tunnel — so on the
 * deployment the Overview tab itself describes, there is no clipboard to write
 * to and this returns false.
 *
 * document.execCommand("copy") is deliberately NOT used as a fallback. It is
 * deprecated, and worse, in an insecure context it can return true having
 * copied nothing at all — which turns a Copy button into one that lies. For a
 * value shown once and unrecoverable afterwards, a button that admits it
 * cannot copy is worth far more than one that might have. The caller shows the
 * value and lets the operator take it.
 */
export async function copyText(text: string): Promise<boolean> {
  if (!window.isSecureContext || navigator.clipboard === undefined) {
    return false
  }
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // Present but refused: a denied permission, or an unfocused document.
    return false
  }
}

/**
 * A copy, and what it came to: `copied` for a moment after it worked, and
 * `failed` until the failure is acknowledged.
 *
 * Written out three times before — the field, the code viewer, the request
 * record — and the three disagreed about what a failure looks like. One of
 * them said so in a line of small text beside the button, which over plain
 * HTTP is the common case and was easy to miss.
 */
export function useCopy(value: string) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])

  const copy = async () => {
    if (await copyText(value)) {
      setCopied(true)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(false), 1500)
      return
    }
    setFailed(true)
  }

  return { copied, failed, copy, dismiss: () => setFailed(false) }
}

/** What a failed copy says, unless the caller has a better way out to offer. */
const NO_CLIPBOARD = (
  <>
    The browser will only give a page the clipboard over HTTPS, and this gateway is being served
    over plain HTTP. Nothing was copied.
    <br />
    <br />
    The value is selected below — press <span className="font-mono">⌘C</span> or{' '}
    <span className="font-mono">Ctrl-C</span> to take it.
  </>
)

/**
 * A Copy button, and the dialog for when copying is impossible.
 *
 * The failure is a modal rather than a line under the button. The value this
 * most often copies is a freshly minted API key, shown once and unrecoverable
 * afterwards, and a failure the operator scrolls past is a key they have lost.
 * The dialog shows the value selected, so the keyboard shortcut it names does
 * something — unless `failure` offers another way out, such as Download.
 */
export function CopyButton({
  value,
  label = 'Copy',
  failure,
}: {
  value: string
  label?: string
  /** What the failure says instead, when there is a better way out than selecting the value. */
  failure?: ReactNode
}) {
  const c = useCopy(value)
  return (
    <>
      <Button variant="tonal" icon={c.copied ? 'done' : 'copy'} onClick={() => void c.copy()}>
        {c.copied ? 'Copied' : label}
      </Button>
      {c.failed && (
        <ErrorModal title="Could not copy" onClose={c.dismiss} message={failure ?? NO_CLIPBOARD}>
          {failure === undefined ? <SelectedValue value={value} /> : undefined}
        </ErrorModal>
      )}
    </>
  )
}

/**
 * How a secret is drawn until asked for: as password dots. Hidden by default
 * because a key on screen is a key in every screenshot and screen share.
 *
 * Drawn as dots by the browser rather than replaced by them, so the text is
 * still there: it can be selected and copied by hand, out of a config file as
 * much as out of the field, and the dots are exactly as many as it has.
 */
export const CONCEAL = '[-webkit-text-security:disc] [text-security:disc]'

/** Show or hide a secret: an eye, struck through while it is shown. */
export function RevealButton({ shown, onToggle }: { shown: boolean; onToggle: () => void }) {
  return (
    <IconButton label={shown ? 'Hide' : 'Show'} onClick={onToggle}>
      <path d={shown ? ICON.eyeOff : ICON.eye} />
    </IconButton>
  )
}

/**
 * A read-only value with a copy button: one short value to take away — a base
 * URL, an API key, a command.
 *
 * A whole config file goes to CodeViewer instead, which bounds its height,
 * numbers the lines and highlights them — see primitives/code.tsx.
 */
export function CopyField({
  label,
  value,
  secret = false,
  conceal = '',
}: {
  label: string
  value: string
  /** Dots until the eye is pressed. Copy copies the value either way. */
  secret?: boolean
  /**
   * Only this part of the value as dots — a token inside a command, so the
   * command can be read and the token cannot.
   */
  conceal?: string
}) {
  const hides = secret || conceal !== ''
  const [shown, setShown] = useState(!hides)

  return (
    <div>
      <div className="mb-1">
        <Overline>{label}</Overline>
      </div>
      <div className="flex items-center gap-2">
        {/* Solid rather than translucent: at 60% the fill landed between two
            near-identical surfaces and the block had no edge at all. */}
        <code className="min-w-0 flex-1 overflow-x-auto rounded-md3-m bg-surface-lowest px-3 py-2 font-mono text-xs leading-6 whitespace-pre text-on-surface">
          {shown ? (
            value
          ) : conceal !== '' ? (
            value.split(conceal).map((part, i, parts) => (
              <Fragment key={i}>
                {part}
                {i < parts.length - 1 && <span className={CONCEAL}>{conceal}</span>}
              </Fragment>
            ))
          ) : (
            <span className={CONCEAL}>{value}</span>
          )}
        </code>
        {hides && <RevealButton shown={shown} onToggle={() => setShown(!shown)} />}
        <CopyButton value={value} />
      </div>
    </div>
  )
}

/** Hand the browser a file without a round trip to anything. */
export function saveAs(filename: string, value: string) {
  const url = URL.createObjectURL(new Blob([value], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  // Freed on the next turn of the loop: revoking before the click is handled
  // cancels the download in some browsers.
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

/**
 * The value, selected the moment it appears, so the keyboard shortcut the
 * modal just told the operator to press actually does something.
 */
function SelectedValue({ value }: { value: string }) {
  const ref = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    const el = ref.current
    if (el === null) return
    el.focus()
    el.select()
  }, [])

  return (
    <textarea
      ref={ref}
      readOnly
      rows={Math.min(6, value.split('\n').length)}
      value={value}
      className="w-full resize-y rounded-md3-s border border-outline bg-surface-lowest px-3 py-2 font-mono text-xs leading-6 text-on-surface"
    />
  )
}
