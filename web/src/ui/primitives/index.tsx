import {
  useEffect,
  useId,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { useEscapeKey } from '../hooks'

/* Material 3 primitives, shared by every screen. */

/**
 * The outline is doing real work: surface-container sits 1.1:1 against the page
 * behind it, so on a screen made of stacked cards the fills alone give the eye
 * nothing to catch. The border is what says where one card ends.
 */
export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <section
      className={`rounded-[var(--radius-md3-xl)] border border-outline bg-surface-container p-6 ${className}`}
    >
      {children}
    </section>
  )
}

export function CardTitle({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  return (
    <div className="mb-4 flex items-center justify-between gap-4">
      <h2 className="text-xl leading-7 font-normal text-on-surface">{children}</h2>
      {aside}
    </div>
  )
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
  className = '',
}: {
  label: string
  value: string
  onChange: (v: string) => void
  type?: string
  autoFocus?: boolean
  mono?: boolean
  /** Password fields name themselves so a manager can offer to save them. */
  autoComplete?: string
  className?: string
}) {
  return (
    <label className={`block ${className}`}>
      <span className="relative block">
        <input
          type={type}
          value={value}
          autoFocus={autoFocus}
          placeholder=" "
          spellCheck={false}
          autoComplete={autoComplete}
          onChange={(e) => onChange(e.target.value)}
          className={`peer h-14 w-full rounded-[var(--radius-md3-xs)] border border-outline bg-transparent px-4 pt-4 text-base text-on-surface outline-none transition-colors focus:border-2 focus:border-primary focus:px-[15px] ${
            mono ? 'font-mono text-sm' : ''
          }`}
        />
        <span className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-base text-on-surface-variant transition-all peer-focus:top-2.5 peer-focus:text-xs peer-focus:text-primary peer-[:not(:placeholder-shown)]:top-2.5 peer-[:not(:placeholder-shown)]:text-xs">
          {label}
        </span>
      </span>
    </label>
  )
}

export function FilledButton({
  children,
  disabled,
  onClick,
  type = 'submit',
  className = '',
}: {
  children: ReactNode
  disabled?: boolean
  onClick?: () => void
  type?: 'submit' | 'button'
  className?: string
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`state-layer inline-flex h-10 items-center gap-2 rounded-[var(--radius-md3-full)] bg-primary px-6 text-sm font-medium text-on-primary disabled:pointer-events-none disabled:opacity-40 ${className}`}
    >
      {children}
    </button>
  )
}

export function TonalButton({
  children,
  onClick,
  disabled,
  type = 'button',
  className = '',
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  type?: 'submit' | 'button'
  className?: string
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`state-layer inline-flex h-10 items-center gap-2 rounded-[var(--radius-md3-full)] bg-secondary-container px-5 text-sm font-medium text-on-secondary-container disabled:pointer-events-none disabled:opacity-40 ${className}`}
    >
      {children}
    </button>
  )
}

export function OutlinedButton({
  children,
  onClick,
  disabled,
  tone,
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  tone?: 'error'
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`state-layer inline-flex h-10 items-center gap-2 rounded-[var(--radius-md3-full)] border border-outline px-5 text-sm font-medium disabled:pointer-events-none disabled:opacity-40 ${
        tone === 'error' ? 'text-error' : 'text-on-surface-variant'
      }`}
    >
      {children}
    </button>
  )
}

/**
 * The lightest button that is still visibly a button.
 *
 * MD3 text buttons carry no boundary at all, which reads fine on a marketing
 * page and badly in a table of actions, where a bare "Delete" read as a label
 * until you hovered it. The outline is what makes them findable, and it uses
 * `outline` rather than `outline-variant` because a control boundary is
 * exactly the case that wants 3:1. What separates this from OutlinedButton is
 * now the ink, not the presence of an edge.
 */
export function TextButton({
  children,
  onClick,
  disabled,
  tone,
  size = 'md',
  type = 'button',
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  tone?: 'error'
  /**
   * 'sm' for a control inside a table row.
   *
   * The 40px target is right for a button you go to, and wrong for one that
   * sits in a row of 13px text: it made those rows taller than their
   * neighbours, so a column of timestamps stepped up and down the page
   * depending on which requests had failed.
   */
  size?: 'md' | 'sm'
  type?: 'submit' | 'button'
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`state-layer inline-flex items-center rounded-[var(--radius-md3-full)] border border-outline font-medium disabled:pointer-events-none disabled:opacity-40 ${
        size === 'sm' ? 'h-7 px-2.5 text-xs' : 'h-10 px-3 text-sm'
      } ${tone === 'error' ? 'text-error' : 'text-primary'}`}
    >
      {children}
    </button>
  )
}

export type ChipTone = 'ok' | 'warn' | 'error' | 'neutral'

export function Chip({ children, tone = 'neutral' }: { children: ReactNode; tone?: ChipTone }) {
  const tones: Record<ChipTone, string> = {
    ok: 'bg-success-container text-on-success-container',
    warn: 'bg-warning-container text-on-warning-container',
    error: 'bg-error-container text-on-error-container',
    neutral: 'border border-outline text-on-surface-variant',
  }
  return (
    <span
      className={`inline-flex h-7 shrink-0 items-center gap-1.5 rounded-[var(--radius-md3-s)] px-2.5 text-xs font-medium ${tones[tone]}`}
    >
      {children}
    </span>
  )
}

/**
 * Every message in the UI that appears in response to something the operator
 * did goes through here, which is why the live region lives here too. Without
 * it a failure is a purely visual event: the text appears, nothing announces
 * it, and a screen-reader user is left looking at a form that seems to have
 * done nothing. `alert` is assertive, which is right for a failure and wrong
 * for anything else.
 */
export function Banner({
  children,
  tone,
  className = '',
}: {
  children: ReactNode
  tone?: 'error' | 'warn'
  className?: string
}) {
  const tones = {
    error: 'bg-error-container text-on-error-container',
    warn: 'bg-warning-container text-on-warning-container',
  }
  return (
    <div
      role={tone === 'error' ? 'alert' : 'status'}
      aria-live={tone === 'error' ? 'assertive' : 'polite'}
      className={`rounded-[var(--radius-md3-m)] px-4 py-3 text-sm ${
        tone ? tones[tone] : 'bg-surface-high text-on-surface-variant'
      } ${className}`}
    >
      {children}
    </div>
  )
}

/**
 * A panel that could not load, with the way out of it.
 *
 * A bare error banner where a whole screen should be is a dead end: the tab
 * shows a red box and offers nothing to press, so the only recovery is knowing
 * to reload the page. A transient failure — a lapsed session, a gateway
 * restarting — should cost one click.
 */
export function ErrorState({
  message,
  onRetry,
  busy,
}: {
  message: string
  onRetry: () => void
  busy?: boolean
}) {
  return (
    <div className="flex flex-col items-start gap-4">
      <Banner tone="error">{message}</Banner>
      <TonalButton onClick={onRetry} disabled={busy}>
        {busy === true && <Spinner />}
        {busy === true ? 'Retrying…' : 'Try again'}
      </TonalButton>
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
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
  disabled?: boolean
}) {
  return (
    <label className="flex cursor-pointer items-center gap-3 select-none">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={`relative inline-flex h-8 w-13 shrink-0 items-center rounded-[var(--radius-md3-full)] border-2 transition-colors disabled:pointer-events-none disabled:opacity-40 ${
          checked ? 'border-primary bg-primary' : 'border-outline bg-surface-high'
        }`}
      >
        <span
          className={`absolute rounded-[var(--radius-md3-full)] transition-all ${
            checked
              ? 'left-[calc(100%-1.75rem)] size-6 bg-on-primary'
              : 'left-1 size-4 bg-outline'
          }`}
        />
      </button>
      <span className="text-sm text-on-surface">{label}</span>
    </label>
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
    (best, step, i) =>
      Math.abs(step - value) < Math.abs(steps[best] - value) ? i : best,
    0,
  )
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between">
        <span className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
          {label}
        </span>
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
        onChange={(e) => onChange(steps[Number(e.target.value)])}
        className="h-2 w-full cursor-pointer appearance-none rounded-[var(--radius-md3-full)] bg-surface-high accent-primary"
      />
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <p className="rounded-[var(--radius-md3-m)] border border-dashed border-outline-variant px-4 py-10 text-center text-sm text-on-surface-variant">
      {children}
    </p>
  )
}

/** Label/value pairs, dense enough to scan. */
export function KeyValue({ items }: { items: [string, ReactNode][] }) {
  return (
    // items-baseline, because the label is 12px and the value 14px: left to
    // stretch, each starts at the top of its own cell and the two sit visibly
    // out of step. Baselines are what the eye reads a label/value pair by.
    <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-4 gap-y-1.5 text-sm">
      {items.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
            {label}
          </dt>
          <dd className="m-0 min-w-0 break-words text-on-surface">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * Upstream error text, shown exactly as it arrived. It is the only thing that
 * separates an expired token from a plan restriction, so it is never
 * summarised, and never rendered as anything but text.
 */
export function Verbatim({ children }: { children: string }) {
  return (
    <pre className="m-0 max-h-56 overflow-auto rounded-[var(--radius-md3-s)] border border-outline-variant bg-surface-lowest p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-on-surface">
      {children}
    </pre>
  )
}

export function Spinner({ className = '' }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={`size-4 animate-spin ${className}`} aria-hidden>
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeOpacity="0.25" strokeWidth="3" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        fill="none"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  )
}

/** A headline number with its label. The unit of the Overview tab. */
export function Stat({
  label,
  value,
  hint,
  tone,
}: {
  label: string
  value: ReactNode
  hint?: ReactNode
  tone?: 'ok' | 'error'
}) {
  const colour =
    tone === 'ok' ? 'text-success' : tone === 'error' ? 'text-error' : 'text-on-surface'
  return (
    <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
      <div className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
        {label}
      </div>
      <div className={`mt-1 text-2xl leading-8 font-normal tabular-nums ${colour}`}>{value}</div>
      {hint !== undefined && (
        <div className="mt-0.5 text-xs text-on-surface-variant">{hint}</div>
      )}
    </div>
  )
}

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
 * A read-only value with a copy button. Used for things that exist to be
 * pasted somewhere else — a base URL, an API key.
 *
 * When copying is impossible it says so in a modal rather than a line of text
 * under the field. That is deliberate: the value this most often wraps is a
 * freshly minted API key, shown once and unrecoverable afterwards, and a
 * failure the operator scrolls past is a key they have lost.
 */
/**
 * One short value to take away: a base URL, a freshly minted key.
 *
 * A whole config file goes to CodeViewer instead, which bounds its height,
 * numbers the lines and highlights them — see primitives/code.tsx.
 */
export function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)

  const copy = async () => {
    if (await copyText(value)) {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
      return
    }
    setFailed(true)
  }

  return (
    <div>
      <div className="mb-1 text-xs font-medium tracking-wide text-on-surface-variant uppercase">
        {label}
      </div>
      <div className="flex items-stretch gap-2">
        {/* Solid rather than translucent: at 60% the fill landed between two
            near-identical surfaces and the block had no edge at all. */}
        <code className="min-w-0 flex-1 overflow-x-auto rounded-[var(--radius-md3-s)] border border-outline-variant bg-surface-lowest px-3 py-2 font-mono text-xs leading-6 whitespace-pre text-on-surface">
          {value}
        </code>
        <TonalButton onClick={() => void copy()}>{copied ? 'Copied' : 'Copy'}</TonalButton>
      </div>

      {failed && (
        <ErrorModal
          title="Could not copy"
          onClose={() => setFailed(false)}
          message={
            <>
              The browser will only give a page the clipboard over HTTPS, and this gateway is
              being served over plain HTTP. Nothing was copied.
              <br />
              <br />
              The value is selected below — press <span className="font-mono">⌘C</span> or{' '}
              <span className="font-mono">Ctrl-C</span> to take it.
            </>
          }
        >
          <SelectedValue value={value} />
        </ErrorModal>
      )}
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
      className="w-full resize-y rounded-[var(--radius-md3-s)] border border-outline bg-surface-lowest px-3 py-2 font-mono text-xs leading-6 text-on-surface"
    />
  )
}

/** A scrollable table. Wide content must never make the page scroll. */
/**
 * `cap` bounds the height and pins the header inside it.
 *
 * For a list that grows without limit: fifty rows is most of a screen, and a
 * "load more" that appends fifty at a time pushes everything after the table
 * somewhere no one will scroll to. Sticky sits on the cells rather than the
 * row, because a sticky `thead` is still not honoured everywhere.
 */
/**
 * A column that can be sorted by clicking its heading.
 *
 * `sorted` is set only on the one column currently in effect, so the table has
 * exactly one direction indicator and `aria-sort` says the same thing.
 */
export type Column = {
  label: string
  /**
   * Called with no direction to turn the column around, or with one to set it.
   * The heading button uses the first; the menu, where there is one, offers
   * both explicitly, because a menu that toggles is a menu whose two entries
   * do the same thing.
   */
  onSort?: (dir?: 'asc' | 'desc') => void
  sorted?: 'asc' | 'desc'
  /**
   * A panel to drop under the heading — a column of checkboxes, typically.
   * When present the heading opens this instead of sorting, and the sort moves
   * inside it, so one click on a column title reaches everything that column
   * can do.
   */
  menu?: ReactNode
  /** This column is narrowing the table, so the heading can say so. */
  filtered?: boolean
}

/**
 * A column heading that opens a menu.
 *
 * Portalled rather than drawn inside the cell. A capped table scrolls under
 * overflow-auto, which clips anything absolutely positioned within it, so a
 * panel opened from a heading would be cut off at the first row — and the
 * sticky header makes it worse, not better. Fixed coordinates are read from
 * the heading at the moment it opens.
 */
function HeaderMenu({ col, children }: { col: Column; children: ReactNode }) {
  const [at, setAt] = useState<{ left: number; top: number } | null>(null)
  const anchor = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const open = at !== null

  useEscapeKey(() => setAt(null), open)

  useEffect(() => {
    if (!open) return
    // Any scroll closes it rather than following: the panel sits at
    // coordinates taken when it opened, and a menu drifting away from the
    // heading it belongs to is worse than one that shuts.
    const shut = () => setAt(null)
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node
      if (panel.current !== null && panel.current.contains(target)) return
      if (anchor.current !== null && anchor.current.contains(target)) return
      setAt(null)
    }
    window.addEventListener('scroll', shut, true)
    window.addEventListener('resize', shut)
    document.addEventListener('mousedown', onDown)
    return () => {
      window.removeEventListener('scroll', shut, true)
      window.removeEventListener('resize', shut)
      document.removeEventListener('mousedown', onDown)
    }
  }, [open])

  const toggle = () => {
    if (open) {
      setAt(null)
      return
    }
    const rect = anchor.current?.getBoundingClientRect()
    if (rect === undefined) return
    // Kept on screen: a menu on the last column would otherwise open past the
    // right edge, where reading it means scrolling the page sideways.
    const width = 264
    setAt({
      left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)),
      top: rect.bottom + 4,
    })
  }

  return (
    <>
      <button
        ref={anchor}
        type="button"
        onClick={toggle}
        aria-haspopup="menu"
        aria-expanded={open}
        className={`state-layer group flex w-full items-center gap-1.5 px-2 py-2 text-left ${
          col.sorted !== undefined || col.filtered === true
            ? 'text-on-surface'
            : 'hover:text-on-surface'
        }`}
      >
        {col.label}
        {/* Which way it is sorted, when it is. */}
        <svg
          viewBox="0 0 10 6"
          aria-hidden
          className={`h-2 w-3 shrink-0 fill-current ${col.sorted === 'asc' ? 'rotate-180' : ''} ${
            col.sorted !== undefined ? 'opacity-100' : 'opacity-0'
          }`}
        >
          <path d="M0 0h10L5 6z" />
        </svg>
        {/* A funnel when the column is narrowing, so a filtered table says
            which column did it without opening anything. Otherwise a chevron
            that appears on hover, which is what says the heading opens. */}
        {col.filtered === true ? (
          <svg viewBox="0 0 12 12" aria-hidden className="h-3 w-3 shrink-0 fill-primary">
            <path d="M1 2h10L7 6.5V11L5 9.5V6.5z" />
          </svg>
        ) : (
          <svg
            viewBox="0 0 10 6"
            aria-hidden
            className="h-2 w-3 shrink-0 fill-current opacity-0 group-hover:opacity-50"
          >
            <path d="M0 0h10L5 6z" />
          </svg>
        )}
      </button>
      {open &&
        createPortal(
          <div
            ref={panel}
            role="menu"
            aria-label={col.label}
            style={{ left: at.left, top: at.top, width: 264 }}
            className="fixed z-50 max-h-[22rem] overflow-auto rounded-[var(--radius-md3-s)] border border-outline-variant bg-surface-lowest py-1 text-sm normal-case shadow-lg"
          >
            {col.onSort !== undefined && (
              <>
                <MenuRow
                  onClick={() => {
                    col.onSort?.('asc')
                    setAt(null)
                  }}
                  active={col.sorted === 'asc'}
                >
                  Sort ascending
                </MenuRow>
                <MenuRow
                  onClick={() => {
                    col.onSort?.('desc')
                    setAt(null)
                  }}
                  active={col.sorted === 'desc'}
                >
                  Sort descending
                </MenuRow>
                <div className="my-1 border-t border-outline-variant" />
              </>
            )}
            {children}
          </div>,
          document.body,
        )}
    </>
  )
}

/** One line of a header menu that does something when clicked. */
export function MenuRow({
  onClick,
  active = false,
  children,
}: {
  onClick: () => void
  active?: boolean
  children: ReactNode
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={`state-layer flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm font-normal tracking-normal ${
        active ? 'text-primary' : 'text-on-surface'
      }`}
    >
      {children}
    </button>
  )
}

/**
 * Column widths an operator has dragged, remembered per table.
 *
 * In localStorage because it is a preference about this browser, not a fact
 * about the gateway: two people looking at the same instance want their own
 * widths, and a width does not belong in a database that gets backed up
 * alongside the sealing key. Every read and write is guarded — private
 * windows, cleared site data and blocked storage all throw rather than return
 * empty — and a failure means unresized columns, which is the state everyone
 * starts in anyway.
 */
function loadWidths(key: string): Record<string, number> {
  try {
    const raw = window.localStorage.getItem(`claudication.cols.${key}`)
    if (raw === null) return {}
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return {}
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'number' && Number.isFinite(v) && v >= MIN_COL) out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

function saveWidths(key: string, widths: Record<string, number>) {
  try {
    window.localStorage.setItem(`claudication.cols.${key}`, JSON.stringify(widths))
  } catch {
    // Not worth telling anyone about: the table still works, it just forgets.
  }
}

/** Narrow enough to be a deliberate choice, wide enough to still be grabbable. */
const MIN_COL = 56

export function Table({
  head,
  children,
  cap = false,
  resizable,
}: {
  /** A plain label, or a column that can be sorted. */
  head: (string | Column)[]
  children: ReactNode
  cap?: boolean
  /**
   * Enable column resizing, remembering widths under this key.
   *
   * Opt-in rather than always on: most tables here are four columns of numbers
   * that need no adjusting, and a drag handle on every heading is a thing to
   * hit by accident. It earns its place on the wide ones, where what you want
   * to read — a chat name, a model list — is exactly what gets truncated.
   */
  resizable?: string
}) {
  const [widths, setWidths] = useState<Record<string, number>>(() =>
    resizable === undefined ? {} : loadWidths(resizable),
  )
  // The drag in flight. In a ref because it changes on every pointer move and
  // none of those should re-render anything but the one column being dragged.
  const drag = useRef<{ label: string; startX: number; startW: number } | null>(null)

  const labels = head.map((entry, i) => {
    const label = typeof entry === 'string' ? entry : entry.label
    return label === '' ? `blank-${i}` : label
  })

  const onDown = (label: string) => (e: ReactPointerEvent<HTMLDivElement>) => {
    const th = (e.currentTarget.parentElement as HTMLElement | null) ?? null
    if (th === null) return
    e.preventDefault()
    e.stopPropagation()
    drag.current = { label, startX: e.clientX, startW: th.getBoundingClientRect().width }
    // Captured on the handle, so a fast drag that outruns the pointer keeps
    // resizing instead of stopping the moment the cursor leaves the 6px strip.
    e.currentTarget.setPointerCapture(e.pointerId)
  }

  const onMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (d === null) return
    const next = Math.max(MIN_COL, Math.round(d.startW + (e.clientX - d.startX)))
    setWidths((w) => ({ ...w, [d.label]: next }))
  }

  const onUp = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (drag.current === null) return
    drag.current = null
    e.currentTarget.releasePointerCapture(e.pointerId)
    if (resizable !== undefined) saveWidths(resizable, widths)
  }

  return (
    <div className={`-mx-2 px-2 ${cap ? 'max-h-[30rem] overflow-auto' : 'overflow-x-auto'}`}>
      <table
        className={`text-sm [&_td]:align-middle ${
          resizable === undefined ? 'w-full min-w-max' : 'min-w-full'
        }`}
        // Fixed only once something has been dragged: until then the browser
        // sizes the columns to their content, which is the better default and
        // the one every other table here relies on.
        style={
          resizable !== undefined && Object.keys(widths).length > 0
            ? { tableLayout: 'fixed' }
            : undefined
        }
      >
        {resizable !== undefined && Object.keys(widths).length > 0 && (
          <colgroup>
            {labels.map((label) => (
              <col key={label} style={widths[label] ? { width: widths[label] } : undefined} />
            ))}
          </colgroup>
        )}
        <thead>
          <tr>
            {/* The rule sits on the cells, not the row: a sticky cell carries
                its own border along, a row's border stays where it started. */}
            {head.map((entry, i) => {
              const col: Column = typeof entry === 'string' ? { label: entry } : entry
              // A sortable heading has no padding of its own: the button
              // carries it, so the whole cell is the target rather than just
              // the few words in the middle of it.
              return (
                <th
                  key={col.label === '' ? `blank-${i}` : col.label}
                  aria-sort={
                    col.sorted === undefined
                      ? undefined
                      : col.sorted === 'asc'
                        ? 'ascending'
                        : 'descending'
                  }
                  className={`relative border-b border-outline text-left text-xs font-semibold tracking-wide text-on-surface-variant uppercase ${
                    col.onSort === undefined && col.menu === undefined ? 'px-2 py-2' : 'p-0'
                  } ${cap ? 'sticky top-0 z-10 bg-surface-container' : ''}`}
                >
                  {col.menu !== undefined ? (
                    <HeaderMenu col={col}>{col.menu}</HeaderMenu>
                  ) : col.onSort === undefined ? (
                    col.label
                  ) : (
                    <button
                      type="button"
                      // Wrapped, not passed: onSort takes an optional
                      // direction, and handing it the click event straight
                      // would make every heading "sort by MouseEvent".
                      onClick={() => col.onSort?.()}
                      className={`state-layer group flex w-full items-center gap-1.5 px-2 py-2 text-left ${
                        col.sorted ? 'text-on-surface' : 'hover:text-on-surface'
                      }`}
                    >
                      {col.label}
                      {/* Its own tight viewBox rather than a glyph inside a
                          24-unit one, where the caret is a third of the box and
                          renders at about a third of its nominal size. Always
                          rendered, so the heading does not jump wider the
                          moment it becomes the sorted column. */}
                      <svg
                        viewBox="0 0 10 6"
                        aria-hidden
                        className={`h-2 w-3 shrink-0 fill-current ${
                          col.sorted === 'asc' ? 'rotate-180' : ''
                        } ${col.sorted ? 'opacity-100' : 'opacity-0 group-hover:opacity-50'}`}
                      >
                        <path d="M0 0h10L5 6z" />
                      </svg>
                    </button>
                  )}
                  {/* The grab strip. Last column excluded: dragging it widens
                      the table into space that is not there, which reads as
                      the whole layout jumping. */}
                  {resizable !== undefined && i < head.length - 1 && (
                    <div
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={`Resize ${col.label}`}
                      onPointerDown={onDown(labels[i])}
                      onPointerMove={onMove}
                      onPointerUp={onUp}
                      onPointerCancel={onUp}
                      onDoubleClick={() => {
                        // Back to content width: the escape hatch for a column
                        // dragged to something unreadable.
                        setWidths((w) => {
                          const { [labels[i]]: _drop, ...rest } = w
                          if (resizable !== undefined) saveWidths(resizable, rest)
                          return rest
                        })
                      }}
                      className="group/resize absolute inset-y-0 -right-[5px] z-20 flex w-[10px] cursor-col-resize touch-none select-none justify-center"
                    >
                      {/* A rule you can see, so the column edge is visible and
                          there is something to aim at. Drawn inside a wider
                          invisible strip: a 1px target is nearly impossible to
                          hit with a mouse, and the strip is what you actually
                          grab. */}
                      <span
                        aria-hidden
                        className="h-full w-px bg-outline-variant transition-colors group-hover/resize:w-[3px] group-hover/resize:bg-primary"
                      />
                    </div>
                  )}
                </th>
              )
            })}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}

/** Thousands separators and a k/M suffix, so a token count stays readable. */
export function compact(n: number): string {
  // A billion tokens is an ordinary week here — cache reads are counted, and
  // they dwarf everything else. Without this tier the biggest number on the
  // screen read "1936M", which is four digits and a unit that has run out: the
  // eye has to divide by a thousand to learn it is about two billion.
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(n >= 10_000_000_000 ? 0 : 1)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`
  // One decimal below ten of a unit and none above it, everywhere: 1.9B, 19B,
  // 1.9M, 19M, 1.9k, 19k. Three significant figures is more than a chart label
  // can be read to anyway, and the exact value is a hover away.
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`
  return n.toLocaleString()
}

/** "3 minutes ago", give or take. */
export function ago(iso: string | undefined): string {
  if (iso === undefined || iso === '') return 'never'
  const seconds = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

/** A duration in seconds as "3d 4h", for uptime. */
export function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
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
      className={`state-layer grid size-9 shrink-0 place-items-center rounded-[var(--radius-md3-full)] border border-outline disabled:pointer-events-none disabled:opacity-30 ${
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
 * because it says so rather than because script said so.
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
      // noreferrer as well as noopener: the opened page has no business
      // knowing which gateway sent it.
      rel="noopener noreferrer"
      className="state-layer grid size-9 shrink-0 place-items-center rounded-[var(--radius-md3-full)] border border-outline text-on-surface-variant"
    >
      <svg viewBox="0 0 24 24" className="size-5 fill-current" aria-hidden>
        {children}
      </svg>
    </a>
  )
}

/**
 * A small outlined pill, for a control sitting inside a card header.
 *
 * on-surface with a visible outline rather than bare primary-coloured text:
 * primary is a mid-tone in both themes, so at this size it neither reads as a
 * button nor holds up against a tinted panel behind it.
 */
export function SmallButton({
  children,
  onClick,
  disabled,
  type = 'button',
}: {
  children: ReactNode
  onClick?: () => void
  disabled?: boolean
  type?: 'submit' | 'button'
}) {
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className="state-layer inline-flex h-7 shrink-0 items-center gap-1.5 rounded-[var(--radius-md3-full)] border border-outline bg-surface px-3 text-xs font-medium text-on-surface disabled:pointer-events-none disabled:opacity-40"
    >
      {children}
    </button>
  )
}

/**
 * A segmented control: one bordered group, divided into options.
 *
 * The group carries the boundary rather than each option, which is what keeps a
 * row of three from looking like three separate buttons — and means the
 * unselected options are still visibly part of a control at rest.
 */
export function Segmented<T extends string>({
  options,
  value,
  onChange,
  label,
}: {
  options: { id: T; label: string; content: ReactNode }[]
  value: T
  onChange: (id: T) => void
  label: string
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex overflow-hidden rounded-[var(--radius-md3-full)] border border-outline"
    >
      {options.map((o, i) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          title={o.label}
          onClick={() => onChange(o.id)}
          className={`state-layer grid h-8 min-w-8 place-items-center px-3 text-xs font-medium ${
            i > 0 ? 'border-l border-outline' : ''
          } ${
            value === o.id
              ? 'bg-secondary-container text-on-secondary-container'
              : 'text-on-surface-variant'
          }`}
        >
          {o.content}
        </button>
      ))}
    </div>
  )
}

/**
 * A secondary tab bar, for choosing which view of one screen to look at.
 *
 * A smaller sibling of the app's own nav rather than a second copy of it: the
 * same underline, but two thirds the type, half the padding and a thinner
 * marker, so it reads as subordinate to the tab that got you here instead of
 * competing with it.
 *
 * Buttons rather than links, because this picks a view inside a screen and the
 * hash already belongs to the screen.
 *
 * `aside` rides at the right of the same rule — an action that belongs to the
 * selected view, with the border still running the full width behind it.
 */
export function SubNav<T extends string>({
  options,
  value,
  onChange,
  label,
  aside,
}: {
  options: { id: T; label: string }[]
  value: T
  onChange: (id: T) => void
  label: string
  aside?: ReactNode
}) {
  return (
    <div className="-mx-1 overflow-x-auto px-1">
      <div
        role="tablist"
        aria-label={label}
        className="flex w-full min-w-max items-center gap-1 border-b border-outline-variant"
      >
        {options.map((o) => (
          <button
            key={o.id}
            type="button"
            role="tab"
            aria-selected={value === o.id}
            onClick={() => onChange(o.id)}
            // 14px, not 12. These are the primary navigation of the screen
            // they sit on — the thing you read to decide where to go — and at
            // text-xs they were set smaller than the table rows underneath
            // them, which inverts the hierarchy: the labels mattered more and
            // looked like a footnote.
            className={`state-layer relative rounded-t-[var(--radius-md3-s)] px-3.5 py-2.5 text-sm font-medium whitespace-nowrap ${
              value === o.id ? 'text-primary' : 'text-on-surface-variant'
            }`}
          >
            {o.label}
            {value === o.id && (
              <span className="absolute inset-x-1 bottom-0 h-[2px] rounded-t-full bg-primary" />
            )}
          </button>
        ))}
        {aside !== undefined && <div className="ml-auto pb-1 pl-4">{aside}</div>}
      </div>
    </div>
  )
}

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
 * makes it harder to check what was copied.
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
  size?: 'md' | 'lg'
}) {
  useEscapeKey(onClose)
  const panel = useRef<HTMLDivElement>(null)
  const titleId = useId()

  useEffect(() => {
    const returnTo = document.activeElement as HTMLElement | null
    // Focus the panel rather than its first control: the operator should hear
    // the dialog's name before its buttons.
    panel.current?.focus()
    return () => returnTo?.focus?.()
  }, [])

  const trap = (e: React.KeyboardEvent) => {
    if (e.key !== 'Tab' || panel.current === null) return
    const stops = Array.from(panel.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (stops.length === 0) return
    const first = stops[0]
    const last = stops[stops.length - 1]
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
    <div className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto p-4">
      <div className="fixed inset-0 bg-black/50 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={trap}
        className={`relative my-auto w-full ${
          size === 'lg' ? 'max-w-xl' : 'max-w-md'
        } rounded-[var(--radius-md3-xl)] border border-outline bg-surface-high p-6 shadow-xl outline-none`}
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <h2 id={titleId} className="m-0 text-xl leading-7 font-normal text-on-surface">
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
        <FilledButton type="button" onClick={onClose}>
          Close
        </FilledButton>
      </div>
    </Modal>
  )
}
