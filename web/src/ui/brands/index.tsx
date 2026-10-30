import type { ReactNode } from 'react'
import { ICON } from '../primitives/icons'
import { BRANDS, type BrandName } from './marks'

/**
 * Whose a row is, at a glance: the provider an account signs in to, the tool
 * a client is. Each is drawn as its vendor draws it (marks.ts); a client type
 * with no published mark — crush, a custom client — gets the generic
 * terminal, so every row still leads with something.
 */

/** A provider's mark, by provider id. */
const PROVIDER: Record<string, BrandName> = {
  anthropic: 'claude',
  openai: 'openai',
  google: 'gemini',
}

/** A client type's mark, by type id (internal/service/clients). */
const CLIENT: Record<string, BrandName> = {
  'claude-code': 'claudecode',
  codex: 'codex',
  opencode: 'opencode',
  'antigravity-cli': 'antigravity',
}

const SIZE = { sm: 'size-4', md: 'size-5', lg: 'size-7' } as const

function Mark({ name, size }: { name: BrandName | undefined; size: keyof typeof SIZE }) {
  if (name === undefined) {
    return (
      <svg viewBox="0 0 24 24" aria-hidden className={`${SIZE[size]} shrink-0 fill-current text-on-surface-variant`}>
        <path d={ICON.clients} />
      </svg>
    )
  }
  const m: { viewBox: string; fill?: string; fillRule?: string; body: string } = BRANDS[name]
  return (
    <svg
      viewBox={m.viewBox}
      aria-hidden
      fill={m.fill}
      fillRule={m.fillRule as 'evenodd' | 'nonzero' | undefined}
      className={`${SIZE[size]} shrink-0`}
      // The vendor's own markup, generated from the icon package's files
      // (marks.ts): paths, gradients and filters, nothing that runs.
      dangerouslySetInnerHTML={{ __html: m.body }}
    />
  )
}

/** The mark of the provider an account signs in to. */
export function ProviderMark({ provider, size = 'md' }: { provider: string; size?: keyof typeof SIZE }) {
  return <Mark name={PROVIDER[provider]} size={size} />
}

/**
 * A brand mark in the round tile a list row leads with (RowBadge's): the
 * same size and place whatever the mark, so a column of rows lines up.
 */
export function MarkTile({ children }: { children: ReactNode }) {
  return (
    <span className="grid size-8 shrink-0 place-items-center rounded-full bg-surface-high">{children}</span>
  )
}

/** The mark of the tool a client is, by its type; the generic terminal for a custom one. */
export function ClientMark({ type, size = 'md' }: { type: string; size?: keyof typeof SIZE }) {
  return <Mark name={CLIENT[type]} size={size} />
}
