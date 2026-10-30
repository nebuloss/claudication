import type { ReactNode } from 'react'
import { useLive, useNow } from '../hooks'
import { toneContainer, type Tone } from '../styles/tone'
import { Button } from './button'
import { Tip } from './info'
import { Spinner } from './spinner'

/**
 * What a panel says about itself rather than about the data in it: that it is
 * loading, that it failed, that there is nothing, how old it is.
 */

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
  tone = 'neutral',
  className = '',
}: {
  children: ReactNode
  tone?: Tone
  className?: string
}) {
  return (
    <div
      role={tone === 'error' ? 'alert' : 'status'}
      aria-live={tone === 'error' ? 'assertive' : 'polite'}
      className={`rounded-md3-m px-4 py-3 text-sm ${toneContainer[tone]} ${className}`}
    >
      {children}
    </div>
  )
}

/**
 * A failure from a load or a write, or nothing while there is none.
 *
 * Every panel wrote `{error !== '' && <Banner tone="error">…}` out by hand,
 * with its own margin each time; `className` is only ever a margin, for where
 * it sits in a card that does not space its children.
 */
export function ErrorBanner({ error, className = '' }: { error: string; className?: string }) {
  if (error === '') return null
  return (
    <Banner tone="error" className={className}>
      {error}
    </Banner>
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
      <Button variant="tonal" icon="refresh" onClick={onRetry} busy={busy === true}>
        {busy === true ? 'Retrying…' : 'Try again'}
      </Button>
    </div>
  )
}

/**
 * A panel still waiting for its first answer. Thirteen panels drew this line
 * by hand, four of them with a margin the others did not have.
 */
export function Loading({ children = 'Loading…' }: { children?: ReactNode }) {
  return (
    <p className="flex items-center gap-2 text-sm text-on-surface-variant">
      <Spinner /> {children}
    </p>
  )
}

/** A list or a report with nothing in it, said in its own words. */
export function Empty({ children }: { children: ReactNode }) {
  // A div, not a paragraph: a card caps its paragraphs at a reading width,
  // and this stands in for a table or a chart as wide as the card.
  return (
    <div className="w-full rounded-md3-m border border-dashed border-outline-variant px-4 py-10 text-center text-sm text-on-surface-variant">
      {children}
    </div>
  )
}

/**
 * Secondary text: a caption under a table, a line beside a button, what a
 * panel says instead of a chart. `xs` for a caption, `sm` for a sentence that
 * stands on its own.
 */
export function Muted({
  children,
  size = 'sm',
  as: Tag = 'p',
}: {
  children: ReactNode
  size?: 'xs' | 'sm'
  as?: 'p' | 'span'
}) {
  return (
    <Tag className={`${size === 'xs' ? 'text-xs' : 'text-sm'} text-on-surface-variant`}>
      {children}
    </Tag>
  )
}

/**
 * Upstream error text, shown exactly as it arrived. It is the only thing that
 * separates an expired token from a plan restriction, so it is never
 * summarised, and never rendered as anything but text.
 */
export function Verbatim({ children }: { children: string }) {
  return (
    <pre className="max-h-56 overflow-auto rounded-md3-m bg-surface-lowest p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-on-surface">
      {children}
    </pre>
  )
}

/**
 * What a panel that keeps itself current says about that: live while the tab
 * is told each change (docs/data-architecture.md), and how old its figures
 * are when it is not.
 *
 * Saying so is the part that matters. A panel that updates silently and one
 * that has quietly stopped look identical, and the difference between them is
 * the whole question an operator is asking when they glance at a dashboard.
 * There is no Refresh: a live panel is current, and one that is not reads
 * again on its own as soon as the gateway answers.
 */
export function Freshness({
  updatedAt,
  refreshing,
}: {
  /** Epoch milliseconds of the last successful load; 0 before the first. */
  updatedAt: number
  refreshing: boolean
}) {
  const now = useNow()
  const live = useLive()
  if (updatedAt === 0) return null

  const seconds = Math.max(0, Math.round((now - updatedAt) / 1000))
  const age =
    seconds < 5
      ? 'just now'
      : seconds < 60
        ? `${seconds}s ago`
        : seconds < 3600
          ? `${Math.floor(seconds / 60)}m ago`
          : `${Math.floor(seconds / 3600)}h ago`

  return (
    <Tip
      tip={
        live
          ? `Updated as it changes · last read ${age}`
          : `Not connected to the gateway: these figures were read ${age}, and are read again every 30 s until it answers`
      }
    >
      <span
        className="flex items-center gap-1.5 text-xs whitespace-nowrap text-on-surface-variant"
        aria-live="polite"
      >
        <span
          className={`size-1.5 shrink-0 rounded-full ${
            !live ? 'bg-error' : refreshing ? 'bg-primary' : 'bg-primary/50'
          }`}
          aria-hidden
        />
        {live ? 'live' : `offline · ${age}`}
      </span>
    </Tip>
  )
}
